package disk_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/partition/gpt"

	"github.com/contemper-project/contemper/internal/disk"
)

func TestBuildGPTImageARM64(t *testing.T) {
	testBuildGPTImage(t, "arm64", "BOOTAA64.EFI", gpt.LinuxRootArm64)
}

func TestBuildGPTImageAMD64(t *testing.T) {
	testBuildGPTImage(t, "amd64", "BOOTX64.EFI", gpt.LinuxRootX86_64)
}

func testBuildGPTImage(t *testing.T, arch, wantBootFile string, wantRootType gpt.Type) {
	t.Helper()
	dir := t.TempDir()

	rootSize := int64(8 * 1024 * 1024)
	rootImgPath := dir + "/root.img"
	rootMarker := bytes.Repeat([]byte{0x42}, 1024)
	rootContent := make([]byte, rootSize)
	copy(rootContent, rootMarker)
	if err := os.WriteFile(rootImgPath, rootContent, 0o644); err != nil {
		t.Fatal(err)
	}

	uki := []byte("fake-uki-content-for-test")
	rawPath := dir + "/disk.raw"

	layout, err := disk.BuildGPTImage(rawPath, disk.BuildOptions{
		Arch:          arch,
		UKI:           uki,
		RootImgPath:   rootImgPath,
		RootSizeBytes: rootSize,
	})
	if err != nil {
		t.Fatalf("BuildGPTImage: %v", err)
	}

	if layout.ESPStartSector%2048 != 0 {
		t.Errorf("ESP start sector %d is not 1 MiB-aligned", layout.ESPStartSector)
	}
	if layout.RootStartSector%2048 != 0 {
		t.Errorf("root start sector %d is not 1 MiB-aligned", layout.RootStartSector)
	}
	if layout.RootStartSector <= layout.ESPEndSector {
		t.Errorf("root partition (start %d) does not follow the ESP (end %d)", layout.RootStartSector, layout.ESPEndSector)
	}

	// Re-open the disk fresh, as an independent reader would, and check
	// the GPT, ESP and root partition contents.
	d, err := diskfs.Open(rawPath)
	if err != nil {
		t.Fatalf("re-opening %s: %v", rawPath, err)
	}
	defer func() { _ = d.Close() }()

	table, err := d.GetPartitionTable()
	if err != nil {
		t.Fatalf("GetPartitionTable: %v", err)
	}
	gptTable, ok := table.(*gpt.Table)
	if !ok {
		t.Fatalf("partition table is %T, want *gpt.Table", table)
	}
	if len(gptTable.Partitions) != 2 {
		t.Fatalf("got %d partitions, want 2", len(gptTable.Partitions))
	}
	esp, root := gptTable.Partitions[0], gptTable.Partitions[1]
	if esp.Type != gpt.EFISystemPartition {
		t.Errorf("ESP type = %s, want %s", esp.Type, gpt.EFISystemPartition)
	}
	if esp.Name != "ESP" {
		t.Errorf("ESP name = %q, want ESP", esp.Name)
	}
	if root.Type != wantRootType {
		t.Errorf("root type = %s, want %s", root.Type, wantRootType)
	}
	if root.Name != "root" {
		t.Errorf("root name = %q, want root", root.Name)
	}

	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatalf("GetFilesystem(ESP): %v", err)
	}
	f, err := fs.OpenFile("/EFI/BOOT/"+wantBootFile, os.O_RDONLY)
	if err != nil {
		t.Fatalf("opening /EFI/BOOT/%s: %v", wantBootFile, err)
	}
	got := make([]byte, len(uki))
	if _, err := io.ReadFull(f, got); err != nil {
		t.Fatalf("reading %s: %v", wantBootFile, err)
	}
	if !bytes.Equal(got, uki) {
		t.Errorf("ESP boot file content = %q, want %q", got, uki)
	}

	var rootOut bytes.Buffer
	if _, err := d.ReadPartitionContents(2, &rootOut); err != nil {
		t.Fatalf("ReadPartitionContents(root): %v", err)
	}
	if !bytes.HasPrefix(rootOut.Bytes(), rootMarker) {
		t.Errorf("root partition content does not start with the expected marker")
	}
}
