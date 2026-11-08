package sem

import (
	"path/filepath"
	"testing"
)

func TestProviderRecordsCachePinsHistoryAcrossConcurrentDeepen(t *testing.T) {
	source := t.TempDir()
	sharedCacheFixture(t, source)
	git(t, source, "remote", "add", "origin", "https://github.com/example/history-cache.git")
	clone := filepath.Join(t.TempDir(), "shallow")
	git(t, source, "clone", "-q", "--depth=1", "--no-local", source, clone)
	git(t, clone, "remote", "set-url", "origin", "https://github.com/example/history-cache.git")
	commit, tree, err := resolveCommittedHEAD(t.Context(), clone)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	before, err := BeginProviderRecordsCache(t.Context(), clone, "history-test", commit, tree, "snapshot", cache, options)
	if err != nil {
		t.Fatal(err)
	}
	git(t, clone, "fetch", "-q", "--unshallow", source)
	pinned, err := BuildProviderSnapshotWithOptions(t.Context(), clone, "history-test", before.Options())
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := BuildProviderSnapshotWithOptions(t.Context(), clone, "history-test", options)
	if err != nil {
		t.Fatal(err)
	}
	count := func(snapshot ProviderSnapshot) int {
		var count int
		for _, relation := range snapshot.Relations {
			if relation.Type == "FILE_CHANGES_WITH" {
				count++
			}
		}
		return count
	}
	if count(pinned) != 0 || count(fresh) == 0 {
		t.Fatalf("transaction adopted a later history view: pinned=%d fresh=%d", count(pinned), count(fresh))
	}
	after, err := BeginProviderRecordsCache(t.Context(), clone, "history-test", commit, tree, "snapshot", cache, options)
	if err != nil {
		t.Fatal(err)
	}
	if before.entry.relative == after.entry.relative {
		t.Fatal("distinct available history views share a record-cache address")
	}
}
