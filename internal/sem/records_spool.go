package sem

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	hashpkg "hash"
	"io"
	"os"
	"path/filepath"
)

// ProviderRecordsSpool receives a provider record stream as it is produced and
// gzips it into a temporary file beside the transaction's cache entry. It is
// the write half of the sidecar layout: the producer tees records here with
// constant memory whatever the stream's size, and Store renames the finished
// file into place. The in-memory tee this replaces capped its capture at 64MB
// and silently stored nothing past the cap, so every repository whose snapshot
// outgrew it re-parsed on each run.
//
// A spool is single-use: Store consumes it, and Abandon (idempotent, nil-safe)
// discards it. The temporary lives in the entry's own directory so the final
// rename stays on one filesystem, and it is created through the same confined
// directory walk entry writes use, so a redirecting component is refused here
// too.
type ProviderRecordsSpool struct {
	directory     *os.Root
	file          *os.File
	temporaryName string
	buffered      *bufio.Writer
	compressor    *gzip.Writer
	digest        hashpkg.Hash
	closed        bool
	// err is the first spool failure. Write remembers it instead of returning
	// it (see Write), and finish surfaces it so Store abandons the entry.
	err error
}

func newProviderRecordsSpool(entry cacheEntry) (*ProviderRecordsSpool, error) {
	directory, err := openCacheEntryDirectory(entry)
	if err != nil {
		return nil, err
	}
	temporary, temporaryName, err := createRootTemp(directory, "stream")
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = directory.Remove(temporaryName)
		_ = directory.Close()
		return nil, err
	}
	// Same stack as cacheEntry.writeValue: the digest covers the stored (gzip)
	// bytes, and the buffer keeps the compressor's small flushes from each
	// becoming a write system call.
	digest := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(temporary, digest), 1<<16)
	return &ProviderRecordsSpool{
		directory:     directory,
		file:          temporary,
		temporaryName: temporaryName,
		buffered:      buffered,
		compressor:    gzip.NewWriter(buffered),
		digest:        digest,
	}, nil
}

// Write appends produced record bytes to the spool. It ALWAYS reports
// success: this writer sits inside the io.MultiWriter that also feeds stdout,
// and a short write there would abort the stream the command exists to
// produce — on exactly the large repositories the cache matters most for. A
// failure (a full or flaky cache disk, a write after finish) is remembered
// and resurfaces from Store, which abandons the entry instead of installing a
// partial one. The cache is best effort; the stream is not. This is the same
// contract the in-memory capture this spool replaced had.
func (spool *ProviderRecordsSpool) Write(p []byte) (int, error) {
	if spool.err == nil && spool.closed {
		spool.err = fmt.Errorf("provider records spool written after finish")
	}
	if spool.err == nil {
		if _, err := spool.compressor.Write(p); err != nil {
			spool.err = err
		}
	}
	return len(p), nil
}

// finish flushes and closes the temporary file and returns the digest of its
// stored bytes, or the first failure the spool deferred. The file is closed
// exactly once, before any discard, so a failed finish cannot leak the handle
// — on Windows removing a still-open file fails, which would leave `.stream-*`
// leftovers in the cache directory. The spool keeps its directory handle for
// install.
func (spool *ProviderRecordsSpool) finish() (cacheDigest, error) {
	if spool.closed {
		return cacheDigest{}, fmt.Errorf("provider records spool is closed")
	}
	spool.closed = true
	err := spool.err
	if closeErr := spool.compressor.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if flushErr := spool.buffered.Flush(); err == nil && flushErr != nil {
		err = flushErr
	}
	if closeErr := spool.file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	spool.file = nil
	if err != nil {
		spool.discard()
		return cacheDigest{}, err
	}
	var digest cacheDigest
	copy(digest[:], spool.digest.Sum(nil))
	return digest, nil
}

// install renames the finished temporary onto the sidecar's final name. The
// rename-not-write rationale is cacheEntry.writeValue's: renameat replaces a
// symlink sitting at the artifact name instead of following it.
func (spool *ProviderRecordsSpool) install(entry cacheEntry) error {
	if spool.directory == nil {
		return fmt.Errorf("provider records spool already discarded")
	}
	if err := spool.directory.Rename(spool.temporaryName, filepath.Base(entry.relative)); err != nil {
		spool.discard()
		return err
	}
	spool.temporaryName = ""
	spool.discard()
	return nil
}

// Abandon discards the spool and its temporary file. Safe on nil and after
// Store, so producers can defer it unconditionally.
func (spool *ProviderRecordsSpool) Abandon() {
	if spool == nil {
		return
	}
	if !spool.closed {
		spool.closed = true
		_ = spool.compressor.Close()
	}
	if spool.file != nil {
		_ = spool.file.Close()
		spool.file = nil
	}
	spool.discard()
}

func (spool *ProviderRecordsSpool) discard() {
	if spool.directory == nil {
		return
	}
	if spool.temporaryName != "" {
		_ = spool.directory.Remove(spool.temporaryName)
		spool.temporaryName = ""
	}
	_ = spool.directory.Close()
	spool.directory = nil
}

// readProviderRecordsStream returns the sidecar's decompressed bytes after
// verifying the stored bytes against the digest the envelope recorded. A
// mismatched or unreadable sidecar is the caller's cache miss: the envelope
// alone no longer proves which stream the entry holds.
func readProviderRecordsStream(entry cacheEntry, wantDigest string) ([]byte, error) {
	digest, err := entry.digest()
	if err != nil {
		return nil, err
	}
	if digest.String() != wantDigest {
		return nil, fmt.Errorf("provider records stream digest mismatch")
	}
	file, err := entry.open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var records bytes.Buffer
	if _, err := io.Copy(&records, reader); err != nil {
		return nil, err
	}
	return records.Bytes(), nil
}
