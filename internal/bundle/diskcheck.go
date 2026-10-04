package bundle

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Disk formats contemper writes into a bundle.
const (
	FormatQcow2 = "qcow2"
	FormatRaw   = "raw"
)

// VerifyDisk checks that the disk a manifest names is safe to hand to
// the hypervisor and returns its path. A bundle may come from someone
// else, and qemu follows symlinks, hard-linked files, backing files and
// external data files, any of which would let the guest read host
// files. So the disk must be a regular file with a single link (not a
// symlink or device) inside dir, and a qcow2 disk must be
// self-contained: no backing file, no external data file.
//
// The qcow2 check parses the image header in Go rather than running
// qemu-img on it, since qemu's own parsers are what an untrusted image
// should not reach before it is vetted.
//
// Known limitation: the disk could still be swapped between this check
// and qemu opening it. That needs write access to the bundle directory
// while deploy runs, which is the same access that could replace the
// manifest or the disk outright, so it is not guarded against (no file
// descriptor is passed to qemu).
func VerifyDisk(dir string, m *Manifest) (string, error) {
	path := filepath.Join(dir, m.Disk.File)
	fi, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("bundle disk %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("bundle disk %s is %s, not a regular file; contemper only boots a disk file stored inside the bundle directory, not a symlink or device", path, describeMode(fi.Mode()))
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && uint64(st.Nlink) > 1 { //nolint:unconvert // Nlink is uint16 on some platforms
		return "", fmt.Errorf("bundle disk %s has %d hard links; contemper only boots a disk file that exists only inside the bundle directory, not a link to another file", path, st.Nlink)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(realPath) != realDir {
		return "", fmt.Errorf("bundle disk %s resolves to %s, outside the bundle directory", path, realPath)
	}
	if m.Disk.Format != FormatQcow2 && m.Disk.Format != "" {
		return path, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("bundle disk %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	head, err := io.ReadAll(io.LimitReader(f, qcow2MaxHeader))
	if err != nil {
		return "", fmt.Errorf("bundle disk %s: %w", path, err)
	}
	if err := checkQcow2Header(head); err != nil {
		return "", fmt.Errorf("bundle disk %s: %w", path, err)
	}
	return path, nil
}

func describeMode(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "a symlink"
	case m&os.ModeDir != 0:
		return "a directory"
	case m&os.ModeDevice != 0:
		return "a device"
	}
	return "a special file"
}

// qcow2 header layout (all fields big-endian), see the QEMU qcow2 spec.
const (
	qcow2Magic          = 0x514649fb // "QFI\xfb"
	qcow2MaxHeader      = 1 << 21    // the header cluster is at most 2 MiB
	qcow2OffVersion     = 4
	qcow2OffBackingOff  = 8
	qcow2OffClusterBits = 20
	qcow2OffIncompat    = 72
	qcow2OffHeaderLen   = 100
	qcow2V2HeaderLen    = 72
	qcow2V3MinHeaderLen = 104

	// Incompatible feature bits this reader knows. Bit 2 (external data
	// file) is known and refused; every bit above the listed ones is
	// unknown, and refused because it may change what the image refers to.
	qcow2IncompatDataFile = 1 << 2
	qcow2IncompatKnown    = 1<<0 | 1<<1 | qcow2IncompatDataFile | 1<<3 | 1<<4

	qcow2ExtDataFile = 0x44415441
)

const qcow2Rebuild = "contemper only boots self-contained disks, so rebuild the bundle with contemper convert"

// checkQcow2Header validates the start of a qcow2 image (head holds at
// least the header cluster, or the whole file if smaller) and rejects one
// that refers to another file: a backing file, or an external data file
// (by feature bit or header extension). Unknown incompatible features
// are refused too. It reads only the header fields and the header
// extensions, every access bounds-checked.
func checkQcow2Header(head []byte) error {
	if len(head) < qcow2V2HeaderLen {
		return fmt.Errorf("not a qcow2 image (%d bytes is too short for a header)", len(head))
	}
	if binary.BigEndian.Uint32(head) != qcow2Magic {
		return fmt.Errorf("not a qcow2 image (bad magic) although the manifest says qcow2")
	}
	version := binary.BigEndian.Uint32(head[qcow2OffVersion:])
	if version != 2 && version != 3 {
		return fmt.Errorf("unsupported qcow2 version %d", version)
	}
	if off := binary.BigEndian.Uint64(head[qcow2OffBackingOff:]); off != 0 {
		return fmt.Errorf("refers to a backing file; %s", qcow2Rebuild)
	}
	clusterBits := binary.BigEndian.Uint32(head[qcow2OffClusterBits:])
	if clusterBits < 9 || clusterBits > 21 {
		return fmt.Errorf("invalid qcow2 cluster size (2^%d)", clusterBits)
	}
	extStart := uint64(qcow2V2HeaderLen)
	if version == 3 {
		if len(head) < qcow2V3MinHeaderLen {
			return fmt.Errorf("truncated qcow2 header")
		}
		incompat := binary.BigEndian.Uint64(head[qcow2OffIncompat:])
		if incompat&qcow2IncompatDataFile != 0 {
			return fmt.Errorf("refers to an external data file; %s", qcow2Rebuild)
		}
		if unknown := incompat &^ qcow2IncompatKnown; unknown != 0 {
			return fmt.Errorf("uses qcow2 incompatible features (0x%x) this contemper does not know", unknown)
		}
		extStart = uint64(binary.BigEndian.Uint32(head[qcow2OffHeaderLen:]))
		if extStart < qcow2V3MinHeaderLen {
			return fmt.Errorf("invalid qcow2 header length %d", extStart)
		}
	}
	end := uint64(len(head))
	if cs := uint64(1) << clusterBits; cs < end {
		end = cs
	}
	for p := extStart; ; {
		if p+8 > end {
			// No terminator before the end of the header cluster.
			return nil
		}
		typ := binary.BigEndian.Uint32(head[p:])
		length := uint64(binary.BigEndian.Uint32(head[p+4:]))
		if typ == 0 {
			return nil
		}
		if typ == qcow2ExtDataFile {
			return fmt.Errorf("refers to an external data file; %s", qcow2Rebuild)
		}
		next := p + 8 + (length+7)&^7
		if next > end {
			return fmt.Errorf("truncated qcow2 header extension 0x%x", typ)
		}
		p = next
	}
}

// maxManifestSize bounds the manifest and group files. Both are small
// JSON documents; a bundle from someone else must not be able to make
// deploy read without end (a link to /dev/zero) or block (a FIFO).
const maxManifestSize = 1 << 20

// readSmallFile reads the regular file at path, refusing anything else
// (symlink, device, FIFO) and anything over maxManifestSize.
func readSmallFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is %s, not a regular file", path, describeMode(fi.Mode()))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxManifestSize)
	}
	return data, nil
}
