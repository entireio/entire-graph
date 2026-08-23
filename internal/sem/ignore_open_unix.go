//go:build !windows

package sem

import (
	"os"
	"syscall"
)

// openBoundedRegularFile uses a non-blocking open so a path raced from a
// regular file to a writerless FIFO cannot park before the descriptor-level
// type and identity checks run. POSIX ignores O_NONBLOCK for regular files.
func openBoundedRegularFile(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func openRootBoundedRegularFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func openedFileIsRegular(_ *os.File, info os.FileInfo) (bool, error) {
	return info.Mode().IsRegular(), nil
}
