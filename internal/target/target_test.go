package target_test

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/target"
	"github.com/contemper-project/contemper/internal/uki"
	"github.com/contemper-project/contemper/internal/validate"
)

func TestResolve(t *testing.T) {
	for name, want := range map[string]string{
		"qemu":        "qemu-qcow2",
		"qemu-qcow2":  "qemu-qcow2",
		"incus":       "incus-qcow2",
		"incus-qcow2": "incus-qcow2",
	} {
		canonical, asm, err := target.Resolve(name)
		if err != nil || canonical != want || asm == nil {
			t.Fatalf("Resolve(%s) = %q, %v, %v; want %q", name, canonical, asm, err, want)
		}
	}
	if _, _, err := target.Resolve("bogus"); err == nil {
		t.Fatalf("Resolve(bogus): expected an error")
	}
}

// TestDefaultSupport pins today's target defaults: neither qemu-qcow2 nor
// incus-qcow2 has one yet (the Incus support image isn't published; see
// the TODO on the target table).
func TestDefaultSupport(t *testing.T) {
	for _, canonical := range []string{"qemu-qcow2", "incus-qcow2"} {
		if got := target.DefaultSupport(canonical); got != "" {
			t.Errorf("DefaultSupport(%s) = %q, want none", canonical, got)
		}
	}
	if got := target.DefaultSupport("bogus"); got != "" {
		t.Errorf("DefaultSupport(bogus) = %q, want none", got)
	}
}

func TestResolveSupport(t *testing.T) {
	cases := []struct {
		name             string
		defaultRef, flag string
		wantRef          string
		wantOrigin       target.SupportOrigin
	}{
		{"neither set", "", "", "", target.SupportNone},
		{"default only", "ghcr.io/example/target-support:v1", "", "ghcr.io/example/target-support:v1", target.SupportFromTarget},
		{"flag only", "", "ghcr.io/example/custom:v1", "ghcr.io/example/custom:v1", target.SupportFromFlag},
		{"flag replaces default, never stacks", "ghcr.io/example/target-support:v1", "ghcr.io/example/custom:v1", "ghcr.io/example/custom:v1", target.SupportFromFlag},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, origin := target.ResolveSupport(c.defaultRef, c.flag)
			if ref != c.wantRef || origin != c.wantOrigin {
				t.Errorf("ResolveSupport(%q, %q) = %q, %q; want %q, %q", c.defaultRef, c.flag, ref, origin, c.wantRef, c.wantOrigin)
			}
		})
	}
}

func TestSerialPattern(t *testing.T) {
	for _, canonical := range []string{"qemu-qcow2", "incus-qcow2", "some-future-target"} {
		if got := target.SerialPattern(canonical, "data"); got != "data" {
			t.Errorf("SerialPattern(%s, data) = %q, want %q", canonical, got, "data")
		}
	}
}

func TestKernelCmdline(t *testing.T) {
	for author, want := range map[string]string{
		"console=ttyAMA0 rw":          "root=LABEL=contemper-root console=ttyAMA0 rw",
		"  console=ttyS0\n":           "root=LABEL=contemper-root console=ttyS0",
		"root=/dev/vda2 console=hvc0": "root=LABEL=contemper-root root=/dev/vda2 console=hvc0",
		"":                            "root=LABEL=contemper-root",
	} {
		if got := target.KernelCmdline(author); got != want {
			t.Errorf("KernelCmdline(%q) = %q, want %q", author, got, want)
		}
	}
}

// testRootSize is a root partition far smaller than the default 1 GiB
// floor: the assembler writes the whole partition out, which dominates the
// run time otherwise.
const testRootSize = 64 << 20

func syntheticRootfs(t *testing.T) (*rootfs.Rootfs, *validate.Result) {
	t.Helper()
	for _, name := range []string{"mkfs.ext4", "debugfs", "e2fsck"} {
		if hostenv.Find(name) == "" {
			t.Skipf("%s not found; skipping assembler test", name)
		}
	}

	files := []imgtest.File{
		{Path: "boot/", Typeflag: tar.TypeDir},
		{Path: "boot/contemper/", Typeflag: tar.TypeDir},
		{Path: "boot/contemper/vmlinuz", Data: append([]byte("MZ"), make([]byte, 128)...)},
		{Path: "boot/contemper/initrd", Data: []byte("fake-initrd-content")},
		{Path: "boot/contemper/cmdline", Data: []byte("rw console=ttyS0\n")},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/os-release", Data: []byte("NAME=Test\n")},
	}
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "arm64"}, map[string]string{
		"io.contemper.ready": "true",
	}, files)
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(t.Context(), img, nil)
	if err != nil {
		t.Fatalf("rootfs.Build: %v", err)
	}
	t.Cleanup(func() { _ = rfs.Close() })

	val, err := validate.Validate(rfs)
	if err != nil {
		t.Fatalf("validate.Validate: %v", err)
	}
	return rfs, val
}

