package volumehelper_test

import (
	"archive/tar"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/volumehelper"
)

var linuxAMD64 = v1.Platform{OS: "linux", Architecture: "amd64"}

func newTestRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
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

func withAnnotations(img v1.Image, anns map[string]string) v1.Image {
	return mutate.Annotations(img, anns).(v1.Image)
}

// sourceWithTools builds a minimal source image providing every tool
// CheckPrereqs looks for, plus whichever init marker files are given.
func sourceWithTools(t *testing.T, extra ...imgtest.File) v1.Image {
	t.Helper()
	files := []imgtest.File{
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/mkfs.ext4", Data: []byte("bin")},
		{Path: "bin/", Typeflag: tar.TypeDir},
		{Path: "bin/dd", Data: []byte("bin")},
		{Path: "bin/od", Data: []byte("bin")},
	}
	files = append(files, extra...)
	img, err := imgtest.Image(linuxAMD64, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestCheckPrereqsAllPresent(t *testing.T) {
	rfs, err := rootfs.Build(t.Context(), sourceWithTools(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()
	if err := volumehelper.CheckPrereqs(rfs); err != nil {
		t.Errorf("CheckPrereqs: %v", err)
	}
}

func TestCheckPrereqsMissingMkfsExt4(t *testing.T) {
	img, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "bin/", Typeflag: tar.TypeDir},
		{Path: "bin/dd", Data: []byte("bin")},
		{Path: "bin/od", Data: []byte("bin")},
	})
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(t.Context(), img)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()

	err = volumehelper.CheckPrereqs(rfs)
	if err == nil {
		t.Fatal("expected an error for a missing mkfs.ext4")
	}
	if !strings.Contains(err.Error(), "mkfs.ext4") || !strings.Contains(err.Error(), "--no-volume-helper") {
		t.Errorf("error should name mkfs.ext4 and --no-volume-helper: %v", err)
	}
}

func TestMergeResolvesWinningVariant(t *testing.T) {
	host := newTestRegistry(t)

	src := sourceWithTools(t,
		imgtest.File{Path: "sbin/openrc", Data: []byte("bin")},
	)

	openrcVariant, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/init.d/", Typeflag: tar.TypeDir},
		{Path: "etc/init.d/contemper-volumes", Data: []byte("openrc script")},
	})
	if err != nil {
		t.Fatal(err)
	}
	openrcRef := pushImage(t, host, "contemper-project/volumes-support-init-system-openrc:v1", openrcVariant)

	systemdRef := host + "/contemper-project/volumes-support-init-system-systemd:not-pushed"

	helperImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{{Path: "etc/contemper-marker", Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	helperImg = withAnnotations(helperImg, map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc",
		"io.contemper.branch.init-system.openrc.image":           openrcRef,
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
		"io.contemper.branch.init-system.systemd.image":          systemdRef,
	})
	helperRef := pushImage(t, host, "contemper-project/volumes-support:v1", helperImg)

	result, err := volumehelper.Merge(t.Context(), helperRef, src, linuxAMD64)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	defer result.Img.Close()
	for _, vi := range result.VariantImages {
		defer vi.Close()
	}

	if len(result.Resolved) != 1 || result.Resolved[0].Variant != "openrc" {
		t.Fatalf("unexpected resolution: %+v", result.Resolved)
	}
	if len(result.Variants) != 1 || result.Variants[0].Ref != openrcRef {
		t.Fatalf("unexpected manifest variants: %+v", result.Variants)
	}
	if len(result.Overlays) != 2 { // helper image + winning variant
		t.Errorf("expected 2 overlays (helper + openrc variant), got %d", len(result.Overlays))
	}
}

// TestMergeResolvesOnMergedUsr checks branch resolution against a
// merged-/usr source image (real openrc at /usr/sbin/openrc, /sbin a
// symlink to /usr/sbin - as on Fedora, Arch and current Debian/Ubuntu):
// the "openrc" branch's requires.files is written as "/sbin/openrc" (see
// docs/reference/support-image-annotations.md and
// docs/guide/volumes.md), which only matches such an image because
// rootfs.Resolve follows a symlink in an intermediate path segment, not
// just the final one.
func TestMergeResolvesOnMergedUsr(t *testing.T) {
	host := newTestRegistry(t)

	src, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "sbin/", Typeflag: tar.TypeSymlink, Linkname: "usr/sbin"},
		{Path: "usr/", Typeflag: tar.TypeDir},
		{Path: "usr/sbin/", Typeflag: tar.TypeDir},
		{Path: "usr/sbin/mkfs.ext4", Data: []byte("bin")},
		{Path: "usr/sbin/openrc", Data: []byte("bin")},
		{Path: "bin/", Typeflag: tar.TypeDir},
		{Path: "bin/dd", Data: []byte("bin")},
		{Path: "bin/od", Data: []byte("bin")},
	})
	if err != nil {
		t.Fatal(err)
	}

	openrcVariant, err := imgtest.Image(linuxAMD64, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	openrcRef := pushImage(t, host, "contemper-project/volumes-support-init-system-openrc:v1", openrcVariant)

	helperImg, err := imgtest.Image(linuxAMD64, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	helperImg = withAnnotations(helperImg, map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc",
		"io.contemper.branch.init-system.openrc.image":           openrcRef,
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
	})
	helperRef := pushImage(t, host, "contemper-project/volumes-support:v1", helperImg)

	result, err := volumehelper.Merge(t.Context(), helperRef, src, linuxAMD64)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	defer result.Img.Close()
	for _, vi := range result.VariantImages {
		defer vi.Close()
	}

	if len(result.Resolved) != 1 || result.Resolved[0].Variant != "openrc" {
		t.Fatalf("unexpected resolution on a merged-/usr image: %+v", result.Resolved)
	}
}

func TestMergeNoMatchNoDefaultMentionsNoVolumeHelper(t *testing.T) {
	host := newTestRegistry(t)

	// Neither openrc nor systemd is present in the source, and there's no
	// declared default.
	src := sourceWithTools(t)

	helperImg, err := imgtest.Image(linuxAMD64, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	helperImg = withAnnotations(helperImg, map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc",
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
	})
	helperRef := pushImage(t, host, "contemper-project/volumes-support:v1", helperImg)

	_, err = volumehelper.Merge(t.Context(), helperRef, src, linuxAMD64)
	if err == nil {
		t.Fatal("expected an error when no init-system variant matches")
	}
	if !strings.Contains(err.Error(), "branch init-system") || !strings.Contains(err.Error(), "--no-volume-helper") {
		t.Errorf("error should name the branch and mention --no-volume-helper: %v", err)
	}
}

func TestMergePlainOverrideWithNoBranches(t *testing.T) {
	host := newTestRegistry(t)
	src := sourceWithTools(t)

	plainImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/my-custom-helper", Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := pushImage(t, host, "custom-helper:v1", plainImg)

	result, err := volumehelper.Merge(t.Context(), ref, src, linuxAMD64)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	defer result.Img.Close()

	if len(result.Resolved) != 0 || len(result.Variants) != 0 {
		t.Errorf("a plain override should resolve no branches: %+v / %+v", result.Resolved, result.Variants)
	}
	if len(result.Overlays) != 1 {
		t.Errorf("expected exactly the helper's own overlay, got %d", len(result.Overlays))
	}
}
