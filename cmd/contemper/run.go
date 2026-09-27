package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"
	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/buildinfo"
	"github.com/contemper-project/contemper/internal/bundle"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/qemu"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/source"
	"github.com/contemper-project/contemper/internal/support"
	"github.com/contemper-project/contemper/internal/target"
	"github.com/contemper-project/contemper/internal/validate"
)

type convertOptions struct {
	sourceRef    string
	target       string
	supportRef   string
	outDir       string
	arch         string
	rootSize     string
	keepRaw      bool
	quiet        bool
	verbose      bool
	progressMode string
}

type deployOptions struct {
	bundleDir    string
	to           string
	serialLog    string
	expect       string
	timeout      string
	quiet        bool
	verbose      bool
	progressMode string
}

func newReporter(w *os.File, modeStr string, verbose, quiet bool) (*progress.Reporter, error) {
	mode, err := progress.ParseMode(modeStr)
	if err != nil {
		return nil, err
	}
	return progress.New(w, mode, verbose, quiet), nil
}

func runConvert(cmd *cobra.Command, opts convertOptions) error {
	rep, err := newReporter(os.Stderr, opts.progressMode, opts.verbose, opts.quiet)
	if err != nil {
		return err
	}

	canonicalTarget, asm, err := target.Resolve(opts.target)
	if err != nil {
		return err
	}

	platform, err := source.HostPlatform(opts.arch)
	if err != nil {
		return err
	}

	var rootSizeBytes int64
	if opts.rootSize != "" {
		rootSizeBytes, err = parseSize(opts.rootSize)
		if err != nil {
			return fmt.Errorf("--root-size: %w", err)
		}
	}

	ref, err := source.ParseRef(opts.sourceRef)
	if err != nil {
		return err
	}
	img, err := source.Load(ref, platform)
	if err != nil {
		rep.Fail("resolve source", err.Error(), "")
		return err
	}
	defer img.Close()

	rep.Line("📦", opts.sourceRef, platform.String())

	cfg, err := img.Image.ConfigFile()
	if err != nil {
		rep.Fail("readiness check", err.Error(), "")
		return fmt.Errorf("reading image config: %w", err)
	}
	if err := source.CheckReady(cfg); err != nil {
		rep.Fail("readiness check", err.Error(), "")
		return err
	}
	rep.Line("✅", "contemper-ready", "")

	if opts.target == canonicalTarget {
		rep.Line("🎯", "target "+canonicalTarget, "(explicit, no alias)")
	} else {
		rep.Line("🎯", fmt.Sprintf("target %s → %s", opts.target, canonicalTarget), "")
	}
	if !img.Reproducible {
		rep.Warn("local source", "bundle is not reproducible")
	}
	rep.Blank()

	var supportImg *source.Image
	var supportRefStr string
	var schema *support.Schema
	var overlays []v1.Image
	var resolvedVariants []bundle.SupportVariant
	baseImg := img.Image

	resolvedSupportRef, supportOrigin := target.ResolveSupport(target.DefaultSupport(canonicalTarget), opts.supportRef)

	if resolvedSupportRef != "" {
		supportRef, err := source.ParseRef(resolvedSupportRef)
		if err != nil {
			return err
		}
		supportRefStr = supportRef.String()
		supportImg, err = source.Load(supportRef, platform)
		if err != nil {
			rep.Fail("support image", err.Error(), "")
			return fmt.Errorf("loading support image: %w", err)
		}
		defer supportImg.Close()
		overlays = append(overlays, supportImg.Image)

		supportManifest, err := supportImg.Image.Manifest()
		if err != nil {
			return fmt.Errorf("reading support image manifest: %w", err)
		}
		indexAnnotations, err := source.IndexAnnotations(supportRef, platform)
		if err != nil {
			return fmt.Errorf("reading support image index: %w", err)
		}
		schema, err = support.Parse(support.MergeAnnotations(indexAnnotations, supportManifest.Annotations))
		if err != nil {
			rep.Fail("support image", err.Error(), "")
			return err
		}

		supportLayers, err := supportImg.Image.Layers()
		if err != nil {
			return fmt.Errorf("reading support image layers: %w", err)
		}
		originLabel := "target default"
		if supportOrigin == target.SupportFromFlag {
			originLabel = "--support"
		}
		rep.Line("🧩", "support image · "+resolvedSupportRef, "("+originLabel+")")
		rep.Sub("✔", "rootfs", fmt.Sprintf("%d layers", len(supportLayers)))

		if len(schema.Branches) > 0 {
			vr, err := resolveVariants(schema, img, platform, rep)
			if err != nil {
				rep.Fail("support image", err.Error(), "")
				return err
			}
			defer os.RemoveAll(vr.cacheDir)
			for _, vi := range vr.images {
				defer vi.Close()
			}
			overlays = append(overlays, vr.overlays...)
			resolvedVariants = vr.resolved
			baseImg = vr.cachedSrc
		}
		rep.Blank()
	}

	sourceLayers, err := img.Image.Layers()
	if err != nil {
		return fmt.Errorf("reading source image layers: %w", err)
	}
	mergeLabel := fmt.Sprintf("merging %d %s", len(sourceLayers), pluralize(len(sourceLayers), "layer"))
	if len(overlays) > 0 {
		overlayLayers := 0
		for _, o := range overlays {
			ls, _ := o.Layers()
			overlayLayers += len(ls)
		}
		mergeLabel = fmt.Sprintf("merging %d + %d layers", len(sourceLayers), overlayLayers)
	}
	mergeStage := rep.BeginStage("🧬", mergeLabel)

	rfs, err := rootfs.Build(baseImg, overlays...)
	if err != nil {
		mergeStage.Fail("merge", err.Error(), "")
		return err
	}
	defer rfs.Close()
	mergeStage.Done("🧬", mergeLabel, "")

	if schema != nil {
		if err := schema.CheckRequires(rfs); err != nil {
			rep.Fail("support image", err.Error(), "")
			return err
		}
	}

	val, err := validate.Validate(rfs)
	if err != nil {
		rep.Fail("validate", err.Error(), "a contemper-ready image must provide a kernel, initrd, cmdline and init at the fixed paths")
		return err
	}
	reportFixedPaths(rep, rfs, val)
	rep.Line("✅", "requirements satisfied", "kernel · initrd · init")
	rep.Blank()

	bundleName := fmt.Sprintf("%s-%s.%s", img.RepoBase, img.Tag, machineArch(platform.Architecture))
	outBundleDir := filepath.Join(opts.outDir, bundleName)
	if err := os.MkdirAll(outBundleDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outBundleDir, err)
	}

	diskInfo, warnings, err := asm.Assemble(rfs, val, platform.Architecture, outBundleDir, target.Options{
		RootSizeBytes: rootSizeBytes,
		KeepRaw:       opts.keepRaw,
		Progress:      rep,
	})
	if err != nil {
		return err
	}
	for _, w := range warnings {
		rep.Warn("warning", w)
	}

	manifest := &bundle.Manifest{
		FormatVersion:    bundle.FormatVersion,
		ContemperVersion: buildinfo.Get().Version,
		CreatedAt:        time.Now().UTC(),
		Source: bundle.ImageRef{
			Ref:    ref.String(),
			Digest: img.Digest.String(),
		},
		Target: canonicalTarget,
		Arch:   platform.Architecture,
		Disk: bundle.DiskInfo{
			File:      diskInfo.Filename,
			Format:    diskInfo.Format,
			SizeBytes: diskInfo.SizeBytes,
			SHA256:    diskInfo.SHA256,
		},
		Volumes:      bundle.SortedKeys(cfg.Config.Volumes),
		Hints:        hintsFrom(cfg),
		Reproducible: img.Reproducible,
	}
	if supportImg != nil {
		manifest.Support = &bundle.SupportRef{
			Ref:    supportRefStr,
			Digest: supportImg.Digest.String(),
			Origin: string(supportOrigin),
		}
		manifest.SupportVariants = resolvedVariants
	}

	if err := bundle.Write(outBundleDir, manifest); err != nil {
		return err
	}

	rep.Blank()
	rep.Line("📦", bundleName+"/", "disk + manifest")
	elapsed := rep.Elapsed().Round(100 * time.Millisecond)
	rep.Finish("bundle ready", fmt.Sprintf("%-16s%s", progress.HumanBytes(diskInfo.SizeBytes), elapsed))

	fmt.Fprintln(cmd.OutOrStdout(), outBundleDir)
	return nil
}

