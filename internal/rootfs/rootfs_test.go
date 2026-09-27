package rootfs_test

import (
	"archive/tar"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
)

var linuxAMD64 = v1.Platform{OS: "linux", Architecture: "amd64"}

func TestWhiteoutsAndOpaqueDirs(t *testing.T) {
	base := []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/keep.conf", Data: []byte("base-keep\n")},
		{Path: "etc/remove.conf", Data: []byte("base-remove\n")},
		{Path: "var/", Typeflag: tar.TypeDir},
		{Path: "var/lib/", Typeflag: tar.TypeDir},
		{Path: "var/lib/hidden-by-opaque", Data: []byte("should be hidden\n")},
	}
	upper := []imgtest.File{
		// Whiteout removes etc/remove.conf from the merged view.
		imgtest.WhiteoutFile("etc/remove.conf"),
		{Path: "etc/added.conf", Data: []byte("upper-added\n")},
		// Opaque marker hides all lower-layer entries under var/lib.
		{Path: "var/lib/.wh..wh..opq", Data: nil},
		{Path: "var/lib/fresh", Data: []byte("fresh\n")},
	}

	img, err := imgtest.Image(linuxAMD64, nil, base, upper)
	if err != nil {
		t.Fatalf("building image: %v", err)
	}

	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	if _, ok := rfs.Lookup("/etc/remove.conf"); ok {
		t.Errorf("etc/remove.conf should have been whited out")
	}
	if e, ok := rfs.Lookup("/etc/keep.conf"); !ok {
		t.Errorf("etc/keep.conf should survive")
	} else if got, _ := rfs.ReadFile(e.Path); string(got) != "base-keep\n" {
		t.Errorf("etc/keep.conf content = %q", got)
	}
	if _, ok := rfs.Lookup("/etc/added.conf"); !ok {
		t.Errorf("etc/added.conf should be present")
	}
	if _, ok := rfs.Lookup("/var/lib/hidden-by-opaque"); ok {
		t.Errorf("var/lib/hidden-by-opaque should be hidden by the opaque marker")
	}
	if _, ok := rfs.Lookup("/var/lib/fresh"); !ok {
		t.Errorf("var/lib/fresh should be present")
	}
}

func TestSymlinkResolution(t *testing.T) {
	layer := []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/real.conf", Data: []byte("hello\n")},
		{Path: "etc/link.conf", Typeflag: tar.TypeSymlink, Linkname: "real.conf"},
		{Path: "etc/abslink.conf", Typeflag: tar.TypeSymlink, Linkname: "/etc/real.conf"},
		{Path: "etc/chain.conf", Typeflag: tar.TypeSymlink, Linkname: "link.conf"},
		{Path: "etc/loop-a", Typeflag: tar.TypeSymlink, Linkname: "loop-b"},
		{Path: "etc/loop-b", Typeflag: tar.TypeSymlink, Linkname: "loop-a"},
		// A relative symlink target that escapes the rootfs; mutate.Extract
		// drops these during flattening, so it should never reach the index.
		{Path: "etc/escape-link", Typeflag: tar.TypeSymlink, Linkname: "../../../../etc/passwd"},
	}
	img, err := imgtest.Image(linuxAMD64, nil, layer)
	if err != nil {
		t.Fatalf("building image: %v", err)
	}
	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	for _, p := range []string{"/etc/link.conf", "/etc/abslink.conf", "/etc/chain.conf"} {
		data, err := rfs.ReadFile(p)
		if err != nil {
			t.Errorf("ReadFile(%s): %v", p, err)
			continue
		}
		if string(data) != "hello\n" {
			t.Errorf("ReadFile(%s) = %q, want %q", p, data, "hello\n")
		}
	}

	if _, err := rfs.Resolve("/etc/loop-a"); err == nil {
		t.Errorf("Resolve should fail on a symlink loop")
	}

	// A relative symlink that would escape the rootfs is dropped entirely
	// by the flattening step (mutate.Extract's own safety check), so it
	// simply never appears in the index.
	if _, ok := rfs.Lookup("/etc/escape-link"); ok {
		t.Errorf("escaping symlink should not appear in the index")
	}
}

// TestResolveMergedUsr checks that Resolve follows a symlink in an
// *intermediate* path segment, not just the final one - the
// merged-/usr layout real distros (Fedora, Arch, current Debian/Ubuntu)
// use, where /sbin is itself a symlink to /usr/sbin and no tar entry is
// ever literally named "/sbin/<anything>". This is what lets a
// requires.files predicate written as "/sbin/openrc" match such an
// image.
func TestResolveMergedUsr(t *testing.T) {
	img, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "usr/", Typeflag: tar.TypeDir},
		{Path: "usr/sbin/", Typeflag: tar.TypeDir},
		{Path: "usr/sbin/openrc", Data: []byte("bin")},
		{Path: "sbin", Typeflag: tar.TypeSymlink, Linkname: "usr/sbin"},
	})
	if err != nil {
		t.Fatalf("building image: %v", err)
	}
	rfs, err := rootfs.Build(img)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	e, err := rfs.Resolve("/sbin/openrc")
	if err != nil {
		t.Fatalf("Resolve(/sbin/openrc): %v", err)
	}
	if e.Path != "/usr/sbin/openrc" {
		t.Errorf("Resolve(/sbin/openrc).Path = %q, want /usr/sbin/openrc", e.Path)
	}

	if _, err := rfs.Resolve("/sbin/does-not-exist"); err == nil {
		t.Errorf("Resolve(/sbin/does-not-exist) should fail")
	}
}

