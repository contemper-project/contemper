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
		Volumes:      []bundle.Volume{{Name: "data", Path: "/data", SizeBytes: 10 << 30, FS: "ext4"}},
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

func TestWriteNestsSupportAndVolumeHelperVariants(t *testing.T) {
	dir := t.TempDir()
	m := &bundle.Manifest{
		FormatVersion:    bundle.FormatVersion,
		ContemperVersion: "test",
		Source:           bundle.ImageRef{Ref: "ghcr.io/example/app:v2", Digest: "sha256:aaa", Repo: "app"},
		Support: &bundle.SupportRef{
			Ref: "ghcr.io/example/support:v1", Digest: "sha256:bbb",
			Variants: []bundle.SupportVariant{{Branch: "init-system", Variant: "openrc", Ref: "ghcr.io/example/support-openrc:v1", Digest: "sha256:ccc"}},
		},
		VolumeHelper: &bundle.SupportRef{
			Ref: "ghcr.io/contemper-project/volumes-support:v1", Digest: "sha256:ddd",
			Variants: []bundle.SupportVariant{{Branch: "init-system", Variant: "systemd", Ref: "ghcr.io/contemper-project/volumes-support-init-system-systemd:v1", Digest: "sha256:eee"}},
		},
		Target:       "qemu-qcow2",
		Arch:         "arm64",
		Disk:         bundle.DiskInfo{File: "disk.qcow2", Format: "qcow2"},
		Volumes:      []bundle.Volume{{Name: "data", Path: "/data", FS: "ext4"}}, // unsized
		Hints:        bundle.Hints{},
		Reproducible: true,
	}
	if err := bundle.Write(dir, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(dir + "/contemper.json")
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["support.variants"]; ok {
		t.Errorf(`the old top-level "support.variants" key must not appear`)
	}
	support, ok := raw["support"].(map[string]any)
	if !ok {
		t.Fatalf("support field missing or wrong shape: %+v", raw["support"])
	}
	if _, ok := support["variants"]; !ok {
		t.Errorf("support.variants should be nested inside support")
	}
	volumeHelper, ok := raw["volumeHelper"].(map[string]any)
	if !ok {
		t.Fatalf("volumeHelper field missing or wrong shape: %+v", raw["volumeHelper"])
	}
	if _, ok := volumeHelper["variants"]; !ok {
		t.Errorf("volumeHelper.variants should be nested inside volumeHelper")
	}
	source := raw["source"].(map[string]any)
	if source["repo"] != "app" {
		t.Errorf("source.repo = %v, want %q", source["repo"], "app")
	}
	volumes := raw["volumes"].([]any)
	vol0 := volumes[0].(map[string]any)
	if _, ok := vol0["size"]; ok {
		t.Errorf("an unsized volume must omit the size field: %+v", vol0)
	}

	var got bundle.Manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Support.Variants[0].Variant != "openrc" || got.VolumeHelper.Variants[0].Variant != "systemd" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Source.Repo != "app" {
		t.Errorf("Source.Repo round-trip = %q, want %q", got.Source.Repo, "app")
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

func TestReadRejectsUnsafeValues(t *testing.T) {
	valid := func() *bundle.Manifest {
		return &bundle.Manifest{
			FormatVersion: bundle.FormatVersion,
			Disk:          bundle.DiskInfo{File: "disk.qcow2", Format: "qcow2"},
			Volumes:       []bundle.Volume{{Name: "data", Path: "/data", FS: "ext4"}},
		}
	}
	dir := t.TempDir()
	if err := bundle.Write(dir, valid()); err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.Read(dir); err != nil {
		t.Fatalf("Read of a valid manifest: %v", err)
	}

	for name, mutate := range map[string]func(*bundle.Manifest){
		"disk file with a directory": func(m *bundle.Manifest) { m.Disk.File = "../../elsewhere/disk.qcow2" },
		"empty disk file":            func(m *bundle.Manifest) { m.Disk.File = "" },
		"volume name with a path":    func(m *bundle.Manifest) { m.Volumes[0].Name = "../../../x" },
	} {
		t.Run(name, func(t *testing.T) {
			m := valid()
			mutate(m)
			dir := t.TempDir()
			if err := bundle.Write(dir, m); err != nil {
				t.Fatal(err)
			}
			if _, err := bundle.Read(dir); err == nil {
				t.Fatal("Read succeeded, want an error")
			}
		})
	}
}

func TestBootField(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"", "uki"},
		{bundle.BootUKI, "uki"},
		{bundle.BootBootloader, "bootloader"},
	} {
		dir := t.TempDir()
		m := &bundle.Manifest{
			FormatVersion: bundle.FormatVersion,
			Boot:          tc.set,
			Disk:          bundle.DiskInfo{File: "disk.qcow2"},
		}
		if err := bundle.Write(dir, m); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(dir + "/contemper.json")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"boot": "`+tc.want+`"`) {
			t.Errorf("Boot %q: manifest lacks \"boot\": %q:\n%s", tc.set, tc.want, raw)
		}
		got, err := bundle.Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Boot != tc.want {
			t.Errorf("Boot %q: Read gives %q, want %q", tc.set, got.Boot, tc.want)
		}
	}
}

func TestReadManifestWithoutBootField(t *testing.T) {
	dir := t.TempDir()
	old := `{"formatVersion": 1, "contemperVersion": "0.2.0", "target": "qemu-qcow2", "arch": "arm64",
  "disk": {"file": "disk.qcow2", "format": "qcow2", "sizeBytes": 1, "sha256": "x"}}`
	if err := os.WriteFile(dir+"/contemper.json", []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := bundle.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Boot != bundle.BootUKI {
		t.Errorf("Boot = %q for a manifest without the field, want %q", m.Boot, bundle.BootUKI)
	}
}
