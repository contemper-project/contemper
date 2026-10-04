package distros

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

var sinceRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func TestMatrixIsValid(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("matrix has no entries")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		t.Run(e.ID, func(t *testing.T) {
			if e.ID == "" {
				t.Fatal("empty id")
			}
			if seen[e.ID] {
				t.Errorf("duplicate id %q", e.ID)
			}
			seen[e.ID] = true
			if !slices.Contains(Kinds, e.Kind) {
				t.Errorf("kind %q not in %v", e.Kind, Kinds)
			}
			if e.Family == "" {
				t.Error("empty family")
			}
			if e.Base == "" {
				t.Error("empty base")
			}
			if _, ok := e.BuildArgs["BASE"]; ok {
				t.Error(`buildArgs must not set BASE; use "base"`)
			}
			if _, err := os.Stat(filepath.Join(e.Containerfile, "Containerfile")); err != nil {
				t.Errorf("containerfile %q: %v", e.Containerfile, err)
			}
			if len(e.Arch) == 0 {
				t.Error("no architectures")
			}
			if !slices.Contains(e.Arch, "amd64") {
				t.Error("every entry is tested on amd64")
			}
			for i, a := range e.Arch {
				if !slices.Contains(Arches, a) {
					t.Errorf("arch %q not in %v", a, Arches)
				}
				if slices.Contains(e.Arch[:i], a) {
					t.Errorf("duplicate arch %q", a)
				}
			}
			if !sinceRE.MatchString(e.Since) {
				t.Errorf("since %q is not a release version like 0.3.0", e.Since)
			}
		})
	}
}

func TestMatrixRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`[{"id":"x","bogus":1}]`)); err == nil {
		t.Fatal("want an error for an unknown field")
	}
}

func TestFind(t *testing.T) {
	entries := []Entry{{ID: "a"}, {ID: "b"}}
	if e, err := Find(entries, "b"); err != nil || e.ID != "b" {
		t.Fatalf("Find(b) = %v, %v", e, err)
	}
	if _, err := Find(entries, "c"); err == nil {
		t.Fatal("want an error for an unknown id")
	}
}

func TestMirrorRef(t *testing.T) {
	const m = "mirror.example"
	tests := []struct{ ref, mirror, want string }{
		{"alpine:3.24", m, m + "/library/alpine:3.24"},
		{"docker.io/library/debian:13", m, m + "/library/debian:13"},
		{"docker.io/alpine", m, m + "/library/alpine"},
		{"rockylinux/rockylinux:9", m, m + "/rockylinux/rockylinux:9"},
		{"quay.io/centos/centos:stream10", m, "quay.io/centos/centos:stream10"},
		{"registry.opensuse.org/opensuse/leap:16.0", m, "registry.opensuse.org/opensuse/leap:16.0"},
		{"localhost:5000/x:1", m, "localhost:5000/x:1"},
		{"alpine:3.24", m + "/", m + "/library/alpine:3.24"},
		{"alpine:3.24", "", "alpine:3.24"},
	}
	for _, tt := range tests {
		if got := MirrorRef(tt.ref, tt.mirror); got != tt.want {
			t.Errorf("MirrorRef(%q, %q) = %q, want %q", tt.ref, tt.mirror, got, tt.want)
		}
	}
}
