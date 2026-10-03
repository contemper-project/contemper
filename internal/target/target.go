// Package target maps a target name to its canonical spelling and
// Assembler, and implements the UEFI qcow2 assembler shared by the qemu
// and incus targets: UKI (or the image's own bootloader), then
// GPT/ESP/ext4, then qcow2.
package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/uki"
	"github.com/contemper-project/contemper/internal/validate"
)

// DiskInfo is what an Assembler reports back about the disk it built.
type DiskInfo struct {
	Filename  string
	Format    string
	SizeBytes int64
	SHA256    string
}

// Options carries the user-overridable assembly knobs.
type Options struct {
	// RootSizeBytes overrides the default root partition sizing when
	// non-zero.
	RootSizeBytes int64
	// KeepRaw keeps disk.raw in outDir alongside the qcow2 output.
	KeepRaw bool
	// Progress, if non-nil, receives the assembly stage's progress
	// output (a live spinner while building, then a summary line and
	// sub-results - UKI/ESP/root sizes).
	Progress *progress.Reporter
}

// Assembler takes a validated, merged rootfs view and produces this
// target's disk output in outDir. It must not depend on the registry, so
// it can be tested against a synthetic rootfs. Canceling ctx stops any
// subprocess the assembler is currently running (mkfs.ext4, debugfs,
// e2fsck, qemu-img) and is noticed at reasonable points in between.
type Assembler interface {
	Assemble(ctx context.Context, rfs *rootfs.Rootfs, val *validate.Result, arch string, outDir string, opts Options) (*DiskInfo, []string, error)
}

// RootLabel is the filesystem label contemper gives the root partition.
const RootLabel = "contemper-root"

// KernelCmdline returns the command line sealed into the UKI: contemper's
// own root= for the partition it creates, followed by the author's
// command line. Because the author's part comes last, a root= of their
// own takes precedence (the kernel and common initramfs implementations
// use the last occurrence). Resolving LABEL= is done by the initrd, which
// the fixed-path contract requires anyway.
func KernelCmdline(author string) string {
	return strings.TrimSpace("root=LABEL=" + RootLabel + " " + strings.TrimSpace(author))
}

// table maps every accepted spelling (aliases and canonical names alike)
// to a canonical name, its Assembler, and its default support image
// reference (empty for none).
//
// TODO(incus-support): once ghcr.io/contemper-project/incus-support is
// published, set incus-qcow2's defaultSupport to it. Until then, leaving
// it empty is deliberate: pointing at a reference that doesn't exist yet
// would make every incus conversion fail.
var table = map[string]struct {
	canonical      string
	assembler      Assembler
	defaultSupport string
}{
	"qemu":        {canonical: "qemu-qcow2", assembler: UEFIQcow2{}},
	"qemu-qcow2":  {canonical: "qemu-qcow2", assembler: UEFIQcow2{}},
	"incus":       {canonical: "incus-qcow2", assembler: UEFIQcow2{}},
	"incus-qcow2": {canonical: "incus-qcow2", assembler: UEFIQcow2{}},
}

// Resolve maps name (an alias or a canonical name) to its canonical
// spelling and Assembler.
func Resolve(name string) (canonical string, asm Assembler, err error) {
	e, ok := table[name]
	if !ok {
		return "", nil, fmt.Errorf("unknown target %q", name)
	}
	return e.canonical, e.assembler, nil
}

// DefaultSupport returns canonical's default support image reference, or
// "" if the target has none. canonical must already be a canonical
// target name, as returned by Resolve.
func DefaultSupport(canonical string) string {
	return table[canonical].defaultSupport
}

// SupportOrigin names where a resolved support image reference came from,
// for progress output and the bundle manifest.
type SupportOrigin string

const (
	// SupportNone means no support image applies.
	SupportNone SupportOrigin = ""
	// SupportFromFlag means --support named the reference, replacing any
	// target default.
	SupportFromFlag SupportOrigin = "flag"
	// SupportFromTarget means the target's own default applied, because
	// --support was not given.
	SupportFromTarget SupportOrigin = "target"
)