// TestUEFIQcow2Assemble keeps the default root sizing (no RootSizeBytes) so
// that path still runs; TestUEFIQcow2AssembleBootloader uses testRootSize.
func TestUEFIQcow2Assemble(t *testing.T) {
	rfs, val := syntheticRootfs(t)
	outDir := t.TempDir()

	asm := target.UEFIQcow2{}
	info, warnings, err := asm.Assemble(t.Context(), rfs, val, "arm64", outDir, target.Options{})

	if target.QemuImgMissing() {
		// qemu-img isn't available in this environment; the disk.raw
		// step is exercised directly by
		// internal/disk's own tests, so just confirm the assembler fails
		// clearly rather than silently producing a bad bundle.
		if err == nil {
			t.Fatalf("expected an error when qemu-img is missing, got a disk at %v", info)
		}
		t.Logf("qemu-img missing, as expected in this environment: %v", err)
		return
	}

	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	t.Logf("warnings: %v", warnings)
	if info.Format != "qcow2" {
		t.Errorf("Format = %q, want qcow2", info.Format)
	}
	if info.SizeBytes == 0 {
		t.Errorf("SizeBytes should not be zero")
	}
	if info.SHA256 == "" {
		t.Errorf("SHA256 should not be empty")
	}
	if _, err := os.Stat(outDir + "/" + info.Filename); err != nil {
		t.Errorf("disk file missing: %v", err)
	}
}

func TestRootPartitionSize(t *testing.T) {
	if got := disk.RootPartitionSize(0); got != 1<<30 {
		t.Errorf("RootPartitionSize(0) = %d, want 1 GiB floor", got)
	}
	content := int64(2 << 30) // 2 GiB
	want := int64(float64(content)*1.5) + (256 << 20)
	if got := disk.RootPartitionSize(content); got != want {
		t.Errorf("RootPartitionSize(2GiB) = %d, want %d", got, want)
	}
	// Content sizes that make 1.5x land off a sector boundary still give
	// a whole number of MiB.
	for _, content := range []int64{621_175_125, 620_000_001, 1<<30 + 1} {
		if got := disk.RootPartitionSize(content); got%(1<<20) != 0 {
			t.Errorf("RootPartitionSize(%d) = %d, not MiB-aligned", content, got)
		}
	}
}

