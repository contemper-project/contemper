// Package rootfs builds the merged filesystem view of a source image
// (plus an optional support-image overlay) as a flattened tar stream on
// disk, and provides safe, index-based lookup and symlink resolution
// over it without ever extracting file content to the host filesystem
// under its real name or ownership.
package rootfs

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// maxSymlinkHops bounds symlink resolution inside the index.
const maxSymlinkHops = 40

// Entry describes one path in the merged filesystem.
type Entry struct {
	Path string
	// Header is the tar header as it appeared in the flattened stream
	// (whiteouts and opaque dirs already applied).
	Header *tar.Header
	// DataOffset is the byte offset of this entry's content within the
	// rootfs tar file, valid only for regular files.
	DataOffset int64
}

// Rootfs is the merged filesystem view, backed by a tar file on disk.
type Rootfs struct {
	// TarPath is the path to the flattened rootfs tar on disk.
	TarPath string
	// Index maps an absolute, cleaned path ("/etc/os-release") to its entry.
	Index map[string]*Entry

	tmpDir string
}

// Build flattens base (with each overlay's layers appended on top, in
// order, skipping any nil overlay) into a single tar stream, writes it to
// a temp file, and indexes every path it contains. The caller must call
// Close when done.
func Build(base v1.Image, overlays ...v1.Image) (*Rootfs, error) {
	img := base
	for _, overlay := range overlays {
		if overlay == nil {
			continue
		}
		layers, err := overlay.Layers()
		if err != nil {
			return nil, fmt.Errorf("reading overlay image layers: %w", err)
		}
		img, err = mutate.AppendLayers(img, layers...)
		if err != nil {
			return nil, fmt.Errorf("appending overlay layers: %w", err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "contemper-rootfs-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	tarPath := path.Join(tmpDir, "rootfs.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("creating %s: %w", tarPath, err)
	}
	defer f.Close()

	rc := mutate.Extract(img)
	defer rc.Close()

	index, err := indexTar(rc, f)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("flattening rootfs: %w", err)
	}

	return &Rootfs{TarPath: tarPath, Index: index, tmpDir: tmpDir}, nil
}

// countingWriter tracks the number of bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// indexTar copies the tar stream r into w byte-for-byte while recording
// each entry's normalized path, header and data offset.
func indexTar(r io.Reader, w io.Writer) (map[string]*Entry, error) {
	cw := &countingWriter{w: w}
	tr := tar.NewReader(io.TeeReader(r, cw))

	index := make(map[string]*Entry)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		p := normalizePath(hdr.Name)
		offset := cw.n
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return nil, fmt.Errorf("reading content of %s: %w", hdr.Name, err)
		}
		if p == "/" {
			continue
		}
		index[p] = &Entry{Path: p, Header: hdr, DataOffset: offset}
	}
	// Drain any trailing archive padding so the file on disk is a
	// complete, independently readable tar (not required for our own
	// offset-based reads, but cheap and useful for debugging).
	io.Copy(cw, r) //nolint:errcheck // best-effort trailing padding
	return index, nil
}

// normalizePath turns a tar entry name into an absolute, cleaned path.
func normalizePath(name string) string {
	name = strings.TrimPrefix(name, "./")
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return path.Clean(name)
}

// Close removes the temporary directory backing this Rootfs.
func (r *Rootfs) Close() error {
	if r == nil || r.tmpDir == "" {
		return nil
	}
	return os.RemoveAll(r.tmpDir)
}

// Lookup returns the entry at the exact path, without symlink resolution.
func (r *Rootfs) Lookup(p string) (*Entry, bool) {
	e, ok := r.Index[normalizePath(p)]
	return e, ok
}

// Resolve resolves p, following symlinks inside the index only, clamped
// at "/" and bounded by maxSymlinkHops. It returns the final, non-symlink
// entry.
func (r *Rootfs) Resolve(p string) (*Entry, error) {
	cur := normalizePath(p)
	for hop := 0; hop < maxSymlinkHops; hop++ {
		e, ok := r.Index[cur]
		if !ok {
			return nil, fmt.Errorf("%s: no such path (resolving %s)", cur, p)
		}
		if e.Header.Typeflag != tar.TypeSymlink {
			return e, nil
		}
		target := e.Header.Linkname
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(cur), target)
		}
		cur = normalizePath(target)
	}
	return nil, fmt.Errorf("%s: too many symlink hops", p)
}

// ReadFile returns the content of the regular file at p, resolving
// symlinks first. It reads directly from the backing tar file by offset;
// image content is never written to the host filesystem under its
// original name.
func (r *Rootfs) ReadFile(p string) ([]byte, error) {
	e, err := r.Resolve(p)
	if err != nil {
		return nil, err
	}
	if e.Header.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("%s: not a regular file", p)
	}
	f, err := os.Open(r.TarPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(e.DataOffset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, e.Header.Size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	return buf, nil
}

// Open returns a reader over the regular file at p (no symlink
// resolution - callers that need it should Resolve first), positioned at
// its content and limited to its size.
func (r *Rootfs) Open(e *Entry) (io.ReadCloser, error) {
	if e.Header.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("%s: not a regular file", e.Path)
	}
	f, err := os.Open(r.TarPath)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(e.DataOffset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &limitedReadCloser{r: io.LimitReader(f, e.Header.Size), c: f}, nil
}

type limitedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.c.Close() }
