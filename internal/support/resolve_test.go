package support_test

import (
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/support"
)

// existsSet returns a predicate over a fixed set of present paths, the
// shape support.Resolve expects.
func existsSet(present ...string) func(string) bool {
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

func initSystemSchema(t *testing.T) *support.Schema {
	t.Helper()
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc-init",
		"io.contemper.branch.init-system.openrc.image":           "ghcr.io/example/openrc:v1",
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
		"io.contemper.branch.init-system.systemd.image":          "ghcr.io/example/systemd:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestResolveSingleMatch(t *testing.T) {
	resolved, err := support.Resolve(initSystemSchema(t), existsSet("/sbin/openrc-init"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 branch resolved, got %d", len(resolved))
	}
	r := resolved[0]
	if r.Branch != "init-system" || r.Variant != "openrc" || r.Image != "ghcr.io/example/openrc:v1" || r.Default {
		t.Errorf("unexpected resolution: %+v", r)
	}
	if len(r.Matched) != 1 || r.Matched[0] != "/sbin/openrc-init" {
		t.Errorf("Matched = %v", r.Matched)
	}
}

func TestResolveDefaultApplied(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.first-boot.cloud-init.requires.files": "/usr/bin/cloud-init",
		"io.contemper.branch.first-boot.cloud-init.image":          "ghcr.io/example/cloud-init:v1",
		"io.contemper.branch.first-boot.default":                   "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := support.Resolve(schema, existsSet()) // nothing present
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r := resolved[0]
	if !r.Default || r.Variant != "none" {
		t.Errorf("expected default variant %q to win, got %+v", "none", r)
	}
	if r.Image != "" {
		t.Errorf("no-op default should carry no image, got %q", r.Image)
	}
}

func TestResolveZeroMatchesNoDefault(t *testing.T) {
	_, err := support.Resolve(initSystemSchema(t), existsSet())
	if err == nil {
		t.Fatal("expected an error: no variant matched and no default is declared")
	}
	msg := err.Error()
	for _, want := range []string{"init-system", "openrc", "/sbin/openrc-init", "systemd", "/usr/lib/systemd/systemd"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
}

func TestResolveTwoMatches(t *testing.T) {
	_, err := support.Resolve(initSystemSchema(t), existsSet("/sbin/openrc-init", "/usr/lib/systemd/systemd"))
	if err == nil {
		t.Fatal("expected an error: two variants matched")
	}
	msg := err.Error()
	for _, want := range []string{"init-system", "openrc", "systemd"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
}

func TestResolveTwoMatchesEvenWithDefault(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc-init",
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
		"io.contemper.branch.init-system.default":                "openrc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := support.Resolve(schema, existsSet("/sbin/openrc-init", "/usr/lib/systemd/systemd")); err == nil {
		t.Fatal("a declared default must not rescue a two-way match")
	}
}

func TestResolveMultipleBranchesSortedByName(t *testing.T) {
	schema, err := support.Parse(map[string]string{
		"io.contemper.branch.zzz.a.requires.files": "/zzz",
		"io.contemper.branch.zzz.a.image":          "ghcr.io/example/zzz:v1",
		"io.contemper.branch.aaa.a.requires.files": "/aaa",
		"io.contemper.branch.aaa.a.image":          "ghcr.io/example/aaa:v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := support.Resolve(schema, existsSet("/zzz", "/aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[0].Branch != "aaa" || resolved[1].Branch != "zzz" {
		t.Fatalf("branches not returned in sorted order: %+v", resolved)
	}
}
