package target_test

import (
	"archive/tar"
	"os"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/imgtest"
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
	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("rootfs.Build: %v", err)
	}
	t.Cleanup(func() { rfs.Close() })

	val, err := validate.Validate(rfs)
	if err != nil {
		t.Fatalf("validate.Validate: %v", err)
	}
	return rfs, val
}

func TestUEFIQcow2Assemble(t *testing.T) {
	rfs, val := syntheticRootfs(t)
	outDir := t.TempDir()

	asm := target.UEFIQcow2{}
	info, warnings, err := asm.Assemble(rfs, val, "arm64", outDir, target.Options{})

	if target.QemuImgMissing() {
		// qemu-img isn't available in this environment (see the MVP
		// plan's M4 notes); the disk.raw step is exercised directly by
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
