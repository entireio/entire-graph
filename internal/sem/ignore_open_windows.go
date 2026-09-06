//go:build windows

package sem

import (
	"os"
	"syscall"
)

func openBoundedRegularFile(file string) (*os.File, error) {
	return os.Open(file)
}

func openRootBoundedRegularFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}

// Windows reports reserved DOS devices such as NUL as regular through parts of
// the os.Stat surface. Check the opened handle too, so only disk files reach the
// bounded read.
func openedFileIsRegular(file *os.File, info os.FileInfo) (bool, error) {
	if !info.Mode().IsRegular() {
		return false, nil
	}
	fileType, err := syscall.GetFileType(syscall.Handle(file.Fd()))
	if err != nil {
		return false, err
	}
	return fileType == syscall.FILE_TYPE_DISK, nil
}
