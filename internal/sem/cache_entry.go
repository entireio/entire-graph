package sem

import (
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

func (entry cacheEntry) open() (*os.File, error) {
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(entry.relative)
}

// write installs the gzipped JSON encoding of value at the entry, atomically, without following a
// symlinked component below the cache root.
//
// The cache ROOT is caller-named, and creating and opening it resolves a symlink exactly as
// entry.open does — so the deployments that put the cache somewhere else by linking the cache
// directory itself (another volume, a shared cache, out of a container's writable layer) keep
// working. Everything BELOW the root is named by this program and nothing else: two constants and
// a SHA-256 digest. A symlink at one of those components was therefore planted by whatever owns
// the bytes in that directory, and that is the scanned repository whenever `--cache-dir` or
// `ENTIRE_PLUGIN_DATA_DIR` resolves inside a checkout. os.MkdirAll followed such a link and put
// the artifact wherever it pointed.
//
// os.Root alone does not close that, for the same reason it does not close it for `--report`
// (see internal/cli/outputpath.go): it stops a link LEAVING the root but follows one that stays
// inside it, and with the cache directory at the checkout root, `.git` is inside it. So each
// component is created and then checked with Lstat before it is descended into, and a symlink
// there is refused rather than followed. The check is not exact-case, so a case-insensitive
// filesystem cannot answer it with a differently-spelled link.
//
// The refusal costs nothing that works today: entry.open reads through os.Root, so an escaping
// link is one the reader could never have followed either (see
// TestCacheEntryReadRejectsSymlinkEscape), and an internal one only ever redirected the write to
// somewhere the tool does not own. Such an entry is written on every run and read on none. What
// was a silent permanent cache miss is now an error, which the best-effort call sites discard and
// the explicit `index` persist path reports.
func (entry cacheEntry) write(temporaryPrefix string, value any) error {
	if err := os.MkdirAll(entry.root, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return err
	}
	defer root.Close()

	directory, err := openCacheDirectory(root, filepath.Dir(entry.relative))
	if err != nil {
		return err
	}
	defer directory.Close()

	temporary, temporaryName, err := createRootTemp(directory, temporaryPrefix)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = directory.Remove(temporaryName)
		}
	}()
	// O_CREATE's mode is masked by the process umask; the artifact holds derivative repository
	// content, so set the mode explicitly rather than inheriting whatever the umask allowed.
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	writer := gzip.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		_ = writer.Close()
		_ = temporary.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Rename, not a write through the destination: renameat replaces a symlink sitting at the
	// artifact name instead of following it, so the one component an attacker can predict without
	// planting a directory cannot redirect the bytes either.
	if err := directory.Rename(temporaryName, filepath.Base(entry.relative)); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

// openCacheDirectory creates and opens each component of the entry's directory beneath root,
// refusing a symlinked one. Descending through an opened handle rather than re-walking the path
// means the create and the rename that follow act on the directory this loop actually checked.
func openCacheDirectory(root *os.Root, directory string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if directory == "." {
		return current, nil
	}
	for _, component := range strings.Split(filepath.ToSlash(directory), "/") {
		next, err := openCacheComponent(current, component)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func openCacheComponent(parent *os.Root, name string) (*os.Root, error) {
	if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("cache directory component %q is a symlink", name)
	}
	return parent.OpenRoot(name)
}

// createRootTemp is os.CreateTemp confined to an opened directory. The name is a cryptographically
// random suffix opened with O_EXCL, so there is no predictable name for anything to occupy first,
// and it keeps the visible `.<prefix>-*.json.gz` shape that operator cleanup globs already match.
func createRootTemp(directory *os.Root, prefix string) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		name := "." + prefix + "-" + rand.Text() + ".json.gz"
		file, err := directory.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("create temporary cache file in %s: no unused name", directory.Name())
}
