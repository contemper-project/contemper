package disk

// ESPGeometry returns the FAT32 cluster size and the bytes usable for file
// and directory data on an ESP partition of espSize bytes, as the ESP
// writer formats it. usable is zero when the partition is too small to
// hold a FAT32 filesystem.
//
// It mirrors how the FAT library lays out a volume (512-byte sectors, 32
// reserved sectors, two FATs, cluster size by volume size), so callers can
// check a tree against the real capacity instead of the raw partition
// size. A test compares it with an actually formatted filesystem.
func ESPGeometry(espSize uint64) (clusterBytes, usable uint64) {
	const (
		kb       = 1024
		mb       = 1024 * kb
		gb       = 1024 * mb
		reserved = 32
	)
	switch {
	case espSize <= 260*mb:
		clusterBytes = 512
	case espSize <= 8*gb:
		clusterBytes = 4 * kb
	case espSize <= 16*gb:
		clusterBytes = 8 * kb
	case espSize <= 32*gb:
		clusterBytes = 16 * kb
	default:
		clusterBytes = 32 * kb
	}
	totalSectors := espSize / sectorSize
	if totalSectors <= reserved {
		return clusterBytes, 0
	}
	spc := clusterBytes / sectorSize
	denom := sectorSize*spc + 8
	sectorsPerFAT := (4*(totalSectors-reserved) + denom - 1) / denom
	if totalSectors < reserved+2*sectorsPerFAT {
		return clusterBytes, 0
	}
	dataSectors := totalSectors - reserved - 2*sectorsPerFAT
	usable = dataSectors / spc * clusterBytes
	if dataSectors*sectorSize < 32*kb {
		usable = 0
	}
	return clusterBytes, usable
}
