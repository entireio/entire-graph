//go:build windows

package cli

import "io/fs"

// fileLinkCount cannot answer on Windows: the FileInfo Go exposes there carries
// no link count, and obtaining one needs a handle query this package does not
// make. Reporting "unknown" rather than 1 keeps the caller from treating an
// unanswerable question as a clean answer — the guard is skipped, not faked.
func fileLinkCount(fs.FileInfo) (uint64, bool) { return 0, false }

// fileIdentity is likewise unanswerable from the FileInfo Go exposes on Windows.
func fileIdentity(fs.FileInfo) (device, inode uint64, ok bool) { return 0, 0, false }
