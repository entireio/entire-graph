package sem

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The spool is the cache's size-independence guarantee: records go to disk as
// they arrive, so there is no capture cap past which an entry is silently not
// stored. Pinned here: a spooled store round-trips byte-identically, an
// abandoned spool leaves nothing behind, and a disabled transaction hands the
// producer a nil spool rather than a temporary it would have to clean up.
func TestProviderRecordsSpoolRoundTripAndCleanup(t *testing.T) {
	repo := t.TempDir()
	cacheDir := t.TempDir()
	records := bytes.Repeat([]byte(`{"record_type":"symbol","path":"a.go"}`+"\n"), 1024)

	transaction, err := BeginProviderRecordsCache(
		t.Context(), repo, "test", "commit-a", "tree-a", "snapshot", cacheDir, ProviderSnapshotOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	abandoned, err := spoolRecords(transaction, records)
	if err != nil {
		t.Fatal(err)
	}
	abandoned.Abandon()
	abandoned.Abandon() // idempotent, and safe after the first
	if leftovers := spoolTemporaries(t, transaction); len(leftovers) != 0 {
		t.Fatalf("abandoned spool left %v", leftovers)
	}

	spool, err := spoolRecords(transaction, records)
	if err != nil {
		t.Fatal(err)
	}
	summary := &SnapshotSummary{RecordType: "summary", Languages: []string{"Go"}}
	if err := transaction.Store(spool, summary, SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: "test",
		RepoKey:         transaction.repositoryKey,
		Commit:          "commit-a",
		Tree:            "tree-a",
		Profile:         string(transaction.options.Profile),
	}); err != nil {
		t.Fatal(err)
	}
	if leftovers := spoolTemporaries(t, transaction); len(leftovers) != 0 {
		t.Fatalf("store left spool temporaries %v", leftovers)
	}
	replay, replaySummary, hit := transaction.Load()
	if !hit {
		t.Fatal("stored spool did not produce a cache hit")
	}
	if !bytes.Equal(replay, records) {
		t.Fatalf("replay differs from spooled records: %d bytes, want %d", len(replay), len(records))
	}
	if replaySummary == nil || len(replaySummary.Languages) != 1 || replaySummary.Languages[0] != "Go" {
		t.Fatalf("replay summary = %+v", replaySummary)
	}

	disabled, err := BeginProviderRecordsCache(
		t.Context(), repo, "test", "commit-a", "tree-a", "snapshot", "", ProviderSnapshotOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	nilSpool, err := disabled.NewSpool()
	if err != nil || nilSpool != nil {
		t.Fatalf("disabled transaction should hand out no spool, got %v, %v", nilSpool, err)
	}
	if err := disabled.Store(nilSpool, nil, SnapshotHeader{}); err != nil {
		t.Fatalf("disabled store must stay a no-op: %v", err)
	}
}

// A spool failure must never abort the record stream it tees from: Write keeps
// reporting success and the deferred error surfaces from Store, which refuses
// the entry and cleans up. This is the contract that keeps a full or flaky
// cache disk from failing a snapshot after partial output.
func TestProviderRecordsSpoolDefersWriteFailureToStore(t *testing.T) {
	repo := t.TempDir()
	transaction, err := BeginProviderRecordsCache(
		t.Context(), repo, "test", "commit-a", "tree-a", "snapshot", t.TempDir(), ProviderSnapshotOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	spool, err := transaction.NewSpool()
	if err != nil {
		t.Fatal(err)
	}
	spool.err = fmt.Errorf("disk full")
	if n, writeErr := spool.Write([]byte("records\n")); n != 8 || writeErr != nil {
		t.Fatalf("spool write must report success after a failure, got n=%d err=%v", n, writeErr)
	}
	if err := transaction.Store(spool, nil, SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: "test",
		RepoKey:         transaction.repositoryKey,
		Commit:          "commit-a",
		Tree:            "tree-a",
		Profile:         string(transaction.options.Profile),
	}); err == nil {
		t.Fatal("store must surface the deferred spool failure")
	}
	if _, _, hit := transaction.Load(); hit {
		t.Fatal("a failed spool must not produce a cache entry")
	}
	if leftovers := spoolTemporaries(t, transaction); len(leftovers) != 0 {
		t.Fatalf("failed store left spool temporaries %v", leftovers)
	}
}

func spoolTemporaries(t *testing.T, transaction *ProviderRecordsCacheTransaction) []string {
	t.Helper()
	pattern := filepath.Join(transaction.streamEntry.root, filepath.Dir(transaction.streamEntry.relative), ".stream-*")
	leftovers, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	// The directory may not exist before the first store; Glob answers empty.
	if _, statErr := os.Stat(filepath.Dir(pattern)); statErr != nil && len(leftovers) != 0 {
		t.Fatal(statErr)
	}
	return leftovers
}
