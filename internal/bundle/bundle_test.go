package bundle_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contemper-project/contemper/internal/bundle"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	m := &bundle.Manifest{
		FormatVersion:    bundle.FormatVersion,
		ContemperVersion: "test",
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:           bundle.ImageRef{Ref: "example:dev", Digest: "sha256:abc"},
		Target:           "incus-qcow2",
		Arch:             "arm64",
		Disk: bundle.DiskInfo{
			File: "disk.qcow2", Format: "qcow2", SizeBytes: 1234, SHA256: "sha256:def",
		},
		Volumes:      []string{"/data"},
		Hints:        bundle.Hints{ExposedPorts: []string{"80/tcp"}},
		Reproducible: false,
	}
	if err := bundle.Write(dir, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(dir + "/contemper.json")
	if err != nil {
		t.Fatalf("reading contemper.json: %v", err)
	}
	var got bundle.Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}
	if got.Target != "incus-qcow2" || got.Disk.SHA256 != "sha256:def" || len(got.Volumes) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestWriteSupportOrigin(t *testing.T) {
	dir := t.TempDir()
	m := &bundle.Manifest{
		FormatVersion:    bundle.FormatVersion,
		ContemperVersion: "test",
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:           bundle.ImageRef{Ref: "example:dev", Digest: "sha256:abc"},
		Support:          &bundle.SupportRef{Ref: "example/support:v1", Digest: "sha256:aaa", Origin: "target"},
		Target:           "incus-qcow2",
		Arch:             "arm64",
		Disk: bundle.DiskInfo{
			File: "disk.qcow2", Format: "qcow2", SizeBytes: 1234, SHA256: "sha256:def",
		},
		Hints: bundle.Hints{},
	}
	if err := bundle.Write(dir, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(dir + "/contemper.json")
	if err != nil {
		t.Fatalf("reading contemper.json: %v", err)
	}
	if !strings.Contains(string(data), `"origin": "target"`) {
		t.Errorf("contemper.json missing support.origin: %s", data)
	}

	var got bundle.Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}
	if got.Support == nil || got.Support.Origin != "target" || got.Support.Ref != "example/support:v1" {
		t.Errorf("round-trip mismatch: %+v", got.Support)
	}
}

func TestSortedKeys(t *testing.T) {
	if got := bundle.SortedKeys(nil); got != nil {
		t.Errorf("SortedKeys(nil) = %v, want nil", got)
	}
	m := map[string]struct{}{"80/tcp": {}, "22/tcp": {}}
	got := bundle.SortedKeys(m)
	if len(got) != 2 || got[0] != "22/tcp" || got[1] != "80/tcp" {
		t.Errorf("SortedKeys = %v", got)
	}
}
