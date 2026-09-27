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
	"time"

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
// at "/" and bounded (in total) by maxSymlinkHops. Unlike a plain
// lookup by the literal path string, it also follows a symlink in any
// *intermediate* path segment - not just the final one - so a
// merged-/usr layout (where /bin, /sbin and /lib are themselves
// symlinks to their /usr equivalents, as on Fedora, Arch and current
// Debian/Ubuntu) resolves a path like /sbin/openrc correctly even
// though no tar entry is ever literally named "/sbin/openrc". This
// matters for every requires.files-style predicate contemper checks
// (support-image variants, the volume helper's own prerequisites): a
// predicate written as "/sbin/foo" must still match an image that only
// has /usr/sbin/foo plus a merged-/usr /sbin symlink. It returns the
// final, non-symlink entry.
func (r *Rootfs) Resolve(p string) (*Entry, error) {
	hops := 0
	resolved, err := r.resolvePath(normalizePath(p), &hops)
	if err != nil {
		return nil, fmt.Errorf("%s (resolving %s)", err, p)
	}
	e, ok := r.Index[resolved]
	if !ok {
		return nil, fmt.Errorf("%s: no such path (resolving %s)", resolved, p)
	}
	return e, nil
}

// resolvePath resolves target (an absolute, cleaned path) to its final,
// symlink-free path string, resolving every segment's own symlink chain
// as it descends - including the leaf's. A segment with no index entry
// of its own is passed through unresolved rather than treated as an
// error (there is nothing to redirect through, and reporting "missing"
// is the top-level caller's job, once it looks up the fully resolved
// path itself): most tar streams carry an explicit entry for every
// parent directory, but nothing requires it.
func (r *Rootfs) resolvePath(target string, hops *int) (string, error) {
	if target == "/" {
		return "/", nil
	}
	dir, base := path.Split(target)
	dir = path.Clean(dir)

	resolvedDir := "/"
	if dir != "/" {
		var err error
		resolvedDir, err = r.resolvePath(dir, hops)
		if err != nil {
			return "", err
		}
	}
	full := path.Join(resolvedDir, base)

	e, ok := r.Index[full]
	if !ok || e.Header.Typeflag != tar.TypeSymlink {
		return full, nil
	}

	*hops++
	if *hops > maxSymlinkHops {
		return "", fmt.Errorf("too many symlink hops")
	}
	linkTarget := e.Header.Linkname
	if !strings.HasPrefix(linkTarget, "/") {
		linkTarget = path.Join(path.Dir(full), linkTarget)
	}
	return r.resolvePath(normalizePath(linkTarget), hops)
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

// synthEpoch is the fixed modification time given to every synthesized
// entry (WriteFile, EnsureDir), so identical inputs always produce a
// byte-identical rootfs regardless of wall-clock build time.
var synthEpoch = time.Unix(0, 0).UTC()

// SyntheticFile is one entry to inject into the merged rootfs after Build,
// as if it had been present in a layer.
type SyntheticFile struct {
	Path string
	// Typeflag selects the entry's tar type; the zero value means
	// tar.TypeReg.
	Typeflag byte
	// Mode is the entry's permission bits; 0 means 0644 for a regular
	// file or 0755 for a directory.
	Mode    int64
	Content []byte
}

// WriteFile creates or replaces the regular file at p in the merged
// rootfs with content, mode 0644, by appending a new tar entry to the
// backing tar file and updating the index to point at it. It is used to
// inject convert-time synthesized content (/etc/contemper/*, an updated
// /etc/fstab) without ever re-merging or re-reading image layers.
//
// The backing tar file is no longer a single valid archive stream for a
// generic sequential reader once this has been called (the old bytes for
// a replaced path are left in place, now dead); only Rootfs's own
// index-driven reads (Lookup, Resolve, ReadFile, Open, and whatever
// PopulateExt4 does with r.Index) ever see the result.
//
// p's parent directories are created first (see EnsureDir) if they
// aren't already present, so a nested path like /etc/contemper/build
// works even when the image's layers have no explicit /etc/contemper
// entry.
func (r *Rootfs) WriteFile(p string, content []byte) error {
	if err := r.EnsureDir(path.Dir(normalizePath(p))); err != nil {
		return err
	}
	return r.appendSynthetic([]SyntheticFile{{Path: p, Content: content}})
}

// EnsureDir creates every path segment of dir not already present in the
// index (as any entry type), mode 0755, so a volume's mount point exists
// even when the image's layers never included an explicit tar entry for
// one of its parent directories. Existing entries, of any type, are left
// untouched.
func (r *Rootfs) EnsureDir(dir string) error {
	dir = normalizePath(dir)
	if dir == "/" {
		return nil
	}
	segs := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	var missing []SyntheticFile
	cur := ""
	for _, seg := range segs {
		cur += "/" + seg
		if _, ok := r.Index[cur]; ok {
			continue
		}
		missing = append(missing, SyntheticFile{Path: cur, Typeflag: tar.TypeDir})
	}
	if len(missing) == 0 {
		return nil
	}
	return r.appendSynthetic(missing)
}

// appendSynthetic appends files to the end of the rootfs tar file (which
// Build has already closed) and updates r.Index so each path resolves to
// its newly appended entry, replacing any prior one.
func (r *Rootfs) appendSynthetic(files []SyntheticFile) error {
	f, err := os.OpenFile(r.TarPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("opening %s to append synthesized entries: %w", r.TarPath, err)
	}
	defer f.Close()

	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seeking %s: %w", r.TarPath, err)
	}
	cw := &countingWriter{w: f, n: offset}
	tw := tar.NewWriter(cw)

	for _, sf := range files {
		typeflag := sf.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := sf.Mode
		if mode == 0 {
			if typeflag == tar.TypeDir {
				mode = 0o755
			} else {
				mode = 0o644
			}
		}
		p := normalizePath(sf.Path)
		hdr := &tar.Header{
			Name:     strings.TrimPrefix(p, "/"),
			Typeflag: typeflag,
			Mode:     mode,
			Size:     int64(len(sf.Content)),
			ModTime:  synthEpoch,
		}
		if typeflag == tar.TypeDir && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("writing header for %s: %w", p, err)
		}
		dataOffset := cw.n
		if len(sf.Content) > 0 {
			if _, err := tw.Write(sf.Content); err != nil {
				return fmt.Errorf("writing content for %s: %w", p, err)
			}
		}
		r.Index[p] = &Entry{Path: p, Header: hdr, DataOffset: dataOffset}
	}

	return tw.Flush()
}
