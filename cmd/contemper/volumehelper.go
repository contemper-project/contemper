package main

import (
	"fmt"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/source"
	"github.com/contemper-project/contemper/internal/volumehelper"
)

// mergeVolumeHelper wraps volumehelper.Merge with convert's progress
// reporting, mirroring how resolveVariants wraps a --support image's own
// resolution (see docs/reference/support-image-annotations.md's
// resolution algorithm, which this equally follows).
func mergeVolumeHelper(ref string, img *source.Image, platform v1.Platform, rep *progress.Reporter) (*volumehelper.Result, error) {
	result, err := volumehelper.Merge(ref, img.Image, platform)
	if err != nil {
		return nil, err
	}

	helperLayers, err := result.Img.Image.Layers()
	if err != nil {
		return nil, fmt.Errorf("reading volume helper layers: %w", err)
	}
	rep.Line("🧰", "volume helper · "+ref, "")
	rep.Sub("✔", "rootfs", fmt.Sprintf("%d layers", len(helperLayers)))

	for i, r := range result.Resolved {
		variantLabel := r.Variant
		detail := ""
		if r.Default {
			variantLabel += " (default)"
		} else {
			detail = "matched " + strings.Join(r.Matched, ", ")
		}
		rep.Sub("✔", fmt.Sprintf("branch %s → %s", r.Branch, variantLabel), detail)
		if v := result.Variants[i]; v.Ref != "" {
			rep.SubChild("%s %s", v.Ref, platform.String())
		}
	}

	return result, nil
}
