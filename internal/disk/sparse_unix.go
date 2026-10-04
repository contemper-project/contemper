//go:build linux || darwin

package disk

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// nextDataExtent implements nextData with SEEK_DATA and SEEK_HOLE.
func nextDataExtent(f *os.File, pos int64) (start, end int64, err error) {
	fd := int(f.Fd())
	start, err = unix.Seek(fd, pos, unix.SEEK_DATA)
	if err != nil {
		if errors.Is(err, unix.ENXIO) {
			// No data at or after pos: the rest of the file is a hole.
			return 0, 0, io.EOF
		}
		// Any other failure means this file can't be asked about holes:
		// EINVAL, ENOTSUP or ENOSYS on Linux, and on macOS an ioctl error
		// (ENOTTY, EOPNOTSUPP, ...) from file systems without hole support
		// such as HFS+, msdosfs, ExFAT or SMB. Reading the file instead
		// surfaces a real I/O error from ReadAt.
		return 0, 0, errNoHoleInfo
	}
	end, err = unix.Seek(fd, start, unix.SEEK_HOLE)
	if err != nil {
		if errors.Is(err, unix.ENXIO) {
			// Cannot normally happen (the end of a file is a hole), but
			// read to the end rather than fail.
			return start, 1<<63 - 1, nil
		}
		return 0, 0, errNoHoleInfo
	}
	return start, end, nil
}
