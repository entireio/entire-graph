//go:build windows

package cli

import (
	"os"
	"syscall"
)

// openFileIdentity is the Windows half of the hard-link guard. NTFS supports
// hard links (`mklink /H`, CreateHardLink), so the protection has to work here:
// a guard that no-ops on a platform whose filesystem has the feature is a guard
// in name only.
//
// The FileInfo Go exposes on Windows carries neither a link count nor a file
// identity, which is why an earlier version reported "unknown" and skipped the
// check. GetFileInformationByHandle answers both from the open handle:
// NumberOfLinks is the link count, and (VolumeSerialNumber, FileIndexHigh,
// FileIndexLow) is the volume-scoped identity — Windows' equivalent of
// (device, inode).
//
// FileIndex is stable while the file is open, which is exactly the window this
// is used in: the handle is already held, and the answer is only needed for the
// write about to happen on it.
func openFileIdentity(file *os.File) (links, device, inode uint64, ok bool) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return 0, 0, 0, false
	}
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return uint64(info.NumberOfLinks), uint64(info.VolumeSerialNumber), index, true
}
