package sem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
)

const defaultCaptureMemoryBytes int64 = 64 << 20

// capturedStore is operation-owned. It accepts only an already confined and
// bounded reader. It never makes a cache lookup an authorization to read a path.
// Integration with query lifetimes is deliberately separate from this primitive.
type capturedStore struct {
	ctx         context.Context
	cancel      context.CancelFunc
	read        contentReader
	limit       int64
	mu          sync.Mutex
	entries     map[string]*captureEntry
	memory      int64
	root        *os.Root
	directory   string
	nextBacking int
	closeDone   chan struct{}
	closed      bool
	active      sync.WaitGroup
	err         error
	failure     error
}

type captureEntry struct {
	policy  bool
	ready   chan struct{}
	source  capturedSource
	ok      bool
	backing string
	size    int64
	err     error
}

func newCapturedStore(ctx context.Context, read contentReader, memoryLimit int64) *capturedStore {
	ctx, cancel := context.WithCancel(ctx)
	if memoryLimit < 0 {
		memoryLimit = defaultCaptureMemoryBytes
	}
	return &capturedStore{ctx: ctx, cancel: cancel, read: read, limit: memoryLimit, entries: make(map[string]*captureEntry), closeDone: make(chan struct{})}
}

func (store *capturedStore) acquire(path string) (capturedSource, bool, error) {
	return store.acquireFrom(path, store.read)
}

func (store *capturedStore) acquireFrom(path string, read contentReader) (sourceResult capturedSource, available bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			store.mu.Lock()
			if store.failure == nil {
				store.failure = resultErr
			}
			store.mu.Unlock()
		}
	}()
	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		return capturedSource{}, false, os.ErrClosed
	}
	if err := store.ctx.Err(); err != nil {
		store.mu.Unlock()
		return capturedSource{}, false, err
	}
	entry, exists := store.entries[path]
	if !exists {
		entry = &captureEntry{ready: make(chan struct{})}
		store.entries[path] = entry
	}
	store.active.Add(1)
	store.mu.Unlock()
	defer store.active.Done()
	if !exists {
		store.capture(path, entry, read)
	}
	select {
	case <-store.ctx.Done():
		return capturedSource{}, false, store.ctx.Err()
	case <-entry.ready:
	}
	if entry.err != nil {
		return capturedSource{}, false, entry.err
	}
	if !entry.ok {
		return capturedSource{}, false, nil
	}
	source := entry.source
	if entry.backing != "" {
		// Close waits for active readers, so the held root cannot disappear here.
		file, err := store.root.Open(entry.backing)
		if err != nil {
			return capturedSource{}, false, err
		}
		bytes, err := io.ReadAll(io.LimitReader(file, entry.size+1))
		err = errors.Join(err, file.Close())
		if err != nil {
			return capturedSource{}, false, err
		}
		if int64(len(bytes)) != entry.size || contentHash(bytes) != source.digest {
			return capturedSource{}, false, errors.New("captured source backing integrity mismatch")
		}
		source.content = string(bytes)
	}
	return source, true, nil
}

func (store *capturedStore) capture(path string, entry *captureEntry, read contentReader) {
	defer func() {
		if entry.err != nil {
			entry.source.content = ""
		}
		close(entry.ready)
	}()
	content, ok := read(path)
	if err := store.ctx.Err(); err != nil {
		entry.err = err
		return
	}
	entry.ok = ok
	if !ok {
		return
	}
	entry.source = captureSource(path, content)
	entry.size = int64(len(content))
	store.mu.Lock()
	defer store.mu.Unlock()
	if entry.size <= store.limit-store.memory {
		store.memory += entry.size
		return
	}
	if store.root == nil {
		directory, err := os.MkdirTemp("", "entire-graph-capture-")
		if err != nil {
			entry.err = fmt.Errorf("create capture backing: %w", err)
			return
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			_ = os.Remove(directory)
			entry.err = err
			return
		}
		store.root, store.directory = root, directory
	}
	store.nextBacking++
	name := strconv.Itoa(store.nextBacking) + "-" + entry.source.digest
	file, err := store.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		entry.err = err
		return
	}
	_, writeErr := io.WriteString(file, content)
	entry.err = errors.Join(writeErr, file.Close())
	if entry.err != nil {
		_ = store.root.Remove(name)
		return
	}
	entry.backing = name
	entry.source.content = ""
}

func (store *capturedStore) close() error {
	store.mu.Lock()
	if store.closed {
		done := store.closeDone
		store.mu.Unlock()
		<-done
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.err
	}
	store.closed = true
	store.cancel()
	store.mu.Unlock()
	store.active.Wait()
	defer close(store.closeDone)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.root != nil {
		// Only remove names created by this store through its held directory.
		for _, entry := range store.entries {
			if entry.backing != "" {
				store.err = errors.Join(store.err, store.root.Remove(entry.backing))
			}
		}
		store.err = errors.Join(store.err, store.root.Close(), os.Remove(store.directory))
	}
	return store.err
}
