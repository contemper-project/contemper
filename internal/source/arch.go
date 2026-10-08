package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// SupportedArchs lists the architectures contemper can convert for, in
// the stable order used everywhere a set of them is reported.
var SupportedArchs = []string{"amd64", "arm64"}

// ArchSelection is a parsed --arch value.
type ArchSelection struct {
	// All is set for "all": every supported architecture the source
	// provides.
	All bool
	// Archs are the explicitly requested architectures, in the stable
	// SupportedArchs order. Empty when All is set.
	Archs []string
	// Group is set when the user asked for a set of architectures (a
	// list, or "all") rather than one, even if it ends up resolving to a
	// single architecture. It is what makes convert write a group file.
	Group bool
}

// ParseArchSelection parses a --arch value: empty (the host
// architecture), one architecture, a comma-separated list, or "all". The
// list is normalised to SupportedArchs order; duplicates, empty items
// and unknown names are errors.
func ParseArchSelection(value string) (ArchSelection, error) {
	if value == "" {
		p, err := HostPlatform("")
		if err != nil {
			return ArchSelection{}, err
		}
		return ArchSelection{Archs: []string{p.Architecture}}, nil
	}
	if value == "all" {
		return ArchSelection{All: true, Group: true}, nil
	}
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		if item == "" {
			return ArchSelection{}, fmt.Errorf("--arch %q: empty item in the list", value)
		}
		if _, err := HostPlatform(item); err != nil {
			return ArchSelection{}, fmt.Errorf("--arch %q: unknown architecture %q (want amd64, arm64, a comma-separated list of them, or all)", value, item)
		}
		if seen[item] {
			return ArchSelection{}, fmt.Errorf("--arch %q: %s listed more than once", value, item)
		}
		seen[item] = true
	}
	sel := ArchSelection{Group: strings.Contains(value, ",")}
	for _, a := range SupportedArchs {
		if seen[a] {
			sel.Archs = append(sel.Archs, a)
		}
	}
	return sel, nil
}

// ErrPlatformsUnknown is returned by Platforms for a source whose
// platforms can't be listed without loading it (a docker-daemon: source,
// which would need a docker save), or that is a single image whose
// config doesn't say which architecture it is (a legacy docker-archive,
// or a layout with one image and no platform metadata). Load accepts
// such an image for whatever architecture is asked for.
var ErrPlatformsUnknown = errors.New("platforms of this source can't be listed without loading it")

// Resolve turns the selection into the platforms to convert, checking
// them against the source's own platforms before anything is fetched.
// An explicit architecture the source lacks is an error; "all" means
// every supported architecture the source provides, and is an error if
// that is none. A source whose platforms can't be listed
// (ErrPlatformsUnknown) is accepted for exactly one explicit
// architecture only, which Load then checks as usual.
func (s ArchSelection) Resolve(ctx context.Context, ref Ref) ([]v1.Platform, error) {
	if !s.All && len(s.Archs) == 1 && !s.Group {
		// The single-architecture path is unchanged: Load itself
		// reports a missing platform.
		return []v1.Platform{{OS: "linux", Architecture: s.Archs[0]}}, nil
	}
	have, err := Platforms(ctx, ref)
	if errors.Is(err, ErrPlatformsUnknown) {
		// Only reachable for "all" or a list: a single architecture
		// returned above.
		if ref.Kind == KindDockerDaemon {
			return nil, fmt.Errorf("docker-daemon: sources convert one architecture per run; pass --arch amd64 or --arch arm64 (or push or `docker save` the multi-platform image and convert that)")
		}
		return nil, fmt.Errorf("%s has no platform metadata to choose architectures from; pass --arch with one architecture", ref.String())
	}
	if err != nil {
		return nil, err
	}
	haveArch := map[string]bool{}
	for _, p := range have {
		if p.OS == "linux" {
			haveArch[p.Architecture] = true
		}
	}
	var out []v1.Platform
	if s.All {
		for _, a := range SupportedArchs {
			if haveArch[a] {
				out = append(out, v1.Platform{OS: "linux", Architecture: a})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s has no linux/amd64 or linux/arm64 platform (found: %s)", ref.String(), describePlatforms(have))
		}
		return out, nil
	}
	for _, a := range s.Archs {
		if !haveArch[a] {
			return nil, fmt.Errorf("%s has no linux/%s platform (found: %s)", ref.String(), a, describePlatforms(have))
		}
		out = append(out, v1.Platform{OS: "linux", Architecture: a})
	}
	return out, nil
}

func describePlatforms(ps []v1.Platform) string {
	if len(ps) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.OS+"/"+p.Architecture)
	}
	return strings.Join(parts, ", ")
}

