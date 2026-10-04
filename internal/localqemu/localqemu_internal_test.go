package localqemu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateInstanceName(t *testing.T) {
	for _, name := range []string{"myapp", "my-app", "my.app"} {
		if err := ValidateInstanceName(name); err != nil {
			t.Errorf("ValidateInstanceName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\\b"} {
		if err := ValidateInstanceName(name); err == nil {
			t.Errorf("ValidateInstanceName(%q): expected an error", name)
		}
	}
}

func TestDefaultInstance(t *testing.T) {
	got, err := DefaultInstance("contemper-example")
	if err != nil || got != "contemper-example" {
		t.Errorf("DefaultInstance = %q, %v", got, err)
	}
	if _, err := DefaultInstance(""); err == nil {
		t.Errorf("expected an error for an empty repo")
	}
}

func TestStateDirUsesXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg-state")
	got, err := StateDir("myapp")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/xdg-state", "contemper", "local-qemu", "myapp")
	if got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

func TestStateDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/example")
	got, err := StateDir("myapp")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/home/example", ".local", "state", "contemper", "local-qemu", "myapp")
	if got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

func TestStateDirRejectsBadInstanceName(t *testing.T) {
	if _, err := StateDir("../escape"); err == nil {
		t.Errorf("expected an error for an unsafe instance name")
	}
}

func TestPlanVolumeDiskCreatesWhenMissing(t *testing.T) {
	action, err := planVolumeDisk("/state/myapp", "data", 10<<30, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !action.Create {
		t.Errorf("expected Create=true for a missing disk")
	}
	if want := filepath.Join("/state/myapp", "data.qcow2"); action.Path != want {
		t.Errorf("Path = %q, want %q", action.Path, want)
	}
}

func TestPlanVolumeDiskReusesMatchingSize(t *testing.T) {
	action, err := planVolumeDisk("/state/myapp", "data", 10<<30, true, 10<<30)
	if err != nil {
		t.Fatal(err)
	}
	if action.Create {
		t.Errorf("expected Create=false when the existing disk already matches")
	}
}

func TestPlanVolumeDiskRefusesSizeMismatch(t *testing.T) {
	_, err := planVolumeDisk("/state/myapp", "data", 20<<30, true, 10<<30)
	if err == nil {
		t.Fatalf("expected an error for a size mismatch")
	}
	if !strings.Contains(err.Error(), "data") || !strings.Contains(err.Error(), "resize") {
		t.Errorf("error should name the volume and mention resizing: %v", err)
	}
}

func TestClaimInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inst")

	// First claim records the source and creates the directory.
	if err := ClaimInstance(dir, "inst", "ghcr.io/acme/app"); err != nil {
		t.Fatal(err)
	}
	// Same source again, e.g. another tag of the image: fine.
	if err := ClaimInstance(dir, "inst", "ghcr.io/acme/app"); err != nil {
		t.Errorf("same source: %v", err)
	}
	// A different image is refused with a hint.
	err := ClaimInstance(dir, "inst", "ghcr.io/evil/app")
	if err == nil || !strings.Contains(err.Error(), "--name") || !strings.Contains(err.Error(), "ghcr.io/acme/app") {
		t.Errorf("different source: err = %v", err)
	}
	// No source recorded in the manifest: not checked, record untouched.
	if err := ClaimInstance(dir, "inst", ""); err != nil {
		t.Errorf("empty source: %v", err)
	}
	if err := ClaimInstance(dir, "inst", "ghcr.io/acme/app"); err != nil {
		t.Errorf("record changed by an empty-source claim: %v", err)
	}
}

func TestClaimInstanceAcceptsLegacyStateDir(t *testing.T) {
	dir := t.TempDir()
	// A state dir from an earlier version holds volume disks and no record.
	if err := os.WriteFile(filepath.Join(dir, "data.qcow2"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClaimInstance(dir, "inst", "ghcr.io/acme/app"); err != nil {
		t.Fatalf("legacy dir: %v", err)
	}
	if err := ClaimInstance(dir, "inst", "ghcr.io/other/app"); err == nil {
		t.Errorf("record was not written for the legacy dir")
	}
}
