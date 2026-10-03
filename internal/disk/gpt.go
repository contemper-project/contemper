package disk

import (
	"fmt"
	"io"
	"os"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

const (
	sectorSize     uint64 = 512
	alignmentBytes uint64 = 1 * 1024 * 1024 // 1 MiB
	// ESPVolumeLabel is the FAT volume label of the ESP. The guest's fstab
	// line for /boot/efi finds the partition by it.
	ESPVolumeLabel = "ESP"
	// ESPSizeBytes is the size of the ESP partition BuildGPTImage
	// creates for a UKI, unless BuildOptions.ESPSizeBytes says otherwise.
	ESPSizeBytes uint64 = 128 * 1024 * 1024
	// BootloaderESPSizeBytes is the ESP size for images that bring their
	// own bootloader, which may keep kernels on the ESP.
	BootloaderESPSizeBytes uint64 = 512 * 1024 * 1024
	// tailBytes leaves room, after the root partition, for the backup
	// GPT header and partition array (about 16.5 KiB), rounded up
	// generously to a full alignment unit.
	tailBytes uint64 = alignmentBytes
	// espCopyBufferBytes is the chunk size for copying a file onto the ESP.
	espCopyBufferBytes = 8 * 1024 * 1024
)

// archInfo carries the two pieces of the disk layout that vary by target
// architecture: the UEFI fallback boot file name and the discoverable
// -partitions-spec root type GUID.
type archInfo struct {
	bootFile string
	rootType gpt.Type
}

var archTable = map[string]archInfo{
	"arm64": {bootFile: "BOOTAA64.EFI", rootType: gpt.LinuxRootArm64},
	"amd64": {bootFile: "BOOTX64.EFI", rootType: gpt.LinuxRootX86_64},
}

func alignUp64(v, align uint64) uint64 {
	if align == 0 || v%align == 0 {
		return v
	}
	return v + (align - v%align)
}

// RootPartitionSize computes the default root partition size, leaving
// headroom for the image to grow without the caller specifying a size
// explicitly: max(1 GiB, 1.5 x contentBytes + 256 MiB), rounded up to a
// whole MiB. See docs/guide/disk.md for the rationale.
func RootPartitionSize(contentBytes int64) int64 {
	const oneGiB = 1 << 30
	const twoFiftySixMiB = 256 << 20
	computed := int64(float64(contentBytes)*1.5) + twoFiftySixMiB
	if computed < oneGiB {
		return oneGiB
	}
	return AlignRootSize(computed)
}

// AlignRootSize rounds a root partition size up to a whole MiB. The ext4
// image fills the partition exactly, and the partition spans whole
// sectors, so a size that isn't sector-aligned can't be written.
func AlignRootSize(size int64) int64 {
	return int64(alignUp64(uint64(size), alignmentBytes)) //nolint:gosec // G115: real disk sizes never approach the int64/uint64 boundary
}

// BuildOptions configures BuildGPTImage.
type BuildOptions struct {
	// Arch is "arm64" or "amd64".
	Arch string
	// UKI is the built Unified Kernel Image, written to the ESP as
	// EFI/BOOT/BOOT{AA64,X64}.EFI. Ignored when ESPTree is set.
	UKI []byte
	// ESPTree, if non-nil, is copied onto the ESP instead of the UKI.
	// Entries must list every directory before its contents.
	ESPTree []ESPEntry
	// ESPSizeBytes is the ESP partition size; zero means ESPSizeBytes.
	// It must be a multiple of the sector size.
	ESPSizeBytes uint64
	// RootImgPath is a pre-built, pre-populated ext4 image (see
	// PopulateExt4) whose raw bytes become the root partition.
	RootImgPath string
	// RootSizeBytes is the exact size of the file at RootImgPath.
	RootSizeBytes int64
}

// Layout describes the sector layout BuildGPTImage chose, mostly useful
// for tests.
type Layout struct {
	ESPStartSector, ESPEndSector   uint64
	RootStartSector, RootEndSector uint64
	TotalSizeBytes                 uint64
}

// ESPEntry is one file or directory to place on the ESP.
type ESPEntry struct {
	// Path is relative to the ESP root, slash-separated, without a
	// leading slash ("EFI/BOOT/BOOTX64.EFI").
	Path string
	// Dir marks a directory; the fields below apply to files only.
	Dir bool
	// Size is the file's size in bytes.
	Size int64
	// Open returns a reader over the file's content.
	Open func() (io.ReadCloser, error)
}

// BuildGPTImage creates a fresh GPT disk image at rawPath: a 1 MiB
// -aligned ESP (FAT32, 128 MiB by default, holding the UEFI fallback boot
// file or the given ESPTree) as partition 1, and the ext4 root (from opts.RootImgPath) as partition 2,
// last on the disk.
func BuildGPTImage(rawPath string, opts BuildOptions) (*Layout, error) {
	info, ok := archTable[opts.Arch]
	if !ok {
		return nil, fmt.Errorf("no disk layout for arch %q", opts.Arch)
	}
	if opts.RootSizeBytes <= 0 {
		return nil, fmt.Errorf("RootSizeBytes must be positive")
	}

	espStart := alignmentBytes / sectorSize
	espSize := opts.ESPSizeBytes
	if espSize == 0 {
		espSize = ESPSizeBytes
	}
	if espSize%sectorSize != 0 {
		return nil, fmt.Errorf("ESPSizeBytes %d is not a multiple of the %d-byte sector size", espSize, sectorSize)
	}
	espSectors := espSize / sectorSize
	espEnd := espStart + espSectors - 1

	rootStart := alignUp64((espEnd+1)*sectorSize, alignmentBytes) / sectorSize
	rootSectors := (uint64(opts.RootSizeBytes) + sectorSize - 1) / sectorSize
	rootEnd := rootStart + rootSectors - 1

	totalBytes := alignUp64((rootEnd+1)*sectorSize, alignmentBytes) + tailBytes

	if err := os.Remove(rawPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("removing existing %s: %w", rawPath, err)
	}

	d, err := diskfs.Create(rawPath, int64(totalBytes), diskfs.SectorSizeDefault) //nolint:gosec // G115: real disk sizes never approach the uint64/int64 boundary
	if err != nil {
		return nil, fmt.Errorf("creating disk image %s: %w", rawPath, err)
	}
	// d.Close resets *d to its zero value, so calling it twice (this
	// defer plus the checked call on the success path below) would call
	// Close on a nil Backend and panic; closed guards against that.
	closed := false
	defer func() {
		if !closed {
			_ = d.Close()
		}
	}()

	table := &gpt.Table{
		ProtectiveMBR: true,
		Partitions: []*gpt.Partition{
			{Index: 1, Start: espStart, End: espEnd, Type: gpt.EFISystemPartition, Name: "ESP"},
			{Index: 2, Start: rootStart, End: rootEnd, Type: info.rootType, Name: "root"},
		},
	}
	if err := d.Partition(table); err != nil {
		return nil, fmt.Errorf("writing GPT: %w", err)
	}

	if opts.ESPTree != nil {
		err = writeESPTree(d, opts.ESPTree)
	} else {
		err = writeESP(d, info.bootFile, opts.UKI)
	}
	if err != nil {
		return nil, err
	}

	if err := writeRootPartition(d, opts.RootImgPath); err != nil {
		return nil, err
	}

	// Checked, not deferred-and-ignored: a flush failure surfaced only
	// at Close must not leave callers thinking a corrupt disk image is
	// good.
	closed = true
	if err := d.Close(); err != nil {
		return nil, fmt.Errorf("finalizing disk image %s: %w", rawPath, err)
	}

	return &Layout{
		ESPStartSector:  espStart,
		ESPEndSector:    espEnd,
		RootStartSector: rootStart,
		RootEndSector:   rootEnd,
		TotalSizeBytes:  totalBytes,
	}, nil
}

func writeESP(d *diskpkg.Disk, bootFile string, uki []byte) error {
	fs, err := d.CreateFilesystem(diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: ESPVolumeLabel})
	if err != nil {
		return fmt.Errorf("creating ESP filesystem: %w", err)
	}
	if err := fs.Mkdir("/EFI/BOOT"); err != nil {
		return fmt.Errorf("creating /EFI/BOOT on ESP: %w", err)
	}
	rw, err := fs.OpenFile("/EFI/BOOT/"+bootFile, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("creating /EFI/BOOT/%s: %w", bootFile, err)
	}
	if _, err := rw.Write(uki); err != nil {
		return fmt.Errorf("writing /EFI/BOOT/%s: %w", bootFile, err)
	}
	return nil
}

