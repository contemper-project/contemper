package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"
	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/buildinfo"
	"github.com/contemper-project/contemper/internal/buildx"
	"github.com/contemper-project/contemper/internal/bundle"
	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/guestmeta"
	"github.com/contemper-project/contemper/internal/limits"
	"github.com/contemper-project/contemper/internal/localqemu"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/qemu"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/source"
	"github.com/contemper-project/contemper/internal/support"
	"github.com/contemper-project/contemper/internal/target"
	"github.com/contemper-project/contemper/internal/validate"
	"github.com/contemper-project/contemper/internal/volume"
	"github.com/contemper-project/contemper/internal/volumehelper"
)

type convertOptions struct {
	sourceRef    string
	target       string
	supportRef   string
	outDir       string
	arch         string
	rootSize     string
	maxRootfs    string
	noFstab      bool
	volumeHelper string
	noVolHelper  bool
	keepRaw      bool
	quiet        bool
	verbose      bool
	progressMode string
}

type buildOptions struct {
	context   string
	file      string
	tag       string
	buildArgs []string
	imageOnly bool
	// convert carries every flag `build` shares with `convert` (see
	// registerConvertFlags); convert.arch also selects the buildx
	// --platform, and convert.sourceRef is filled in by runBuild once
	// the tag is known, from "docker-daemon:<tag>".
	convert convertOptions
}

type deployOptions struct {
	bundleDir    string
	arch         string
	to           string
	name         string
	volumes      []string
	serialLog    string
	expect       string
	timeout      string
	quiet        bool
	verbose      bool
	progressMode string
}

func newReporter(ctx context.Context, w *os.File, modeStr string, verbose, quiet bool) (*progress.Reporter, error) {
	mode, err := progress.ParseMode(modeStr)
	if err != nil {
		return nil, err
	}
	rep := progress.New(w, mode, verbose, quiet)
	rep.SetContext(ctx)
	return rep, nil
}

func runConvert(ctx context.Context, cmd *cobra.Command, opts convertOptions) error {
	rep, err := newReporter(ctx, os.Stderr, opts.progressMode, opts.verbose, opts.quiet)
	if err != nil {
		return err
	}

	if opts.noVolHelper && opts.volumeHelper != "" {
		return fmt.Errorf("--volume-helper and --no-volume-helper are mutually exclusive")
	}

	canonicalTarget, asm, err := target.Resolve(opts.target)
	if err != nil {
		return err
	}

	sel, err := source.ParseArchSelection(opts.arch)
	if err != nil {
		return err
	}

	var rootSizeBytes int64
	if opts.rootSize != "" {
		rootSizeBytes, err = volume.ParseSize(opts.rootSize)
		if err != nil {
			return fmt.Errorf("--root-size: %w", err)
		}
	}

	ctx, err = withContentLimits(ctx, opts.maxRootfs)
	if err != nil {
		return err
	}

	ref, err := source.ParseRef(opts.sourceRef)
	if err != nil {
		return err
	}
	// Check every requested architecture against the source's index
	// before fetching or converting anything.
	platforms, err := sel.Resolve(ctx, ref)
	if err != nil {
		rep.Fail("resolve source", err.Error(), "")
		return err
	}

	// A single architecture reports exactly as it always has; several
	// get a section header each and their own elapsed time.
	multi := len(platforms) > 1
	// Drop a group file left by an earlier run as soon as the bundle name
	// (and so the group file's name) is known, so a failure part-way
	// can't leave one describing bundles that have since changed. Every
	// run does this, a single-architecture one too: its bundle replaces
	// one of the group's. A failure before the first image is loaded
	// (an unreadable source, a missing architecture) leaves it alone.
	var groupName string
	onNamed := func(name string) error {
		if groupName != "" {
			// A later architecture of a group run.
			if name != groupName {
				return fmt.Errorf("the architectures resolve to different bundle names (%s and %s), so one group file can't describe them", groupName, name)
			}
			return nil
		}
		groupName = name
		path := filepath.Join(opts.outDir, bundle.GroupFileName(name))
		if _, err := os.Stat(path); err != nil {
			return nil
		}
		if err := bundle.RemoveGroup(opts.outDir, name); err != nil {
			return err
		}
		rep.Sub("✔", "removed stale group file", filepath.Base(path))
		return nil
	}
	var converted []convertedBundle
	for i, platform := range platforms {
		var priorElapsed time.Duration
		if multi {
			if i > 0 {
				rep.Blank()
			}
			priorElapsed = rep.Elapsed()
			rep.Line("🔨", "architecture "+platform.Architecture, fmt.Sprintf("%d of %d", i+1, len(platforms)))
			rep.Blank()
		}
		res, err := convertPlatform(ctx, rep, opts, ref, platform, canonicalTarget, asm, rootSizeBytes, priorElapsed, onNamed)
		if err != nil {
			if multi {
				return fmt.Errorf("converting linux/%s: %w", platform.Architecture, err)
			}
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), res.dir)
		converted = append(converted, res)
	}

	if sel.Group {
		group := &bundle.Group{
			FormatVersion: bundle.GroupFormatVersion,
			Source:        bundle.ImageRef{Ref: converted[0].source.Ref, Repo: converted[0].source.Repo},
		}
		for _, res := range converted {
			group.Bundles = append(group.Bundles, bundle.GroupBundle{Arch: res.arch, Path: filepath.Base(res.dir)})
		}
		groupPath, err := bundle.WriteGroup(opts.outDir, converted[0].name, group)
		if err != nil {
			return err
		}
		rep.Blank()
		rep.Line("📚", filepath.Base(groupPath), fmt.Sprintf("%d %s", len(converted), pluralize(len(converted), "bundle")))
	}
	return nil
}

