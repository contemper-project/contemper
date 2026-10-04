//go:build !linux && !darwin

package disk

import "os"

// nextDataExtent implements nextData where holes can't be queried.
func nextDataExtent(*os.File, int64) (start, end int64, err error) {
	return 0, 0, errNoHoleInfo
}
