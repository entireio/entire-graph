package sem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cachePruneDir creates a directory under the cache root and, when mod is not
// zero, backdates it so the grace period can be exercised without waiting.
func cachePruneDir(t *testing.T, cacheDir, family, name string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(cacheDir, family, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestPruneStaleCacheVersionsRemovesOnlyUnreachableVersions pins the boundary:
// a version directory this build cannot address goes, and the current version,
// a foreign directory and a plain file all stay.
func TestPruneStaleCacheVersionsRemovesOnlyUnreachableVersions(t *testing.T) {
	cacheDir := t.TempDir()
	old := time.Now().Add(-2 * cacheVersionGracePeriod)

	staleSearch := cachePruneDir(t, cacheDir, "search", "search-snapshot-v1-old", old)
	currentSearch := cachePruneDir(t, cacheDir, "search", searchSnapshotCacheVersion, old)
	staleRecords := cachePruneDir(t, cacheDir, "records", "provider-records-v1-old", old)
	currentRecords := cachePruneDir(t, cacheDir, "records", providerRecordsCacheVersion, old)
	foreign := cachePruneDir(t, cacheDir, "search", "operator-data", old)
	file := filepath.Join(cacheDir, "search", "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	PruneStaleCacheVersions(cacheDir)

	for _, path := range []string{staleSearch, staleRecords} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale version directory %s was not removed", path)
		}
	}
	for _, path := range []string{currentSearch, currentRecords, foreign, file} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
}

// TestPruneStaleCacheVersionsKeepsRecentlyTouchedVersions pins the grace
// period: an older build may still be writing to its version directory, so a
// directory touched within the period is left alone.
func TestPruneStaleCacheVersionsKeepsRecentlyTouchedVersions(t *testing.T) {
	cacheDir := t.TempDir()
	recent := cachePruneDir(t, cacheDir, "search", "search-snapshot-v1-old", time.Time{})

	PruneStaleCacheVersions(cacheDir)

	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("a version directory touched within the grace period was removed: %v", err)
	}
}

// TestPruneStaleCacheVersionsIgnoresAnAbsentCache pins that pruning never turns
// an absent or caller-supplied empty cache root into an error or a surprise.
func TestPruneStaleCacheVersionsIgnoresAnAbsentCache(t *testing.T) {
	PruneStaleCacheVersions("")
	PruneStaleCacheVersions(filepath.Join(t.TempDir(), "does-not-exist"))
}
