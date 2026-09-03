//go:build windows

package sem

import (
	"os"
	"syscall"
)

func openBoundedRegularFile(file string) (*os.File, error) {
	return os.Open(file)
}

// openRepoIgnoreFile has no O_NOFOLLOW to offer on Windows, so the containment
// rests entirely on the caller comparing the Lstat'd inode with a stat of THIS
// handle: a path raced to a reparse point or to another file after the check
// opens something os.SameFile refuses, and the read fails instead of returning
// the substituted object's bytes.
func openRepoIgnoreFile(file string) (*os.File, error) {
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
