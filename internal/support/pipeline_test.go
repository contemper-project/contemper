package support_test

import (
	"archive/tar"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/source"
	"github.com/contemper-project/contemper/internal/support"
)

// This file exercises the full support-image pipeline - manifest read,
// schema parse, predicate resolution against the source's own merged
// filesystem, then fetching only the winners and merging - against an
// in-process registry, mirroring what cmd/contemper's convert pipeline
// does (see resolveVariants in cmd/contemper/run.go).

// requestTracker wraps an http.Handler and records every request path it
// sees, so tests can assert a losing variant's manifest or blobs were
// never requested.
type requestTracker struct {
	inner http.Handler
	mu    sync.Mutex
	paths []string
}

func (rt *requestTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mu.Lock()
	rt.paths = append(rt.paths, r.URL.Path)
	rt.mu.Unlock()
	rt.inner.ServeHTTP(w, r)
}

func (rt *requestTracker) reset() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.paths = nil
}

func (rt *requestTracker) requestedPathContaining(substr string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, p := range rt.paths {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

func newTestRegistry(t *testing.T) (host string, tracker *requestTracker) {
	t.Helper()
	tracker = &requestTracker{inner: registry.New(registry.Logger(log.New(io.Discard, "", 0)))}
	srv := httptest.NewServer(tracker)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host, tracker
}

func pushImage(t *testing.T, host, repoTag string, img v1.Image) string {
	t.Helper()
	ref := host + "/" + repoTag
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("name.NewTag(%s): %v", ref, err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("pushing %s: %v", ref, err)
	}
	return ref
}

// withAnnotations sets manifest-level annotations (not config labels) on
// img, the way a real support image publishes its schema.
func withAnnotations(img v1.Image, anns map[string]string) v1.Image {
	return mutate.Annotations(img, anns).(v1.Image)
}

// resolveAndMerge is a minimal stand-in for the convert pipeline's support
// stage (cmd/contemper/run.go's resolveVariants plus its final
// rootfs.Build call): parse the support image's schema, resolve it
// against the source's own merged filesystem, fetch only the winning
// variants, and return the final merged rootfs alongside the resolution.
func resolveAndMerge(t *testing.T, srcImg v1.Image, supportRef string, platform v1.Platform) (*rootfs.Rootfs, []support.Resolved) {
	t.Helper()
	ref, err := source.ParseRef(supportRef)
	if err != nil {
		t.Fatal(err)
	}
	supportImg, err := source.Load(ref, platform)
	if err != nil {
		t.Fatalf("loading support image: %v", err)
	}
	t.Cleanup(supportImg.Close)

	manifest, err := supportImg.Image.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	indexAnnotations, err := source.IndexAnnotations(ref, platform)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := support.Parse(support.MergeAnnotations(indexAnnotations, manifest.Annotations))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	srcRfs, err := rootfs.Build(srcImg)
	if err != nil {
		t.Fatal(err)
	}
	defer srcRfs.Close()

	resolved, err := support.Resolve(schema, func(p string) bool {
		_, err := srcRfs.Resolve(p)
		return err == nil
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	overlays := []v1.Image{supportImg.Image}
	for _, r := range resolved {
		if r.Image == "" {
			continue
		}
		vref, err := source.ParseRef(r.Image)
		if err != nil {
			t.Fatal(err)
		}
		vimg, err := source.Load(vref, platform)
		if err != nil {
			t.Fatalf("loading variant %s/%s: %v", r.Branch, r.Variant, err)
		}
		t.Cleanup(vimg.Close)
		overlays = append(overlays, vimg.Image)
	}

	finalRfs, err := rootfs.Build(srcImg, overlays...)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return finalRfs, resolved
}

func TestPipelineIgnoresVariantImageOwnAnnotations(t *testing.T) {
	host, _ := newTestRegistry(t)

	src, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/openrc-init", Data: []byte("bin")},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The variant image declares its own branch annotations, with an
	// invalid branch name so that if resolution ever mistakenly parsed
	// them, it would fail loudly rather than silently. Resolution is
	// exactly one level deep: this must never be read.
	variant, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "usr/", Typeflag: tar.TypeDir},
		{Path: "usr/local/", Typeflag: tar.TypeDir},
		{Path: "usr/local/bin/", Typeflag: tar.TypeDir},
		{Path: "usr/local/bin/agent", Data: []byte("agent")},
	})
	if err != nil {
		t.Fatal(err)
	}
	variant = withAnnotations(variant, map[string]string{
		"io.contemper.branch.Trap.oops.requires.files": "/", // invalid: uppercase branch name
	})
	variantRef := pushImage(t, host, "openrc:v1", variant)

	supportImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{{Path: "etc/", Typeflag: tar.TypeDir}})
	if err != nil {
		t.Fatal(err)
	}
	supportImg = withAnnotations(supportImg, map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files": "/sbin/openrc-init",
		"io.contemper.branch.init-system.openrc.image":          variantRef,
	})
	supportRef := pushImage(t, host, "support:v1", supportImg)

	rfs, resolved := resolveAndMerge(t, src, supportRef, linuxAMD64)
	defer rfs.Close()

	if len(resolved) != 1 || resolved[0].Variant != "openrc" {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
	if _, ok := rfs.Lookup("/usr/local/bin/agent"); !ok {
		t.Errorf("winning variant's file should be merged in")
	}
}

