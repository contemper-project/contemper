package bundle_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/contemper-project/contemper/internal/bundle"
)

// dirEntries returns the sorted base names of parent's direct children,
// for asserting that a temp or "old" sibling was (or wasn't) left behind.
func dirEntries(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", parent, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func writeBundle(t *testing.T, dir string, extraFiles map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "contemper.json"), []byte(`{"formatVersion":1}`), 0o644); err != nil {
		t.Fatalf("writing contemper.json: %v", err)
	}
	for name, content := range extraFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

func TestStageCreatesSiblingDir(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "my-bundle-v1.aarch64")

	stage, err := bundle.Stage(dest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if filepath.Dir(stage) != parent {
		t.Errorf("Stage(%s) = %s, not a child of %s", dest, stage, parent)
	}
	info, err := os.Stat(stage)
	if err != nil {
		t.Fatalf("Stat(stage): %v", err)
	}
	if !info.IsDir() {
		t.Errorf("Stage(%s) did not create a directory", dest)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("Stage(%s) mode = %o, want 0755", dest, perm)
	}
	if stage == dest {
		t.Errorf("Stage(%s) returned dest itself", dest)
	}
}

func TestCheckDest(t *testing.T) {
	parent := t.TempDir()

	missing := filepath.Join(parent, "missing")
	if err := bundle.CheckDest(missing); err != nil {
		t.Errorf("CheckDest(missing) = %v, want nil", err)
	}

	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bundle.CheckDest(empty); err != nil {
		t.Errorf("CheckDest(empty dir) = %v, want nil", err)
	}

	prevBundle := filepath.Join(parent, "prev-bundle")
	writeBundle(t, prevBundle, map[string]string{"disk.qcow2": "old-disk"})
	if err := bundle.CheckDest(prevBundle); err != nil {
		t.Errorf("CheckDest(previous bundle) = %v, want nil", err)
	}

	notABundle := filepath.Join(parent, "not-a-bundle")
	if err := os.Mkdir(notABundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notABundle, "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bundle.CheckDest(notABundle); err == nil {
		t.Errorf("CheckDest(non-bundle non-empty dir) = nil, want an error")
	}
}

func TestCommitIntoMissingDest(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "bundle-v1.aarch64")

	stage, err := bundle.Stage(dest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "contemper.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "disk.qcow2"), []byte("new-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := bundle.Commit(stage, dest); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stage dir %s still exists after Commit", stage)
	}
	data, err := os.ReadFile(filepath.Join(dest, "disk.qcow2"))
	if err != nil || string(data) != "new-disk" {
		t.Errorf("dest disk.qcow2 = %q, %v; want new-disk, nil", data, err)
	}
	if got := dirEntries(t, parent); len(got) != 1 || got[0] != "bundle-v1.aarch64" {
		t.Errorf("parent dir entries = %v, want only the bundle dir (no leftover temp dirs)", got)
	}
}

func TestCommitReplacesOldBundleDroppingStaleFiles(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "bundle-v1.aarch64")

	// An old bundle from a --keep-raw run: contemper.json, disk.qcow2,
	// and a disk.raw that a run without --keep-raw would not recreate.
	writeBundle(t, dest, map[string]string{
		"disk.qcow2": "old-disk",
		"disk.raw":   "stale-raw",
	})

	stage, err := bundle.Stage(dest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "contemper.json"), []byte(`{"formatVersion":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "disk.qcow2"), []byte("new-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := bundle.Commit(stage, dest); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dest, "disk.qcow2"))
	if err != nil || string(data) != "new-disk" {
		t.Errorf("dest disk.qcow2 = %q, %v; want new-disk, nil", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "disk.raw")); !os.IsNotExist(err) {
		t.Errorf("stale disk.raw from the old bundle survived the replace")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stage dir %s still exists after Commit", stage)
	}
	if got := dirEntries(t, parent); len(got) != 1 || got[0] != "bundle-v1.aarch64" {
		t.Errorf("parent dir entries = %v, want only the bundle dir (no leftover old-bundle or temp dirs)", got)
	}
}

func TestCommitRefusesNonBundleNonEmptyDir(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "not-a-bundle")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "README.txt"), []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}

	stage, err := bundle.Stage(dest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.WriteFile(filepath.Join(stage, "contemper.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := bundle.Commit(stage, dest); err == nil {
		t.Fatalf("Commit into a non-bundle non-empty dir succeeded, want a refusal")
	}

	data, err := os.ReadFile(filepath.Join(dest, "README.txt"))
	if err != nil || string(data) != "do not touch" {
		t.Errorf("dest was modified by the refused Commit: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "contemper.json")); !os.IsNotExist(err) {
		t.Errorf("dest gained a contemper.json from the refused Commit")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Errorf("stage dir was removed by the refused Commit: %v", err)
	}
}

// TestFailureMidwayLeavesBundleAndTempDirsClean models what convert does
// on an error after Stage but before Commit: it removes the staging
// directory and never touches dest, the same as runConvert's own
// deferred cleanup.
func TestFailureMidwayLeavesBundleAndTempDirsClean(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "bundle-v1.aarch64")
	writeBundle(t, dest, map[string]string{"disk.qcow2": "good-disk"})

	before, err := os.ReadFile(filepath.Join(dest, "disk.qcow2"))
	if err != nil {
		t.Fatal(err)
	}

	stage, err := bundle.Stage(dest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "disk.qcow2"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate a mid-assembly failure: the caller returns an error
	// without ever calling Commit, and its deferred cleanup removes the
	// staging directory.
	_ = os.RemoveAll(stage)

	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("staging dir %s survived cleanup", stage)
	}
	after, err := os.ReadFile(filepath.Join(dest, "disk.qcow2"))
	if err != nil || string(after) != string(before) {
		t.Errorf("existing bundle changed after a failed convert: got %q, want %q (err %v)", after, before, err)
	}
	if got := dirEntries(t, parent); len(got) != 1 || got[0] != "bundle-v1.aarch64" {
		t.Errorf("parent dir entries = %v, want only the untouched bundle dir", got)
	}
}
