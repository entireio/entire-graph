//go:build windows

package sem

import (
	"os"
	"syscall"
)

// openedFileLinkCount is the number of names the open file has. Go's Windows FileInfo carries no
// link count, so it comes from GetFileInformationByHandle, as agentsetup's hard-link guard does.
func openedFileLinkCount(file *os.File, _ os.FileInfo) (uint64, bool) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return 0, false
	}
	return uint64(info.NumberOfLinks), true
}