// ResolveSupport decides which support image reference, if any, applies
// to a build: flagRef, an explicit --support value, always replaces
// defaultRef, the resolved target's default, rather than stacking with
// it. It returns "" with SupportNone when neither is set.
func ResolveSupport(defaultRef, flagRef string) (ref string, origin SupportOrigin) {
	if flagRef != "" {
		return flagRef, SupportFromFlag
	}
	if defaultRef != "" {
		return defaultRef, SupportFromTarget
	}
	return "", SupportNone
}

// serialPatterns maps a canonical target name to the function that turns
// a volume's name into the serial-matching pattern
// /etc/contemper/volumes records for it - the hook a future target (in
// particular Incus, whose own device-identification scheme is not yet
// decided) can override independently of qemu's. Every target currently
// in table attaches volumes as virtio-blk with serial=<name>, so the
// guest looks the disk up by that same name; a target not listed here
// falls back to the same identity function in SerialPattern.
var serialPatterns = map[string]func(volumeName string) string{
	"qemu-qcow2":  identitySerialPattern,
	"incus-qcow2": identitySerialPattern,
}

func identitySerialPattern(volumeName string) string { return volumeName }

// SerialPattern returns the string /etc/contemper/volumes records as a
// volume's serial-matching pattern for canonicalTarget, so a first-boot
// helper can find the right disk under /sys/block/*/serial (qemu:
// virtio-blk's serial=<name>, the same as the ext4 label and fstab
// LABEL=).
func SerialPattern(canonicalTarget, volumeName string) string {
	if f, ok := serialPatterns[canonicalTarget]; ok {
		return f(volumeName)
	}
	return identitySerialPattern(volumeName)
}

// UEFIQcow2 assembles a UEFI-bootable GPT disk with a FAT32 ESP (holding a
// UKI, or the image's own bootloader when val.Bootloader is set) and an
// ext4 root, converted to qcow2. qemu-qcow2 (the reference case,
// no support image) and incus-qcow2 (Incus runs VMs on QEMU/OVMF) both
// use it; the targets differ only in their support image.
type UEFIQcow2 struct{}

