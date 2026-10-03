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

func memEntry(path string, data []byte) disk.ESPEntry {
	return disk.ESPEntry{
		Path: path,
		Size: int64(len(data)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

func TestBuildGPTImageESPTree(t *testing.T) {
	dir := t.TempDir()
	rootSize := int64(4 * 1024 * 1024)
	rootImgPath := dir + "/root.img"
	if err := os.WriteFile(rootImgPath, make([]byte, rootSize), 0o644); err != nil {
		t.Fatal(err)
	}

	big := make([]byte, 5*1024*1024+123)
	for i := range big {
		big[i] = byte(i * 7)
	}
	files := map[string][]byte{
		"EFI/BOOT/BOOTAA64.EFI":                     []byte("fallback"),
		"EFI/debian/grubaa64.efi":                   big,
		"EFI/debian/grub.cfg":                       []byte("search --label contemper-root\n"),
		"EFI/debian/A Long Name With Spaces.conf":   []byte("long"),
		"loader/entries/some-quite-long-entry.conf": []byte("entry"),
		"empty.txt": nil,
	}
	tree := []disk.ESPEntry{
		{Path: "EFI", Dir: true},
		{Path: "EFI/BOOT", Dir: true},
		{Path: "EFI/debian", Dir: true},
		{Path: "loader", Dir: true},
		{Path: "loader/entries", Dir: true},
		{Path: "emptydir", Dir: true},
	}
	for p, data := range files {
		tree = append(tree, memEntry(p, data))
	}

	rawPath := dir + "/disk.raw"
	layout, err := disk.BuildGPTImage(rawPath, disk.BuildOptions{
		Arch:          "arm64",
		ESPTree:       tree,
		ESPSizeBytes:  disk.BootloaderESPSizeBytes,
		RootImgPath:   rootImgPath,
		RootSizeBytes: rootSize,
	})
	if err != nil {
		t.Fatalf("BuildGPTImage: %v", err)
	}
	if got := (layout.ESPEndSector - layout.ESPStartSector + 1) * 512; got != 512<<20 {
		t.Errorf("ESP size = %d, want 512 MiB", got)
	}
	if layout.RootStartSector <= layout.ESPEndSector {
		t.Errorf("root (start %d) does not follow the ESP (end %d)", layout.RootStartSector, layout.ESPEndSector)
	}

	d, err := diskfs.Open(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	table, err := d.GetPartitionTable()
	if err != nil {
		t.Fatal(err)
	}
	esp := table.GetPartitions()[0]
	if esp.GetSize() != 512<<20 {
		t.Errorf("ESP partition size = %d, want 512 MiB", esp.GetSize())
	}

	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatalf("GetFilesystem(ESP): %v", err)
	}
	for p, want := range files {
		f, err := fs.OpenFile("/"+p, os.O_RDONLY)
		if err != nil {
			t.Errorf("opening %s: %v", p, err)
			continue
		}
		got, err := io.ReadAll(f)
		if err != nil {
			t.Errorf("reading %s: %v", p, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: content differs (got %d bytes, want %d)", p, len(got), len(want))
		}
	}
	for _, p := range []string{"EFI/debian", "loader/entries", "emptydir"} {
		if _, err := fs.ReadDir(p); err != nil {
			t.Errorf("ReadDir(%s): %v", p, err)
		}
	}
	names, err := fs.ReadDir("EFI/debian")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, fi := range names {
		got = append(got, fi.Name())
	}
	for _, want := range []string{"A Long Name With Spaces.conf", "grub.cfg", "grubaa64.efi"} {
		found := false
		for _, n := range got {
			found = found || n == want
		}
		if !found {
			t.Errorf("/EFI/debian lists %q, missing long name %q", got, want)
		}
	}
}

func TestBuildGPTImageESPSizeMustBeSectorMultiple(t *testing.T) {
	_, err := disk.BuildGPTImage(t.TempDir()+"/d.raw", disk.BuildOptions{
		Arch: "arm64", RootSizeBytes: 1 << 20, ESPSizeBytes: 1000,
	})
	if err == nil {
		t.Fatal("expected an error for a non-sector-multiple ESP size")
	}
}
