//go:build linux || darwin

package disk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Any failure of the first SEEK_DATA other than ENXIO (macOS reports
// ENOTTY or EOPNOTSUPP from file systems without hole support) means
// "no hole information", so the copy reads the file instead of failing.
func TestNextDataExtentUnsupportedIsNoHoleInfo(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close() // seeking a closed file fails with EBADF
	if _, _, err := nextDataExtent(f, 0); !errors.Is(err, errNoHoleInfo) {
		t.Errorf("got %v, want errNoHoleInfo", err)
	}
}
