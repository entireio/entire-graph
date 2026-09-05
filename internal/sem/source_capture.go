package sem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/entireio/entire-graph/internal/filedigest"
)

// captureOpenedSource gives classification, declarations and resolver inputs one
// observation. An unavailable read is retained too: retrying could mix revisions.
func captureOpenedSource(ctx context.Context, opened openedSource, counters ...*extractionCache) openedSource {
	originalRead, originalOver, originalClose := opened.read, opened.oversize, opened.close
	var mu sync.Mutex
	overs := map[string]oversizeFile{}
	store := newCapturedStore(ctx, func(path string) (string, bool) {
		content, ok := originalRead(path)
		if ok && len(counters) > 0 && counters[0] != nil {
			counters[0].sourceBytes.Add(int64(len(content)))
		}
		if !ok && originalOver != nil {
			if over, found := originalOver(path); found {
				mu.Lock()
				overs[path] = over
				if len(counters) > 0 && counters[0] != nil {
					counters[0].sourceBytes.Add(over.Bytes)
				}
				mu.Unlock()
			}
		}
		return content, ok
	}, -1)
	opened.capture = store
	opened.read = func(path string) (string, bool) {
		source, ok, err := store.acquire(path)
		return source.content, ok && err == nil
	}
	opened.readPrefix = func(path string, limit int) (string, bool) {
		content, ok := opened.read(path)
		if !ok {
			mu.Lock()
			over, found := overs[path]
			mu.Unlock()
			content, ok = over.Prefix, found && over.Prefix != ""
		}
		if limit >= 0 && len(content) > limit {
			content = content[:limit]
		}
		return content, ok
	}
	opened.oversize = func(path string) (oversizeFile, bool) {
		mu.Lock()
		defer mu.Unlock()
		over, ok := overs[path]
		return over, ok
	}
	opened.close = func() error {
		err := store.close()
		if originalClose != nil {
			err = errors.Join(err, originalClose())
		}
		return err
	}
	return opened
}

// readCapturedFile retains at most max(limit+1, shebangSniffLimit) bytes. An oversized
// observation is digested on the same descriptor, retaining only its prefix.
func readCapturedFile(file io.Reader, limit int64) (string, *oversizeFile, error) {
	var reader io.Reader = file
	if limit > 0 {
		reader = io.LimitReader(file, max(limit+1, int64(shebangSniffLimit)))
	}
	contentBytes, err := io.ReadAll(reader)
	if err != nil {
		return "", nil, err
	}
	if limit <= 0 || int64(len(contentBytes)) <= limit {
		return string(contentBytes), nil, nil
	}
	prefix := string(contentBytes[:min(len(contentBytes), shebangSniffLimit)])
	digest, err := filedigest.Stream(io.MultiReader(bytes.NewReader(contentBytes), file))
	if err != nil {
		return "", nil, err
	}
	return "", &oversizeFile{Bytes: digest.Bytes, Hash: digest.Hash, Lines: digest.Lines, Prefix: prefix}, nil
}

// capturedWorktreeReader is used only by an operation which retains observations.
// The descriptor and leaf identity are checked before any source bytes are read.
func capturedWorktreeReader(ctx context.Context, root *os.Root, repo string, limit int64, registry *oversizeRegistry) contentReader {
	pinned := pinnedRootIdentity(root)
	return func(path string) (string, bool) {
		name := filepath.FromSlash(path)
		info, err := root.Lstat(name)
		var file *os.File
		if err == nil && info.Mode().IsRegular() {
			file, err = root.Open(name)
			if err == nil {
				actual, statErr := file.Stat()
				named, nameErr := root.Lstat(name)
				if statErr != nil || nameErr != nil || !actual.Mode().IsRegular() || !named.Mode().IsRegular() || !os.SameFile(info, actual) || !os.SameFile(named, actual) {
					_ = file.Close()
					return "", false
				}
			}
		} else if err == nil {
			return "", false
		}
		if err != nil {
			var ok bool
			file, _, ok = openContainedRegularFile(pinned, repo, path)
			if !ok {
				return "", false
			}
		}
		defer file.Close()
		content, over, err := readCapturedFile(captureContextReader{ctx, file}, limit)
		if err != nil {
			return "", false
		}
		if over != nil {
			registry.mu.Lock()
			registry.digests[path] = *over
			registry.mu.Unlock()
			return "", false
		}
		return content, true
	}
}

// Cancellation is checked between bounded streaming reads, including oversize
// digests, so a growing input cannot hold the operation alive indefinitely.
type captureContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader captureContextReader) Read(bytes []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(bytes)
}