// TestOverlayStatsOwnLayersLastWriteWins checks that OverlayStats is
// computed from the overlay's own flattened view: when the overlay's own
// two layers both write etc/a.conf, only the winning (upper) layer's
// version is counted, not both - and the base image's own content (here,
// etc/keep.conf) plays no part in the numbers at all, since it belongs
// to a different image entirely.
func TestOverlayStatsOwnLayersLastWriteWins(t *testing.T) {
	baseImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/keep.conf", Data: []byte("0123456789")}, // 10 bytes, irrelevant to the overlay's own stats
	})
	if err != nil {
		t.Fatalf("building base image: %v", err)
	}
	overlay, err := imgtest.Image(linuxAMD64, nil,
		[]imgtest.File{
			{Path: "etc/a.conf", Data: []byte("hello")},  // 5 bytes, overwritten below
			{Path: "etc/b.conf", Data: []byte("world!")}, // 6 bytes, survives untouched
		},
		[]imgtest.File{
			{Path: "etc/a.conf", Data: []byte("HI")}, // 2 bytes, this overlay's own upper layer wins
		},
	)
	if err != nil {
		t.Fatalf("building overlay: %v", err)
	}

	rfs, err := rootfs.Build(baseImg, overlay)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer rfs.Close()

	if len(rfs.OverlayStats) != 1 {
		t.Fatalf("OverlayStats has %d entries, want 1", len(rfs.OverlayStats))
	}
	// etc/a.conf (2 bytes, upper layer's version) + etc/b.conf (6 bytes) = 8.
	if got := rfs.OverlayStats[0]; got.Files != 2 || got.Bytes != 8 || got.Removed != 0 {
		t.Errorf("overlay stats = %+v, want {Files:2 Bytes:8 Removed:0}", got)
	}

	got, err := rfs.ReadFile("/etc/a.conf")
	if err != nil || string(got) != "HI" {
		t.Errorf("etc/a.conf = %q, %v, want %q", got, err, "HI")
	}
}

// TestOverlayStatsWhiteoutsCounted checks that a whiteout or opaque
// directory marker in the overlay's own layers is excluded from Files
// (it's never a surviving entry) and counted in Removed instead - a raw
// count of markers, independent of whether the paths they name ever
// existed in the base image or an earlier overlay.
func TestOverlayStatsWhiteoutsCounted(t *testing.T) {
	baseImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/keep.conf", Data: []byte("x")},
	})
	if err != nil {
		t.Fatalf("building base image: %v", err)
	}
	overlay, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/new.conf", Data: []byte("hi!!")}, // 4 bytes
		imgtest.WhiteoutFile("etc/keep.conf"),
		{Path: "var/lib/.wh..wh..opq", Data: nil},
	})
	if err != nil {
		t.Fatalf("building overlay: %v", err)
	}

	rfs, err := rootfs.Build(baseImg, overlay)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer rfs.Close()

	if len(rfs.OverlayStats) != 1 {
		t.Fatalf("OverlayStats has %d entries, want 1", len(rfs.OverlayStats))
	}
	if got := rfs.OverlayStats[0]; got.Files != 1 || got.Bytes != 4 || got.Removed != 2 {
		t.Errorf("overlay stats = %+v, want {Files:1 Bytes:4 Removed:2}", got)
	}
}

func TestOverlayStatsEmptyAndNil(t *testing.T) {
	baseImg, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/keep.conf", Data: []byte("x")},
	})
	if err != nil {
		t.Fatalf("building base image: %v", err)
	}

	rfs, err := rootfs.Build(baseImg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer rfs.Close()
	if len(rfs.OverlayStats) != 0 {
		t.Errorf("OverlayStats = %+v, want empty (no overlays given)", rfs.OverlayStats)
	}

	rfsNil, err := rootfs.Build(baseImg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer rfsNil.Close()
	if len(rfsNil.OverlayStats) != 1 || rfsNil.OverlayStats[0] != (rootfs.OverlayStats{}) {
		t.Errorf("OverlayStats = %+v, want one zero-value entry for the nil overlay", rfsNil.OverlayStats)
	}
}

func TestHardlinksAndDeviceNodes(t *testing.T) {
	layer := []imgtest.File{
		{Path: "bin/", Typeflag: tar.TypeDir},
		{Path: "bin/real", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: "bin/hardlink", Typeflag: tar.TypeLink, Linkname: "bin/real"},
		{Path: "dev/", Typeflag: tar.TypeDir},
		{Path: "dev/null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3, Mode: 0o666},
	}
	img, err := imgtest.Image(linuxAMD64, nil, layer)
	if err != nil {
		t.Fatalf("building image: %v", err)
	}
	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	link, ok := rfs.Lookup("/bin/hardlink")
	if !ok {
		t.Fatalf("bin/hardlink missing")
	}
	if link.Header.Typeflag != tar.TypeLink || link.Header.Linkname != "bin/real" {
		t.Errorf("bin/hardlink header = %+v", link.Header)
	}

	dev, ok := rfs.Lookup("/dev/null")
	if !ok {
		t.Fatalf("dev/null missing")
	}
	if dev.Header.Typeflag != tar.TypeChar || dev.Header.Devmajor != 1 || dev.Header.Devminor != 3 {
		t.Errorf("dev/null header = %+v", dev.Header)
	}
}