func TestPipelineNeverFetchesLosingVariant(t *testing.T) {
	host, tracker := newTestRegistry(t)

	src, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/openrc-init", Data: []byte("bin")},
	})
	if err != nil {
		t.Fatal(err)
	}

	openrcVariant, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/openrc-marker", Data: []byte("openrc")},
	})
	if err != nil {
		t.Fatal(err)
	}
	openrcRef := pushImage(t, host, "openrc:v1", openrcVariant)

	systemdVariant, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/systemd-marker", Data: []byte("systemd")},
	})
	if err != nil {
		t.Fatal(err)
	}
	systemdRef := pushImage(t, host, "systemd:v1", systemdVariant)

	supportImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{{Path: "etc/", Typeflag: tar.TypeDir}})
	if err != nil {
		t.Fatal(err)
	}
	supportImg = withAnnotations(supportImg, map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc-init",
		"io.contemper.branch.init-system.openrc.image":           openrcRef,
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
		"io.contemper.branch.init-system.systemd.image":          systemdRef,
	})
	supportRef := pushImage(t, host, "support:v1", supportImg)

	// Only requests made from here on (during resolution and merging)
	// count; everything above was setup (pushing the fixtures).
	tracker.reset()

	rfs, resolved := resolveAndMerge(t, src, supportRef, linuxAMD64)
	defer rfs.Close()

	if len(resolved) != 1 || resolved[0].Variant != "openrc" {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
	if tracker.requestedPathContaining("/systemd/") {
		t.Errorf("the losing systemd variant should never have been requested; got %v", tracker.paths)
	}
	if !tracker.requestedPathContaining("/openrc/") {
		t.Errorf("expected the winning openrc variant to have been requested; got %v", tracker.paths)
	}
	if _, ok := rfs.Lookup("/etc/systemd-marker"); ok {
		t.Errorf("the losing variant's file must not be merged in")
	}
}

func TestPipelineMergeOrder(t *testing.T) {
	host, _ := newTestRegistry(t)

	src, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/openrc-init", Data: []byte("bin")},
		{Path: "usr/", Typeflag: tar.TypeDir},
		{Path: "usr/bin/", Typeflag: tar.TypeDir},
		{Path: "usr/bin/cloud-init", Data: []byte("bin")},
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/marker", Data: []byte("source\n")},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Branch "aaa" sorts before "bbb": if the merge truly applies
	// branches in sorted-name order, bbb's variant is applied last and
	// wins the path both variants write.
	aVariant, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/marker", Data: []byte("aaa\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	aRef := pushImage(t, host, "aaa:v1", aVariant)

	bVariant, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/marker", Data: []byte("bbb\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	bRef := pushImage(t, host, "bbb:v1", bVariant)

	supportImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/marker", Data: []byte("support\n")},
		{Path: "etc/support-only", Data: []byte("support-only\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	supportImg = withAnnotations(supportImg, map[string]string{
		"io.contemper.branch.aaa.a.requires.files": "/sbin/openrc-init",
		"io.contemper.branch.aaa.a.image":          aRef,
		"io.contemper.branch.bbb.b.requires.files": "/usr/bin/cloud-init",
		"io.contemper.branch.bbb.b.image":          bRef,
	})
	supportRef := pushImage(t, host, "support:v1", supportImg)

	rfs, resolved := resolveAndMerge(t, src, supportRef, linuxAMD64)
	defer rfs.Close()

	if len(resolved) != 2 {
		t.Fatalf("expected 2 branches resolved, got %+v", resolved)
	}

	got, err := rfs.ReadFile("/etc/marker")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bbb\n" {
		t.Errorf("etc/marker = %q, want %q (source, support, aaa, then bbb last)", got, "bbb\n")
	}
	if _, ok := rfs.Lookup("/etc/support-only"); !ok {
		t.Errorf("the support image's own layer should be merged in")
	}
}
