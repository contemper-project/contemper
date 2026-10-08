package support_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/support"
)

func TestParseTopLevelRequires(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		support.RequiresFilesLabel: " /usr/bin/cloud-init , /sbin/openrc-init ,,",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/cloud-init", "/sbin/openrc-init"}
	if len(schema.Requires) != len(want) || schema.Requires[0] != want[0] || schema.Requires[1] != want[1] {
		t.Errorf("Requires = %v, want %v", schema.Requires, want)
	}
	if len(schema.Branches) != 0 {
		t.Errorf("expected no branches, got %v", schema.Branches)
	}
}

// TestParseIgnoresUnrelatedAnnotations checks that annotations outside
// contemper's own "io.contemper.*" namespace - such as the standard
// org.opencontainers.image.* keys a publisher sets for a registry UI -
// are silently ignored rather than tripping the "unrecognized key"
// validation that applies to io.contemper.branch.* keys.
func TestParseIgnoresUnrelatedAnnotations(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files": "/sbin/openrc-init",
		"org.opencontainers.image.description":                  "a support image",
		"org.opencontainers.image.source":                       "https://github.com/contemper-project/contemper",
		"org.opencontainers.image.licenses":                     "Apache-2.0",
		"org.opencontainers.image.revision":                     "deadbeef",
		"org.opencontainers.image.title":                        "volumes-support",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Branches) != 1 || schema.Branches[0].Name != "init-system" {
		t.Errorf("expected only the init-system branch, got %+v", schema.Branches)
	}
}

func TestParseBranchAndVariant(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc-init",
		"io.contemper.branch.init-system.openrc.image":           "ghcr.io/example/openrc:v1",
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
		"io.contemper.branch.init-system.systemd.image":          "ghcr.io/example/systemd:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Branches) != 1 {
		t.Fatalf("expected 1 branch, got %d", len(schema.Branches))
	}
	b := schema.Branches[0]
	if b.Name != "init-system" {
		t.Errorf("branch name = %q", b.Name)
	}
	if len(b.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(b.Variants))
	}
	// Variants must be sorted by name: openrc, systemd.
	if b.Variants[0].Name != "openrc" || b.Variants[1].Name != "systemd" {
		t.Errorf("variants not sorted: %+v", b.Variants)
	}
	if b.Variants[0].Image != "ghcr.io/example/openrc:v1" {
		t.Errorf("openrc image = %q", b.Variants[0].Image)
	}
	if b.Default != "" {
		t.Errorf("expected no default, got %q", b.Default)
	}
}

func TestParseNoOpDefault(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.first-boot.cloud-init.requires.files": "/usr/bin/cloud-init",
		"io.contemper.branch.first-boot.cloud-init.image":          "ghcr.io/example/cloud-init:v1",
		"io.contemper.branch.first-boot.default":                   "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := schema.Branches[0]
	if b.Default != "none" {
		t.Fatalf("Default = %q, want none", b.Default)
	}
	var none *support.Variant
	for i := range b.Variants {
		if b.Variants[i].Name == "none" {
			none = &b.Variants[i]
		}
	}
	if none == nil {
		t.Fatalf("expected a %q variant to be declared purely by being the default", "none")
	}
	if none.Image != "" || len(none.Requires) != 0 {
		t.Errorf("no-op default should have no image and no predicate, got %+v", none)
	}
}

func TestParseInvalidNames(t *testing.T) {
	cases := map[string]string{
		"io.contemper.branch.Init-System.openrc.requires.files": "/sbin/openrc-init", // uppercase branch
		"io.contemper.branch.init-system.OpenRC.requires.files": "/sbin/openrc-init", // uppercase variant
		"io.contemper.branch.-init.openrc.requires.files":       "/sbin/openrc-init", // leading dash
		"io.contemper.branch.init-system.default":               "Bad_Name",          // invalid default value
	}
	for key, value := range cases {
		if _, err := support.Parse(map[string]string{key: value}); err == nil {
			t.Errorf("Parse(%s=%s): expected an error", key, value)
		} else if !strings.Contains(err.Error(), key) {
			t.Errorf("Parse(%s=%s): error %q does not name the key", key, value, err)
		}
	}
}

func TestParseInvalidKeyShape(t *testing.T) {
	cases := []string{
		"io.contemper.branch.init-system",                      // missing everything after the branch
		"io.contemper.branch.init-system.openrc",               // no suffix at all
		"io.contemper.branch.init-system.openrc.requires",      // missing ".files"
		"io.contemper.branch.init-system.openrc.requires.dirs", // wrong leaf
		"io.contemper.branch.init-system.openrc.bogus",         // unrecognized suffix
	}
	for _, key := range cases {
		if _, err := support.Parse(map[string]string{key: "x"}); err == nil {
			t.Errorf("Parse(%s): expected an error", key)
		} else if !strings.Contains(err.Error(), key) {
			t.Errorf("Parse(%s): error %q does not name the key", key, err)
		}
	}
}

func TestParseVariantWithoutPredicateOrDefaultIsAnError(t *testing.T) {
	_, err := support.Parse(map[string]string{
		"io.contemper.branch.init-system.openrc.image": "ghcr.io/example/openrc:v1",
	})
	if err == nil {
		t.Fatal("expected an error: a non-default variant needs a predicate")
	}
	if !strings.Contains(err.Error(), "init-system") || !strings.Contains(err.Error(), "openrc") {
		t.Errorf("error %q should name the branch and variant", err)
	}
}

func TestMergeDeclarationsPrecedence(t *testing.T) {
	merged := support.MergeDeclarations(
		map[string]string{"a": "label"},
		map[string]string{"a": "manifest", "b": "manifest"},
		map[string]string{"a": "index", "b": "index", "c": "index"},
	)
	want := map[string]string{"a": "label", "b": "manifest", "c": "index"}
	if !reflect.DeepEqual(merged, want) {
		t.Errorf("merged = %v, want %v", merged, want)
	}
}
