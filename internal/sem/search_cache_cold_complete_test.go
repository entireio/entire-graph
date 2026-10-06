package sem

import (
	"reflect"
	"testing"
)

// TestColdSelectiveQueryPersistsCompleteSnapshot pins that a cold committed-tree
// query leaves a COMPLETE snapshot behind, not only its own selective view, so
// a later query asking something else is served from the cache instead of
// rebuilding. The cold query's own answer must not change for it: it is still
// computed over its own selection and ranks exactly like an uncached search.
func TestColdSelectiveQueryPersistsCompleteSnapshot(t *testing.T) {
	repo := selectiveFilterFixture(t)
	cacheDir := t.TempDir()
	options := SearchOptions{Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 2, CacheDir: cacheDir}

	cold, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", options)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Stats.IndexCacheHit || cold.Stats.FilesIndexed == 0 || cold.Stats.FilesIndexed >= 4 {
		t.Fatalf("premise lost: first query must be a cold, selective build: %#v", cold.Stats)
	}
	uncachedOptions := options
	uncachedOptions.CacheDir, uncachedOptions.DisableCache = "", true
	uncached, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", uncachedOptions)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cold.Results, uncached.Results) || len(cold.Results) == 0 {
		t.Fatalf("building the complete snapshot changed the cold query's answer:\ncold=%#v\nuncached=%#v", cold.Results, uncached.Results)
	}

	complete, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("a cold selective query did not persist the complete snapshot")
	}
	if len(complete.Files) != 4 {
		t.Fatalf("persisted complete snapshot has %d files, want all 4", len(complete.Files))
	}

	other, err := SearchRepository(t.Context(), repo, "test-version", "Audit checks", options)
	if err != nil {
		t.Fatal(err)
	}
	if !other.Stats.IndexCacheHit {
		t.Fatalf("a different query after a cold one rebuilt instead of reusing the complete snapshot: %#v", other.Stats)
	}
}

// TestColdSelectiveQueryLeavesLargeRepositoriesToIndex pins the bound: above
// coldCompleteSnapshotMaxFiles a cold query builds only its selection, as
// before, and leaves the complete build to `index`.
func TestColdSelectiveQueryLeavesLargeRepositoriesToIndex(t *testing.T) {
	previous := coldCompleteSnapshotMaxFiles
	coldCompleteSnapshotMaxFiles = 3
	t.Cleanup(func() { coldCompleteSnapshotMaxFiles = previous })

	repo := selectiveFilterFixture(t)
	cacheDir := t.TempDir()
	cold, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", SearchOptions{
		Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 2, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cold.Stats.IndexCacheHit || len(cold.Results) == 0 {
		t.Fatalf("premise lost: %#v", cold.Stats)
	}
	if _, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil || hit {
		t.Fatalf("a repository over the bound got a complete snapshot from a cold query: hit=%v err=%v", hit, err)
	}
}
