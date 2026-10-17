package sem

import (
	"os"
	"path/filepath"
)

// inSlashDir reports whether filepath.ToSlash(filepath.Dir(path)) == slashDir
// without allocating for the paths the index actually stores.
//
// Same-name resolution compares every candidate's directory with the
// referrer's, once per lookup. On Windows, filepath.Dir cleans its result and
// Clean rewrites every '/' to '\\' (a fresh buffer plus a fresh string), and
// ToSlash then rewrites them back (a third allocation). For a repository path
// such as "gen/pkg0007/file.pb.go" that is 48 bytes per candidate per lookup,
// so a name declared N times cost N allocations on every reference: the
// quadratic the same-name lookups were rewritten to remove came back on
// Windows only. On Unix both calls return substrings and nothing allocates,
// which is why the cost never showed there.
func inSlashDir(path, slashDir string) bool {
	return inSlashDirWith(path, slashDir, os.IsPathSeparator)
}

// inSlashDirWith is inSlashDir with the platform's separator rule injected,
// so both the Windows and the Unix behaviour are testable on any host.
//
// The allocation-free comparison covers relative paths whose directory part
// is already clean: no volume or drive (':'), no leading separator, and no
// empty, "." or ".." element. For those, filepath.Dir only trims the final
// element and swaps separators, so comparing the untouched prefix with
// separators read as '/' gives the same answer. Anything else takes the
// original filepath route, keeping its exact semantics.
func inSlashDirWith(path, slashDir string, isSep func(byte) bool) bool {
	if dir, ok := cleanRelativeDir(path, isSep); ok {
		if len(dir) != len(slashDir) {
			return false
		}
		for i := 0; i < len(dir); i++ {
			if isSep(dir[i]) {
				if slashDir[i] != '/' {
					return false
				}
			} else if dir[i] != slashDir[i] {
				return false
			}
		}
		return true
	}
	return filepath.ToSlash(filepath.Dir(path)) == slashDir
}

// cleanRelativeDir returns path's directory part as a substring of path, or
// "." when path has no separator, reporting ok only when that substring is
// already what filepath.Clean would produce apart from separator spelling.
func cleanRelativeDir(path string, isSep func(byte) bool) (string, bool) {
	if path == "" || isSep(path[0]) {
		return "", false
	}
	last := -1
	for i := 0; i < len(path); i++ {
		switch {
		case path[i] == ':':
			return "", false
		case isSep(path[i]):
			last = i
		}
	}
	if last < 0 {
		return ".", true
	}
	dir := path[:last]
	start := 0
	for i := 0; i <= len(dir); i++ {
		if i < len(dir) && !isSep(dir[i]) {
			continue
		}
		switch dir[start:i] {
		case "", ".", "..":
			return "", false
		}
		start = i + 1
	}
	return dir, true
}
