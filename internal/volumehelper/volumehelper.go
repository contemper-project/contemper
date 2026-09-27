// Package volumehelper resolves and loads contemper's automatically
// merged volume-formatting support image: the same schema/predicate
// mechanism a user's own --support image uses (see internal/support),
// applied to a fixed, contemper-supplied image instead of one named on
// the command line.
//
// See docs/design/volumes-and-providers.md for why the helper exists and
// docs/guide/volumes.md for how it works.
package volumehelper

import (
	"fmt"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/bundle"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/source"
	"github.com/contemper-project/contemper/internal/support"
)

// Prereqs lists the host tools the volume-formatting helper needs the
// *image* to provide (the helper runs in the guest, not on the host),
// and the usual locations to look for each: mkfs.ext4 from e2fsprogs,
// and dd/od, which either busybox or coreutils installs in /bin or
// /usr/bin on every Linux contemper targets. The helper recognizes a
// volume it can reuse by reading the ext2/3/4 superblock directly with
// dd and od (see docs/guide/volumes.md) rather than shelling out to
// e2label, which lives in e2fsprogs-extra on Alpine and so isn't
// guaranteed just because mkfs.ext4 is present.
var Prereqs = []struct {
	Tool  string
	Paths []string
}{
	{"mkfs.ext4", []string{"/sbin/mkfs.ext4", "/usr/sbin/mkfs.ext4"}},
	{"dd", []string{"/bin/dd", "/usr/bin/dd"}},
	{"od", []string{"/bin/od", "/usr/bin/od"}},
}

// CheckPrereqs fails with an error naming every tool in Prereqs (and the
// paths checked for it) that isn't found somewhere in rfs, which must be
// the *source* image's own merged filesystem, before the helper's own
// layers are merged in - the helper cannot supply its own prerequisites.
func CheckPrereqs(rfs *rootfs.Rootfs) error {
	var missing []string
	for _, c := range Prereqs {
		found := false
		for _, p := range c.Paths {
			if _, err := rfs.Resolve(p); err == nil {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, fmt.Sprintf("%s (checked %s)", c.Tool, strings.Join(c.Paths, ", ")))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the volume-formatting helper needs %s, not found in the image; provide it, or skip the helper with --no-volume-helper",
			strings.Join(missing, "; "))
	}
	return nil
}

// Result is the outcome of resolving and loading ref's winning variant,
// ready to merge.
type Result struct {
	// Img is the loaded helper image; the caller must Close it.
	Img *source.Image
	// VariantImages is every winning variant's loaded image; the caller
	// must Close each.
	VariantImages []*source.Image
	// Overlays is the helper's own image followed by each winning
	// variant's image, in the order rootfs.Build should merge them.
	Overlays []v1.Image
	// Resolved is the per-branch resolution detail (Default, Matched),
	// for progress reporting; nil if ref declares no branches.
	Resolved []support.Resolved
	// Variants is Resolved rendered as manifest records.
	Variants []bundle.SupportVariant
	// Schema is ref's parsed annotation schema, so the caller can also
	// check its generic io.contemper.requires.files predicate against
	// the final merged rootfs.
	Schema *support.Schema
	// ParsedRef is ref, parsed - the caller needs it (rather than just
	// ref, the string) to redact a local source for /etc/contemper/build.
	ParsedRef source.Ref
}

// Merge resolves ref (the volume helper image) and its branches against
// srcImg's own merged filesystem, never against any already-merged
// overlay - the same rule every support-image predicate follows - and
// loads only the winning variants (manifest first, then layers). It also
// requires srcImg to satisfy CheckPrereqs, since the helper cannot
// install anything it needs itself.
//
// A ref with no declared branches (a custom --volume-helper override
// that isn't itself variant-aware) resolves with a nil Resolved/Variants
// and is merged as a plain overlay, the same as a support image with no
// annotations.
//
// On error, everything Merge opened so far is closed before it returns;
// on success that's the caller's responsibility (Result.Img and each of
// Result.VariantImages).
func Merge(ref string, srcImg v1.Image, platform v1.Platform) (*Result, error) {
	parsedRef, err := source.ParseRef(ref)
	if err != nil {
		return nil, fmt.Errorf("volume helper ref %q: %w", ref, err)
	}
	helperImg, err := source.Load(parsedRef, platform)
	if err != nil {
		return nil, fmt.Errorf("loading volume helper %s: %w", ref, err)
	}
	result := &Result{Img: helperImg, ParsedRef: parsedRef}
	ok := false
	defer func() {
		if !ok {
			result.Img.Close()
			for _, vi := range result.VariantImages {
				vi.Close()
			}
		}
	}()

	manifest, err := helperImg.Image.Manifest()
	if err != nil {
		return nil, fmt.Errorf("reading volume helper manifest: %w", err)
	}
	indexAnnotations, err := source.IndexAnnotations(parsedRef, platform)
	if err != nil {
		return nil, fmt.Errorf("reading volume helper index: %w", err)
	}
	schema, err := support.Parse(support.MergeAnnotations(indexAnnotations, manifest.Annotations))
	if err != nil {
		return nil, fmt.Errorf("volume helper %s: %w", ref, err)
	}
	result.Schema = schema

	srcRfs, err := rootfs.Build(srcImg)
	if err != nil {
		return nil, fmt.Errorf("building source rootfs for the volume helper: %w", err)
	}
	defer func() { _ = srcRfs.Close() }()

	if err := CheckPrereqs(srcRfs); err != nil {
		return nil, err
	}

	result.Overlays = append(result.Overlays, helperImg.Image)

	if len(schema.Branches) > 0 {
		resolved, err := support.Resolve(schema, func(p string) bool {
			_, err := srcRfs.Resolve(p)
			return err == nil
		})
		if err != nil {
			return nil, fmt.Errorf("%w (use --no-volume-helper to skip automatic volume formatting)", err)
		}
		result.Resolved = resolved

		for _, r := range resolved {
			sv := bundle.SupportVariant{Branch: r.Branch, Variant: r.Variant}
			if r.Image != "" {
				vref, err := source.ParseRef(r.Image)
				if err != nil {
					return nil, fmt.Errorf("branch %s: variant %s: %w", r.Branch, r.Variant, err)
				}
				vimg, err := source.Load(vref, platform)
				if err != nil {
					return nil, fmt.Errorf("branch %s: variant %s: loading %s: %w", r.Branch, r.Variant, r.Image, err)
				}
				result.VariantImages = append(result.VariantImages, vimg)
				result.Overlays = append(result.Overlays, vimg.Image)
				sv.Ref = vref.String()
				sv.Digest = vimg.Digest.String()
			}
			result.Variants = append(result.Variants, sv)
		}
	}

	ok = true
	return result, nil
}
