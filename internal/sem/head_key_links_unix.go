//go:build !windows

package sem

import (
	"os"
	"syscall"
)

// openedFileLinkCount is the number of names the open file has, from the FileInfo the caller already
// took of it.
func openedFileLinkCount(_ *os.File, info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true //nolint:unconvert // Nlink is narrower than uint64 on some platforms
}
