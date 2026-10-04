package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/limits"
)

// fuzzCtx bounds what one fuzz iteration may claim, so a header that
// declares a huge sparse file is refused instead of slowing the run.
func fuzzCtx() context.Context {
	return limits.NewContext(context.Background(), limits.Limits{
		MaxFileSize:  1 << 20,
		MaxTotalSize: 1 << 20,
		MaxEntries:   1000,
	})
}

// checkIndexPath fails if p is not an absolute, cleaned path inside the
// root, the form every index key and resolved path has to take.
func checkIndexPath(t *testing.T, what, p string) {
	t.Helper()
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		t.Fatalf("%s %q is not absolute and clean", what, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			t.Fatalf("%s %q has a .. segment", what, p)
		}
	}
	if strings.ContainsRune(p, 0) {
		t.Fatalf("%s %q has a NUL", what, p)
	}
}

// checkIndex verifies the invariants every index must keep and that
// resolution over it stays inside it: Resolve terminates (it is bounded
// by maxSymlinkHops) and returns either an error or a non-symlink entry
// whose path is clean and absolute.
func checkIndex(t *testing.T, index map[string]*Entry, extra ...string) {
	t.Helper()
	r := &Rootfs{Index: index}
	for p, e := range index {
		checkIndexPath(t, "index key", p)
		if p == "/" {
			t.Fatalf("root is indexed")
		}
		if e.Path != p {
			t.Fatalf("entry path %q under key %q", e.Path, p)
		}
		if e.Header == nil {
			t.Fatalf("entry %q has no header", p)
		}
		if e.Header.Typeflag == tar.TypeReg && e.DataOffset < 0 {
			t.Fatalf("entry %q has offset %d", p, e.DataOffset)
		}
		if e.Header.PAXRecords != nil {
			for k := range e.Header.PAXRecords {
				if !strings.HasPrefix(k, "SCHILY.xattr.") {
					t.Fatalf("entry %q keeps PAX record %q", p, k)
				}
			}
		}
	}
	probes := append([]string{"/", "", ".", "..", "/../.."}, extra...)
	for p := range index {
		probes = append(probes, p)
	}
	for _, p := range probes {
		e, err := r.Resolve(p)
		if err != nil {
			continue
		}
		checkIndexPath(t, "resolved path of "+p, e.Path)
		if e.Header.Typeflag == tar.TypeSymlink {
			t.Fatalf("Resolve(%q) returned a symlink %q", p, e.Path)
		}
		if index[e.Path] != e {
			t.Fatalf("Resolve(%q) returned an entry that is not in the index", p)
		}
	}
}

func tarBytes(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(make([]byte, h.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// FuzzIndexTar feeds arbitrary bytes to the layer tar indexer.
func FuzzIndexTar(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 1024))
	f.Add(tarBytes(&testing.T{}, &tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}))
	f.Add(tarBytes(&testing.T{},
		&tar.Header{Name: "./etc/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "etc/hosts", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5},
		&tar.Header{Name: "etc/link", Typeflag: tar.TypeSymlink, Linkname: "hosts"},
		&tar.Header{Name: "/abs/../x", Typeflag: tar.TypeReg, Size: 1},
		&tar.Header{Name: "bin/ls", Typeflag: tar.TypeLink, Linkname: "etc/hosts"},
		&tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
	))
	f.Add(tarBytes(&testing.T{},
		&tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "b"},
		&tar.Header{Name: "b", Typeflag: tar.TypeSymlink, Linkname: "a"},
		&tar.Header{Name: "up", Typeflag: tar.TypeSymlink, Linkname: "../../.."},
	))
	f.Add(tarBytes(&testing.T{}, &tar.Header{
		Name: "x", Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		PAXRecords: map[string]string{"SCHILY.xattr.user.a": "b", "comment": "c"},
	}))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		var out bytes.Buffer
		index, err := indexTar(fuzzCtx(), bytes.NewReader(data), &out)
		if err != nil {
			return
		}
		checkIndex(t, index)
		for p, e := range index {
			if e.Header.Typeflag == tar.TypeReg && e.DataOffset+e.Header.Size > int64(out.Len()) {
				t.Fatalf("%q: content at %d+%d lies beyond the %d bytes written", p, e.DataOffset, e.Header.Size, out.Len())
			}
		}
		if out.Len() < len(data) && !bytes.HasPrefix(data, out.Bytes()) {
			t.Fatalf("flattened tar is not a copy of the input")
		}
	})
}

