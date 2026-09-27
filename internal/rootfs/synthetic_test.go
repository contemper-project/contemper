package rootfs_test

import (
	"archive/tar"
	"testing"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
)

func buildTestRootfs(t *testing.T, files []imgtest.File) *rootfs.Rootfs {
	t.Helper()
	img, err := imgtest.Image(linuxAMD64, nil, files)
	if err != nil {
		t.Fatalf("building image: %v", err)
	}
	rfs, err := rootfs.Build(img)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { rfs.Close() })
	return rfs
}

func TestWriteFileCreatesNewFile(t *testing.T) {
	rfs := buildTestRootfs(t, []imgtest.File{{Path: "etc/", Typeflag: tar.TypeDir}})

	if err := rfs.WriteFile("/etc/contemper/build", []byte("a=b\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := rfs.ReadFile("/etc/contemper/build")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "a=b\n" {
		t.Errorf("content = %q, want %q", got, "a=b\n")
	}
}

func TestWriteFileCreatesMissingParentDirs(t *testing.T) {
	// No image layer ever creates an explicit /etc/contemper entry; a
	// nested WriteFile must still work (and be visible to debugfs's
	// mkdir-before-write ordering, which depends on the parent existing
	// in the index).
	rfs := buildTestRootfs(t, []imgtest.File{{Path: "etc/", Typeflag: tar.TypeDir}})

	if err := rfs.WriteFile("/etc/contemper/build", []byte("a=b\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, ok := rfs.Lookup("/etc/contemper"); !ok {
		t.Errorf("expected /etc/contemper to have been created")
	}
	got, err := rfs.ReadFile("/etc/contemper/build")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "a=b\n" {
		t.Errorf("content = %q", got)
	}
}

func TestWriteFileReplacesExistingFile(t *testing.T) {
	rfs := buildTestRootfs(t, []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/fstab", Data: []byte("LABEL=contemper-root / ext4 rw,relatime 0 1\n")},
	})

	newContent := []byte("LABEL=contemper-root / ext4 rw,relatime 0 1\nLABEL=data /data ext4 defaults,nofail 0 2\n")
	if err := rfs.WriteFile("/etc/fstab", newContent); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := rfs.ReadFile("/etc/fstab")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(newContent) {
		t.Errorf("content = %q, want %q", got, newContent)
	}
}

func TestEnsureDirCreatesMissingSegments(t *testing.T) {
	rfs := buildTestRootfs(t, []imgtest.File{{Path: "var/", Typeflag: tar.TypeDir}})

	if err := rfs.EnsureDir("/var/lib/myapp"); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	for _, p := range []string{"/var", "/var/lib", "/var/lib/myapp"} {
		e, ok := rfs.Lookup(p)
		if !ok {
			t.Fatalf("expected %s to exist", p)
		}
		if e.Header.Typeflag != tar.TypeDir {
			t.Errorf("%s: typeflag = %v, want TypeDir", p, e.Header.Typeflag)
		}
	}
}

func TestEnsureDirLeavesExistingEntryAlone(t *testing.T) {
	rfs := buildTestRootfs(t, []imgtest.File{
		{Path: "data/", Typeflag: tar.TypeDir, Mode: 0o700},
	})
	before, _ := rfs.Lookup("/data")

	if err := rfs.EnsureDir("/data"); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	after, _ := rfs.Lookup("/data")
	if before != after {
		t.Errorf("EnsureDir replaced an existing entry it should have left alone")
	}
}

func TestEnsureDirRoot(t *testing.T) {
	rfs := buildTestRootfs(t, []imgtest.File{{Path: "etc/", Typeflag: tar.TypeDir}})
	if err := rfs.EnsureDir("/"); err != nil {
		t.Fatalf("EnsureDir(/): %v", err)
	}
}

func TestWriteFileThenPopulateOrderIsIndexDriven(t *testing.T) {
	// Regression check: after WriteFile appends past the tar's original
	// end-of-archive markers, the index (not a sequential re-read of the
	// tar) must still be what every other Rootfs method sees.
	rfs := buildTestRootfs(t, []imgtest.File{{Path: "etc/", Typeflag: tar.TypeDir}})
	if err := rfs.WriteFile("/etc/a", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := rfs.WriteFile("/etc/b", []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := rfs.WriteFile("/etc/a", []byte("overwritten")); err != nil {
		t.Fatal(err)
	}
	got, err := rfs.ReadFile("/etc/a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "overwritten" {
		t.Errorf("/etc/a = %q, want %q", got, "overwritten")
	}
	got, err = rfs.ReadFile("/etc/b")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "2" {
		t.Errorf("/etc/b = %q, want %q", got, "2")
	}
}
