package source

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/limits"
)

func TestExtractTarLimitedSkipsLargeEntries(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, size := range map[string]int{"index.json": 10, "blobs/sha256/small": 20, "blobs/sha256/layer": 5000} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(size), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(make([]byte, size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := extractTarLimited(t.Context(), bytes.NewReader(buf.Bytes()), dir, 100); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"index.json", "blobs/sha256/small"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s not extracted: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs/sha256/layer")); !os.IsNotExist(err) {
		t.Errorf("large entry was extracted (stat err = %v)", err)
	}

	// No limit extracts everything.
	all := t.TempDir()
	if err := extractTar(t.Context(), bytes.NewReader(buf.Bytes()), all); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(all, "blobs/sha256/layer")); err != nil {
		t.Errorf("extractTar skipped an entry: %v", err)
	}
}

func TestExtractTarEnforcesContentLimits(t *testing.T) {
	// A header that claims 100 GiB and carries no data: refused from the
	// header alone, with nothing written.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "blobs/sha256/huge", Mode: 0o644, Size: 100 << 30, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := extractTar(t.Context(), bytes.NewReader(buf.Bytes()), dir)
	if err == nil || !strings.Contains(err.Error(), "blobs/sha256/huge") || !strings.Contains(err.Error(), "--max-rootfs-size") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %d entries before refusing", len(entries))
	}

	// The override raises the limit.
	ctx := limits.NewContext(t.Context(), limits.Default().WithMaxSize(200<<30))
	if err := extractTar(ctx, bytes.NewReader(buf.Bytes()), t.TempDir()); err != nil && strings.Contains(err.Error(), "limit") {
		t.Errorf("override ignored: %v", err)
	}
}
