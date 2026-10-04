// Package rootfs builds the merged filesystem view of a source image
// (plus an optional support-image overlay) as a flattened tar stream on
// disk, and provides safe, index-based lookup and symlink resolution
// over it without ever extracting file content to the host filesystem
// under its real name or ownership.
package rootfs

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/contemper-project/contemper/internal/limits"
	"github.com/contemper-project/contemper/internal/progress"
)

// maxReadFileSize bounds Rootfs.ReadLargeFile, which holds a whole file
// in memory. It is used for the kernel and initrd, which are read to
// build the UKI (its PE sections use 32-bit sizes, so a few GiB is the
// most it can carry; real kernels and initrds are under a few hundred
// MiB).
const maxReadFileSize = 1 << 30

// maxReadTextSize bounds ReadFile for the small text files (kernel
// command line, os-release, fstab and similar), which are never large.
const maxReadTextSize = 1 << 20

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
	// OverlayStats reports, for each overlay Build was given (same
	// index, same length, a zero value for a nil overlay), what that
	// overlay contributed to the merged filesystem. See OverlayStats's
	// doc comment for exactly what is and isn't counted.
	OverlayStats []OverlayStats

	tmpDir string
}

// OverlayStats reports what one overlay itself writes, independent of
// whatever it's merged onto - the base image or any other overlay is
// never read to compute it. It exists purely to drive convert's progress
// output - how much a support image, its variants and the volume helper
// each add - and is never recorded in the bundle.
//
// Files and Bytes describe the overlay's own flattened view: its layers,
// with its own internal overwrites resolved (the same
// whiteout/opaque-directory rules the real merge uses, applied only
// within this overlay - a path two of the overlay's own layers both
// write counts once, from the layer that wins). Files counts every
// surviving tar entry (regular files, directories, symlinks, device
// nodes, ...); a whiteout or opaque-directory marker is never itself a
// surviving entry, so it is excluded from Files. Bytes is the
// regular-file subset: the sum of Size for entries with
// Typeflag == tar.TypeReg.
//
// Removed is a raw count of the whiteout (".wh.<name>") and opaque
// directory (".wh..wh..opq") marker entries the overlay's own layers
// carry, shown only when nonzero. It is not resolved against what
// existed before this overlay: the overlay may be whiting out a path
// from the base image, from an earlier overlay, or a path nothing ever
// had - Removed does not distinguish these, it simply reports how many
// deletions the overlay's own layers declare.
//
// In short, this line means "this image writes N files, X bytes" - not
// "the merged filesystem grew by N files, X bytes", which would require
// reading the (potentially much larger) base image and every earlier
// overlay to answer.
type OverlayStats struct {
	Files   int
	Bytes   int64
	Removed int
}

// Build flattens base (with each overlay's layers appended on top, in
// order, skipping any nil overlay) into a single tar stream, writes it to
// a temp file, and indexes every path it contains. The caller must call
// Close when done.
//
// Along the way it also computes each overlay's OverlayStats by
// flattening that overlay alone - never base, never any other overlay -
// so the cost is proportional to the overlay's own (typically small)
// size, not the base image's.
//
// Canceling ctx is noticed at reasonable points during the flatten (the
// potentially large, pure-Go loop over base's and every overlay's
// layers), without waiting for it to finish.
func Build(ctx context.Context, base v1.Image, overlays ...v1.Image) (*Rootfs, error) {
	img := base
	stats := make([]OverlayStats, len(overlays))
	for i, overlay := range overlays {
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

		st, err := overlayStats(ctx, overlay)
		if err != nil {
			return nil, fmt.Errorf("computing overlay stats: %w", err)
		}
		stats[i] = st
	}

	tmpDir, err := os.MkdirTemp("", "contemper-rootfs-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	tarPath := path.Join(tmpDir, "rootfs.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("creating %s: %w", tarPath, err)
	}

	rc := mutate.Extract(img)
	defer func() { _ = rc.Close() }()

	index, err := indexTar(ctx, rc, f)
	if err != nil {
		_ = f.Close()
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("flattening rootfs: %w", err)
	}
	// Checked, not deferred-and-ignored: every later read of this Rootfs
	// goes through TarPath by byte offset, so a write error surfaced
	// only at Close (e.g. a delayed flush failure) would otherwise
	// silently corrupt every subsequent read.
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("closing %s: %w", tarPath, err)
	}

	r := &Rootfs{TarPath: tarPath, Index: index, OverlayStats: stats, tmpDir: tmpDir}
	if err := r.addMissingParents(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("flattening rootfs: %w", err)
	}
	return r, nil
}

