// Package volume implements contemper's volume declaration model: naming
// (explicit or derived from the path), size parsing, and turning an
// image's `VOLUME` paths plus its `io.contemper.volume.*` labels into
// resolved Spec values.
//
// See docs/design/volumes-and-providers.md for why volumes are named and
// sized this way.
package volume

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// MaxNameLen is the ext4 filesystem label limit, and so the longest name
// a volume may have.
const MaxNameLen = 16

// RootSizeLabel overrides the root partition's default size, the same as
// `convert --root-size`, which takes precedence when both are given.
const RootSizeLabel = "io.contemper.root.size"

// FstabLabel opts an image out of the fstab lines contemper would
// otherwise append, the same as `convert --no-fstab`.
const FstabLabel = "io.contemper.fstab"

// DefaultHelperRef is the published support image supplying the
// first-boot volume-formatting helper (see
// docs/design/volumes-and-providers.md), merged automatically whenever
// an image declares volumes unless `--no-volume-helper` is given.
// `--volume-helper <ref>` overrides it.
const DefaultHelperRef = "ghcr.io/contemper-project/volumes-support:v1"

// Spec is one declared volume, resolved to a name and (if known at
// convert time) a size.
type Spec struct {
	// Path is the volume's mount point, and its identity: two images
	// that declare the same VOLUME path are talking about "the same"
	// volume, even across a name change.
	Path string
	// Name is the ext4 label and, where the target lets contemper
	// choose, the disk serial: at most MaxNameLen bytes of [a-z0-9-].
	Name string
	// SizeBytes is the volume's size in bytes, or 0 if none is known
	// yet (deploy must supply one before this volume can be used).
	SizeBytes int64
	// Seed is false when an explicit io.contemper.volume.<path>.seed
	// label opts this volume out of being seeded from the image's own
	// content the first time its disk is formatted (see
	// docs/guide/volumes.md); true, the default, leaves the guest
	// helper free to seed it when the image actually has content at
	// Path.
	Seed bool
}

// sizeLabelKey and nameLabelKey compute the two labels that customize one
// declared volume, given its path. nameLabelKey is kept as the only place
// that reads the optional explicit-name label, so it can be dropped
// independently of path-derived naming (see DeriveName) with a small,
// isolated change.
func sizeLabelKey(path string) string { return "io.contemper.volume." + path + ".size" }
func nameLabelKey(path string) string { return "io.contemper.volume." + path + ".name" }

// seedLabelKey computes the per-volume seed opt-out label: setting
// io.contemper.volume.<path>.seed="false" skips copying the image's own
// content onto that volume's disk the first time it is formatted (see
// docs/guide/volumes.md). Any value other than "true" or "false" is a
// convert error, the same as the other volume labels.
func seedLabelKey(path string) string { return "io.contemper.volume." + path + ".seed" }

// FromConfig resolves paths (an image's declared VOLUME instructions, in
// any order) against labels (the image's config labels) into a sorted-by-
// path list of Specs. Each volume's name is either taken from its
// explicit io.contemper.volume.<path>.name label (validated with
// ValidateName) or derived from its path (DeriveName); its size, if any,
// comes from io.contemper.volume.<path>.size; its Seed is false only when
// io.contemper.volume.<path>.seed is exactly "false" ("true", the
// default, and any other value are handled the same way: true, or a
// convert error for anything but those two strings). Two volumes that
// resolve to the same name fail, naming both paths and the name.
func FromConfig(paths []string, labels map[string]string) ([]Spec, error) {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)

	specs := make([]Spec, 0, len(sorted))
	byName := map[string][]string{}

	for _, p := range sorted {
		if err := validatePath(p); err != nil {
			return nil, err
		}
		name := DeriveName(p)
		if explicit, ok := labels[nameLabelKey(p)]; ok {
			explicit = strings.TrimSpace(explicit)
			if err := ValidateName(explicit); err != nil {
				return nil, fmt.Errorf("label %s: %w", nameLabelKey(p), err)
			}
			name = explicit
		}

		var size int64
		if raw, ok := labels[sizeLabelKey(p)]; ok {
			var err error
			size, err = ParseSize(raw)
			if err != nil {
				return nil, fmt.Errorf("label %s: %w", sizeLabelKey(p), err)
			}
			if err := CheckDeclaredSize(size); err != nil {
				return nil, fmt.Errorf("label %s: %w", sizeLabelKey(p), err)
			}
		}

		seed := true
		if raw, ok := labels[seedLabelKey(p)]; ok {
			switch strings.TrimSpace(raw) {
			case "true":
				seed = true
			case "false":
				seed = false
			default:
				return nil, fmt.Errorf("label %s: invalid value %q, want \"true\" or \"false\"", seedLabelKey(p), raw)
			}
		}

		specs = append(specs, Spec{Path: p, Name: name, SizeBytes: size, Seed: seed})
		byName[name] = append(byName[name], p)
	}

	var dupNames []string
	for name := range byName {
		if len(byName[name]) > 1 {
			dupNames = append(dupNames, name)
		}
	}
	sort.Strings(dupNames)
	if len(dupNames) > 0 {
		name := dupNames[0]
		return nil, fmt.Errorf("volumes %s all resolve to the name %q; give one an explicit %s label",
			strings.Join(byName[name], " and "), name, nameLabelKey(byName[name][0]))
	}

	return specs, nil
}

