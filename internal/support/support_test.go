package support_test

import (
	"archive/tar"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/support"
)

var linuxAMD64 = v1.Platform{OS: "linux", Architecture: "amd64"}

func TestCheckRequiresSatisfied(t *testing.T) {
	base, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "usr/bin/cloud-init", Data: []byte("bin")},
	})
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()

	schema, err := support.Parse(map[string]string{
		support.RequiresFilesAnnotation: "/usr/bin/cloud-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.CheckRequires(rfs); err != nil {
		t.Errorf("CheckRequires: %v", err)
	}
}

func TestCheckRequiresMissing(t *testing.T) {
	base, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/hostname", Data: []byte("host")},
	})
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()

	schema, err := support.Parse(map[string]string{
		support.RequiresFilesAnnotation: "/usr/bin/cloud-init, /etc/hostname",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.CheckRequires(rfs); err == nil {
		t.Fatalf("expected an error naming the missing path")
	}
}

func TestMergeAppliesSupportOverlay(t *testing.T) {
	base, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/hostname", Data: []byte("base\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	supportImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "usr/", Typeflag: tar.TypeDir},
		{Path: "usr/bin/", Typeflag: tar.TypeDir},
		{Path: "usr/bin/contemper-agent", Data: []byte("agent\n")},
	})
	if err != nil {
		t.Fatal(err)
	}

	rfs, err := rootfs.Build(base, supportImg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	if _, ok := rfs.Lookup("/etc/hostname"); !ok {
		t.Errorf("base file should survive the overlay")
	}
	if _, ok := rfs.Lookup("/usr/bin/contemper-agent"); !ok {
		t.Errorf("support file should be merged in")
	}
}
