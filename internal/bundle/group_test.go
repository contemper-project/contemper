package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testGroup() *Group {
	return &Group{
		FormatVersion: GroupFormatVersion,
		Source:        ImageRef{Ref: "ghcr.io/acme/app:v1", Repo: "app"},
		Bundles: []GroupBundle{
			{Arch: "amd64", Path: "app-v1.x86_64"},
			{Arch: "arm64", Path: "app-v1.aarch64"},
		},
	}
}

func TestGroupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteGroup(dir, "app-v1", testGroup())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "app-v1.multiarch.json"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"digest"`) {
		t.Errorf("group file has a digest key: %s", data)
	}
	g, err := ReadGroup(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := g.Find("arm64"); !ok || p != "app-v1.aarch64" {
		t.Errorf("Find(arm64) = %q, %v", p, ok)
	}
	if _, ok := g.Find("riscv64"); ok {
		t.Errorf("Find(riscv64) found something")
	}
	if g.Source.Repo != "app" || g.Source.Ref != "ghcr.io/acme/app:v1" {
		t.Errorf("source = %+v", g.Source)
	}

	// Overwrite on re-run, leaving no temp files behind.
	g2 := testGroup()
	g2.Bundles = g2.Bundles[:1]
	if _, err := WriteGroup(dir, "app-v1", g2); err != nil {
		t.Fatal(err)
	}
	if g, err = ReadGroup(path); err != nil || len(g.Bundles) != 1 {
		t.Errorf("after overwrite: %+v, %v", g, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("dir has %d entries, want just the group file", len(entries))
	}

	if err := RemoveGroup(dir, "app-v1"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveGroup(dir, "app-v1"); err != nil {
		t.Errorf("removing a missing group file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("group file still there: %v", err)
	}
}

func TestGroupValidation(t *testing.T) {
	cases := map[string]func(g *Group){
		"version":                        func(g *Group) { g.FormatVersion = 2 },
		"no bundles":                     func(g *Group) { g.Bundles = nil },
		"empty arch":                     func(g *Group) { g.Bundles[0].Arch = "" },
		"duplicate":                      func(g *Group) { g.Bundles[1].Arch = "amd64" },
		"unknown":                        func(g *Group) { g.Bundles[1].Arch = "riscv64" },
		"same path":                      func(g *Group) { g.Bundles[1].Path = "app-v1.x86_64" },
		"same path, spelled differently": func(g *Group) { g.Bundles[1].Path = "./app-v1.x86_64/" },
		"empty path":                     func(g *Group) { g.Bundles[0].Path = "" },
		"dot path":                       func(g *Group) { g.Bundles[0].Path = "." },
		"absolute":                       func(g *Group) { g.Bundles[0].Path = "/srv/app" },
		"parent":                         func(g *Group) { g.Bundles[0].Path = "../app" },
		"nested up":                      func(g *Group) { g.Bundles[0].Path = "a/../../app" },
		"backslash":                      func(g *Group) { g.Bundles[0].Path = `a\b` },
	}
	for name, mutate := range cases {
		g := testGroup()
		mutate(g)
		if _, err := WriteGroup(t.TempDir(), "app", g); err == nil {
			t.Errorf("%s: WriteGroup accepted an invalid group", name)
		}
	}
}

func TestRemoveGroupWhenDirIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveGroup(f, "app-v1"); err != nil {
		t.Errorf("RemoveGroup under a regular file: %v", err)
	}
}

func TestReadGroupRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"garbage":  "not json",
		"version":  `{"formatVersion": 9, "source": {"ref": "x"}, "bundles": [{"arch": "amd64", "path": "a"}]}`,
		"empty":    `{"formatVersion": 1, "source": {"ref": "x"}, "bundles": []}`,
		"escaping": `{"formatVersion": 1, "source": {"ref": "x"}, "bundles": [{"arch": "amd64", "path": "../a"}]}`,
		"unknown":  `{"formatVersion": 1, "source": {"ref": "x"}, "bundles": [{"arch": "riscv64", "path": "a"}]}`,
		"samedir":  `{"formatVersion": 1, "source": {"ref": "x"}, "bundles": [{"arch": "amd64", "path": "a"}, {"arch": "arm64", "path": "a"}]}`,
		"dupe":     `{"formatVersion": 1, "source": {"ref": "x"}, "bundles": [{"arch": "amd64", "path": "a"}, {"arch": "amd64", "path": "b"}]}`,
	} {
		p := filepath.Join(dir, name+".multiarch.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadGroup(p); err == nil {
			t.Errorf("%s: ReadGroup accepted it", name)
		}
	}
	if _, err := ReadGroup(filepath.Join(dir, "missing.multiarch.json")); err == nil {
		t.Errorf("missing file: expected an error")
	}
}