// reportFixedPaths prints one ✔ sub-line per fixed path, showing what a
// symlink resolved to and the file's size, mirroring the doc's
// "✔ /boot/contemper/vmlinuz → /boot/vmlinuz-virt  9.8 MiB" example.
func reportFixedPaths(rep *progress.Reporter, rfs *rootfs.Rootfs, val *validate.Result) {
	reportOne := func(fixedPath string, size int64) {
		e, err := rfs.Resolve(fixedPath)
		if err != nil {
			return
		}
		label := fixedPath
		if e.Path != fixedPath {
			label = fixedPath + " → " + e.Path
		}
		rep.Sub("✔", label, progress.HumanBytes(size))
	}
	reportOne(validate.KernelPath, int64(len(val.Kernel)))
	reportOne(validate.InitrdPath, int64(len(val.Initrd)))
	if val.Cmdline != "" {
		rep.Sub("✔", validate.CmdlinePath, strconv.Quote(val.Cmdline))
	} else {
		rep.Sub("✔", validate.CmdlinePath, "(optional, not set)")
	}
	rep.Sub("└", "kernel command line", strconv.Quote(target.KernelCmdline(val.Cmdline)))
	rep.Sub("✔", validate.InitPath, "")
	if val.OSRelease != nil {
		rep.Sub("✔", validate.OSReleasePath, "")
	}
}

// variantResolution is what resolveVariants hands back to runConvert: the
// winning variants' bundle records and layers to merge, the loaded
// *source.Image handles the caller must Close, the cache directory the
// caller must remove, and the cache-wrapped source image the final
// rootfs.Build call should use in place of img.Image, so the source
// layers already read once for predicate evaluation aren't pulled again
// from the registry for the final merge.
type variantResolution struct {
	resolved  []bundle.SupportVariant
	overlays  []v1.Image
	images    []*source.Image
	cacheDir  string
	cachedSrc v1.Image
}