// convertedBundle describes one finished bundle of a conversion run.
type convertedBundle struct {
	// dir is the bundle directory (inside --out).
	dir string
	// name is the bundle's name without its ".<machine-arch>" suffix,
	// which names the group file of a multi-architecture run.
	name   string
	arch   string
	source bundle.ImageRef
}

// convertPlatform converts ref's image for one platform into a bundle
// and describes the bundle it wrote. It is a complete, independent
// conversion: the same support-image and volume-helper resolution,
// validation, bundle naming and manifest a single-architecture run
// produces. priorElapsed is subtracted from the reporter's clock for the
// "bundle ready" timing, so each architecture of a multi-arch run
// reports its own. onNamed is called with the bundle's name
// (see convertedBundle.name) once the source is loaded.
func convertPlatform(ctx context.Context, rep *progress.Reporter, opts convertOptions, ref source.Ref, platform v1.Platform, canonicalTarget string, asm target.Assembler, rootSizeBytes int64, priorElapsed time.Duration, onNamed func(name string) error) (convertedBundle, error) {
	img, err := source.Load(ctx, ref, platform)
	if err != nil {
		rep.Fail("resolve source", err.Error(), "")
		return convertedBundle{}, err
	}
	defer img.Close()

	rep.Line("📦", opts.sourceRef, platform.String())
	if err := onNamed(fmt.Sprintf("%s-%s", img.RepoBase, img.Tag)); err != nil {
		return convertedBundle{}, err
	}

	cfg, err := img.Image.ConfigFile()
	if err != nil {
		rep.Fail("readiness check", err.Error(), "")
		return convertedBundle{}, fmt.Errorf("reading image config: %w", err)
	}
	if err := source.CheckReady(cfg); err != nil {
		rep.Fail("readiness check", err.Error(), "")
		return convertedBundle{}, err
	}
	rep.Line("✅", "contemper-ready", "")

	bootMode, err := source.BootMode(cfg)
	if err != nil {
		rep.Fail("boot mode", err.Error(), "")
		return convertedBundle{}, err
	}
	if bootMode == source.BootBootloader {
		rep.Line("✅", "boot mode", "bootloader")
	}

	secureBoot, err := source.SecureBoot(cfg, bootMode)
	if err != nil {
		rep.Fail("secure boot", err.Error(), "")
		return convertedBundle{}, err
	}
	if secureBoot {
		rep.Line("✅", "secure boot", "requested")
	}

	specs, err := volume.FromConfig(bundle.SortedKeys(cfg.Config.Volumes), cfg.Config.Labels)
	if err != nil {
		rep.Fail("volumes", err.Error(), "")
		return convertedBundle{}, err
	}
	if bootMode == source.BootBootloader {
		for _, sp := range specs {
			if coversESPMountPoint(sp.Path) {
				err := fmt.Errorf("volume %s covers the ESP mount point %s: a volume there would hide or double-mount the ESP", sp.Path, guestmeta.ESPMountPoint)
				rep.Fail("volumes", err.Error(), "in bootloader mode contemper mounts the ESP at "+guestmeta.ESPMountPoint+"; declare the volume at a path that is not "+guestmeta.ESPMountPoint+" or one of its parents")
				return convertedBundle{}, err
			}
		}
	}
	if rootSizeBytes == 0 {
		rootSizeBytes, err = volume.RootSize(cfg.Config.Labels)
		if err != nil {
			rep.Fail("volumes", err.Error(), "")
			return convertedBundle{}, err
		}
	}

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
	var supportRefStr, supportRefForBuild string
	var schema *support.Schema
	var overlays []v1.Image
	var resolvedVariants []bundle.SupportVariant
	baseImg := img.Image

	resolvedSupportRef, supportOrigin := target.ResolveSupport(target.DefaultSupport(canonicalTarget), opts.supportRef)

	if resolvedSupportRef != "" {
		supportRef, err := source.ParseRef(resolvedSupportRef)
		if err != nil {
			return convertedBundle{}, err
		}
		supportRefStr = supportRef.String()
		supportRefForBuild = redactedRefString(supportRef)
		supportImg, err = source.Load(ctx, supportRef, platform)
		if err != nil {
			rep.Fail("support image", err.Error(), "")
			return convertedBundle{}, fmt.Errorf("loading support image: %w", err)
		}
		defer supportImg.Close()
		overlays = append(overlays, supportImg.Image)

		supportManifest, err := supportImg.Image.Manifest()
		if err != nil {
			return convertedBundle{}, fmt.Errorf("reading support image manifest: %w", err)
		}
		indexAnnotations, err := source.IndexAnnotations(ctx, supportRef, platform)
		if err != nil {
			return convertedBundle{}, fmt.Errorf("reading support image index: %w", err)
		}
		schema, err = support.Parse(support.MergeAnnotations(indexAnnotations, supportManifest.Annotations))
		if err != nil {
			rep.Fail("support image", err.Error(), "")
			return convertedBundle{}, err
		}

		supportLayers, err := supportImg.Image.Layers()
		if err != nil {
			return convertedBundle{}, fmt.Errorf("reading support image layers: %w", err)
		}
		originLabel := "target default"
		if supportOrigin == target.SupportFromFlag {
			originLabel = "--support"
		}
		rep.Line("🧩", "support image · "+resolvedSupportRef, "("+originLabel+")")
		rep.Sub("✔", "rootfs", fmt.Sprintf("%d %s · %s download", len(supportLayers), pluralize(len(supportLayers), "layer"), progress.HumanBytes(manifestDownloadSize(supportManifest))))

		if len(schema.Branches) > 0 {
			vr, err := resolveVariants(ctx, schema, supportRef, img, platform, rep)
			if err != nil {
				rep.Fail("support image", err.Error(), "")
				return convertedBundle{}, err
			}
			defer func() { _ = os.RemoveAll(vr.cacheDir) }()
			for _, vi := range vr.images {
				defer vi.Close()
			}
			overlays = append(overlays, vr.overlays...)
			resolvedVariants = vr.resolved
			baseImg = vr.cachedSrc
		}
		rep.Blank()
	}

	// The volume-formatting helper merges in addition to any --support
	// image, after it and its resolved variants, so a user's own support
	// customizations are never shadowed by it.
	var helperResult *volumehelper.Result
	switch {
	case len(specs) == 0 && opts.volumeHelper != "":
		rep.Warn("volume helper", "--volume-helper given but the image declares no volumes; ignoring")
	case len(specs) > 0 && !opts.noVolHelper:
		helperRef := opts.volumeHelper
		if helperRef == "" {
			helperRef = volume.DefaultHelperRef
		}
		hr, err := mergeVolumeHelper(ctx, helperRef, img, platform, rep)
		if err != nil {
			rep.Fail("volume helper", err.Error(), "")
			return convertedBundle{}, err
		}
		helperResult = hr
		defer helperResult.Img.Close()
		for _, vi := range helperResult.VariantImages {
			defer vi.Close()
		}
		overlays = append(overlays, helperResult.Overlays...)
		rep.Blank()
	}

	// overlayReportLabels lines up positionally with overlays (and so with
	// rootfs.Build's resulting OverlayStats): support image, then each of
	// its winning variants, then the volume helper and each of its own.
	var overlayReportLabels []string
	if resolvedSupportRef != "" {
		overlayReportLabels = append(overlayReportLabels, overlayLabels("support image", resolvedVariants)...)
	}
	if helperResult != nil {
		overlayReportLabels = append(overlayReportLabels, overlayLabels("volume helper", helperResult.Variants)...)
	}

	sourceLayers, err := img.Image.Layers()
	if err != nil {
		return convertedBundle{}, fmt.Errorf("reading source image layers: %w", err)
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

	rfs, err := rootfs.Build(ctx, baseImg, overlays...)
	if err != nil {
		mergeStage.Fail("merge", err.Error(), "")
		return convertedBundle{}, err
	}
	defer func() { _ = rfs.Close() }()
	mergeStage.Done("🧬", mergeLabel, "")
	reportOverlayStats(rep, overlayReportLabels, rfs.OverlayStats)

	if schema != nil {
		if err := schema.CheckRequires(rfs); err != nil {
			rep.Fail("support image", err.Error(), "")
			return convertedBundle{}, err
		}
	}
	if helperResult != nil && helperResult.Schema != nil {
		if err := helperResult.Schema.CheckRequires(rfs); err != nil {
			rep.Fail("volume helper", err.Error(), "")
			return convertedBundle{}, err
		}
	}

	buildInfo := guestmeta.BuildInfo{
		ContemperVersion: buildinfo.Get().Version,
		Target:           canonicalTarget,
		Arch:             platform.Architecture,
		Source:           guestmeta.ImageRef{Ref: redactedRefString(ref), Digest: img.Digest.String()},
	}
	if supportImg != nil {
		buildInfo.Support = &guestmeta.ImageRef{Ref: supportRefForBuild, Digest: supportImg.Digest.String()}
		for _, v := range resolvedVariants {
			buildInfo.SupportVariants = append(buildInfo.SupportVariants, guestmeta.VariantRef{
				Branch: v.Branch, Variant: v.Variant, Ref: redactedVariantRef(v.Ref), Digest: v.Digest,
			})
		}
	}
	if helperResult != nil {
		buildInfo.VolumeHelper = &guestmeta.ImageRef{
			Ref:    redactedRefString(helperResult.ParsedRef),
			Digest: helperResult.Img.Digest.String(),
		}
		for _, v := range helperResult.Variants {
			buildInfo.VolumeHelperVariants = append(buildInfo.VolumeHelperVariants, guestmeta.VariantRef{
				Branch: v.Branch, Variant: v.Variant, Ref: redactedVariantRef(v.Ref), Digest: v.Digest,
			})
		}
	}
	if err := rfs.WriteFile(guestmeta.BuildPath, guestmeta.RenderBuild(buildInfo)); err != nil {
		return convertedBundle{}, fmt.Errorf("writing %s: %w", guestmeta.BuildPath, err)
	}

	fstabOptedOut := opts.noFstab || volume.FstabOptedOut(cfg.Config.Labels)
	var fstabLines []string
	if len(specs) > 0 {
		rep.Line("💾", fmt.Sprintf("%d %s declared", len(specs), pluralize(len(specs), "volume")), "")
		var lines []guestmeta.VolumeLine
		// Every mount point is created before any volume's seed report
		// below, so a volume nested under another one (/data/sub under
		// /data) counts as content of the outer volume: the guest helper
		// copies that mount point onto the outer volume too, which is
		// what lets the nested one mount on top of it.
		for _, s := range specs {
			if err := rfs.EnsureDir(s.Path); err != nil {
				return convertedBundle{}, fmt.Errorf("creating mount point %s: %w", s.Path, err)
			}
		}
		for _, s := range specs {
			detail := "unsized"
			if s.SizeBytes > 0 {
				detail = progress.HumanBytes(s.SizeBytes)
			}
			rep.Sub("✔", s.Path+" → "+s.Name, detail)
			rep.SubChild("%s", seedReportLine(s, rfs.DirHasContent(s.Path)))
			lines = append(lines, guestmeta.VolumeLine{
				Name: s.Name, SerialPattern: target.SerialPattern(canonicalTarget, s.Name), FS: "ext4", Mountpoint: s.Path,
			})
			fstabLines = append(fstabLines, guestmeta.FstabLine(s.Name, s.Path))
		}
		if err := rfs.WriteFile(guestmeta.VolumesPath, guestmeta.RenderVolumes(lines)); err != nil {
			return convertedBundle{}, fmt.Errorf("writing %s: %w", guestmeta.VolumesPath, err)
		}
		if names := noSeedVolumeNames(specs); len(names) > 0 {
			if err := rfs.WriteFile(guestmeta.NoSeedPath, guestmeta.RenderNoSeed(names)); err != nil {
				return convertedBundle{}, fmt.Errorf("writing %s: %w", guestmeta.NoSeedPath, err)
			}
		}

		if fstabOptedOut {
			rep.Sub("·", "fstab", "opted out")
		} else {
			rep.Sub("✔", "fstab", "LABEL=<name> <path> ext4 defaults,nofail 0 2")
		}
		rep.Blank()
	}

	// The ESP is mounted from fstab in bootloader mode so kernel and
	// bootloader updates inside the guest reach it, unless the image opts
	// out of fstab lines or already mounts its ESP directory itself.
	var espFstab espFstabOutcome
	var existingFstab []byte
	if !fstabOptedOut && (bootMode == source.BootBootloader || len(fstabLines) > 0) {
		existingFstab, err = readFstab(rfs)
		if err != nil {
			return convertedBundle{}, err
		}
	}
	if bootMode == source.BootBootloader {
		// /boot/efi may be a symlink (to /efi, say); an entry for the
		// directory it resolves to is the image's own ESP entry too. A
		// missing or unusable /boot/efi is reported by the validation below.
		espDirs := []string{guestmeta.ESPMountPoint}
		if root, err := rfs.Resolve(validate.EFIDir); err == nil && root.Path != guestmeta.ESPMountPoint {
			espDirs = append(espDirs, root.Path)
		}
		switch {
		case fstabOptedOut:
			espFstab = espFstabSkipped
		case slices.ContainsFunc(espDirs, func(d string) bool { return guestmeta.HasMountPoint(existingFstab, d) }):
			espFstab = espFstabKept
		default:
			espFstab = espFstabAdded
			fstabLines = append(fstabLines, guestmeta.ESPFstabLine(disk.ESPVolumeLabel))
		}
	}
	if !fstabOptedOut && len(fstabLines) > 0 {
		if updated, changed := guestmeta.AppendFstab(existingFstab, fstabLines); changed {
			if err := rfs.WriteFile(guestmeta.FstabPath, updated); err != nil {
				return convertedBundle{}, fmt.Errorf("writing %s: %w", guestmeta.FstabPath, err)
			}
		}
	}

	var val *validate.Result
	if bootMode == source.BootBootloader {
		val, err = validate.CheckBootloader(rfs, platform.Architecture, disk.BootloaderESPSizeBytes)
		if err != nil {
			rep.Fail("validate", err.Error(), "a bootloader image must provide a bootable EFI tree at "+validate.EFIDir)
			return convertedBundle{}, err
		}
		if secureBoot {
			if err := validate.CheckSecureBoot(rfs, val.Bootloader); err != nil {
				rep.Fail("secure boot", err.Error(), "with "+source.SecureBootLabel+"=\"true\" the bootloader must be signed (this only checks that a signature is present, not that firmware trusts it)")
				return convertedBundle{}, err
			}
		}
		reportBootloader(rep, val.Bootloader)
		reportESPFstab(rep, espFstab)
		rep.Line("✅", "requirements satisfied", "bootloader · ESP tree")
	} else {
		val, err = validate.Validate(rfs)
		if err != nil {
			rep.Fail("validate", err.Error(), "a contemper-ready image must provide a kernel, initrd, cmdline and init at the fixed paths")
			return convertedBundle{}, err
		}
		reportFixedPaths(rep, rfs, val)
		rep.Line("✅", "requirements satisfied", "kernel · initrd · init")
	}
	rep.Blank()

	bundleName := fmt.Sprintf("%s-%s.%s", img.RepoBase, img.Tag, machineArch(platform.Architecture))
	outBundleDir := filepath.Join(opts.outDir, bundleName)
	// Fail before doing any of the expensive work below (ext4
	// population, qcow2 conversion) if --out already names something we
	// can't safely replace.
	if err := bundle.CheckDest(outBundleDir); err != nil {
		return convertedBundle{}, err
	}

	// The bundle is assembled in a staging directory next to outBundleDir
	// and only swapped into place once everything below succeeds
	// (bundle.Commit), so a failure partway through - or a stale
	// disk.raw from an earlier --keep-raw run - never leaves a broken or
	// mixed-version bundle at outBundleDir.
	stageDir, err := bundle.Stage(outBundleDir)
	if err != nil {
		return convertedBundle{}, err
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	diskInfo, warnings, err := asm.Assemble(ctx, rfs, val, platform.Architecture, stageDir, target.Options{
		RootSizeBytes: rootSizeBytes,
		KeepRaw:       opts.keepRaw,
		Progress:      rep,
	})
	if err != nil {
		return convertedBundle{}, err
	}
	for _, w := range warnings {
		rep.Warn("warning", w)
	}

	volumes := make([]bundle.Volume, 0, len(specs))
	for _, s := range specs {
		volumes = append(volumes, bundle.Volume{Name: s.Name, Path: s.Path, SizeBytes: s.SizeBytes, FS: "ext4"})
	}

	manifest := &bundle.Manifest{
		FormatVersion:    bundle.FormatVersion,
		ContemperVersion: buildinfo.Get().Version,
		CreatedAt:        time.Now().UTC(),
		Source: bundle.ImageRef{
			Ref:    ref.String(),
			Digest: img.Digest.String(),
			Repo:   img.RepoBase,
		},
		Target:     canonicalTarget,
		Boot:       bootMode,
		SecureBoot: secureBoot,
		Arch:       platform.Architecture,
		Disk: bundle.DiskInfo{
			File:      diskInfo.Filename,
			Format:    diskInfo.Format,
			SizeBytes: diskInfo.SizeBytes,
			SHA256:    diskInfo.SHA256,
		},
		Volumes:      volumes,
		Hints:        hintsFrom(cfg),
		Reproducible: img.Reproducible,
	}
	if supportImg != nil {
		manifest.Support = &bundle.SupportRef{
			Ref:      supportRefStr,
			Digest:   supportImg.Digest.String(),
			Origin:   string(supportOrigin),
			Variants: resolvedVariants,
		}
	}
	if helperResult != nil {
		manifest.VolumeHelper = &bundle.SupportRef{
			Ref: helperResult.ParsedRef.String(), Digest: helperResult.Img.Digest.String(), Variants: helperResult.Variants,
		}
	}

	if err := bundle.Write(stageDir, manifest); err != nil {
		return convertedBundle{}, err
	}

	if err := bundle.Commit(stageDir, outBundleDir); err != nil {
		return convertedBundle{}, err
	}

	rep.Blank()
	rep.Line("📦", bundleName+"/", "disk + manifest")
	elapsed := (rep.Elapsed() - priorElapsed).Round(100 * time.Millisecond)
	rep.Finish("bundle ready", fmt.Sprintf("%-16s%s", progress.HumanBytes(diskInfo.SizeBytes), elapsed))

	return convertedBundle{
		dir:    outBundleDir,
		name:   fmt.Sprintf("%s-%s", img.RepoBase, img.Tag),
		arch:   platform.Architecture,
		source: manifest.Source,
	}, nil
}

// runBuild builds opts.context with `docker buildx build --load`, then -
// unless --image-only stops it there - converts the resulting image
// through the docker-daemon: source with the same runConvert code path
// `convert` uses, so every convert flag applies to build too.
func runBuild(ctx context.Context, cmd *cobra.Command, opts buildOptions) error {
	sel, err := source.ParseArchSelection(opts.convert.arch)
	if err != nil {
		return err
	}
	if sel.All || sel.Group {
		return fmt.Errorf("--arch %q: build produces one architecture at a time; build a multi-platform image with docker buildx, then run `contemper convert --arch all` on it", opts.convert.arch)
	}
	platform := v1.Platform{OS: "linux", Architecture: sel.Archs[0]}
	if !opts.imageOnly {
		// Catch a bad convert flag now rather than after a full build.
		if err := checkConvertOptions(opts.convert); err != nil {
			return err
		}
	}

	tag := opts.tag
	if tag == "" {
		tag = buildx.DefaultTag(opts.context)
		if !opts.convert.quiet {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "contemper: build: no --tag given, using %s\n", tag)
		}
	} else if _, err := name.ParseReference(tag); err != nil {
		return fmt.Errorf("--tag %q: %w", tag, err)
	}

	file := opts.file
	if file == "" {
		file = buildx.DefaultFile(opts.context)
	}

	bxOpts := buildx.Options{
		Context:   opts.context,
		File:      file,
		Tag:       tag,
		Platform:  "linux/" + platform.Architecture,
		BuildArgs: opts.buildArgs,
	}

	dockerPath, err := buildx.CheckAvailable(ctx, bxOpts)
	if err != nil {
		return err
	}

	if err := buildx.Build(ctx, dockerPath, bxOpts); err != nil {
		return fmt.Errorf("docker buildx build: %w", err)
	}

	if opts.imageOnly {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), tag)
		return nil
	}

	opts.convert.sourceRef = "docker-daemon:" + tag
	return runConvert(ctx, cmd, opts.convert)
}

