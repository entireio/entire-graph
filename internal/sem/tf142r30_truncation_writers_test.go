package sem

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// truncatedSearchSnapshot is a cache envelope whose snapshot carries the
// truncation marker.
func truncatedSearchSnapshot() cachedSearchSnapshot {
	var cache cachedSearchSnapshot
	cache.Tree = strings.Repeat("c", 40)
	cache.Snapshot.Header.PartialFailures = []PartialFailure{{
		Code:                 AnalysisBudgetExceededCode,
		Severity:             "warning",
		EffectOnCompleteness: "partial",
	}}
	return cache
}

func truncationTestEntry(t *testing.T) cacheEntry {
	t.Helper()
	entry, err := newCacheEntry(filepath.Join(t.TempDir(), "cache"), "search", "v1", strings.Repeat("d", 64))
	if err != nil {
		t.Fatalf("newCacheEntry: %v", err)
	}
	return entry
}

// TestEverySearchSnapshotWriterRefusesATruncatedSnapshot enumerates the
// snapshot writers so a new one cannot quietly reopen the hole.
//
// The search cache is keyed by tree and by the options that SHAPE the graph. A
// stored truncation is therefore served to every later query on the same tree
// as though it were the complete index, and its missing symbols read as
// confident negatives.
//
// The refusal originally lived inside writeSearchSnapshot alone, with a
// comment saying it sat in the writer so a new call site could not
// reintroduce the hole. Two new writers were then added that go straight to
// entry.writeDigest and bypassed it. This test is the guard that the comment
// was asserting: it drives all three, and a fourth writer that forgets
// refuseTruncatedSnapshot is a visible omission here rather than a silent one.
func TestEverySearchSnapshotWriterRefusesATruncatedSnapshot(t *testing.T) {
	t.Parallel()
	cache := truncatedSearchSnapshot()

	writers := []struct {
		name  string
		write func(cacheEntry) error
	}{
		{"writeSearchSnapshot", func(entry cacheEntry) error {
			return writeSearchSnapshot(entry, cache)
		}},
		{"writeSearchSnapshotAndStore", func(entry cacheEntry) error {
			return writeSearchSnapshotAndStore(context.Background(), entry, cache)
		}},
		{"persistVerifiedSearchSnapshot", func(entry cacheEntry) error {
			return persistVerifiedSearchSnapshot(context.Background(), entry, cache,
				func(cacheEntry) (cacheDigest, error) {
					t.Fatal("a truncated snapshot must be refused before anything is written")
					return cacheDigest{}, nil
				})
		}},
	}
	for _, writer := range writers {
		t.Run(writer.name, func(t *testing.T) {
			entry := truncationTestEntry(t)
			err := writer.write(entry)
			if !errors.Is(err, ErrTruncatedSnapshotNotCacheable) {
				t.Fatalf("%s stored a truncated snapshot (err = %v)", writer.name, err)
			}
			if _, err := readSearchSnapshot(entry); err == nil {
				t.Fatalf("%s left a readable entry behind", writer.name)
			}
		})
	}
}

// TestCompleteSearchSnapshotStillWrites keeps the refusal honest: a complete
// snapshot must still reach the cache through every writer, or the guard above
// would pass against a writer that simply never writes.
func TestCompleteSearchSnapshotStillWrites(t *testing.T) {
	t.Parallel()
	var cache cachedSearchSnapshot
	cache.Tree = strings.Repeat("e", 40)

	entry := truncationTestEntry(t)
	if err := writeSearchSnapshot(entry, cache); err != nil {
		t.Fatalf("writeSearchSnapshot refused a complete snapshot: %v", err)
	}
	if _, err := readSearchSnapshot(entry); err != nil {
		t.Fatalf("complete snapshot did not land: %v", err)
	}

	storeEntry := truncationTestEntry(t)
	if err := writeSearchSnapshotAndStore(context.Background(), storeEntry, cache); err != nil {
		t.Fatalf("writeSearchSnapshotAndStore refused a complete snapshot: %v", err)
	}
	if _, err := readSearchSnapshot(storeEntry); err != nil {
		t.Fatalf("complete snapshot did not land via the store writer: %v", err)
	}
}
