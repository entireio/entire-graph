package sem

import (
	"os"
	"path/filepath"
)

// The stable lock inode is never removed: unlinking a held lock would let a
// second writer lock a replacement inode and bypass admission serialization.
func lockExtractionAdmission(entry cacheEntry) (*os.File, error) {
	if err := os.MkdirAll(entry.root, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := openCacheDirectory(root, filepath.Dir(entry.relative))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	const name = ".admission.lock"
	file, err := dir.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		named, statErr := dir.Lstat(name)
		if statErr != nil || !named.Mode().IsRegular() {
			return nil, os.ErrInvalid
		}
		file, err = dir.OpenFile(name, os.O_RDWR, 0600)
	}
	if err != nil {
		return nil, err
	}
	actual, statErr := file.Stat()
	named, nameErr := dir.Lstat(name)
	if statErr != nil || nameErr != nil || !actual.Mode().IsRegular() || !named.Mode().IsRegular() || !os.SameFile(actual, named) {
		file.Close()
		return nil, os.ErrInvalid
	}
	if err := tryExtractionLock(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
