package source

import (
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// FuzzParseRef checks the source reference parser: it never accepts an
// empty value, picks the kind from the prefix, refuses a docker-daemon
// reference that `docker save` would read as a flag, and String gives a
// form that parses back to the same reference.
func FuzzParseRef(f *testing.F) {
	for _, s := range []string{
		"", "alpine", "ghcr.io/acme/app:v1", "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64),
		"oci-archive:img.tar", "oci:layout", "docker-archive:a/../b.tar", "docker-daemon:app:dev",
		"oci-archive:", "oci:", "docker-archive:", "docker-daemon:", "docker-daemon:-x", "docker-daemon:--rm", "containers-storage:app:dev", "containers-storage:", "containers-storage:-x", "containers-storage:[s]app",
		"oci:/", "oci:.", "OCI:x", "oci-archive", "docker-daemon:a:b:c", "oci:a//b/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		ref, err := ParseRef(raw)
		if err != nil {
			return
		}
		if ref.Value == "" {
			t.Fatalf("ParseRef(%q) has an empty value", raw)
		}
		if ref.Kind == KindRegistry {
			if ref.Value != raw {
				t.Fatalf("registry ref %q changed to %q", raw, ref.Value)
			}
			for _, k := range []Kind{KindOCIArchive, KindOCILayout, KindDockerArchive, KindDockerDaemon, KindContainersStorage} {
				if strings.HasPrefix(raw, string(k)+":") {
					t.Fatalf("%q has the %s prefix but parsed as a registry ref", raw, k)
				}
			}
		} else if !strings.HasPrefix(raw, string(ref.Kind)+":") || raw[len(ref.Kind)+1:] != ref.Value {
			t.Fatalf("ParseRef(%q) = %+v does not match its prefix", raw, ref)
		}
		if (ref.Kind == KindDockerDaemon || ref.Kind == KindContainersStorage) && strings.HasPrefix(ref.Value, "-") {
			t.Fatalf("docker-daemon ref %q would be read as a flag", ref.Value)
		}
		back, err := ParseRef(ref.String())
		if err != nil || back.Kind != ref.Kind {
			t.Fatalf("String %q of %+v does not parse back: %+v, %v", ref.String(), ref, back, err)
		}
		if back.Kind != KindRegistry && back.Kind != KindDockerDaemon && back.Kind != KindContainersStorage && back.Value != filepath.Clean(ref.Value) {
			t.Fatalf("path %q came back as %q", ref.Value, back.Value)
		}
		if again := back.String(); again != ref.String() {
			t.Fatalf("String is not stable: %q then %q", ref.String(), again)
		}
	})
}

// FuzzParseVariantRef checks the rule that keeps a registry image's
// labels from naming local files or pulling with credentials from other
// namespaces: when the parent is a registry reference, an accepted
// variant is a registry reference with a clean repository path, and it
// is not marked Anonymous only if it is in the same registry, within the
// parent's namespace (or the parent's own repository if it has none). A
// local parent's variants are never anonymous.
func FuzzParseVariantRef(f *testing.F) {
	f.Add("ghcr.io/acme/support:v1", "ghcr.io/acme/openrc:v1", true)
	f.Add("ghcr.io/acme/support:v1", "ghcr.io/other/openrc:v1", true)
	f.Add("ghcr.io/acme/support:v1", "docker.io/acme/openrc", true)
	f.Add("ghcr.io/acme/support:v1", "oci:/etc", true)
	f.Add("ghcr.io/acme/support:v1", "docker-daemon:secret", true)
	f.Add("ghcr.io/acme/support:v1", "containers-storage:secret", true)
	f.Add("ghcr.io/acme/support:v1", "ghcr.io/acme/../victim/x:v1", true)
	f.Add("ghcr.io/acme/support:v1", "ghcr.io/acme//x:v1", true)
	f.Add("ghcr.io/acme/support:v1", "ghcr.io/acme/a/b:v1", true)
	f.Add("alpine", "alpine:3", true)
	f.Add("alpine", "ubuntu", true)
	f.Add("docker.io/library/alpine", "alpine", true)
	f.Add("localhost:5000/s", "localhost:5000/s:2", true)
	f.Add("oci:layout", "oci:/anywhere", false)
	f.Add("oci-archive:s.tar", "ghcr.io/x/y", false)
	f.Add("ghcr.io/acme/support@sha256:"+strings.Repeat("a", 64), "GHCR.IO/acme/x:v1", true)
	f.Fuzz(func(t *testing.T, parentRaw, raw string, registryParent bool) {
		parent := Ref{Kind: KindRegistry, Value: parentRaw}
		if !registryParent {
			parent = Ref{Kind: KindOCILayout, Value: parentRaw}
		}
		ref, err := ParseVariantRef(parent, raw)
		if err != nil {
			return
		}
		plain, plainErr := ParseRef(raw)
		plain.Anonymous = ref.Anonymous
		if plainErr != nil || plain != ref {
			t.Fatalf("ParseVariantRef(%q) = %+v but ParseRef gives %+v, %v", raw, ref, plain, plainErr)
		}
		if !registryParent {
			if ref.Anonymous {
				t.Fatalf("local parent %q produced the anonymous variant %q", parentRaw, raw)
			}
			return
		}
		if ref.Kind != KindRegistry {
			t.Fatalf("registry parent %q accepted the local variant %q", parentRaw, raw)
		}
		pr, perr := name.ParseReference(parentRaw)
		vr, verr := name.ParseReference(ref.Value)
		if perr != nil || verr != nil {
			t.Fatalf("accepted references do not parse: %v, %v", perr, verr)
		}
		pRepo, vRepo := pr.Context().RepositoryStr(), vr.Context().RepositoryStr()
		for _, repo := range []string{pRepo, vRepo} {
			if path.Clean(repo) != repo {
				t.Fatalf("unclean repository %q accepted", repo)
			}
		}
		pNS, _, pHas := strings.Cut(pRepo, "/")
		vNS, _, _ := strings.Cut(vRepo, "/")
		sameScope := pr.Context().RegistryStr() == vr.Context().RegistryStr() &&
			((pHas && pNS == vNS) || (!pHas && vRepo == pRepo))
		if ref.Anonymous == sameScope {
			t.Fatalf("variant %q of %q: Anonymous = %v but same scope = %v", raw, parentRaw, ref.Anonymous, sameScope)
		}
	})
}

