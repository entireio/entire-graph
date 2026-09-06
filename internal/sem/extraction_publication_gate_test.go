package sem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func extractionGateTestCache(t *testing.T, ctx context.Context) *extractionCache {
	t.Helper()
	return &extractionCache{
		ctx:         ctx,
		directory:   t.TempDir(),
		repository:  "publication-gate",
		build:       "test",
		maxBytes:    extractionDiskLimit,
		maxEntries:  extractionEntryLimit,
		limitsReady: true,
	}
}

func extractionGateTestEntry(t *testing.T, cache *extractionCache, index int) cacheEntry {
	t.Helper()
	entry, err := newCacheEntry(cache.directory, "extraction-publication-gate", "v1", fmt.Sprintf("%064x", index+1))
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func waitForDetachedExtractionBatch(t *testing.T, cache *extractionCache) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cache.pendingMu.Lock()
		pending := len(cache.pending)
		cache.pendingMu.Unlock()
		if pending == 0 && len(cache.publicationToken()) == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("publication did not detach the threshold batch")
		case <-ticker.C:
		}
	}
}

func waitExtractionGateCall(t *testing.T, done <-chan struct{}, detail string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(detail)
	}
}

func TestExtractionPublicationGatePrecedesDetachmentAndPreservesEligibleWrites(t *testing.T) {
	cache := extractionGateTestCache(t, nil)
	for index := 0; index < extractionPublishBatchEntries-1; index++ {
		cache.enqueue(extractionGateTestEntry(t, cache, index), 100, []byte("seed"))
	}

	cache.admissionMu.Lock()
	admissionHeld := true
	defer func() {
		if admissionHeld {
			cache.admissionMu.Unlock()
		}
	}()
	first := extractionGateTestEntry(t, cache, extractionPublishBatchEntries-1)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		cache.enqueue(first, 100, []byte("first threshold payload"))
	}()
	waitForDetachedExtractionBatch(t, cache)

	second := extractionGateTestEntry(t, cache, extractionPublishBatchEntries)
	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		close(secondStarted)
		cache.enqueue(second, 100, []byte("second eligible payload"))
	}()
	<-secondStarted
	select {
	case <-secondDone:
		t.Fatal("second producer passed publication gate while first batch was detached")
	case <-time.After(25 * time.Millisecond):
	}
	cache.pendingMu.Lock()
	if got := len(cache.pending); got != 0 {
		cache.pendingMu.Unlock()
		t.Fatalf("second producer appended behind detached batch: pending=%d", got)
	}
	cache.pendingMu.Unlock()

	cache.admissionMu.Unlock()
	admissionHeld = false
	waitExtractionGateCall(t, firstDone, "first producer did not finish")
	waitExtractionGateCall(t, secondDone, "second producer did not resume")
	cache.flush()
	for _, entry := range []cacheEntry{first, second} {
		if _, err := os.Stat(filepath.Join(entry.root, entry.relative)); err != nil {
			t.Fatalf("eligible publication %s was lost: %v", filepath.Base(entry.relative), err)
		}
	}
	if got := cache.inventoryCalls.Load(); got != 1 {
		t.Fatalf("inventory calls = %d, want one across detached and final batches", got)
	}
}

func TestExtractionPublicationGateCancelledWaiterDoesNotAppend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := extractionGateTestCache(t, ctx)
	gate := cache.publicationToken()
	<-gate
	gateHeld := true
	defer func() {
		if gateHeld {
			gate <- struct{}{}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.enqueue(extractionGateTestEntry(t, cache, 0), 100, []byte("cancelled"))
	}()
	cancel()
	waitExtractionGateCall(t, done, "cancelled producer remained blocked on publication gate")
	cache.pendingMu.Lock()
	if got := len(cache.pending); got != 0 {
		cache.pendingMu.Unlock()
		t.Fatalf("cancelled producer appended %d pending entries", got)
	}
	cache.pendingMu.Unlock()
	gate <- struct{}{}
	gateHeld = false

	// A ready token and cancellation may race in select; the post-acquire check
	// must return the token without changing pending state.
	cache.enqueue(extractionGateTestEntry(t, cache, 1), 100, []byte("also cancelled"))
	if got := len(gate); got != 1 {
		t.Fatalf("post-acquire cancellation retained publication token: available=%d", got)
	}
}

func TestExtractionPublicationGateCancelledFlushCleansAndAllowsReuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := extractionGateTestCache(t, ctx)
	first := extractionGateTestEntry(t, cache, 0)
	cache.publishBatch([]extractionPending{{entry: first, bound: 100, encoded: []byte("published")}})
	if cache.admission == nil {
		t.Fatal("fixture did not establish admission session")
	}
	pending := extractionGateTestEntry(t, cache, 1)
	cache.enqueue(pending, 100, []byte("pending"))
	gate := cache.publicationToken()
	<-gate
	gateHeld := true
	defer func() {
		if gateHeld {
			gate <- struct{}{}
		}
	}()
	cancel()
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		cache.flush()
	}()
	select {
	case <-flushDone:
		t.Fatal("cancelled flush skipped unconditional publication ownership")
	case <-time.After(25 * time.Millisecond):
	}
	if cache.admission == nil {
		t.Fatal("flush released admission without first acquiring publication ownership")
	}
	gate <- struct{}{}
	gateHeld = false
	waitExtractionGateCall(t, flushDone, "cancelled flush did not finish cleanup")
	cache.pendingMu.Lock()
	remaining := len(cache.pending)
	cache.pendingMu.Unlock()
	if remaining != 0 || cache.admission != nil {
		t.Fatalf("cancelled flush retained state: pending=%d admission=%v", remaining, cache.admission != nil)
	}
	lock, err := lockExtractionAdmission(first)
	if err != nil {
		t.Fatalf("cancelled flush retained admission lock: %v", err)
	}
	lock.Close()

	cache.ctx = context.Background()
	reused := extractionGateTestEntry(t, cache, 2)
	cache.enqueue(reused, 100, []byte("reuse"))
	cache.flush()
	if _, err := os.Stat(filepath.Join(reused.root, reused.relative)); err != nil {
		t.Fatalf("later cache reuse did not publish: %v", err)
	}
}

func TestExtractionPublicationGateReleasesAfterRefusalAndError(t *testing.T) {
	for _, fixture := range []string{"refusal", "write error"} {
		t.Run(fixture, func(t *testing.T) {
			cache := extractionGateTestCache(t, context.Background())
			bad := extractionGateTestEntry(t, cache, 0)
			if fixture == "refusal" {
				cache.maxBytes = 1024
			} else if err := os.MkdirAll(filepath.Join(bad.root, bad.relative), 0o700); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				cache.enqueue(bad, extractionPublishBatchBytes, []byte("rejected or failed"))
			}()
			waitExtractionGateCall(t, done, "refused or failed publisher retained publication gate")
			if got := len(cache.publicationToken()); got != 1 {
				t.Fatalf("publication token availability = %d, want 1 after %s", got, fixture)
			}
			if fixture == "write error" {
				if err := os.Remove(filepath.Join(bad.root, bad.relative)); err != nil {
					t.Fatal(err)
				}
			}
			good := extractionGateTestEntry(t, cache, 1)
			cache.enqueue(good, 100, []byte(strings.Repeat("g", 8)))
			cache.flush()
			if _, err := os.Stat(filepath.Join(good.root, good.relative)); err != nil {
				t.Fatalf("eligible publication after %s was lost: %v", fixture, err)
			}
		})
	}
}
