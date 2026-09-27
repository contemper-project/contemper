// Package target maps a target name to its canonical spelling and
// Assembler, and implements the UEFI qcow2 assembler shared by the qemu
// and incus targets: UKI, then GPT/ESP/ext4, then qcow2.
package target

import (
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
// it can be tested against a synthetic rootfs.
type Assembler interface {
	Assemble(rfs *rootfs.Rootfs, val *validate.Result, arch string, outDir string, opts Options) (*DiskInfo, []string, error)
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

// UEFIQcow2 assembles a UEFI-bootable UKI on a GPT disk with a FAT32 ESP
// and an ext4 root, converted to qcow2. qemu-qcow2 (the reference case,
// no support image) and incus-qcow2 (Incus runs VMs on QEMU/OVMF) both
// use it; the targets differ only in their support image.
type UEFIQcow2 struct{}

func (UEFIQcow2) Assemble(rfs *rootfs.Rootfs, val *validate.Result, arch string, outDir string, opts Options) (*DiskInfo, []string, error) {
	var warnings []string

	linuxData, warn, err := uki.PrepareKernel(val.Kernel)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing kernel: %w", err)
	}
	if warn != "" {
		warnings = append(warnings, warn)
	}

	ukiBytes, err := uki.Build(arch, uki.Sections{
		OSRelease: val.OSRelease,
		Cmdline:   []byte(KernelCmdline(val.Cmdline)),
		Initrd:    val.Initrd,
		Linux:     linuxData,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("building UKI: %w", err)
	}

	rep := opts.Progress
	stage := rep.BeginStage("💿", fmt.Sprintf("assembling qcow2 · %s · UEFI/UKI", arch))

	workDir, err := os.MkdirTemp("", "contemper-assemble-")
	if err != nil {
		stage.Fail("assemble", err.Error(), "")
		return nil, nil, fmt.Errorf("creating work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	contentBytes := int64(0)
	if fi, err := os.Stat(rfs.TarPath); err == nil {
		contentBytes = fi.Size()
	}
	rootSize := opts.RootSizeBytes
	if rootSize == 0 {
		rootSize = disk.RootPartitionSize(contentBytes)
	}

	rootImgPath := filepath.Join(workDir, "root.img")
	ext4Warnings, err := disk.PopulateExt4(rfs, rootImgPath, disk.Ext4Options{
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
	if _, err := disk.BuildGPTImage(rawPath, disk.BuildOptions{
		Arch:          arch,
		UKI:           ukiBytes,
		RootImgPath:   rootImgPath,
		RootSizeBytes: rootSize,
	}); err != nil {
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
	if err := disk.ConvertToQcow2(rawPath, qcow2Path, rep); err != nil {
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

	stage.Done("💿", fmt.Sprintf("assembling qcow2 · %s · UEFI/UKI", arch), "")
	rep.Sub("✔", "UKI", progress.HumanBytes(int64(len(ukiBytes))))
	rep.Sub("✔", "ESP", progress.HumanBytes(int64(disk.ESPSizeBytes)))
	rep.Sub("✔", "root fs", fmt.Sprintf("%d files · %s", len(rfs.Index), progress.HumanBytes(rootSize)))

	return info, warnings, nil
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
	defer f.Close()

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
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