// FuzzParseArchSelection checks that an accepted --arch value yields
// supported architectures in canonical order without duplicates, and
// that the canonical form of a selection parses back to itself.
func FuzzParseArchSelection(f *testing.F) {
	for _, s := range []string{
		"", "all", "amd64", "arm64", "amd64,arm64", "arm64,amd64", "amd64,amd64", "amd64,", ",", ",amd64",
		"x86_64", "ALL", "all,amd64", "amd64, arm64", "riscv64", "amd64,arm64,all",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		sel, err := ParseArchSelection(value)
		if err != nil {
			return
		}
		if value == "" {
			if len(sel.Archs) != 1 || sel.All || sel.Group {
				t.Fatalf("host selection = %+v", sel)
			}
			return
		}
		if sel.All {
			if value != "all" || len(sel.Archs) != 0 || !sel.Group {
				t.Fatalf("ParseArchSelection(%q) = %+v", value, sel)
			}
			return
		}
		if len(sel.Archs) == 0 {
			t.Fatalf("ParseArchSelection(%q) selects nothing", value)
		}
		last := -1
		for _, a := range sel.Archs {
			i := indexOf(SupportedArchs, a)
			if i <= last {
				t.Fatalf("ParseArchSelection(%q).Archs = %q is unsupported, duplicated or out of order", value, sel.Archs)
			}
			last = i
		}
		if sel.Group != strings.Contains(value, ",") {
			t.Fatalf("ParseArchSelection(%q).Group = %v", value, sel.Group)
		}
		canonical := strings.Join(sel.Archs, ",")
		again, err := ParseArchSelection(canonical)
		if err != nil || !reflect.DeepEqual(again.Archs, sel.Archs) || again.Group != (len(sel.Archs) > 1) {
			t.Fatalf("canonical form %q of %q parses as %+v, %v", canonical, value, again, err)
		}
	})
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// FuzzConfigLabels checks the label readers CheckReady, BootMode and
// SecureBoot against the documented values, for any label contents.
func FuzzConfigLabels(f *testing.F) {
	f.Add("true", "uki", "false")
	f.Add("", "bootloader", "true")
	f.Add("TRUE", "grub", "yes")
	f.Add("true", "", "")
	f.Fuzz(func(t *testing.T, ready, boot, secure string) {
		cfg := &v1.ConfigFile{Config: v1.Config{Labels: map[string]string{
			ReadyLabel: ready, BootLabel: boot, SecureBootLabel: secure,
		}}}
		if (CheckReady(cfg) == nil) != (ready == "true") {
			t.Fatalf("CheckReady with %q", ready)
		}
		mode, err := BootMode(cfg)
		if (err == nil) != (boot == BootUKI || boot == BootBootloader) || (err == nil && mode != boot) {
			t.Fatalf("BootMode with %q = %q, %v", boot, mode, err)
		}
		for _, m := range []string{BootUKI, BootBootloader, boot} {
			on, err := SecureBoot(cfg, m)
			want := secure == "true" && m == BootBootloader
			if (err == nil) != (secure == "true" && m == BootBootloader || secure == "false") || on != want {
				t.Fatalf("SecureBoot(%q, mode %q) = %v, %v", secure, m, on, err)
			}
		}
		// Absent labels are the documented defaults.
		empty := &v1.ConfigFile{}
		if mode, err := BootMode(empty); err != nil || mode != BootUKI {
			t.Fatalf("BootMode without labels = %q, %v", mode, err)
		}
		if on, err := SecureBoot(empty, BootUKI); err != nil || on {
			t.Fatalf("SecureBoot without labels = %v, %v", on, err)
		}
		if CheckReady(empty) == nil || CheckReady(nil) == nil {
			t.Fatalf("CheckReady accepted an image without the label")
		}
	})
}

// FuzzBundleNaming checks that the naming helpers fed with annotation
// and path content give a non-empty base name and a tag, and never
// panic.
func FuzzBundleNaming(f *testing.F) {
	for _, s := range []string{
		"", "alpine", "alpine:3", "docker.io/library/example:dev", "a/b:c/d", "/", ":", "/:t", "x:", ":y",
		"a/b/example.tar", "a/b/example.tar.gz", "example.tgz", ".tar", "..", "a/..", "host:5000/img",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		base, tag := splitRepoTag(s)
		if base == "" || tag == "" && !strings.HasSuffix(s, ":") {
			t.Fatalf("splitRepoTag(%q) = %q, %q", s, base, tag)
		}
		if b := archiveBaseName(s); b == "" {
			t.Fatalf("archiveBaseName(%q) is empty", s)
		}
		d := v1.Descriptor{Annotations: map[string]string{RefNameAnnotation: s}}
		if b, tg := bundleName([]v1.Descriptor{d}, d, "fallback"); b == "" || (tg == "" && s != "" && !strings.HasSuffix(s, ":")) {
			t.Fatalf("bundleName(%q) = %q, %q", s, b, tg)
		}
	})
}
