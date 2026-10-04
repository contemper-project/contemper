package disk

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// sparseSource creates a sparse file of size bytes with data at a few
// places: an unaligned run, a block of zeros written as data, and the
// final bytes.
func sparseSource(t *testing.T, size int64) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "src"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	rnd := rand.New(rand.NewSource(7))
	rb := func(n int) []byte {
		b := make([]byte, n)
		rnd.Read(b)
		return b
	}
	for _, w := range []struct {
		off int64
		b   []byte
	}{
		{100, rb(3000)},
		{1<<20 - 50, rb(200)}, // straddles a chunk boundary
		{2 << 20, make([]byte, 3*sparseBlockBytes)},
		{5<<20 + 4096, rb(2*sparseBlockBytes + 1)},
		{size - 10, rb(10)},
	} {
		if w.off+int64(len(w.b)) > size {
			continue
		}
		if _, err := f.WriteAt(w.b, w.off); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func copyAndCompare(t *testing.T, src *os.File, size, dstOff int64) *os.File {
	t.Helper()
	dst, err := os.Create(filepath.Join(t.TempDir(), "dst"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	if err := dst.Truncate(dstOff + size + 777); err != nil {
		t.Fatal(err)
	}
	if err := CopySparse(dst, dstOff, src, size); err != nil {
		t.Fatalf("CopySparse: %v", err)
	}
	want, err := os.ReadFile(src.Name())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	full := make([]byte, dstOff+size+777)
	copy(full[dstOff:], want[:size])
	if !bytes.Equal(got, full) {
		t.Fatalf("copy differs from a plain copy (sha256 %x vs %x)", sha256.Sum256(got), sha256.Sum256(full))
	}
	return dst
}

func TestCopySparseMatchesPlainCopy(t *testing.T) {
	const size = 8<<20 + 1234
	for _, off := range []int64{0, 1 << 20, 1<<20 + 512} {
		copyAndCompare(t, sparseSource(t, size), size, off)
	}
	// A prefix of the source only.
	copyAndCompare(t, sparseSource(t, size), 3<<20+5, 512)
}

func TestCopySparseFallback(t *testing.T) {
	orig := nextData
	t.Cleanup(func() { nextData = orig })
	calls := 0
	nextData = func(*os.File, int64) (int64, int64, error) {
		calls++
		return 0, 0, errNoHoleInfo
	}
	const size = 8<<20 + 1234
	dst := copyAndCompare(t, sparseSource(t, size), size, 1<<20)
	if calls == 0 {
		t.Errorf("the forced fallback was never consulted")
	}
	assertSparse(t, dst, size)
}

func TestCopySparseSkipsZeros(t *testing.T) {
	const size = 64 << 20
	src := sparseSource(t, size)
	dst := copyAndCompare(t, src, size, 1<<20)
	assertSparse(t, dst, size)
}

type failWriter struct{ err error }

func (w failWriter) WriteAt([]byte, int64) (int, error) { return 0, w.err }

func TestCopySparseErrors(t *testing.T) {
	src := sparseSource(t, 4<<20)

	wantErr := errors.New("disk full")
	if err := CopySparse(failWriter{wantErr}, 0, src, 4<<20); !errors.Is(err, wantErr) {
		t.Errorf("write error: got %v, want %v", err, wantErr)
	}

	// The source is shorter than asked for.
	if err := CopySparse(failWriter{}, 0, src, 5<<20); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: got %v, want ErrUnexpectedEOF", err)
	}

	if err := CopySparse(failWriter{}, -1, src, 10); err == nil {
		t.Errorf("negative offset: expected an error")
	}

	// A hole query failing for a reason other than lack of support.
	orig := nextData
	t.Cleanup(func() { nextData = orig })
	boom := errors.New("boom")
	nextData = func(*os.File, int64) (int64, int64, error) { return 0, 0, boom }
	if err := CopySparse(failWriter{}, 0, src, 4<<20); !errors.Is(err, boom) {
		t.Errorf("seek error: got %v, want %v", err, boom)
	}
	nextData = orig

	// A read error: the file is closed.
	closed := sparseSource(t, 1<<20)
	nextData = func(*os.File, int64) (int64, int64, error) { return 0, 0, errNoHoleInfo }
	_ = closed.Close()
	if err := CopySparse(failWriter{}, 0, closed, 1<<20); err == nil {
		t.Errorf("closed source: expected an error")
	}
}

func TestCopySparseAllHoleSource(t *testing.T) {
	const size = 6<<20 + 99
	f, err := os.Create(filepath.Join(t.TempDir(), "src"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	dst := copyAndCompare(t, f, size, 1<<20)
	assertSparse(t, dst, size)
}

func TestCopySparseZeroSize(t *testing.T) {
	copyAndCompare(t, sparseSource(t, 1<<20), 0, 512)
}

// A hole query that reports an empty extent at pos must not stall the
// copy; it falls back to reading the rest of the range.
func TestCopySparseEmptyExtentFallsBack(t *testing.T) {
	orig := nextData
	t.Cleanup(func() { nextData = orig })
	calls := 0
	nextData = func(_ *os.File, pos int64) (int64, int64, error) {
		calls++
		if calls > 100 {
			t.Fatal("CopySparse is not advancing")
		}
		return pos, pos, nil
	}
	const size = 8<<20 + 1234
	copyAndCompare(t, sparseSource(t, size), size, 1<<20)
}
