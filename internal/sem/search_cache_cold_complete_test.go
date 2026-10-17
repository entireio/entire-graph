package sem

import (
	"path/filepath"
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
	// The DEFAULT pool, shrunk to fit the fixture: an explicit MaxIndexedFiles is
	// a bound on cold parsing and never builds the complete snapshot.
	options := SearchOptions{Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: 2, CacheDir: cacheDir}

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
		Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: 2, CacheDir: cacheDir,
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

// TestColdSelectiveQueryHonoursExplicitIndexedFileBound pins that an explicit
// MaxIndexedFiles (--max-indexed-files, "bound cold-search parsing to N files")
// keeps a cold cached query to its selection. Building the complete snapshot
// instead parses every file the caller asked not to: measured on an 838-file
// repository at a bound of 5, 21.0s and a 2.8MB entry against 0.3s uncached.
func TestColdSelectiveQueryHonoursExplicitIndexedFileBound(t *testing.T) {
	repo := selectiveFilterFixture(t)
	cacheDir := t.TempDir()
	builds := 0
	previous := beforeColdCompleteBuild
	beforeColdCompleteBuild = func() { builds++ }
	t.Cleanup(func() { beforeColdCompleteBuild = previous })

	cold, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", SearchOptions{
		Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 2, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cold.Stats.IndexCacheHit || len(cold.Results) == 0 || cold.Stats.FilesIndexed == 0 || cold.Stats.FilesIndexed > 2 {
		t.Fatalf("premise lost: want a cold query indexing at most the 2 files it was bounded to: %#v", cold.Stats)
	}
	if builds != 0 {
		t.Fatalf("an explicitly bounded cold query built the complete snapshot %d time(s)", builds)
	}
	if _, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil || hit {
		t.Fatalf("an explicitly bounded cold query persisted the complete snapshot: hit=%v err=%v", hit, err)
	}
	// The bounded query is still cached as its own selection.
	warm, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", SearchOptions{
		Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 2, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !warm.Stats.IndexCacheHit || !reflect.DeepEqual(warm.Results, cold.Results) {
		t.Fatalf("the repeated bounded query was not served its own cached selection: %#v", warm.Stats)
	}
}

// TestColdCompleteAffordabilityReusesTheSearchListing pins that a count the
// search already has answers the bound without listing the tree again: on a
// large repository that second listing was most of a cold miss's cost. A
// directory that cannot be listed at all proves no listing happened.
func TestColdCompleteAffordabilityReusesTheSearchListing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-repository")
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	if completeSnapshotAffordableOnColdQuery(t.Context(), missing, options) {
		t.Fatal("premise lost: an unlistable directory must be unaffordable when it has to be listed")
	}
	options.coldComplete.sourceFiles = coldCompleteSnapshotMaxFiles
	if !completeSnapshotAffordableOnColdQuery(t.Context(), missing, options) {
		t.Fatal("a known count at the bound was not used")
	}
	options.coldComplete.sourceFiles = coldCompleteSnapshotMaxFiles + 1
	if completeSnapshotAffordableOnColdQuery(t.Context(), missing, options) {
		t.Fatal("a known count over the bound was reported affordable")
	}
}

func TestSelectiveDerivationReportsCompleteHeaderOnce(t *testing.T) {
	repo := selectiveFilterFixture(t)
	cacheDir := t.TempDir()
	calls := 0
	options := ProviderSnapshotOptions{Profile: ProfileFull, OnlyFiles: []string{"alpha/run.go"}}
	options.onCompleteDerivation = func(header SnapshotHeader) {
		calls++
		if header.Stats.Files != 4 {
			t.Fatalf("callback must report complete scope: %#v", header.Stats)
		}
	}
	for i := 0; i < 2; i++ {
		snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", options, cacheDir, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Files) != 1 {
			t.Fatalf("selected files = %d", len(snapshot.Files))
		}
		if calls != 1 {
			t.Fatalf("callback count after request %d = %d, want 1 (direct hits must not derive again)", i+1, calls)
		}
	}
}