// resolveVariants evaluates schema's branches against img's own merged
// filesystem - built before any support-image layers are merged, so a
// support image (or a sibling variant) can never satisfy its own
// predicates - prints the per-branch progress lines to rep, and loads
// only the winning variants' images (manifest first, then layers;
// losing variants are never fetched).
//
// On error, everything opened so far (the layer cache directory, any
// variant images already loaded) is cleaned up before returning; on
// success that cleanup is the caller's responsibility.
func resolveVariants(schema *support.Schema, img *source.Image, platform v1.Platform, rep *progress.Reporter) (*variantResolution, error) {
	cacheDir, err := os.MkdirTemp("", "contemper-layer-cache-")
	if err != nil {
		return nil, fmt.Errorf("creating layer cache dir: %w", err)
	}
	vr := &variantResolution{
		cacheDir:  cacheDir,
		cachedSrc: cache.Image(img.Image, cache.NewFilesystemCache(cacheDir)),
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(vr.cacheDir)
			for _, vi := range vr.images {
				vi.Close()
			}
		}
	}()

	srcRfs, err := rootfs.Build(vr.cachedSrc)
	if err != nil {
		return nil, fmt.Errorf("building source rootfs for variant resolution: %w", err)
	}
	defer srcRfs.Close()

	branches, err := support.Resolve(schema, func(p string) bool {
		_, err := srcRfs.Resolve(p)
		return err == nil
	})
	if err != nil {
		return nil, err
	}

	for _, r := range branches {
		variantLabel := r.Variant
		detail := ""
		if r.Default {
			variantLabel += " (default)"
		} else {
			detail = "matched " + strings.Join(r.Matched, ", ")
		}
		rep.Sub("✔", fmt.Sprintf("branch %s → %s", r.Branch, variantLabel), detail)

		sv := bundle.SupportVariant{Branch: r.Branch, Variant: r.Variant}
		if r.Image != "" {
			variantRef, err := source.ParseRef(r.Image)
			if err != nil {
				return nil, fmt.Errorf("branch %s: variant %s: %w", r.Branch, r.Variant, err)
			}
			variantImg, err := source.Load(variantRef, platform)
			if err != nil {
				return nil, fmt.Errorf("branch %s: variant %s: loading %s: %w", r.Branch, r.Variant, r.Image, err)
			}
			vr.images = append(vr.images, variantImg)
			vr.overlays = append(vr.overlays, variantImg.Image)
			rep.SubChild("%s %s", variantRef.String(), platform.String())
			sv.Ref = variantRef.String()
			sv.Digest = variantImg.Digest.String()
		}
		vr.resolved = append(vr.resolved, sv)
	}

	ok = true
	return vr, nil
}

func hintsFrom(cfg *v1.ConfigFile) bundle.Hints {
	h := bundle.Hints{ExposedPorts: bundle.SortedKeys(cfg.Config.ExposedPorts)}
	if hc := cfg.Config.Healthcheck; hc != nil {
		h.Healthcheck = &bundle.Healthcheck{
			Test:        hc.Test,
			Interval:    hc.Interval.String(),
			Timeout:     hc.Timeout.String(),
			StartPeriod: hc.StartPeriod.String(),
			Retries:     hc.Retries,
		}
	}
	return h
}

func runDeploy(cmd *cobra.Command, opts deployOptions) error {
	rep, err := newReporter(os.Stderr, opts.progressMode, opts.verbose, opts.quiet)
	if err != nil {
		return err
	}

	if opts.to != "local-qemu" {
		return fmt.Errorf("unknown deploy target %q (only local-qemu is supported)", opts.to)
	}

	manifest, err := bundle.Read(opts.bundleDir)
	if err != nil {
		return err
	}

	var timeout time.Duration
	if opts.timeout != "" {
		timeout, err = time.ParseDuration(opts.timeout)
		if err != nil {
			return fmt.Errorf("--timeout: %w", err)
		}
	}

	return qemu.Deploy(qemu.Options{
		Arch:          manifest.Arch,
		DiskPath:      filepath.Join(opts.bundleDir, manifest.Disk.File),
		DiskFormat:    manifest.Disk.Format,
		SerialLogPath: opts.serialLog,
		Expect:        opts.expect,
		Timeout:       timeout,
		Progress:      rep,
	})
}

// pluralize returns unit or unit+"s" depending on n, for the common case
// of a regular plural.
func pluralize(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return unit + "s"
}

// parseSize parses a human size like "2GiB", "512MiB", "1073741824" into
// bytes.
func parseSize(s string) (int64, error) {
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
			return int64(n * float64(sfx.mult)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n, nil
}

// machineArch maps an OCI architecture to the machine name used in bundle
// directory names (as in uname -m and the UEFI/qemu naming).
func machineArch(ociArch string) string {
	switch ociArch {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	}
	return ociArch
}