// writeESPTree creates the ESP's FAT32 filesystem and copies tree onto it.
func writeESPTree(d *diskpkg.Disk, tree []ESPEntry) error {
	fs, err := d.CreateFilesystem(diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: ESPVolumeLabel})
	if err != nil {
		return fmt.Errorf("creating ESP filesystem: %w", err)
	}
	for _, e := range tree {
		p := "/" + e.Path
		if e.Dir {
			if err := fs.Mkdir(p); err != nil {
				return fmt.Errorf("creating %s on ESP: %w", p, err)
			}
			continue
		}
		if err := copyToESP(fs, p, e); err != nil {
			return err
		}
	}
	return nil
}

func copyToESP(fs filesystem.FileSystem, p string, e ESPEntry) error {
	src, err := e.Open()
	if err != nil {
		return fmt.Errorf("reading %s for the ESP: %w", p, err)
	}
	defer func() { _ = src.Close() }()
	dst, err := fs.OpenFile(p, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("creating %s on ESP: %w", p, err)
	}
	// Every Write to the FAT library rewrites the FATs, so a big buffer
	// matters: 32 KiB chunks are about ten times slower.
	buf := make([]byte, espCopyBufferBytes)
	n, err := io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, buf)
	if err != nil {
		return fmt.Errorf("writing %s to ESP: %w", p, err)
	}
	if cerr := dst.Close(); cerr != nil {
		return fmt.Errorf("closing %s on ESP: %w", p, cerr)
	}
	if n != e.Size {
		return fmt.Errorf("copying %s to ESP: wrote %d bytes, expected %d", p, n, e.Size)
	}
	return nil
}

func writeRootPartition(d *diskpkg.Disk, rootImgPath string) error {
	f, err := os.Open(rootImgPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", rootImgPath, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := d.WritePartitionContents(2, f); err != nil {
		return fmt.Errorf("writing root partition contents: %w", err)
	}
	return nil
}
