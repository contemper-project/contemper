package disk

import (
	"fmt"
	"os"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

const (
	sectorSize     uint64 = 512
	alignmentBytes uint64 = 1 * 1024 * 1024 // 1 MiB
	// ESPSizeBytes is the fixed size of the ESP partition BuildGPTImage
	// creates.
	ESPSizeBytes uint64 = 128 * 1024 * 1024
	// tailBytes leaves room, after the root partition, for the backup
	// GPT header and partition array (about 16.5 KiB), rounded up
	// generously to a full alignment unit.
	tailBytes uint64 = alignmentBytes
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
// explicitly: max(1 GiB, 1.5 x contentBytes + 256 MiB). See
// docs/guide/disk.md for the rationale.
func RootPartitionSize(contentBytes int64) int64 {
	const oneGiB = 1 << 30
	const twoFiftySixMiB = 256 << 20
	computed := int64(float64(contentBytes)*1.5) + twoFiftySixMiB
	if computed < oneGiB {
		return oneGiB
	}
	return computed
}

// BuildOptions configures BuildGPTImage.
type BuildOptions struct {
	// Arch is "arm64" or "amd64".
	Arch string
	// UKI is the built Unified Kernel Image, written to the ESP as
	// EFI/BOOT/BOOT{AA64,X64}.EFI.
	UKI []byte
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

// BuildGPTImage creates a fresh GPT disk image at rawPath: a 1 MiB
// -aligned ESP (FAT32, 128 MiB, holding the UEFI fallback boot file) as
// partition 1, and the ext4 root (from opts.RootImgPath) as partition 2,
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
	espSectors := ESPSizeBytes / sectorSize
	espEnd := espStart + espSectors - 1

	rootStart := alignUp64((espEnd+1)*sectorSize, alignmentBytes) / sectorSize
	rootSectors := (uint64(opts.RootSizeBytes) + sectorSize - 1) / sectorSize
	rootEnd := rootStart + rootSectors - 1

	totalBytes := alignUp64((rootEnd+1)*sectorSize, alignmentBytes) + tailBytes

	if err := os.Remove(rawPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("removing existing %s: %w", rawPath, err)
	}

	d, err := diskfs.Create(rawPath, int64(totalBytes), diskfs.SectorSizeDefault)
	if err != nil {
		return nil, fmt.Errorf("creating disk image %s: %w", rawPath, err)
	}
	defer d.Close()

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

	if err := writeESP(d, info.bootFile, opts.UKI); err != nil {
		return nil, err
	}

	if err := writeRootPartition(d, opts.RootImgPath); err != nil {
		return nil, err
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
	fs, err := d.CreateFilesystem(diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"})
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

func writeRootPartition(d *diskpkg.Disk, rootImgPath string) error {
	f, err := os.Open(rootImgPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", rootImgPath, err)
	}
	defer f.Close()
	if _, err := d.WritePartitionContents(2, f); err != nil {
		return fmt.Errorf("writing root partition contents: %w", err)
	}
	return nil
}