// checkConvertOptions runs the checks runConvert makes on its flags
// before loading anything, so `build` can reject a bad convert flag
// before it spends a whole image build on it.
func checkConvertOptions(opts convertOptions) error {
	if _, err := progress.ParseMode(opts.progressMode); err != nil {
		return err
	}
	if opts.noVolHelper && opts.volumeHelper != "" {
		return fmt.Errorf("--volume-helper and --no-volume-helper are mutually exclusive")
	}
	if _, _, err := target.Resolve(opts.target); err != nil {
		return err
	}
	if opts.rootSize != "" {
		if _, err := volume.ParseSize(opts.rootSize); err != nil {
			return fmt.Errorf("--root-size: %w", err)
		}
	}
	if _, err := withContentLimits(context.Background(), opts.maxRootfs); err != nil {
		return err
	}
	if opts.supportRef != "" {
		if _, err := source.ParseRef(opts.supportRef); err != nil {
			return err
		}
	}
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

// reportBootloader prints the sub-lines for a bootloader image's /boot/efi
// tree: where it resolved to, the fallback file, and whether
// /boot/contemper was left unused.
func reportBootloader(rep *progress.Reporter, b *validate.Bootloader) {
	label := validate.EFIDir
	if b.EFIRoot != validate.EFIDir {
		label += " → " + b.EFIRoot
	}
	files := 0
	for _, f := range b.Files {
		if !f.Dir {
			files++
		}
	}
	rep.Sub("✔", label, fmt.Sprintf("%d %s · %s", files, pluralize(files, "file"), progress.HumanBytes(b.TotalBytes)))
	rep.Sub("✔", b.FallbackPath, fmt.Sprintf("EFI application · %s", progress.HumanBytes(b.FallbackSize)))
	if b.ContemperIgnored {
		rep.Sub("·", "/boot/contemper", "present, ignored in bootloader mode")
	}
	if b.InitMissing {
		rep.Warn(validate.InitPath, "not found in the image; the bootloader configuration must then pass init= on the kernel command line")
	}
}

// coversESPMountPoint reports whether a volume mounted at p is the ESP
// mount point or one of its parents (/boot, /).
func coversESPMountPoint(p string) bool {
	p = path.Clean(p)
	for d := guestmeta.ESPMountPoint; ; d = path.Dir(d) {
		if p == d {
			return true
		}
		if d == "/" {
			return false
		}
	}
}

// readFstab returns the merged rootfs's /etc/fstab, or nil if it has none.
func readFstab(rfs *rootfs.Rootfs) ([]byte, error) {
	if _, ok := rfs.Lookup(guestmeta.FstabPath); !ok {
		return nil, nil
	}
	b, err := rfs.ReadFile(guestmeta.FstabPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", guestmeta.FstabPath, err)
	}
	return b, nil
}

// espFstabOutcome is what happened to the ESP's fstab line.
type espFstabOutcome int

const (
	espFstabAdded espFstabOutcome = iota
	espFstabKept
	espFstabSkipped
)

// reportESPFstab prints the sub-line for the ESP's fstab entry.
func reportESPFstab(rep *progress.Reporter, o espFstabOutcome) {
	switch o {
	case espFstabAdded:
		rep.Sub("✔", "fstab", guestmeta.ESPFstabLine(disk.ESPVolumeLabel))
	case espFstabKept:
		rep.Sub("·", "fstab", "kept the image's own "+guestmeta.ESPMountPoint+" entry")
	case espFstabSkipped:
		rep.Sub("·", "fstab", "skipped: fstab opt-out, no "+guestmeta.ESPMountPoint+" entry added")
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
func resolveVariants(ctx context.Context, schema *support.Schema, supportRef source.Ref, img *source.Image, platform v1.Platform, rep *progress.Reporter) (*variantResolution, error) {
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
			_ = os.RemoveAll(vr.cacheDir)
			for _, vi := range vr.images {
				vi.Close()
			}
		}
	}()

	srcRfs, err := rootfs.Build(ctx, vr.cachedSrc)
	if err != nil {
		return nil, fmt.Errorf("building source rootfs for variant resolution: %w", err)
	}
	defer func() { _ = srcRfs.Close() }()

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
			variantRef, err := source.ParseVariantRef(supportRef, r.Image)
			if err != nil {
				return nil, fmt.Errorf("branch %s: variant %s: %w", r.Branch, r.Variant, err)
			}
			variantImg, err := source.Load(ctx, variantRef, platform)
			if err != nil {
				return nil, fmt.Errorf("branch %s: variant %s: loading %s: %w", r.Branch, r.Variant, r.Image, err)
			}
			vr.images = append(vr.images, variantImg)
			vr.overlays = append(vr.overlays, variantImg.Image)
			vDownload, err := downloadSize(variantImg.Image)
			if err != nil {
				return nil, fmt.Errorf("branch %s: variant %s: reading manifest: %w", r.Branch, r.Variant, err)
			}
			rep.SubChild("%s %s · %s download", variantRef.String(), platform.String(), progress.HumanBytes(vDownload))
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

func runDeploy(ctx context.Context, _ *cobra.Command, opts deployOptions) error {
	rep, err := newReporter(ctx, os.Stderr, opts.progressMode, opts.verbose, opts.quiet)
	if err != nil {
		return err
	}

	if opts.to != "local-qemu" {
		return fmt.Errorf("unknown deploy target %q (only local-qemu is supported)", opts.to)
	}

	bundleDir, manifest, chosen, err := resolveDeployBundle(opts.bundleDir, opts.arch)
	if err != nil {
		return err
	}
	if chosen != "" {
		rep.Line("🧭", "bundle "+filepath.Base(bundleDir), chosen)
	}

	diskPath, err := bundle.VerifyDisk(bundleDir, manifest)
	if err != nil {
		return err
	}

	overrides, err := parseVolumeOverrides(opts.volumes)
	if err != nil {
		return err
	}

	var unsized []string
	sizes := make(map[string]int64, len(manifest.Volumes))
	for _, v := range manifest.Volumes {
		size := v.SizeBytes
		if override, ok := overrides[v.Path]; ok {
			// --volume is the user's own word and is not capped.
			size = override
			delete(overrides, v.Path)
		} else if err := volume.CheckDeclaredSize(size); err != nil {
			return fmt.Errorf("volume %s in the bundle manifest %w", v.Path, err)
		}
		if size == 0 {
			unsized = append(unsized, v.Path)
		}
		sizes[v.Path] = size
	}
	if len(unsized) > 0 {
		return fmt.Errorf("volume(s) %s have no size; supply one with --volume <path>=<size>", strings.Join(unsized, ", "))
	}
	for path := range overrides {
		return fmt.Errorf("--volume %s does not match any volume declared in this bundle", path)
	}

	var timeout time.Duration
	if opts.timeout != "" {
		timeout, err = time.ParseDuration(opts.timeout)
		if err != nil {
			return fmt.Errorf("--timeout: %w", err)
		}
	}

	var attachments []qemu.VolumeAttachment
	if len(manifest.Volumes) > 0 {
		instance := opts.name
		if instance == "" {
			instance, err = localqemu.DefaultInstance(manifest.Source.Repo)
			if err != nil {
				return err
			}
		}
		stateDir, err := localqemu.StateDir(instance)
		if err != nil {
			return err
		}
		rep.Line("📁", "instance "+instance, stateDir)
		if err := localqemu.ClaimInstance(stateDir, instance, manifest.Source.Identity()); err != nil {
			rep.Fail("volumes", err.Error(), "")
			return err
		}
		var reused []string
		for _, v := range manifest.Volumes {
			diskPath, created, err := localqemu.EnsureVolumeDisk(ctx, stateDir, v.Name, sizes[v.Path])
			if err != nil {
				rep.Fail("volumes", err.Error(), "")
				return err
			}
			status := "reusing"
			if created {
				status = "created"
			} else {
				reused = append(reused, v.Name)
			}
			rep.Sub("✔", v.Path+" → "+v.Name, fmt.Sprintf("%s (%s)", status, progress.HumanBytes(sizes[v.Path])))
			attachments = append(attachments, qemu.VolumeAttachment{Name: v.Name, Path: diskPath})
		}
		if len(reused) > 0 {
			rep.Line("📂", "reused volumes", strings.Join(reused, ", "))
		}
		rep.Blank()
	}

	return qemu.Deploy(ctx, qemu.Options{
		Arch:          manifest.Arch,
		DiskPath:      diskPath,
		DiskFormat:    manifest.Disk.Format,
		Volumes:       attachments,
		SecureBoot:    manifest.SecureBoot,
		SerialLogPath: opts.serialLog,
		Expect:        opts.expect,
		Timeout:       timeout,
		Progress:      rep,
	})
}

// resolveDeployBundle turns deploy's path argument into a bundle
// directory and its manifest. path is a bundle directory, or a group
// file (see bundle.Group), from which the bundle for arch is taken, or
// for the host's architecture when arch is empty. chosen describes the
// selection for the report and is empty for a plain bundle directory.
// A plain bundle directory boots whatever its architecture is (a
// foreign one under emulation), but an explicit arch must match it.
func resolveDeployBundle(path, arch string) (dir string, m *bundle.Manifest, chosen string, err error) {
	if arch != "" {
		if _, err := source.HostPlatform(arch); err != nil {
			return "", nil, "", err
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", nil, "", err
	}
	if fi.IsDir() {
		m, err := bundle.Read(path)
		if err != nil {
			return "", nil, "", err
		}
		if arch != "" && m.Arch != arch {
			return "", nil, "", fmt.Errorf("--arch %s does not match the bundle %s, which is for %s", arch, path, m.Arch)
		}
		return path, m, "", nil
	}

	if !strings.HasSuffix(filepath.Base(path), bundle.GroupSuffix) {
		if filepath.Base(path) == bundle.ManifestFile {
			return "", nil, "", fmt.Errorf("%s is a bundle manifest; pass the bundle directory %s instead", path, filepath.Dir(path))
		}
		return "", nil, "", fmt.Errorf("%s is neither a bundle directory nor a <name>%s group file", path, bundle.GroupSuffix)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, "", err
	}
	g, err := bundle.ReadGroup(resolved)
	if err != nil {
		return "", nil, "", err
	}
	var want, how string
	if arch != "" {
		want, how = arch, "from --arch"
	} else {
		// Left unmapped on purpose: an unsupported host falls through to
		// the "no bundle for" error below instead of one about --arch.
		want, how = runtime.GOARCH, "matches host"
	}
	rel, ok := g.Find(want)
	if !ok {
		var have []string
		for _, b := range g.Bundles {
			have = append(have, b.Arch)
		}
		msg := fmt.Sprintf("%s has no bundle for %s (available: %s)", path, want, strings.Join(have, ", "))
		if arch == "" {
			msg += "; pick one with --arch"
		}
		return "", nil, "", errors.New(msg)
	}
	dir = filepath.Join(filepath.Dir(resolved), filepath.FromSlash(rel))
	m, err = bundle.Read(dir)
	if err != nil {
		return "", nil, "", err
	}
	if m.Arch != want {
		return "", nil, "", fmt.Errorf("%s lists %s as %s, but its manifest says %s; convert again", path, rel, want, m.Arch)
	}
	return dir, m, fmt.Sprintf("%s, %s", want, how), nil
}

// parseVolumeOverrides parses --volume <path>=<size> flags into a
// path->bytes map.
func parseVolumeOverrides(raw []string) (map[string]int64, error) {
	if len(raw) == 0 {
		return map[string]int64{}, nil
	}
	out := make(map[string]int64, len(raw))
	for _, spec := range raw {
		path, sizeStr, ok := strings.Cut(spec, "=")
		if !ok || path == "" || sizeStr == "" {
			return nil, fmt.Errorf("--volume %q: want <path>=<size>", spec)
		}
		size, err := volume.ParseSize(sizeStr)
		if err != nil {
			return nil, fmt.Errorf("--volume %s: %w", path, err)
		}
		out[path] = size
	}
	return out, nil
}

// seedReportLine returns the report run.go prints under each declared
// volume (see rep.SubChild's call site above) describing what happens to
// it the first time its disk is formatted: opted out by the volume's own
// seed label, seeded because the image has content at its path, or left
// alone because it doesn't. hasContent is rfs.DirHasContent(s.Path), the
// no-host-extraction check for "does the image have anything there" -
// passed in rather than recomputed so this stays a pure function callers
// can unit test without building a rootfs.
func seedReportLine(s volume.Spec, hasContent bool) string {
	switch {
	case !s.Seed:
		return fmt.Sprintf("seed: opted out, %s starts empty on first format", s.Path)
	case hasContent:
		return fmt.Sprintf("seed: image has content at %s, copied onto the volume on first format", s.Path)
	default:
		return fmt.Sprintf("seed: no content at %s, first format leaves it empty", s.Path)
	}
}

// noSeedVolumeNames returns the names of every spec whose seed label
// opted it out (see volume.Spec.Seed), in specs' own order - which
// volume.FromConfig already sorts by path, so /etc/contemper/volumes-noseed
// always lists them the same way /etc/contemper/volumes does. nil when
// nothing opts out, which is the caller's signal to skip writing the
// file at all: see guestmeta.NoSeedPath on why a missing file, not an
// empty one, is how "seed everything" is spelled.
func noSeedVolumeNames(specs []volume.Spec) []string {
	var names []string
	for _, s := range specs {
		if !s.Seed {
			names = append(names, s.Name)
		}
	}
	return names
}

// pluralize returns unit or unit+"s" depending on n, for the common case
// of a regular plural.
func pluralize(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return unit + "s"
}

// downloadSize reads img's manifest and sums its layer descriptors' Size
// fields: the compressed bytes a puller has to fetch for it, known
// without pulling any layer content and without an extra registry round
// trip beyond the manifest read every image load already does.
func downloadSize(img v1.Image) (int64, error) {
	m, err := img.Manifest()
	if err != nil {
		return 0, err
	}
	return manifestDownloadSize(m), nil
}

// manifestDownloadSize is downloadSize for a manifest the caller has
// already read, so it doesn't fetch (or re-fetch, from go-containerregistry's
// own cache) one just to add its layer sizes up.
func manifestDownloadSize(m *v1.Manifest) int64 {
	var total int64
	for _, l := range m.Layers {
		total += l.Size
	}
	return total
}

// overlayLabels returns the progress labels for one merged piece: name
// (e.g. "support image" or "volume helper") followed by one "variant
// branch=variant" label for each entry in variants that resolved to an
// actual image - in the same order rootfs.Build's overlays parameter
// lists them (see how the caller builds that slice), so the result lines
// up positionally with the matching slice of rootfs.OverlayStats.
func overlayLabels(name string, variants []bundle.SupportVariant) []string {
	labels := []string{name}
	for _, v := range variants {
		if v.Ref != "" {
			labels = append(labels, fmt.Sprintf("variant %s=%s", v.Branch, v.Variant))
		}
	}
	return labels
}

// reportOverlayStats prints, under the merge stage, one "✔" line per
// overlay rootfs.Build was given, pairing labels (built by overlayLabels)
// with the matching entry of stats (Rootfs.OverlayStats) - see
// rootfs.OverlayStats's doc comment for exactly what Files/Bytes/Removed
// count. A whiteout/opaque-directory removal count is only shown when
// nonzero; an overlay that changed nothing at all (Files == 0 and
// Removed == 0) is skipped rather than printed as a no-op line.
func reportOverlayStats(rep *progress.Reporter, labels []string, stats []rootfs.OverlayStats) {
	for i, st := range stats {
		if i >= len(labels) {
			break
		}
		if st.Files == 0 && st.Removed == 0 {
			continue
		}
		detail := fmt.Sprintf("+%d %s · %s", st.Files, pluralize(st.Files, "file"), progress.HumanBytes(st.Bytes))
		if st.Removed > 0 {
			detail += fmt.Sprintf(" · %d removed", st.Removed)
		}
		rep.Sub("✔", labels[i], detail)
	}
}

// redactedRefString returns the string /etc/contemper/build records for
// ref: the full reference for a registry source, or (per the design's
// "no build-machine paths in the guest" rule) just the scheme and file
// name for a local archive/layout source. contemper.json's own
// source.ref/support.ref, by contrast, keep the reference exactly as
// given, since the bundle directory is already build-host-specific.
func redactedRefString(ref source.Ref) string {
	if ref.Kind == source.KindRegistry {
		return ref.String()
	}
	return guestmeta.RedactLocalRef(string(ref.Kind), ref.Value)
}

// redactedVariantRef is redactedRefString for a resolved variant's
// recorded reference string (empty for a no-op variant), so a local
// variant's host path stays out of /etc/contemper/build like any other
// local reference.
func redactedVariantRef(raw string) string {
	if raw == "" {
		return ""
	}
	ref, err := source.ParseRef(raw)
	if err != nil {
		return raw
	}
	return redactedRefString(ref)
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

// withContentLimits returns ctx carrying the image content limits,
// raised to the size --max-rootfs-size gives (empty keeps the defaults).
func withContentLimits(ctx context.Context, maxRootfs string) (context.Context, error) {
	if maxRootfs == "" {
		return ctx, nil
	}
	n, err := volume.ParseSize(maxRootfs)
	if err != nil {
		return ctx, fmt.Errorf("--max-rootfs-size: %w", err)
	}
	return limits.NewContext(ctx, limits.Default().WithMaxSize(n)), nil
}
