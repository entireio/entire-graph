package sem

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type extractionAdmissionLock struct {
	directory         *os.Root
	file              *os.File
	root              string
	relativeDirectory string
	closeOnce         sync.Once
	closeErr          error
}

func (lock *extractionAdmissionLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.closeOnce.Do(func() {
		lock.closeErr = errors.Join(lock.file.Close(), lock.directory.Close())
	})
	return lock.closeErr
}

func (lock *extractionAdmissionLock) holds(entry cacheEntry) bool {
	return lock != nil && entry.root == lock.root && filepath.Dir(entry.relative) == lock.relativeDirectory
}

// The stable lock inode is never removed: unlinking a held lock would let a
// second writer lock a replacement inode and bypass admission serialization.
func lockExtractionAdmission(entry cacheEntry) (*extractionAdmissionLock, error) {
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
	const name = ".admission.lock"
	file, err := dir.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		named, statErr := dir.Lstat(name)
		if statErr != nil || !named.Mode().IsRegular() {
			dir.Close()
			return nil, os.ErrInvalid
		}
		file, err = dir.OpenFile(name, os.O_RDWR, 0600)
	}
	if err != nil {
		dir.Close()
		return nil, err
	}
	actual, statErr := file.Stat()
	named, nameErr := dir.Lstat(name)
	if statErr != nil || nameErr != nil || !actual.Mode().IsRegular() || !named.Mode().IsRegular() || !os.SameFile(actual, named) {
		file.Close()
		dir.Close()
		return nil, os.ErrInvalid
	}
	if err := tryExtractionLock(file); err != nil {
		file.Close()
		dir.Close()
		return nil, err
	}
	return &extractionAdmissionLock{
		directory:         dir,
		file:              file,
		root:              entry.root,
		relativeDirectory: filepath.Dir(entry.relative),
	}, nil
}
