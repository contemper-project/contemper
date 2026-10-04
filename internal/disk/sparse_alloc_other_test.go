//go:build !unix

package disk

import (
	"os"
	"testing"
)

// assertSparse is a no-op where allocated blocks can't be read.
func assertSparse(t *testing.T, _ *os.File, _ int64) {
	t.Helper()
	t.Skip("allocated blocks can't be read on this platform")
}