// FuzzIndexTarEntries builds a tar from fuzzed header fields, so names,
// link targets and types reach the indexer in shapes raw bytes rarely
// produce, then resolves the fuzzed names through the index.
func FuzzIndexTarEntries(f *testing.F) {
	f.Add("etc/hosts", "", byte(tar.TypeReg), int64(0o644), "etc/link", "hosts", byte(tar.TypeSymlink))
	f.Add("./a/b/../c", "", byte(tar.TypeDir), int64(0o755), "/a/c/x", "../../..", byte(tar.TypeSymlink))
	f.Add("a", "b", byte(tar.TypeSymlink), int64(0), "b", "a", byte(tar.TypeSymlink))
	f.Add("lib", "usr/lib", byte(tar.TypeSymlink), int64(0), "usr/lib/", "", byte(tar.TypeDir))
	f.Add("//x//", "/", byte(tar.TypeSymlink), int64(0o777), "x/y", "..", byte(tar.TypeLink))
	f.Add("dev/null", "", byte(tar.TypeChar), int64(0o666), "dev/sda", "", byte(tar.TypeBlock))
	f.Add("", "", byte(tar.TypeReg), int64(0), ".", "", byte(tar.TypeDir))
	f.Fuzz(func(t *testing.T, name1, link1 string, typ1 byte, mode int64, name2, link2 string, typ2 byte) {
		if len(name1)+len(link1)+len(name2)+len(link2) > 4096 {
			t.Skip()
		}
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, h := range []*tar.Header{
			{Name: name1, Linkname: link1, Typeflag: typ1, Mode: mode},
			{Name: name2, Linkname: link2, Typeflag: typ2, Mode: mode},
		} {
			if tw.WriteHeader(h) != nil {
				t.Skip()
			}
		}
		if tw.Close() != nil {
			t.Skip()
		}
		index, err := indexTar(fuzzCtx(), &buf, io.Discard)
		if err != nil {
			return
		}
		checkIndex(t, index, name1, name2, link1, link2)
	})
}

// FuzzBuildLayers merges two fuzzed layers through Build and checks the
// whiteout rules and that every path in the result has its parents.
func FuzzBuildLayers(f *testing.F) {
	f.Add("etc", "remove.conf", "keep.conf", "var/lib", "hidden")
	f.Add("a/b", "c", "d", "a/b", "e")
	f.Add("./x//y/", "z", "z", "x/y", "q")
	f.Add("", "f", "g", ".", "h")
	f.Add("../up", "f", "g", "..", "h")
	f.Fuzz(func(t *testing.T, dir, victim, keeper, opaque, fresh string) {
		if len(dir)+len(victim)+len(keeper)+len(opaque)+len(fresh) > 1024 {
			t.Skip()
		}
		// Layer entry names are relative, as every image builder writes
		// them: mutate.Extract (go-containerregistry) matches whiteouts
		// against names verbatim, so "/f" in one layer is not hidden by
		// ".wh.f" in the next. Leading slashes are dropped here instead
		// of asserting a rule the merge library does not provide.
		join := func(d, n string) string { return strings.TrimLeft(d+"/"+n, "/") }
		lower := []imgtest.File{
			{Path: join(dir, victim), Data: []byte("v")},
			{Path: join(dir, keeper), Data: []byte("k")},
		}
		upper := []imgtest.File{
			imgtest.WhiteoutFile(join(dir, victim)),
			{Path: join(opaque, ".wh..wh..opq")},
			{Path: join(opaque, fresh), Data: []byte("f")},
		}
		img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "amd64"}, nil, lower, upper)
		if err != nil {
			t.Skip()
		}
		r, err := Build(fuzzCtx(), img)
		if err != nil {
			return
		}
		defer func() { _ = r.Close() }()

		checkIndex(t, r.Index, join(dir, victim), join(opaque, fresh))
		// Every parent directory of every entry has an entry itself.
		for p := range r.Index {
			if dir := path.Dir(p); dir != "/" {
				if _, ok := r.Index[dir]; !ok {
					t.Fatalf("%q has no parent entry %q", p, dir)
				}
			}
		}
		// The upper layer's whiteout and opaque marker never survive,
		// and what they cover from the lower layer does not either
		// (the upper layer's own files are exempt).
		for p := range r.Index {
			if strings.Contains(path.Base(p), ".wh.") {
				t.Fatalf("whiteout marker %q survives", p)
			}
		}
		victimPath := normalizePath(join(dir, victim))
		if _, ok := r.Index[victimPath]; ok && victimPath != normalizePath(join(opaque, fresh)) {
			t.Fatalf("whited-out %q survives", victimPath)
		}
		if _, err := os.Stat(r.TarPath); err != nil {
			t.Fatal(err)
		}
	})
}