// Platforms lists the platforms ref provides, sorted and without
// duplicates (other OSes included, so errors can name what was found).
// It reads only index and (for an image without platform metadata on its
// descriptor) config data, never layers. Attestation manifests are
// ignored. A platform is listed only if Load would find it, since both
// walk the source's descriptors the same way. A docker-daemon: source,
// or a single image that doesn't say which architecture it is, yields
// ErrPlatformsUnknown.
func Platforms(ctx context.Context, ref Ref) ([]v1.Platform, error) {
	switch ref.Kind {
	case KindRegistry:
		nref, err := name.ParseReference(ref.Value)
		if err != nil {
			return nil, fmt.Errorf("parsing registry ref %q: %w", ref.Value, err)
		}
		desc, err := remote.Get(nref, registryOptions(ctx, ref.Anonymous)...)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", ref.Value, err)
		}
		if desc.MediaType.IsIndex() {
			idx, err := desc.ImageIndex()
			if err != nil {
				return nil, fmt.Errorf("reading index %s: %w", ref.Value, err)
			}
			return platformsOfIndex(idx, ref.Value)
		}
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("reading image %s: %w", ref.Value, err)
		}
		return platformsOfImage(img, ref.Value)
	case KindOCILayout:
		return layoutPlatforms(ref.Value)
	case KindOCIArchive:
		tmpDir, err := os.MkdirTemp("", "contemper-oci-archive-")
		if err != nil {
			return nil, fmt.Errorf("creating temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()
		f, err := os.Open(ref.Value)
		if err != nil {
			return nil, fmt.Errorf("opening oci-archive %q: %w", ref.Value, err)
		}
		defer func() { _ = f.Close() }()
		// Listing needs only index.json and the small manifest and
		// config blobs; stream past the layers without writing them.
		if err := extractTarLimited(ctx, f, tmpDir, maxMetadataBlob); err != nil {
			return nil, fmt.Errorf("reading oci-archive %q: %w", ref.Value, err)
		}
		return layoutPlatforms(tmpDir)
	case KindDockerArchive:
		img, err := tarball.Image(func() (io.ReadCloser, error) { return os.Open(ref.Value) }, nil)
		if err != nil {
			return nil, fmt.Errorf("reading docker-archive %q: %w", ref.Value, err)
		}
		return platformsOfImage(img, ref.Value)
	case KindDockerDaemon:
		return nil, ErrPlatformsUnknown
	default:
		return nil, fmt.Errorf("unknown source kind %q", ref.Kind)
	}
}

func layoutPlatforms(dir string) ([]v1.Platform, error) {
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return nil, fmt.Errorf("reading OCI layout %q: %w", dir, err)
	}
	return platformsOfIndex(idx, dir)
}

// platformsOfImage reports the platform an image's own config names. A
// config without an architecture (a legacy docker-archive) says nothing
// useful: Load accepts such an image for any requested architecture, so
// it is reported as ErrPlatformsUnknown. A missing OS is taken as linux
// for the same reason Load ignores it.
func platformsOfImage(img v1.Image, name string) ([]v1.Platform, error) {
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading config of %q: %w", name, err)
	}
	if cfg.Architecture == "" {
		return nil, fmt.Errorf("%w: %q doesn't say which architecture it is", ErrPlatformsUnknown, name)
	}
	osName := cfg.OS
	if osName == "" {
		osName = "linux"
	}
	return []v1.Platform{{OS: osName, Architecture: cfg.Architecture}}, nil
}

// platformsOfIndex lists the platforms of idx. The candidates are the
// platforms on its descriptors (nested indexes included), but each is
// kept only if resolveDescriptor, the walk Load uses, finds an image for
// it, so what is listed is exactly what can be loaded. An index whose
// images carry no platform metadata is accepted only when it holds a
// single image (as Load does), whose config then says what it is.
func platformsOfIndex(idx v1.ImageIndex, name string) ([]v1.Platform, error) {
	var candidates []v1.Platform
	if err := collectPlatforms(idx, name, 0, &candidates); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		im, err := idx.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("reading index manifest of %q: %w", name, err)
		}
		if len(im.Manifests) == 0 {
			return nil, fmt.Errorf("%q has no manifests", name)
		}
		leaf, leafIndex, _, _, err := resolveDescriptor(idx, v1.Platform{OS: "linux", Architecture: "amd64"}, name)
		if err != nil {
			return nil, fmt.Errorf("%q lists images without platform metadata, so they can't be told apart", name)
		}
		img, err := leafIndex.Image(leaf.Digest)
		if err != nil {
			return nil, fmt.Errorf("reading image %s of %q: %w", leaf.Digest, name, err)
		}
		return platformsOfImage(img, name)
	}
	var found []v1.Platform
	for _, p := range normalizePlatforms(candidates) {
		if _, _, _, _, err := resolveDescriptor(idx, p, name); err == nil {
			found = append(found, p)
		}
	}
	return found, nil
}

// collectPlatforms gathers every platform named by a non-attestation
// descriptor of idx or of an index nested in it.
func collectPlatforms(idx v1.ImageIndex, name string, depth int, out *[]v1.Platform) error {
	if depth > maxIndexDepth {
		return fmt.Errorf("%q: image index nesting exceeds %d levels", name, maxIndexDepth)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("reading index manifest of %q: %w", name, err)
	}
	for _, d := range im.Manifests {
		if isAttestationManifest(d) {
			continue
		}
		if d.MediaType.IsIndex() {
			nested, err := idx.ImageIndex(d.Digest)
			if err != nil {
				return fmt.Errorf("reading nested index %s of %q: %w", d.Digest, name, err)
			}
			if err := collectPlatforms(nested, name, depth+1, out); err != nil {
				return err
			}
		}
		if d.Platform != nil {
			*out = append(*out, *d.Platform)
		}
	}
	return nil
}

// normalizePlatforms drops duplicates and sorts by OS, then
// architecture.
func normalizePlatforms(in []v1.Platform) []v1.Platform {
	seen := map[[2]string]bool{}
	var out []v1.Platform
	for _, p := range in {
		k := [2]string{p.OS, p.Architecture}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v1.Platform{OS: p.OS, Architecture: p.Architecture})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		return out[i].Architecture < out[j].Architecture
	})
	return out
}
