package sem

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// cacheEntry is an internally named artifact beneath a caller-selected cache
// directory. Cache families and versions are constants, and key is always the
// lowercase SHA-256 digest produced by the corresponding cache-key function.
// Keeping that invariant here makes the relative path non-injectable.
//
// Reads additionally go through os.Root. The cache directory itself is the
// caller-selected trust boundary and may be a symlink. Writes require every
// family and version component below that root to be a non-redirecting
// directory, including when a link would remain inside the opened root.
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
// inside it, and with the cache directory at the checkout root, `.git` is inside it. Each
// component is therefore opened first and the held directory's identity is compared with an
// Lstat of its name. A link reports a different identity from its target, and a component swapped
// during the open reports a different identity from its replacement; either is refused before the
// held handle receives a write.
//
// Escaping descendant links never produced cache hits because entry.open already refused them.
// In-root descendant links could previously hit, so refusing them is an intentional compatibility
// break: a cache rooted at a checkout must not let a committed link steer derivative bytes into
// `.git` or another repository-chosen directory. Relocation remains supported by naming the
// backing directory as the cache root or making the root itself a symlink. Best-effort query paths
// treat a refusal as a cache miss; the explicit `index` persist path reports it.
//
// This boundary covers repository-controlled path entries and identity substitutions observable
// while a component is opened. Once admitted, a directory is used as a held filesystem capability:
// os.Root deliberately keeps referring to that object if another process with namespace-write
// permission moves it. Concurrent relocation by such a process is outside the cache threat model;
// portable os.Root cannot also pin the object's lexical ancestry, and that process already has the
// authority needed to move existing cache artifacts through the same namespace.
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

// writeEncoded installs already encoded JSON using the same gzip, atomic
// rename, and no-follow filesystem lifecycle as write. It is private to the
// extraction publisher, whose quota admission needs the final encoded size
// before publication. Generic cache consumers continue to use write.
func (entry cacheEntry) writeEncoded(temporaryPrefix string, encoded []byte) error {
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
	_, _, err = entry.writeEncodedHeld(directory, temporaryPrefix, encoded, 0)
	return err
}

// writeEncodedHeld publishes through a caller-held, already validated family
// directory. maxSize is an admission reservation; when positive, the encoded
// artifact is measured from the held temporary file and rejected before rename
// if the reservation was insufficient. The returned size and modification time
// describe the installed inode without reopening its pathname.
func (entry cacheEntry) writeEncodedHeld(directory *os.Root, temporaryPrefix string, encoded []byte, maxSize int64) (int64, int64, error) {
	return entry.writeEncodedHeldWithWriter(directory, temporaryPrefix, encoded, maxSize, nil)
}

// writeEncodedHeldWithWriter optionally reuses one caller-owned compressor.
// The caller serializes use. Resetting to io.Discard after every attempted
// compression releases the temporary file reference without discarding the
// compressor's reusable buffers.
func (entry cacheEntry) writeEncodedHeldWithWriter(directory *os.Root, temporaryPrefix string, encoded []byte, maxSize int64, reusable *gzip.Writer) (int64, int64, error) {
	name := filepath.Base(entry.relative)
	if !strings.HasSuffix(name, ".json.gz") || !validSHA256Hex(strings.TrimSuffix(name, ".json.gz")) {
		return 0, 0, os.ErrInvalid
	}
	temporary, temporaryName, err := createRootTemp(directory, temporaryPrefix)
	if err != nil {
		return 0, 0, err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = directory.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return 0, 0, err
	}
	writer := reusable
	if writer == nil {
		writer = gzip.NewWriter(temporary)
	} else {
		writer.Reset(temporary)
		defer writer.Reset(io.Discard)
	}
	if _, err := writer.Write(encoded); err != nil {
		_ = writer.Close()
		_ = temporary.Close()
		return 0, 0, err
	}
	if err := writer.Close(); err != nil {
		_ = temporary.Close()
		return 0, 0, err
	}
	info, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return 0, 0, err
	}
	if maxSize > 0 && info.Size() > maxSize {
		_ = temporary.Close()
		return 0, 0, fmt.Errorf("encoded cache entry is %d bytes, exceeds %d-byte admission reservation", info.Size(), maxSize)
	}
	if err := temporary.Close(); err != nil {
		return 0, 0, err
	}
	if err := directory.Rename(temporaryName, name); err != nil {
		return 0, 0, err
	}
	removeTemporary = false
	return info.Size(), info.ModTime().UnixNano(), nil
}

