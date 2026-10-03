package validate

import (
	"archive/tar"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/rootfs"
)

// Fixed paths for images that bring their own bootloader.
const (
	// EFIDir is the image directory whose tree is copied onto the ESP.
	EFIDir = "/boot/efi"
	// espFallbackDir is where, relative to the ESP root, the UEFI
	// removable-media fallback file lives.
	espFallbackDir = "EFI/BOOT"
)

// efiArch carries the per-architecture parts of the fallback file check.
type efiArch struct {
	fileName string
	machine  uint16
}

var efiArchTable = map[string]efiArch{
	"amd64": {fileName: "BOOTX64.EFI", machine: 0x8664},
	"arm64": {fileName: "BOOTAA64.EFI", machine: 0xAA64},
}

// ESPFile is one directory or regular file of the tree to copy onto the
// ESP.
type ESPFile struct {
	// Path is relative to the ESP root, slash-separated.
	Path string
	Dir  bool
	// Size is the content size of a file.
	Size int64
	// Source is the entry holding a file's content: the entry itself, or
	// for a hardlink the entry it links to.
	Source *rootfs.Entry
}

// Bootloader is the validated /boot/efi tree of an image that brings its
// own bootloader.
type Bootloader struct {
	// EFIRoot is where /boot/efi resolved to inside the image.
	EFIRoot string
	// Files lists every directory and file, each directory before its
	// contents.
	Files []ESPFile
	// TotalBytes is the summed size of all files.
	TotalBytes int64
	// FallbackPath and FallbackSize describe the UEFI fallback file,
	// relative to the ESP root, with the case it has in the image (FAT
	// and firmware match it case-insensitively).
	FallbackPath string
	FallbackSize int64
	// ContemperIgnored reports that /boot/contemper exists but, in this
	// mode, is not used.
	ContemperIgnored bool
	// InitMissing reports that /sbin/init does not resolve inside the
	// image. Not an error in this mode (the bootloader configuration can
	// pass init=), but worth a warning.
	InitMissing bool
}

// CheckBootloader checks rfs against the contract for images that
// bring their own bootloader: /boot/efi is a directory tree holding only
// regular files and directories with names FAT can store, fitting in an
// ESP of espSize bytes, and containing the fallback boot file for arch as
// an EFI application PE32+ image of the matching machine type. Nothing
// from the image is executed.
func CheckBootloader(rfs *rootfs.Rootfs, arch string, espSize uint64) (*Result, error) {
	ea, ok := efiArchTable[arch]
	if !ok {
		return nil, fmt.Errorf("no bootloader fallback file known for architecture %q", arch)
	}

	root, err := rfs.Resolve(EFIDir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EFIDir, err)
	}
	if root.Header.Typeflag != tar.TypeDir {
		return nil, fmt.Errorf("%s is not a directory", EFIDir)
	}

	b := &Bootloader{EFIRoot: root.Path}
	wantFallback := strings.ToLower(espFallbackDir + "/" + ea.fileName)
	prefix := root.Path + "/"
	if root.Path == "/" {
		prefix = "/"
	}
	paths := make([]string, 0, 16)
	for p := range rfs.Index {
		if strings.HasPrefix(p, prefix) && p != root.Path {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	// Names that map to the same 8.3 short name (or that differ only in
	// case) cannot coexist in one directory with the FAT writer used.
	seen := map[string]string{}
	var fallback *ESPFile
	for _, p := range paths {
		e := rfs.Index[p]
		rel := strings.TrimPrefix(p, prefix)
		f := ESPFile{Path: rel}
		switch e.Header.Typeflag {
		case tar.TypeDir:
			f.Dir = true
		case tar.TypeReg:
			f.Size, f.Source = e.Header.Size, e
		case tar.TypeLink:
			src, err := hardlinkSource(rfs, p)
			if err != nil {
				return nil, err
			}
			f.Size, f.Source = src.Header.Size, src
		case tar.TypeSymlink:
			return nil, fmt.Errorf("%s: a symlink cannot go onto the FAT ESP; %s must hold only regular files and directories", p, EFIDir)
		default:
			return nil, fmt.Errorf("%s: %s cannot go onto the FAT ESP; %s must hold only regular files and directories", p, entryKind(e.Header.Typeflag), EFIDir)
		}
		if err := checkFATName(path.Base(rel)); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		key := path.Dir(rel) + "/" + shortNameKey(path.Base(rel))
		if other, dup := seen[key]; dup {
			return nil, fmt.Errorf("%s and %s map to the same 8.3 short name in one directory, which contemper's FAT writer cannot store (a limitation of the writer): rename one of them", other, p)
		}
		seen[key] = p
		b.Files = append(b.Files, f)
		if !f.Dir {
			b.TotalBytes += f.Size
		}
		if strings.ToLower(rel) == wantFallback {
			fallback = &b.Files[len(b.Files)-1]
			b.FallbackPath = rel
		}
	}

	if err := checkFits(b, espSize); err != nil {
		return nil, err
	}

	if fallback == nil {
		return nil, fmt.Errorf("%s/%s is missing: the firmware of a fresh VM can only find a bootloader at the fallback path", EFIDir, espFallbackDir+"/"+ea.fileName)
	}
	if fallback.Dir {
		return nil, fmt.Errorf("%s/%s is a directory, not the bootloader binary", EFIDir, b.FallbackPath)
	}
	b.FallbackSize = fallback.Size
	hdr, err := readHead(rfs, fallback.Source, peHeadBytes)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", EFIDir, b.FallbackPath, err)
	}
	if err := CheckEFIApplication(hdr, ea.machine); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", EFIDir, b.FallbackPath, err)
	}

	_, hasContemper := rfs.Lookup("/boot/contemper")
	if !hasContemper {
		_, err := rfs.Resolve("/boot/contemper")
		hasContemper = err == nil
	}
	b.ContemperIgnored = hasContemper
	_, initErr := rfs.Resolve(InitPath)
	b.InitMissing = initErr != nil

	return &Result{Bootloader: b}, nil
}

