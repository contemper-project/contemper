//go:build unix

package disk

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// assertSparse fails if dst has much more than the few data blocks of
// sparseSource allocated, unless the file system doesn't keep holes.
func assertSparse(t *testing.T, dst *os.File, size int64) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(dst.Name(), &st); err != nil {
		t.Fatal(err)
	}
	alloc := st.Blocks * 512
	// The probe: a file that was only truncated shows whether holes exist.
	probe := filepath.Join(filepath.Dir(dst.Name()), "probe")
	pf, err := os.Create(probe)
	if err != nil {
		t.Fatal(err)
	}
	_ = pf.Truncate(size)
	_ = pf.Close()
	var pst syscall.Stat_t
	if err := syscall.Stat(probe, &pst); err != nil {
		t.Fatal(err)
	}
	if pst.Blocks*512 > size/2 {
		t.Skip("the test file system doesn't keep holes")
	}
	if alloc > 1<<20 {
		t.Errorf("destination has %d bytes allocated for %d bytes of mostly zeros", alloc, size)
	}
}
