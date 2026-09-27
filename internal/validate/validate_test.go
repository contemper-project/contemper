package validate_test

import (
	"archive/tar"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/validate"
)

var linuxARM64 = v1.Platform{OS: "linux", Architecture: "arm64"}

func completeLayer() []imgtest.File {
	return []imgtest.File{
		{Path: "boot/", Typeflag: tar.TypeDir},
		{Path: "boot/contemper/", Typeflag: tar.TypeDir},
		{Path: "boot/contemper/vmlinuz", Data: []byte("kernel-bytes")},
		{Path: "boot/contemper/initrd", Data: []byte("initrd-bytes")},
		{Path: "boot/contemper/cmdline", Data: []byte("root=LABEL=contemper-root rw\n")},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/os-release", Data: []byte("NAME=Test\n")},
	}
}

func build(t *testing.T, files []imgtest.File) *rootfs.Rootfs {
	t.Helper()
	img, err := imgtest.Image(linuxARM64, nil, files)
	if err != nil {
		t.Fatalf("building image: %v", err)
	}
	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rfs.Close() })
	return rfs
}

func TestValidateHappyPath(t *testing.T) {
	rfs := build(t, completeLayer())
	res, err := validate.Validate(rfs)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if string(res.Kernel) != "kernel-bytes" {
		t.Errorf("Kernel = %q", res.Kernel)
	}
	if string(res.Initrd) != "initrd-bytes" {
		t.Errorf("Initrd = %q", res.Initrd)
	}
	if res.Cmdline != "root=LABEL=contemper-root rw" {
		t.Errorf("Cmdline = %q", res.Cmdline)
	}
	if string(res.OSRelease) != "NAME=Test\n" {
		t.Errorf("OSRelease = %q", res.OSRelease)
	}
}

func TestValidateMissingKernel(t *testing.T) {
	files := completeLayer()
	var without []imgtest.File
	for _, f := range files {
		if f.Path == "boot/contemper/vmlinuz" {
			continue
		}
		without = append(without, f)
	}
	rfs := build(t, without)
	if _, err := validate.Validate(rfs); err == nil {
		t.Fatalf("expected an error for a missing kernel")
	}
}

func TestValidateMissingInit(t *testing.T) {
	files := completeLayer()
	var without []imgtest.File
	for _, f := range files {
		if f.Path == "sbin/init" {
			continue
		}
		without = append(without, f)
	}
	rfs := build(t, without)
	if _, err := validate.Validate(rfs); err == nil {
		t.Fatalf("expected an error for a missing /sbin/init")
	}
}

func TestValidateMissingCmdline(t *testing.T) {
	files := completeLayer()
	var without []imgtest.File
	for _, f := range files {
		if f.Path == "boot/contemper/cmdline" {
			continue
		}
		without = append(without, f)
	}
	rfs := build(t, without)
	res, err := validate.Validate(rfs)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Cmdline != "" {
		t.Errorf("Cmdline = %q, want empty for a missing cmdline file", res.Cmdline)
	}
}

func TestValidateBlankCmdline(t *testing.T) {
	files := completeLayer()
	for i, f := range files {
		if f.Path == "boot/contemper/cmdline" {
			files[i].Data = []byte("   \n")
		}
	}
	rfs := build(t, files)
	res, err := validate.Validate(rfs)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Cmdline != "" {
		t.Errorf("Cmdline = %q, want empty for a blank cmdline file", res.Cmdline)
	}
}

func TestValidateNoOSRelease(t *testing.T) {
	files := completeLayer()
	var without []imgtest.File
	for _, f := range files {
		if f.Path == "etc/os-release" {
			continue
		}
		without = append(without, f)
	}
	rfs := build(t, without)
	res, err := validate.Validate(rfs)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OSRelease != nil {
		t.Errorf("OSRelease should be nil when the image has none, got %q", res.OSRelease)
	}
}
