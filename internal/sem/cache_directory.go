package sem

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenCacheDirectory creates and opens internally named directories beneath the caller-selected
// cache root. The root itself may be a symlink; every component below it must be an ordinary,
// non-redirecting directory. Callers must close the returned handle and use it for subsequent I/O.
func OpenCacheDirectory(cacheDir string, components ...string) (*os.Root, error) {
	if cacheDir == "" {
		return nil, fmt.Errorf("cache directory is empty")
	}
	for _, component := range components {
		if !validCachePathComponent(component) {
			return nil, fmt.Errorf("invalid cache directory component %q", component)
		}
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory := "."
	if len(components) > 0 {
		directory = filepath.Join(components...)
	}
	return openCacheDirectory(root, directory)
}

// OpenCacheRegularFile opens a regular cache artifact without blocking on a FIFO. Its name is
// relative to a directory returned by OpenCacheDirectory; escaping links are refused by os.Root.
func OpenCacheRegularFile(directory *os.Root, name string) (*os.File, error) {
	file, err := openRootBoundedRegularFile(directory, name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		var regular bool
		regular, err = openedFileIsRegular(file, info)
		if err == nil && regular {
			return file, nil
		}
	}
	_ = file.Close()
	return nil, fmt.Errorf("cache artifact %q is not a readable regular file", name)
}

// CreateCacheTemp creates an exclusive temporary artifact relative to a held cache directory.
// The returned name is relative to directory, for Rename and Remove on that same handle.
func CreateCacheTemp(directory *os.Root, prefix string) (*os.File, string, error) {
	if !validCachePathComponent(prefix) {
		return nil, "", fmt.Errorf("invalid cache temporary prefix")
	}
	return createRootTemp(directory, prefix)
}
