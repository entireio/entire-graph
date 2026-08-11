package sem

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// cacheEntry is an internally named artifact beneath a caller-selected cache
// directory. Cache families and versions are constants, and key is always the
// lowercase SHA-256 digest produced by the corresponding cache-key function.
// Keeping that invariant here makes the relative path non-injectable.
//
// Reads additionally go through os.Root. The cache directory itself is a
// caller-owned location and may be a symlink, but no descendant symlink can
// make an artifact read escape the directory OpenRoot actually opened.
type cacheEntry struct {
	root     string
	relative string
}

func newCacheEntry(cacheDir, family, version, key string) (cacheEntry, error) {
	if cacheDir == "" {
		return cacheEntry{}, fmt.Errorf("cache directory is empty")
	}
	if !validCachePathComponent(family) || !validCachePathComponent(version) {
		return cacheEntry{}, fmt.Errorf("invalid cache family or version")
	}
	if !validSHA256Hex(key) {
		return cacheEntry{}, fmt.Errorf("invalid cache key: want %d lowercase hexadecimal characters", sha256.Size*2)
	}
	return cacheEntry{
		root:     cacheDir,
		relative: filepath.Join(family, version, key+".json.gz"),
	}, nil
}

func validCachePathComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		filepath.Base(value) == value && filepath.VolumeName(value) == ""
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

// writePath is intentionally separate from open: writes retain the existing
// atomic temp-file-and-rename sequence beneath the caller-owned cache root,
// while reads use os.Root as the file-inclusion boundary.
func (entry cacheEntry) writePath() string {
	return filepath.Join(entry.root, entry.relative)
}

func (entry cacheEntry) open() (*os.File, error) {
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(entry.relative)
}
