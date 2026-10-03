// Package bundle writes contemper.json, the manifest that accompanies a
// bundle's disk file: which image it came from, which target was
// resolved, and what the image declared (volumes, ports, ...). See
// docs/reference/bundle.md for the on-disk layout and field reference.
package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/contemper-project/contemper/internal/source"
	"github.com/contemper-project/contemper/internal/volume"
)

// FormatVersion is the current contemper.json schema version. It stays 1
// until after the public v0.1.0 release: before then, the format can
// still change (as it does here, for volumes and sizing - volumes became
// objects, and the support image's resolved variants moved from a
// top-level "support.variants" key to Support.Variants) without a bump.
const FormatVersion = 1

// Boot modes recorded in Manifest.Boot.
const (
	BootUKI        = source.BootUKI
	BootBootloader = source.BootBootloader
)

// ImageRef identifies a source image by reference and digest.
type ImageRef struct {
	Ref string `json:"ref"`
	// Digest is always set in a bundle manifest; the group file of a
	// multi-architecture run leaves it empty (and omitted).
	Digest string `json:"digest,omitempty"`
	// Repo is the source image's repository name, without its tag (the
	// same value convert uses to name the bundle directory, before the
	// "-<tag>.<arch>" suffix). deploy --to local-qemu uses it as the
	// default per-instance state directory name, so redeploying a new
	// tag of the same image reuses that instance's volumes. Set only on
	// Source; empty (and omitted) elsewhere.
	Repo string `json:"repo,omitempty"`
}

// SupportRef identifies a support-image merge (the target's or the
// user's support image, or the automatically merged volume helper) by
// reference and digest, where the reference came from, and each of its
// branches' resolved variant.
type SupportRef struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	// Origin is "target" when the target's own default applied, or
	// "flag" when --support replaced it. Empty (and omitted) for the
	// volume helper.
	Origin   string           `json:"origin,omitempty"`
	Variants []SupportVariant `json:"variants,omitempty"`
}

// DiskInfo describes the bundle's disk file.
type DiskInfo struct {
	File      string `json:"file"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

// Healthcheck mirrors the subset of an OCI HEALTHCHECK contemper carries
// through as a deployment hint, in human-readable durations.
type Healthcheck struct {
	Test        []string `json:"test,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty"`
	Retries     int      `json:"retries,omitempty"`
}

// Hints carries deployment-facing metadata that isn't used to assemble
// the disk itself: EXPOSEd ports and a HEALTHCHECK, if the source image
// declared them.
type Hints struct {
	ExposedPorts []string     `json:"exposedPorts,omitempty"`
	Healthcheck  *Healthcheck `json:"healthcheck,omitempty"`
}

// SupportVariant records one branch's resolved variant: which variant won,
// and (unless it was a no-op) the image its layers came from.
type SupportVariant struct {
	Branch  string `json:"branch"`
	Variant string `json:"variant"`
	Ref     string `json:"ref,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// Volume describes one volume declared in the source image, resolved to
// a name and (once known) a size.
type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// SizeBytes is omitted when the volume's size isn't known yet: no
	// io.contemper.volume.<path>.size label and no --root-size-shaped
	// override were given at convert time. deploy fails on an unsized
	// volume unless --volume <path>=<size> supplies one.
	SizeBytes int64  `json:"size,omitempty"`
	FS        string `json:"fs"`
}

// Manifest is the top-level contemper.json document.
type Manifest struct {
	FormatVersion    int         `json:"formatVersion"`
	ContemperVersion string      `json:"contemperVersion"`
	CreatedAt        time.Time   `json:"createdAt"`
	Source           ImageRef    `json:"source"`
	Support          *SupportRef `json:"support,omitempty"`
	// VolumeHelper describes the automatically merged volume-formatting
	// support image, kept separate from Support (the user's own
	// --support image, if any) since the two are independent merges:
	// Support reflects only what the user asked for, VolumeHelper only
	// what contemper added on its own because the image declares
	// volumes. See docs/reference/bundle.md for why.
	VolumeHelper *SupportRef `json:"volumeHelper,omitempty"`
	Target       string      `json:"target"`
	// Boot is how the image boots: "uki" (contemper assembled a UKI) or
	// "bootloader" (the image's own bootloader is on the ESP). Manifests
	// from before the field existed lack it; Read reports those as "uki".
	Boot         string   `json:"boot"`
	Arch         string   `json:"arch"`
	Disk         DiskInfo `json:"disk"`
	Volumes      []Volume `json:"volumes,omitempty"`
	Hints        Hints    `json:"hints"`
	Reproducible bool     `json:"reproducible"`
}

// Read parses <dir>/contemper.json.
func Read(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "contemper.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m.Boot == "" {
		m.Boot = BootUKI
	}
	if err := m.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// check rejects manifest values that deploy turns into host paths: the
// disk file, joined onto the bundle directory, must be a plain file name,
// and each volume name, which names a disk file in the instance's state
// directory, must be a valid volume name. A bundle may have been copied
// from elsewhere, so neither may point outside its directory.
func (m *Manifest) check() error {
	f := m.Disk.File
	if f == "" || f == "." || f == ".." || strings.ContainsAny(f, "/\\") {
		return fmt.Errorf("disk.file %q must be a plain file name", f)
	}
	// m.Boot is deliberately not checked against the known modes: a
	// bundle written by a newer contemper may use a mode this version
	// does not know, and reading it should still work.
	for _, v := range m.Volumes {
		if err := volume.ValidateName(v.Name); err != nil {
			return fmt.Errorf("volume %s: %w", v.Path, err)
		}
	}
	return nil
}

// Write marshals m as indented JSON to <dir>/contemper.json.
func Write(dir string, m *Manifest) error {
	out := *m
	if out.Boot == "" {
		out.Boot = BootUKI
	}
	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling contemper.json: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, "contemper.json")
	// contemper.json is part of the bundle deliverable, meant to be read
	// by whatever deploys it - same reasoning as the bundle directory
	// itself (see the MkdirAll call that creates dir, in cmd/contemper).
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G306: manifest is a bundle output meant to stay world-readable
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// SortedKeys returns the sorted keys of a string-set map, the shape
// v1.Config uses for Volumes and ExposedPorts.
func SortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