// addMissingParents gives every parent directory that some entry needs,
// but that no layer carries an entry for, a synthesized directory entry
// (mode 0755, root-owned). A layer tar need not list a file's parent
// directories, and without these the ext4 population step would have
// nowhere to create the file.
func (r *Rootfs) addMissingParents() error {
	seen := map[string]bool{}
	var missing []SyntheticFile
	for p := range r.Index {
		for dir := path.Dir(p); dir != "/"; dir = path.Dir(dir) {
			if seen[dir] {
				break
			}
			seen[dir] = true
			if _, ok := r.Index[dir]; !ok {
				missing = append(missing, SyntheticFile{Path: dir, Typeflag: tar.TypeDir})
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Path < missing[j].Path })
	return r.appendSynthetic(missing)
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
// each entry's normalized path, header and data offset. It checks ctx
// every 256 entries, so canceling it part way through a large merge
// (base plus every overlay) is noticed without waiting for the whole
// stream to drain.
func indexTar(ctx context.Context, r io.Reader, w io.Writer) (map[string]*Entry, error) {
	cw := &countingWriter{w: w}
	tr := tar.NewReader(io.TeeReader(r, cw))

	lim := limits.FromContext(ctx)
	count := limits.NewCounter(lim)
	index := make(map[string]*Entry)
	for n := 0; ; n++ {
		if n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Before anything is read: a sparse entry claims its expanded
		// size here while carrying almost no data in the layer.
		if err := count.Check(hdr.Name, hdr.Size); err != nil {
			return nil, err
		}
		if err := checkHeaderMetadata(hdr); err != nil {
			return nil, err
		}
		// What is actually written to rootfs.tar counts too, not just
		// the sizes the headers claim: PAX records and long names take
		// space without being anyone's file content.
		if limit := lim.MaxTotalSize + int64(n+1)*streamOverheadPerEntry; cw.n > limit {
			return nil, fmt.Errorf("image content written for %q exceeds the limit of %s plus per-entry headers; a larger image needs --max-rootfs-size", hdr.Name, progress.HumanBytes(lim.MaxTotalSize))
		}
		p := normalizePath(hdr.Name)
		offset := cw.n
		// Not a decompression-bomb risk: this drains one already
		// decompressed tar entry from an image the caller chose to
		// convert (a local file or one they pulled themselves), sized by
		// its own real content - there is no attacker supplying an
		// oversized stream over the wire here.
		if _, err := io.Copy(io.Discard, tr); err != nil { //nolint:gosec // G110: bounded by the source image's real content, not attacker-controlled
			return nil, fmt.Errorf("reading content of %s: %w", hdr.Name, err)
		}
		if p == "/" {
			continue
		}
		slimHeader(hdr)
		index[p] = &Entry{Path: p, Header: hdr, DataOffset: offset}
	}
	// Drain any trailing archive padding so the file on disk is a
	// complete, independently readable tar (not required for our own
	// offset-based reads, but cheap and useful for debugging).
	io.Copy(cw, io.LimitReader(r, maxTrailerBytes)) //nolint:errcheck,gosec // G104/errcheck: best-effort trailing padding, already drained the archive content we need above
	return index, nil
}

const (
	// maxEntryMetadata bounds one entry's name, link target, owner names
	// and PAX records together. Real entries use a few hundred bytes;
	// extended attributes are the largest legitimate part.
	maxEntryMetadata = 64 << 10
	// streamOverheadPerEntry is the room each entry gets, on top of the
	// total content limit, for its header block, padding and PAX records
	// in the flattened tar.
	streamOverheadPerEntry = 2048
	// maxTrailerBytes bounds the archive padding copied after the last
	// entry.
	maxTrailerBytes = 1 << 20
)

// checkHeaderMetadata refuses an entry whose names and PAX records take
// more than maxEntryMetadata, naming the entry.
func checkHeaderMetadata(hdr *tar.Header) error {
	n := len(hdr.Name) + len(hdr.Linkname) + len(hdr.Uname) + len(hdr.Gname)
	for k, v := range hdr.PAXRecords {
		n += len(k) + len(v)
	}
	if n > maxEntryMetadata {
		return fmt.Errorf("entry %q has %d bytes of names and extended headers, over the limit of %d", hdr.Name, n, maxEntryMetadata)
	}
	return nil
}

// slimHeader drops what a header carries that nothing reads, before it
// is kept in memory for the life of the conversion: every PAX record
// except the extended attributes (SCHILY.xattr.*, which the ext4 writer
// uses), and the deprecated Xattrs map, which the reader fills from the
// same records.
func slimHeader(hdr *tar.Header) {
	var keep map[string]string
	for k, v := range hdr.PAXRecords {
		if strings.HasPrefix(k, "SCHILY.xattr.") {
			if keep == nil {
				keep = map[string]string{}
			}
			keep[k] = v
		}
	}
	hdr.PAXRecords = keep
	hdr.Xattrs = nil //nolint:staticcheck // the reader fills it from PAXRecords; xattrs are read from there
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
		return nil, fmt.Errorf("%w (resolving %s)", err, p)
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
		// Not a real extraction: full and linkTarget only ever address
		// r.Index, an in-memory map, never a path on the host
		// filesystem, so there is nothing here for a ".." segment to
		// traverse out of.
		linkTarget = path.Join(path.Dir(full), linkTarget) //nolint:gosec // G305: resolves within the in-memory index only, never touches the host filesystem
	}
	return r.resolvePath(normalizePath(linkTarget), hops)
}

// DirHasContent reports whether the merged rootfs has anything at dir
// (resolved first, so a symlinked mount point is handled the same as a
// real directory) besides the directory entry itself: at least one other
// indexed path nested under it, at any depth. A dir that doesn't exist,
// or doesn't resolve to a directory, or resolves to an empty one, all
// report false. It exists for `convert` to tell whether a declared
// volume's path has image content the guest helper will seed onto that
// volume's disk the first time it formats it (see
// docs/guide/volumes.md) - without ever extracting that content to the
// host.
func (r *Rootfs) DirHasContent(dir string) bool {
	e, err := r.Resolve(dir)
	if err != nil || e.Header.Typeflag != tar.TypeDir {
		return false
	}
	prefix := e.Path
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for p := range r.Index {
		if p != e.Path && strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// ReadFile returns the content of the small regular file at p (kernel
// command line, os-release, fstab and similar; at most maxReadTextSize),
// resolving symlinks first. It reads directly from the backing tar file by offset;
// image content is never written to the host filesystem under its
// original name.
func (r *Rootfs) ReadFile(p string) ([]byte, error) {
	return r.readFile(p, maxReadTextSize)
}

// ReadLargeFile is ReadFile for the kernel and initrd, which are held in
// memory to build the boot image and may be large (at most
// maxReadFileSize).
func (r *Rootfs) ReadLargeFile(p string) ([]byte, error) {
	return r.readFile(p, maxReadFileSize)
}

func (r *Rootfs) readFile(p string, maxSize int64) ([]byte, error) {
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
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(e.DataOffset, io.SeekStart); err != nil {
		return nil, err
	}
	if e.Header.Size > maxSize {
		return nil, fmt.Errorf("%s is %d bytes, too large to read into memory (limit %d)", p, e.Header.Size, maxSize)
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
		_ = f.Close()
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
	defer func() { _ = f.Close() }()

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

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flushing synthesized entries to %s: %w", r.TarPath, err)
	}
	// Checked, not deferred-and-ignored: same reasoning as Build - every
	// later read of this Rootfs goes through TarPath by byte offset.
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", r.TarPath, err)
	}
	return nil
}

// whiteoutPrefix marks a tar entry (mirroring
// mutate.Extract's own unexported constant of the same name) as either a
// per-file tombstone (".wh.<name>") or, when the rest of the basename is
// itself "..wh..opq", an opaque-directory marker. overlayStats only needs
// to recognize and count these, not interpret them the way Build's own
// final flatten (via mutate.Extract) does.
const whiteoutPrefix = ".wh."

// overlayStats computes overlay's OverlayStats: Files/Bytes from its own
// flattened view (mutate.Extract applied to overlay alone, so only its
// own layers are read - never base, never any other overlay), and
// Removed from a raw scan of its own layers' tar entries for whiteout
// and opaque-directory markers. See OverlayStats's doc comment for
// exactly what each field means.
func overlayStats(ctx context.Context, overlay v1.Image) (OverlayStats, error) {
	var st OverlayStats

	layers, err := overlay.Layers()
	if err != nil {
		return st, fmt.Errorf("reading overlay image layers: %w", err)
	}
	for _, l := range layers {
		n, err := countWhiteouts(ctx, l)
		if err != nil {
			return st, fmt.Errorf("scanning overlay layer for whiteouts: %w", err)
		}
		st.Removed += n
	}

	rc := mutate.Extract(overlay)
	defer func() { _ = rc.Close() }() // read-only stream; nothing to flush
	tr := tar.NewReader(rc)
	count := limits.NewCounter(limits.FromContext(ctx))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, err
		}
		if err := count.Check(hdr.Name, hdr.Size); err != nil {
			return st, err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil { //nolint:gosec // G110: discarded, and bounded by the overlay's real content
			return st, fmt.Errorf("reading content of %s: %w", hdr.Name, err)
		}
		if normalizePath(hdr.Name) == "/" {
			continue
		}
		st.Files++
		if hdr.Typeflag == tar.TypeReg {
			st.Bytes += hdr.Size
		}
	}
	return st, nil
}

// countWhiteouts returns the number of whiteout/opaque-directory marker
// entries in l's own tar stream - a plain scan, with no whiteout
// resolution or cross-layer bookkeeping (that's what mutate.Extract does
// for the entries that survive; this just counts the markers themselves,
// which Extract never emits).
func countWhiteouts(ctx context.Context, l v1.Layer) (int, error) {
	r, err := l.Uncompressed()
	if err != nil {
		return 0, fmt.Errorf("reading layer contents: %w", err)
	}
	defer func() { _ = r.Close() }() // read-only stream; nothing to flush

	tr := tar.NewReader(r)
	count := limits.NewCounter(limits.FromContext(ctx))
	n := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if err := count.Check(hdr.Name, hdr.Size); err != nil {
			return 0, err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil { //nolint:gosec // G110: discarded, and bounded by the overlay's real content
			return 0, fmt.Errorf("reading content of %s: %w", hdr.Name, err)
		}
		if strings.HasPrefix(path.Base(path.Clean(hdr.Name)), whiteoutPrefix) {
			n++
		}
	}
	return n, nil
}
