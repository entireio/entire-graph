//go:build !windows && !plan9

package sem

import (
	"os"
	"syscall"
)

func openSearchVerifyRegularFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
