package main

import (
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/contemper-project/contemper/internal/bundle"
	"github.com/contemper-project/contemper/internal/imgtest"
)

// pushSupportImage pushes a minimal support image to an in-process
// registry and returns its reference and digest.
func pushSupportImage(t *testing.T, host, repoTag string) (ref, digest string) {
	t.Helper()
	plat := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	img, err := imgtest.Image(plat, nil, []imgtest.File{
		{Path: "etc/support-marker", Data: []byte("x\n"), Mode: 0o644},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref = host + "/" + repoTag
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("pushing %s: %v", ref, err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return ref, d.String()
}

// TestConvertTargetDefaultSupportRecordsOrigin converts for the incus
// target without --support and checks the manifest records the target's
// default support image, its resolved digest and the "target" origin; with
// --support it records "flag" and the replacement instead.
func TestConvertTargetDefaultSupportRecordsOrigin(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defRef, defDigest := pushSupportImage(t, u.Host, "contemper-project/incus-support:v1")
	flagRef, flagDigest := pushSupportImage(t, u.Host, "other/support:v2")

	orig := defaultSupportFor
	defaultSupportFor = func(canonical string) string {
		if canonical == "incus-qcow2" {
			return defRef
		}
		return ""
	}
	t.Cleanup(func() { defaultSupportFor = orig })

	files := ukiFixtureFiles
	labels := map[string]string{"io.contemper.ready": "true"}
	for _, c := range []struct {
		name, target, flag              string
		wantRef, wantDigest, wantOrigin string
	}{
		{"incus default", "incus", "", defRef, defDigest, "target"},
		{"canonical name", "incus-qcow2", "", defRef, defDigest, "target"},
		{"flag replaces", "incus", flagRef, flagRef, flagDigest, "flag"},
	} {
		dir, err := convertFixtureWith(t, labels, files, func(o *convertOptions) {
			o.target = c.target
			o.supportRef = c.flag
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		m, err := bundle.Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if m.Target != "incus-qcow2" {
			t.Errorf("%s: target = %q", c.name, m.Target)
		}
		if m.Support == nil {
			t.Fatalf("%s: no support recorded", c.name)
		}
		if m.Support.Ref != c.wantRef || m.Support.Digest != c.wantDigest || m.Support.Origin != c.wantOrigin {
			t.Errorf("%s: support = %+v, want ref %s digest %s origin %s", c.name, *m.Support, c.wantRef, c.wantDigest, c.wantOrigin)
		}
	}

	dir, err := convertFixture(t, labels, files)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := bundle.Read(dir); err != nil || m.Support != nil {
		t.Errorf("qemu target: support = %+v, err = %v; want none", m.Support, err)
	}
}
