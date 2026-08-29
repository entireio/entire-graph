//go:build !windows

package cli

import (
	"io/fs"
	"syscall"
)

// fileLinkCount reports how many directory entries point at an open file's inode,
// and whether the platform could answer at all.
//
// It is the only way to see a HARD link. Every other guard in this file reasons
// about a path — its components, what it resolves to, whether a component is a
// symlink — and a hard link has no path to reason about: `ln .git/config
// CLAUDE.md` makes CLAUDE.md a second name for the same inode, so it resolves to
// "CLAUDE.md", carries no `.git` component, and Lstat reports an ordinary
// regular file. The name is innocent; the inode is not.
func fileLinkCount(info fs.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
}

// fileIdentity returns the (device, inode) pair that identifies a file
// independently of any name it is reachable by. It is what lets two managed
// targets be recognised as ONE file when the repository hard-links them, which
// docs/agents.md documents as a supported way to share a single instruction file.
func fileIdentity(info fs.FileInfo) (device, inode uint64, ok bool) {
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return uint64(stat.Dev), uint64(stat.Ino), true
}