// hardlinkSource follows a hardlink entry to the regular file holding its
// content.
func hardlinkSource(rfs *rootfs.Rootfs, p string) (*rootfs.Entry, error) {
	cur := p
	for range 8 {
		e := rfs.Index[cur]
		if e.Header.Typeflag != tar.TypeLink {
			if e.Header.Typeflag != tar.TypeReg {
				return nil, fmt.Errorf("%s: hardlink target %s is %s, not a regular file", p, cur, entryKind(e.Header.Typeflag))
			}
			return e, nil
		}
		target := path.Clean("/" + strings.TrimPrefix(e.Header.Linkname, "./"))
		if _, ok := rfs.Index[target]; !ok {
			return nil, fmt.Errorf("%s: hardlink target %s is not in the merged filesystem", p, target)
		}
		cur = target
	}
	return nil, fmt.Errorf("%s: too many hardlink hops", p)
}

func entryKind(t byte) string {
	switch t {
	case tar.TypeChar:
		return "a character device"
	case tar.TypeBlock:
		return "a block device"
	case tar.TypeFifo:
		return "a FIFO"
	case tar.TypeSymlink:
		return "a symlink"
	case tar.TypeDir:
		return "a directory"
	}
	return fmt.Sprintf("an entry of type %q", t)
}

// peHeadBytes is how much of the fallback file is read to check its PE
// headers.
const peHeadBytes = 64 * 1024

func readHead(rfs *rootfs.Rootfs, e *rootfs.Entry, n int) ([]byte, error) {
	rc, err := rfs.Open(e)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, int64(n)))
}

// checkFATName rejects a file name the ESP writer cannot store: empty,
// starting with a dot (the writer gives such names no short name, which
// breaks the filesystem), too long, ending in a space or dot, or
// containing anything but printable ASCII without the characters FAT
// forbids (the writer cannot derive short names for other characters).
func checkFATName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("%q is not a valid FAT name", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("name %q starts with a dot, which contemper's FAT writer cannot store (a limitation of the writer)", name)
	}
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f, strings.ContainsRune(`\/:*?"<>|`, r):
			return fmt.Errorf("name %q contains %q, which FAT does not allow in names", name, r)
		case r > 0x7e:
			return fmt.Errorf("name %q contains %q: only printable ASCII names can go onto the FAT ESP (a limitation of contemper's FAT writer)", name, r)
		}
	}
	if len(name) > 255 {
		return fmt.Errorf("name is %d characters long; FAT allows 255", len(name))
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("name %q ends in a space or dot, which FAT strips", name)
	}
	return nil
}

// checkFits compares the space the tree takes on the FAT32 ESP, counting
// cluster rounding and directory clusters, with what an ESP of espSize
// offers.
func checkFits(b *Bootloader, espSize uint64) error {
	cluster, usable := disk.ESPGeometry(espSize)
	roundUp := func(n uint64) uint64 { return (n + cluster - 1) / cluster * cluster }
	// Directory entries, 32 bytes each: one short entry plus one long
	// name entry per 13 characters for every child, "." and ".." in
	// subdirectories, and the volume label in the root.
	dirEntries := map[string]uint64{"": 1}
	var need uint64
	for _, f := range b.Files {
		parent := ""
		if i := strings.LastIndex(f.Path, "/"); i >= 0 {
			parent = f.Path[:i]
		}
		name := f.Path[len(parent):]
		name = strings.TrimPrefix(name, "/")
		dirEntries[parent] += 1 + (uint64(len(name))+12)/13
		if f.Dir {
			dirEntries[f.Path] += 2
			continue
		}
		need += roundUp(max(uint64(f.Size), 1)) //nolint:gosec // G115: sizes are non-negative
	}
	for _, n := range dirEntries {
		need += roundUp(n * 32)
	}
	if need > usable {
		return fmt.Errorf("the %s tree needs %s on the FAT ESP (%s of file data, with files and directories rounded up to %s clusters), but the %s ESP has %s usable",
			EFIDir, progress.HumanBytes(int64(need)), progress.HumanBytes(b.TotalBytes), progress.HumanBytes(int64(cluster)), //nolint:gosec // G115: far below the int64 limit
			progress.HumanBytes(int64(espSize)), progress.HumanBytes(int64(usable))) //nolint:gosec // G115: far below the int64 limit
	}
	return nil
}

// shortNameKey folds name the way the ESP writer derives an 8.3 short
// name, so two names that would get the same short name map to the same
// key. The writer only adds a unique numeric tail when the (folded) stem
// exceeds eight characters; a lossy name whose stem fits is used as it
// is, so such names clash ("a b" and "ab", "foo.conf" and "foo.cons",
// "6.1.0-25-amd64" and "6.1.0-26-amd64"). Names with a long stem are
// keyed on the whole name, case-folded.
func shortNameKey(name string) string {
	stem, ext := name, ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		stem, ext = name[:i], name[i+1:]
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	fold := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z':
				b.WriteRune(r - 32)
			case r == ' ' || r == '.':
			case r < 0x80 && strings.ContainsRune("!#$%&'()-0123456789@ABCDEFGHIJKLMNOPQRSTUVWXYZ^_`{}~", r):
				b.WriteRune(r)
			default:
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	stem = fold(stem)
	if len(stem) > 8 {
		// Unique tail: key on the full long name, case-folded.
		return "L:" + strings.ToUpper(name)
	}
	return stem + "." + fold(ext)
}
