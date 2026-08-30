//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// openFileIdentity reports how many directory entries name an OPEN file's
// inode, and the (device, inode) pair that identifies it independently of any
// name.
//
// The link count is the only way to see a HARD link. Every other guard in this
// file reasons about a path — its components, what it resolves to, whether a
// component is a symlink — and a hard link has no path to reason about: `ln
// .git/config CLAUDE.md` makes CLAUDE.md a second name for the same inode, so
// it resolves to "CLAUDE.md", carries no `.git` component, and Lstat reports an
// ordinary regular file. The name is innocent; the inode is not.
//
// It takes the open FILE rather than a FileInfo because Windows can only answer
// either question from a handle, and a guard that no-ops on one platform is not
// a guard.
func openFileIdentity(file *os.File) (links, device, inode uint64, ok bool) {
	info, err := file.Stat()
	if err != nil {
		return 0, 0, 0, false
	}
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, 0, false
	}
	return uint64(stat.Nlink), uint64(stat.Dev), uint64(stat.Ino), true
}
