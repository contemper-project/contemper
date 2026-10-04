package disk

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// sparseChunkBytes is the read size of CopySparse.
	sparseChunkBytes = 1 << 20
	// sparseBlockBytes is the granularity at which all-zero data is
	// skipped: the ext4 block size contemper formats with, which is also
	// the allocation unit of the filesystems a disk image usually lives on.
	sparseBlockBytes = 4096
)

// errNoHoleInfo means the platform or the file system can't report where
// a file's holes are; CopySparse then finds zeros by reading them.
var errNoHoleInfo = errors.New("hole information is not available")

// nextData returns the start of the first data extent at or after pos in
// f and the offset where that extent ends (the next hole, or the end of
// the file). It returns io.EOF when there is no data at or after pos, and
// errNoHoleInfo when holes can't be told apart from data. It is a
// variable so tests can force the fallback.
var nextData = nextDataExtent

var zeroBlock [sparseBlockBytes]byte

// CopySparse copies the first size bytes of src to dst, starting at
// offset dstOff, without writing any zeros: holes in src are skipped, and
// so is data that is all zeros (in sparseBlockBytes units). The skipped
// ranges of dst are left untouched, so dst must already read as zeros
// there (a freshly created and truncated file does) for the result to
// equal a plain copy. dst must be at least dstOff+size bytes long, or the
// skipped tail would leave it short. src must hold at least size bytes.
//
// Where the file system reports holes (SEEK_DATA and SEEK_HOLE, on Linux
// and macOS) the holes are not even read; elsewhere, or when the file
// system doesn't support the query, the whole range is read in 1 MiB
// chunks and the zero blocks dropped.
func CopySparse(dst io.WriterAt, dstOff int64, src *os.File, size int64) error {
	if size < 0 || dstOff < 0 {
		return fmt.Errorf("sparse copy: invalid offset %d or size %d", dstOff, size)
	}
	fi, err := src.Stat()
	if err != nil {
		return fmt.Errorf("sparse copy: %w", err)
	}
	if fi.Size() < size {
		return fmt.Errorf("sparse copy: %s has %d bytes, want %d: %w", src.Name(), fi.Size(), size, io.ErrUnexpectedEOF)
	}

	buf := make([]byte, sparseChunkBytes)
	pos := int64(0)
	for pos < size {
		start, end := pos, size
		if ds, de, err := nextData(src, pos); err == nil {
			if de <= pos {
				// A nonsensical extent (empty or behind pos) would stop the
				// loop from advancing. Don't trust the hole information any
				// further: read the rest of the range, zeros dropped, which
				// is always correct.
				start, end = pos, size
			} else {
				start, end = max(ds, pos), min(de, size)
				if start >= size {
					break
				}
			}
		} else if errors.Is(err, io.EOF) {
			break
		} else if !errors.Is(err, errNoHoleInfo) {
			return fmt.Errorf("sparse copy: finding data in %s: %w", src.Name(), err)
		}
		// Else: the whole rest of the file is read below, zeros included.
		if err := copyNonZero(dst, dstOff, src, start, end, buf); err != nil {
			return err
		}
		pos = end
	}
	return nil
}

// copyNonZero copies src[start:end] to dst at dstOff+start, leaving out
// the all-zero blocks.
func copyNonZero(dst io.WriterAt, dstOff int64, src io.ReaderAt, start, end int64, buf []byte) error {
	for off := start; off < end; {
		n := int(min(int64(len(buf)), end-off))
		// ReadAt fills buf[:n] or returns an error; io.EOF alongside a full
		// read just means the range ends at the end of the file.
		if rn, err := src.ReadAt(buf[:n], off); rn < n || (err != nil && !errors.Is(err, io.EOF)) {
			if err == nil || errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("sparse copy: reading at %d: %w", off, err)
		}
		// Write each run of blocks that aren't all zeros with one call.
		runStart := -1
		flush := func(upTo int) error {
			if runStart < 0 {
				return nil
			}
			at := dstOff + off + int64(runStart)
			if _, err := dst.WriteAt(buf[runStart:upTo], at); err != nil {
				return fmt.Errorf("sparse copy: writing at %d: %w", at, err)
			}
			runStart = -1
			return nil
		}
		for b := 0; b < n; b += sparseBlockBytes {
			e := min(b+sparseBlockBytes, n)
			if !bytes.Equal(buf[b:e], zeroBlock[:e-b]) {
				if runStart < 0 {
					runStart = b
				}
			} else if err := flush(b); err != nil {
				return err
			}
		}
		if err := flush(n); err != nil {
			return err
		}
		off += int64(n)
	}
	return nil
}