// validatePath checks a declared VOLUME path: it must be absolute (it
// becomes an fstab mount point) and free of control characters (it is
// written as the last field of a line in /etc/contemper/volumes, which
// the guest reads line by line).
func validatePath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("volume %q: path must be absolute", p)
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return fmt.Errorf("volume %q: path contains a control character", p)
		}
	}
	return nil
}

// MaxRootSizeLabel is the largest root partition an image's
// io.contemper.root.size label may ask for. The label comes from the
// image, which may not be trusted, and the disk is written out in full,
// so a bigger root needs the trusted `convert --root-size` flag instead.
const MaxRootSizeLabel = 16 << 30

// MaxVolumeSize is the largest volume size an image label, or a bundle's
// manifest, may declare. Volumes are sparse qcow2 files, so a large one
// costs little up front, and a data volume of a few TiB is plausible; but
// the declaring file comes from the image, so it is capped well above any
// ordinary use. A larger volume is sized with the trusted
// `deploy --volume <path>=<size>` flag, which is not capped.
const MaxVolumeSize = 1 << 40

// CheckDeclaredSize returns an error if size, a volume size declared by
// an image label or a bundle manifest, is over MaxVolumeSize.
func CheckDeclaredSize(size int64) error {
	if size > MaxVolumeSize {
		return fmt.Errorf("declares a %d GiB volume, over the %d GiB an image or bundle may declare; leave the size out and pass --volume <path>=<size> to deploy for a larger volume",
			size>>30, MaxVolumeSize>>30)
	}
	return nil
}

// RootSize returns the root partition size labels declare
// (io.contemper.root.size), or 0 if none is set, and an error if it asks
// for more than MaxRootSizeLabel. `convert --root-size` takes precedence
// over this when given and is not capped.
func RootSize(labels map[string]string) (int64, error) {
	raw, ok := labels[RootSizeLabel]
	if !ok {
		return 0, nil
	}
	size, err := ParseSize(raw)
	if err != nil {
		return 0, fmt.Errorf("label %s: %w", RootSizeLabel, err)
	}
	if size > MaxRootSizeLabel {
		return 0, fmt.Errorf("label %s asks for a %d GiB root, over the %d GiB an image label may request; pass --root-size to convert a larger root",
			RootSizeLabel, size>>30, MaxRootSizeLabel>>30)
	}
	return size, nil
}

// FstabOptedOut reports whether the image's io.contemper.fstab label
// opts out of contemper's appended fstab lines.
func FstabOptedOut(labels map[string]string) bool {
	return labels[FstabLabel] == "false"
}

// nameCharset is every byte DeriveName and ValidateName accept.
func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-'
}

// DeriveName derives a volume's name from its path: the leading `/` is
// dropped, each remaining `/` becomes `-`, ASCII letters are lowercased,
// and any character still outside [a-z0-9-] is dropped outright (so
// "/data" becomes "data", "/var/lib/app!" becomes "var-lib-app"). If the
// result is longer than MaxNameLen, it is shortened to its first 11
// characters, a `-`, and the first 4 hex characters of sha256(path), so
// two long paths sharing an 11-character prefix still get different
// names. If filtering leaves nothing at all (a path with no [a-zA-Z0-9]
// characters), the name falls back to "v-" plus 6 hex characters of
// sha256(path), which is never empty.
func DeriveName(p string) string {
	s := strings.TrimPrefix(p, "/")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c - 'A' + 'a')
		case isNameByte(c):
			b.WriteByte(c)
		default:
			// dropped, not substituted
		}
	}
	name := b.String()
	if name == "" {
		sum := sha256.Sum256([]byte(p))
		return "v-" + hex.EncodeToString(sum[:])[:6]
	}
	if len(name) > MaxNameLen {
		sum := sha256.Sum256([]byte(p))
		name = name[:11] + "-" + hex.EncodeToString(sum[:])[:4]
	}
	return name
}

// ValidateName checks an explicit volume name: 1-16 bytes, every byte in
// [a-z0-9-], the same charset DeriveName produces, chosen so an explicit
// name is exactly as safe as a derived one as both an ext4 label and an
// fstab LABEL= token (which cannot contain whitespace).
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("volume name is empty")
	}
	if len(name) > MaxNameLen {
		return fmt.Errorf("volume name %q is %d bytes, want at most %d", name, len(name), MaxNameLen)
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return fmt.Errorf("volume name %q: byte %d (%q) is not in [a-z0-9-]", name, i, name[i])
		}
	}
	return nil
}

// ParseSize parses a human size like "2GiB", "512MiB", or a bare byte
// count, into bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"GB", 1e9},
		{"MB", 1e6},
		{"KB", 1e3},
		{"B", 1},
	}
	for _, sfx := range suffixes {
		if strings.HasSuffix(strings.ToUpper(s), strings.ToUpper(sfx.suffix)) {
			numStr := s[:len(s)-len(sfx.suffix)]
			n, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			bytes := n * float64(sfx.mult)
			// NaN fails every comparison, so this also rejects "NaNGiB";
			// the upper bound keeps the int64 conversion from overflowing.
			if !(bytes >= 0 && bytes < math.MaxInt64) {
				return 0, fmt.Errorf("invalid size %q: must be between 0 and %d bytes", s, int64(math.MaxInt64))
			}
			return int64(bytes), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid size %q: must not be negative", s)
	}
	return n, nil
}
