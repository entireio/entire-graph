//go:build windows

package cli

import (
	"os"
	"syscall"
)

// windowsLinkRequiresDirectory returns the type encoded into a Windows symbolic link or
// name-surrogate reparse point. Windows preserves this bit independently of the current target;
// a directory link cannot be followed after its target is replaced by a regular file, and vice
// versa.
func windowsLinkRequiresDirectory(info os.FileInfo) (bool, bool) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false, false
	}
	return data.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0, true
}
