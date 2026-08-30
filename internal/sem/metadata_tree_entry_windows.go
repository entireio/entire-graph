//go:build windows

package sem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Windows directory enumeration returns reparse attributes from the held
// directory handle, so no secondary pathname lookup is needed to distinguish a
// regular child from a symlink, junction, or other reparse point.
func readGitMetadataDirectory(
	directory *os.File,
	resolved string,
	_ *sameVolumePathResolver,
	count int,
	admit func(string) bool,
) ([]gitMetadataDirectoryEntry, error) {
	dirEntries, readErr := directory.ReadDir(count)
	entries := make([]gitMetadataDirectoryEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if !admit(filepath.Join(resolved, entry.Name())) {
			return nil, errGitMetadataTreeBound
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		kind := gitMetadataEntrySpecial
		switch {
		case pathMayRedirect(info):
			kind = gitMetadataEntryRedirect
		case info.Mode().IsRegular():
			kind = gitMetadataEntryRegular
		case info.IsDir():
			kind = gitMetadataEntryDirectory
		}
		entries = append(entries, gitMetadataDirectoryEntry{name: entry.Name(), kind: kind})
	}
	return entries, readErr
}