func TestAlignRootSize(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{1 << 30, 1 << 30},
		{1<<30 + 1, 1<<30 + 1<<20},
		{1187762944, 1188036608},
	} {
		if got := disk.AlignRootSize(tc.in); got != tc.want {
			t.Errorf("AlignRootSize(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestPrepareKernelUsedByAssembler(t *testing.T) {
	// Sanity check that the assembler's kernel handling matches
	// internal/uki directly, since Assemble does not expose its
	// intermediate steps.
	out, warn, err := uki.PrepareKernel([]byte("MZfake"))
	if err != nil || warn != "" || string(out) != "MZfake" {
		t.Errorf("PrepareKernel(MZ...) = %q, %q, %v", out, warn, err)
	}
}

// captureQemuImg puts a fake qemu-img on PATH that moves the raw disk it
// is asked to convert to the returned path, so a test can read the disk
// the assembler built.
func captureQemuImg(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "disk.raw")
	script := "#!/bin/sh\nmv \"$4\" \"" + capture + "\" && printf 'placeholder qcow2\\n' > \"$5\"\n"
	if err := os.WriteFile(filepath.Join(bin, "qemu-img"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return capture
}

func bootloaderRootfs(t *testing.T, arch string, extra ...imgtest.File) (*rootfs.Rootfs, *validate.Result) {
	t.Helper()
	for _, name := range []string{"mkfs.ext4", "debugfs", "e2fsck"} {
		if hostenv.Find(name) == "" {
			t.Skipf("%s not found; skipping assembler test", name)
		}
	}
	pe := make([]byte, 0x80+24+240)
	pe[0], pe[1] = 'M', 'Z'
	pe[0x3c] = 0x80
	copy(pe[0x80:], "PE\x00\x00")
	machine := map[string]uint16{"arm64": 0xAA64, "amd64": 0x8664}[arch]
	binary.LittleEndian.PutUint16(pe[0x84:], machine)
	binary.LittleEndian.PutUint16(pe[0x84+16:], 240)
	binary.LittleEndian.PutUint16(pe[0x98:], 0x20b)
	binary.LittleEndian.PutUint16(pe[0x98+68:], 10)

	fallback := map[string]string{"arm64": "BOOTAA64.EFI", "amd64": "BOOTX64.EFI"}[arch]
	files := append([]imgtest.File{
		{Path: "boot/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/BOOT/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/BOOT/" + fallback, Data: pe},
		{Path: "boot/efi/EFI/debian/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/debian/grub.cfg", Data: []byte("search --label contemper-root\n")},
		{Path: "boot/efi/EFI/debian/a-rather-long-file-name.cfg", Data: []byte("long\n")},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
	}, extra...)
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: arch}, map[string]string{
		"io.contemper.ready": "true",
		"io.contemper.boot":  "bootloader",
	}, files)
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(t.Context(), img, nil)
	if err != nil {
		t.Fatalf("rootfs.Build: %v", err)
	}
	t.Cleanup(func() { _ = rfs.Close() })
	val, err := validate.CheckBootloader(rfs, arch, disk.BootloaderESPSizeBytes)
	if err != nil {
		t.Fatalf("validate.CheckBootloader: %v", err)
	}
	return rfs, val
}

func TestUEFIQcow2AssembleBootloader(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			rfs, val := bootloaderRootfs(t, arch)
			capture := captureQemuImg(t)
			outDir := t.TempDir()
			var report bytes.Buffer

			if _, _, err := (target.UEFIQcow2{}).Assemble(t.Context(), rfs, val, arch, outDir, target.Options{RootSizeBytes: testRootSize, Progress: progress.New(&report, progress.ModePlain, false, false)}); err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			d, err := diskfs.Open(capture)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Close() }()
			table, err := d.GetPartitionTable()
			if err != nil {
				t.Fatal(err)
			}
			parts := table.GetPartitions()
			if len(parts) != 2 {
				t.Fatalf("%d partitions, want 2", len(parts))
			}
			if got := parts[0].GetSize(); got != 512<<20 {
				t.Errorf("ESP is %d bytes, want 512 MiB", got)
			}
			fs, err := d.GetFilesystem(1)
			if err != nil {
				t.Fatal(err)
			}
			read := func(p string) string {
				f, err := fs.OpenFile(p, os.O_RDONLY)
				if err != nil {
					t.Errorf("opening %s on the ESP: %v", p, err)
					return ""
				}
				b, _ := io.ReadAll(f)
				return string(b)
			}
			fallback := map[string]string{"arm64": "BOOTAA64.EFI", "amd64": "BOOTX64.EFI"}[arch]
			if got := read("/EFI/BOOT/" + fallback); len(got) != 0x80+24+240 || got[:2] != "MZ" {
				t.Errorf("fallback file on the ESP has %d bytes, want the image's PE file", len(got))
			}
			if got := read("/EFI/debian/grub.cfg"); got != "search --label contemper-root\n" {
				t.Errorf("grub.cfg = %q", got)
			}
			if got := read("/EFI/debian/a-rather-long-file-name.cfg"); got != "long\n" {
				t.Errorf("long-named file = %q", got)
			}

			// The whole image root, /boot/efi included, is on the ext4
			// root: the ext4 magic is at offset 1080 of the partition.
			var root bytes.Buffer
			if _, err := d.ReadPartitionContents(2, &root); err != nil {
				t.Fatal(err)
			}
			if b := root.Bytes(); len(b) < 1082 || b[1080] != 0x53 || b[1081] != 0xEF {
				t.Errorf("root partition does not start with an ext4 superblock")
			}
			rootImg := filepath.Join(t.TempDir(), "root.img")
			if err := os.WriteFile(rootImg, root.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"/boot/efi/EFI/BOOT/" + fallback, "/boot/efi/EFI/debian/grub.cfg"} {
				out, err := exec.CommandContext(t.Context(), hostenv.Find("debugfs"), "-R", "stat "+p, rootImg).CombinedOutput()
				if err != nil || !strings.Contains(string(out), "Type: regular") {
					t.Errorf("%s is not on the ext4 root: %v\n%s", p, err, out)
				}
			}
			if !strings.Contains(report.String(), "UEFI/bootloader") {
				t.Errorf("stage title lacks the boot mode:\n%s", report.String())
			}
		})
	}
}
