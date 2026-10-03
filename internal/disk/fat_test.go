package disk_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"

	cdisk "github.com/contemper-project/contemper/internal/disk"
)

// TestESPGeometryMatchesFormattedFilesystem formats a real FAT32 and reads
// the geometry back from its boot sector.
func TestESPGeometryMatchesFormattedFilesystem(t *testing.T) {
	for _, size := range []uint64{128 << 20, 512 << 20} {
		path := filepath.Join(t.TempDir(), "esp.img")
		d, err := diskfs.Create(path, int64(size), diskfs.SectorSizeDefault)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 0, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"}); err != nil {
			t.Fatal(err)
		}
		_ = d.Close()

		bs := make([]byte, 512)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.ReadAt(bs, 0)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		bytesPerSector := uint64(binary.LittleEndian.Uint16(bs[11:]))
		spc := uint64(bs[13])
		reserved := uint64(binary.LittleEndian.Uint16(bs[14:]))
		fats := uint64(bs[16])
		total := uint64(binary.LittleEndian.Uint32(bs[32:]))
		spf := uint64(binary.LittleEndian.Uint32(bs[36:]))
		wantCluster := spc * bytesPerSector
		wantUsable := (total - reserved - fats*spf) / spc * wantCluster

		cluster, usable := cdisk.ESPGeometry(size)
		if cluster != wantCluster || usable != wantUsable {
			t.Errorf("%d MiB: ESPGeometry = (%d, %d), formatted filesystem has (%d, %d)", size>>20, cluster, usable, wantCluster, wantUsable)
		}
	}
}

func TestESPGeometryTooSmall(t *testing.T) {
	if _, usable := cdisk.ESPGeometry(2048); usable != 0 {
		t.Errorf("usable = %d for a 2 KiB partition, want 0", usable)
	}
}