// openCacheDirectory creates and opens each component of the entry's directory beneath root,
// refusing a symlinked one. Descending through an opened handle rather than re-walking the path
// means the create and the rename that follow act on the directory object this loop actually
// checked. It does not promise lexical containment against a concurrent process that later moves
// that object; the write threat model above states that boundary explicitly.
func openCacheDirectory(root *os.Root, directory string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if directory == "." {
		return current, nil
	}
	for _, component := range strings.Split(filepath.ToSlash(directory), "/") {
		next, err := openCacheComponent(current, component, (*os.Root).OpenRoot)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func openCacheComponent(
	parent *os.Root,
	name string,
	open func(*os.Root, string) (*os.Root, error),
) (*os.Root, error) {
	if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	next, err := open(parent, name)
	if err != nil {
		// Preserve the actionable redirect diagnostic when os.Root refuses a
		// component that escapes the root before an identity can be held.
		if named, lstatErr := parent.Lstat(name); lstatErr == nil {
			if redirectErr := cacheComponentRedirectError(name, named); redirectErr != nil {
				return nil, redirectErr
			}
		}
		return nil, err
	}
	if err := refuseRedirectingCacheComponent(parent, name, next); err != nil {
		_ = next.Close()
		return nil, err
	}
	return next, nil
}

// refuseRedirectingCacheComponent verifies that held names the same ordinary
// directory the parent currently exposes at name. Opening first pins the object
// subsequent writes use; comparing that handle with Lstat closes the gap where a
// checked component could otherwise be replaced before OpenRoot followed it.
func refuseRedirectingCacheComponent(parent *os.Root, name string, held *os.Root) error {
	named, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if err := cacheComponentRedirectError(name, named); err != nil {
		return err
	}
	opened, err := held.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(named, opened) {
		return fmt.Errorf("cache directory component %q changed identity while it was opened", name)
	}
	return nil
}

// cacheComponentRedirectError refuses a cache directory component that can send a
// write somewhere other than the directory this loop just checked.
//
// The test is pathMayRedirect, not a bare ModeSymlink comparison, because the
// mode that means "this entry redirects" is platform-specific: Windows reports a
// symlink as ModeSymlink but a directory junction or mount point as
// ModeIrregular, so a ModeSymlink-only check descends into a junction planted at
// the family or version component and follows it opaquely. pathMayRedirect is the
// same predicate the provider already applies at its other redirect boundaries.
func cacheComponentRedirectError(name string, info os.FileInfo) error {
	if !pathMayRedirect(info) {
		return nil
	}
	return fmt.Errorf("cache directory component %q is a symlink or other redirecting entry (%s)", name, info.Mode().Type())
}

// cacheTempNameSuffix draws collision-avoidance words from an infallible
// process-local generator. A cryptographic source is deliberately unnecessary:
// O_EXCL is the security boundary, so predicting and occupying a candidate can
// only make this bounded cache write retry or fail; it cannot make
// the open follow or overwrite that entry. Avoiding crypto/rand also avoids its
// documented irrecoverable process termination when OS entropy fails.
func cacheTempNameSuffix(source func() uint64) string {
	return fmt.Sprintf("%016x%016x", source(), source())
}

// createRootTemp is os.CreateTemp confined to an opened directory. The name uses a process-local
// pseudo-random suffix opened with O_EXCL, and it keeps the visible `.<prefix>-*.json.gz` shape
// that operator cleanup globs already match.
func createRootTemp(directory *os.Root, prefix string) (*os.File, string, error) {
	return createRootTempWithSource(directory, prefix, rand.Uint64)
}

func createRootTempWithSource(
	directory *os.Root,
	prefix string,
	source func() uint64,
) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		suffix := cacheTempNameSuffix(source)
		name := "." + prefix + "-" + suffix + ".json.gz"
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
