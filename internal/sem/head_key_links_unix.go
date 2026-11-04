//go:build !windows

package sem

import (
	"os"
	"syscall"
)

// openedFileLinkCount is the number of names the open file has.
func openedFileLinkCount(file *os.File) (uint64, bool) {
	info, err := file.Stat()
	if err != nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true //nolint:unconvert // Nlink is narrower than uint64 on some platforms
}