// Assemble implements the Assembler interface documented on UEFIQcow2.
func (UEFIQcow2) Assemble(ctx context.Context, rfs *rootfs.Rootfs, val *validate.Result, arch string, outDir string, opts Options) (*DiskInfo, []string, error) {
	var warnings []string

	bl := val.Bootloader
	mode := "UKI"
	var ukiBytes []byte
	if bl != nil {
		mode = "bootloader"
	} else {
		linuxData, warn, err := uki.PrepareKernel(val.Kernel)
		if err != nil {
			return nil, nil, fmt.Errorf("preparing kernel: %w", err)
		}
		if warn != "" {
			warnings = append(warnings, warn)
		}

		ukiBytes, err = uki.Build(arch, uki.Sections{
			OSRelease: val.OSRelease,
			Cmdline:   []byte(KernelCmdline(val.Cmdline)),
			Initrd:    val.Initrd,
			Linux:     linuxData,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("building UKI: %w", err)
		}
	}

	rep := opts.Progress
	stageTitle := fmt.Sprintf("assembling qcow2 · %s · UEFI/%s", arch, mode)
	stage := rep.BeginStage("💿", stageTitle)

	workDir, err := os.MkdirTemp("", "contemper-assemble-")
	if err != nil {
		stage.Fail("assemble", err.Error(), "")
		return nil, nil, fmt.Errorf("creating work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	contentBytes := int64(0)
	if fi, err := os.Stat(rfs.TarPath); err == nil {
		contentBytes = fi.Size()
	}
	rootSize := opts.RootSizeBytes
	if rootSize == 0 {
		rootSize = disk.RootPartitionSize(contentBytes)
	}
	rootSize = disk.AlignRootSize(rootSize)

	rootImgPath := filepath.Join(workDir, "root.img")
	ext4Warnings, err := disk.PopulateExt4(ctx, rfs, rootImgPath, disk.Ext4Options{
		Label:     RootLabel,
		SizeBytes: rootSize,
		Progress:  rep,
		Stage:     stage,
	})
	if err != nil {
		stage.Fail("assemble", "populating the ext4 root failed", err.Error())
		return nil, nil, fmt.Errorf("populating ext4 root: %w", err)
	}
	warnings = append(warnings, ext4Warnings...)

	rawPath := filepath.Join(workDir, "disk.raw")
	espSize := disk.ESPSizeBytes
	gptOpts := disk.BuildOptions{
		Arch:          arch,
		UKI:           ukiBytes,
		RootImgPath:   rootImgPath,
		RootSizeBytes: rootSize,
	}
	if bl != nil {
		espSize = disk.BootloaderESPSizeBytes
		gptOpts.ESPSizeBytes = espSize
		gptOpts.ESPTree = espTree(rfs, bl)
	}
	if _, err := disk.BuildGPTImage(rawPath, gptOpts); err != nil {
		stage.Fail("assemble", "building the GPT disk image failed", err.Error())
		return nil, nil, fmt.Errorf("building disk image: %w", err)
	}

	if opts.KeepRaw {
		if err := copyFile(rawPath, filepath.Join(outDir, "disk.raw")); err != nil {
			stage.Fail("assemble", err.Error(), "")
			return nil, nil, fmt.Errorf("keeping disk.raw: %w", err)
		}
	}

	qcow2Path := filepath.Join(outDir, "disk.qcow2")
	if err := disk.ConvertToQcow2(ctx, rawPath, qcow2Path, rep); err != nil {
		hint := ""
		if hostenv.Find("qemu-img") == "" {
			hint = "install qemu-img: " + hostenv.InstallHint("qemu-img")
		}
		stage.Fail("assemble", "converting to qcow2 failed", hint)
		return nil, nil, err
	}

	info, err := diskInfoFor(qcow2Path, "qcow2")
	if err != nil {
		stage.Fail("assemble", err.Error(), "")
		return nil, nil, err
	}

	stage.Done("💿", stageTitle, "")
	if bl != nil {
		rep.Sub("✔", "bootloader", fmt.Sprintf("%s · %s", bl.FallbackPath, progress.HumanBytes(bl.FallbackSize)))
	} else {
		rep.Sub("✔", "UKI", progress.HumanBytes(int64(len(ukiBytes))))
	}
	rep.Sub("✔", "ESP", progress.HumanBytes(int64(espSize)))
	rep.Sub("✔", "root fs", fmt.Sprintf("%d files · %s", len(rfs.Index), progress.HumanBytes(rootSize)))

	return info, warnings, nil
}

// espTree turns the validated /boot/efi tree into the entries the disk
// builder copies onto the ESP; file content is read straight from rfs.
func espTree(rfs *rootfs.Rootfs, bl *validate.Bootloader) []disk.ESPEntry {
	tree := make([]disk.ESPEntry, 0, len(bl.Files))
	for _, f := range bl.Files {
		e := disk.ESPEntry{Path: f.Path, Dir: f.Dir, Size: f.Size}
		if !f.Dir {
			src := f.Source
			e.Open = func() (io.ReadCloser, error) { return rfs.Open(src) }
		}
		tree = append(tree, e)
	}
	return tree
}

// QemuImgMissing reports whether qemu-img could not be found, so callers
// (chiefly tests) can decide whether to exercise the qcow2 step or fall
// back to asserting on the raw image directly.
func QemuImgMissing() bool {
	return hostenv.Find("qemu-img") == ""
}

func diskInfoFor(path, format string) (*DiskInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return nil, fmt.Errorf("hashing %s: %w", path, err)
	}

	return &DiskInfo{
		Filename:  filepath.Base(path),
		Format:    format,
		SizeBytes: size,
		SHA256:    hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	// Checked, not deferred-and-ignored: dst is a bundle artifact
	// (disk.raw), so a write error surfaced only at Close (e.g. a
	// delayed flush failure) must not be silently swallowed.
	return out.Close()
}
