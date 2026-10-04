package rootfs_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/limits"
	"github.com/contemper-project/contemper/internal/rootfs"
)

func buildWithLimits(t *testing.T, l limits.Limits, files ...imgtest.File) error {
	t.Helper()
	img, err := imgtest.Image(linuxAMD64, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	ctx := limits.NewContext(context.Background(), l)
	rfs, err := rootfs.Build(ctx, img)
	if err == nil {
		_ = rfs.Close()
	}
	return err
}

func TestBuildEnforcesContentLimits(t *testing.T) {
	big := make([]byte, 2048)
	small := imgtest.File{Path: "a", Data: []byte("x")}
	cases := []struct {
		name    string
		l       limits.Limits
		files   []imgtest.File
		wantErr string
	}{
		{"within limits", limits.Limits{MaxFileSize: 4096, MaxTotalSize: 4096, MaxEntries: 100}, []imgtest.File{{Path: "big", Data: big}}, ""},
		{"per file", limits.Limits{MaxFileSize: 1024, MaxTotalSize: 1 << 20, MaxEntries: 100}, []imgtest.File{small, {Path: "big", Data: big}}, `entry "big"`},
		{"total", limits.Limits{MaxFileSize: 4096, MaxTotalSize: 3000, MaxEntries: 100}, []imgtest.File{{Path: "b1", Data: big}, {Path: "b2", Data: big}}, "total limit"},
		{"entries", limits.Limits{MaxFileSize: 4096, MaxTotalSize: 1 << 20, MaxEntries: 2}, []imgtest.File{small, {Path: "b", Data: []byte("y")}, {Path: "c", Data: []byte("z")}}, "more than 2 entries"},
	}
	for _, c := range cases {
		err := buildWithLimits(t, c.l, c.files...)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

// paxLayerImage builds a one-layer image of n size-0 entries, each with a
// PAX "comment" record of paxLen bytes, plus an optional xattr.
func paxLayerImage(t *testing.T, n, paxLen int, xattr bool) v1.Image {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	comment := strings.Repeat("A", paxLen)
	for i := 0; i < n; i++ {
		recs := map[string]string{"comment": comment}
		if xattr {
			recs["SCHILY.xattr.user.keep"] = "yes"
		}
		h := &tar.Header{Name: fmt.Sprintf("f%05d", i), Typeflag: tar.TypeReg, Mode: 0o644, Format: tar.FormatPAX, PAXRecords: recs}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	data := raw.Bytes()
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, l)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestBuildBoundsPaxHeaders(t *testing.T) {
	// Entries with ~1 MiB of PAX comment each carry no content, so the
	// size counts see 0 bytes; the per-entry metadata cap refuses them.
	_, err := rootfs.Build(t.Context(), paxLayerImage(t, 3, 1<<20-200, false))
	if err == nil || !strings.Contains(err.Error(), `entry "f00000"`) || !strings.Contains(err.Error(), "extended headers") {
		t.Errorf("1 MiB PAX records: err = %v", err)
	}

	// Many entries each under the per-entry cap still add up: what is
	// written to rootfs.tar counts against the total limit.
	l := limits.Limits{MaxFileSize: 1 << 20, MaxTotalSize: 100 << 10, MaxEntries: 10000}
	ctx := limits.NewContext(context.Background(), l)
	_, err = rootfs.Build(ctx, paxLayerImage(t, 200, 40<<10, false))
	if err == nil || !strings.Contains(err.Error(), "--max-rootfs-size") {
		t.Errorf("many PAX records: err = %v", err)
	}
}

func TestBuildKeepsOnlyXattrPaxRecords(t *testing.T) {
	rfs, err := rootfs.Build(t.Context(), paxLayerImage(t, 2, 1000, true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()
	e, ok := rfs.Lookup("/f00000")
	if !ok {
		t.Fatal("entry missing")
	}
	if len(e.Header.PAXRecords) != 1 || e.Header.PAXRecords["SCHILY.xattr.user.keep"] != "yes" {
		t.Errorf("PAXRecords = %v, want only the xattr", e.Header.PAXRecords)
	}
	if len(e.Header.Xattrs) != 0 { //nolint:staticcheck // checking the deprecated field is cleared
		t.Errorf("Xattrs = %v, want none", e.Header.Xattrs) //nolint:staticcheck // as above
	}
}

func TestReadFileCaps(t *testing.T) {
	img, err := imgtest.Image(linuxAMD64, nil, []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/big", Data: make([]byte, 2<<20)},
		{Path: "etc/small", Data: []byte("ok")},
	})
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(t.Context(), img)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()
	if _, err := rfs.ReadFile("/etc/big"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("ReadFile of a 2 MiB file: err = %v", err)
	}
	if b, err := rfs.ReadLargeFile("/etc/big"); err != nil || len(b) != 2<<20 {
		t.Errorf("ReadLargeFile: %d bytes, %v", len(b), err)
	}
	if b, err := rfs.ReadFile("/etc/small"); err != nil || string(b) != "ok" {
		t.Errorf("ReadFile small: %q, %v", b, err)
	}
}
