package sem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Version directory names this build must treat as provably older, in both
// families, whatever the current counters are.
const (
	olderSearchVersion  = "search-snapshot-v1-1"
	olderRecordsVersion = "provider-records-v1-1"
)

// cachePruneDir creates a populated directory under the cache root (a top-level
// entry and a nested derived one, as a real version directory holds) and, when
// mod is not zero, backdates it so the grace period can be exercised without
// waiting.
func cachePruneDir(t *testing.T, cacheDir, family, name string, mod time.Time) string {
	t.Helper()
	path := filepath.Join(cacheDir, family, name)
	if err := os.MkdirAll(filepath.Join(path, "derived"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"entry.json.gz", filepath.Join("derived", "view.json.gz")} {
		if err := os.WriteFile(filepath.Join(path, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cachePruneBackdate(t, path, mod)
	return path
}

func cachePruneBackdate(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if mod.IsZero() {
		return
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func cachePruneIdleTime() time.Time {
	return time.Now().Add(-2 * cacheVersionGracePeriod)
}

func cachePruneExpectGone(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s was not removed (err=%v)", path, err)
		}
	}
}

func cachePruneExpectKept(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
}

// TestPruneStaleCacheVersionsRemovesOnlyOlderIdleVersions pins the boundary: a
// populated, idle version directory that is provably older than this build's
// goes, and everything else stays: the current version, a newer one, one from
// a divergent line, a name that cannot be ordered, a foreign directory and a
// plain file.
func TestPruneStaleCacheVersionsRemovesOnlyOlderIdleVersions(t *testing.T) {
	cacheDir := t.TempDir()
	old := cachePruneIdleTime()

	staleSearch := cachePruneDir(t, cacheDir, "search", olderSearchVersion, old)
	legacySearch := cachePruneDir(t, cacheDir, "search", "search-snapshot-v5", old)
	staleRecords := cachePruneDir(t, cacheDir, "records", olderRecordsVersion, old)
	kept := []string{
		cachePruneDir(t, cacheDir, "search", searchSnapshotCacheVersion, old),
		cachePruneDir(t, cacheDir, "records", providerRecordsCacheVersion, old),
		// A newer build's cache: both counters, then each alone, ahead of ours.
		cachePruneDir(t, cacheDir, "search", "search-snapshot-v99999-99999", old),
		cachePruneDir(t, cacheDir, "search", "search-snapshot-v99999-"+IdentityRevision, old),
		cachePruneDir(t, cacheDir, "search", "search-snapshot-v1-99999", old),
		cachePruneDir(t, cacheDir, "records", "provider-records-v99999-99999", old),
		// Not orderable, so not provably older.
		cachePruneDir(t, cacheDir, "search", "search-snapshot-v1-old", old),
		cachePruneDir(t, cacheDir, "search", "search-snapshot-", old),
		cachePruneDir(t, cacheDir, "search", "search-snapshot-v-1", old),
		// The other family's name is not a version of this family.
		cachePruneDir(t, cacheDir, "search", olderRecordsVersion, old),
		cachePruneDir(t, cacheDir, "search", "operator-data", old),
	}
	file := filepath.Join(cacheDir, "search", "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	PruneStaleCacheVersions(cacheDir)

	cachePruneExpectGone(t, staleSearch, legacySearch, staleRecords)
	cachePruneExpectKept(t, append(kept, file)...)
	for _, path := range kept {
		cachePruneExpectKept(t, filepath.Join(path, "entry.json.gz"), filepath.Join(path, "derived", "view.json.gz"))
	}
}

// TestPruneStaleCacheVersionsKeepsRecentlyTouchedVersions pins the grace
// period: an older build may still be writing to its version directory, so a
// directory touched within the period is left alone.
func TestPruneStaleCacheVersionsKeepsRecentlyTouchedVersions(t *testing.T) {
	cacheDir := t.TempDir()
	recent := cachePruneDir(t, cacheDir, "search", olderSearchVersion, time.Time{})

	PruneStaleCacheVersions(cacheDir)

	cachePruneExpectKept(t, recent, filepath.Join(recent, "entry.json.gz"))
}

// TestPruneStaleCacheVersionsKeepsHitOnlyVersions pins that use, not writes,
// is what keeps a directory: a build served only hits never moves the
// directory's mtime, so its use marker has to count. Once the marker is as old
// as the directory, the version is idle and goes.
func TestPruneStaleCacheVersionsKeepsHitOnlyVersions(t *testing.T) {
	cacheDir := t.TempDir()
	old := cachePruneIdleTime()
	version := cachePruneDir(t, cacheDir, "search", olderSearchVersion, time.Time{})

	// What the older build does on a cache hit.
	markCacheVersionUsed(cacheDir, "search", olderSearchVersion, time.Now())
	marker := filepath.Join(version, cacheVersionUseMarkerName)
	cachePruneExpectKept(t, marker)
	cachePruneBackdate(t, version, old)

	PruneStaleCacheVersions(cacheDir)
	cachePruneExpectKept(t, version, filepath.Join(version, "entry.json.gz"))

	cachePruneBackdate(t, marker, old)
	cachePruneBackdate(t, version, old)
	PruneStaleCacheVersions(cacheDir)
	cachePruneExpectGone(t, version)
}

// TestMarkCacheVersionUsedIsThrottledAndNeverCreatesTheCache pins the marker's
// cost and reach: a marker younger than the interval is not rewritten, an
// older one is refreshed, and marking an absent version creates nothing.
func TestMarkCacheVersionUsedIsThrottledAndNeverCreatesTheCache(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()

	markCacheVersionUsed(cacheDir, "search", searchSnapshotCacheVersion, now)
	markCacheVersionUsed(filepath.Join(cacheDir, "absent"), "search", searchSnapshotCacheVersion, now)
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) != 0 {
		t.Fatalf("marking an absent version created %v (err=%v)", entries, err)
	}

	version := cachePruneDir(t, cacheDir, "search", searchSnapshotCacheVersion, time.Time{})
	marker := filepath.Join(version, cacheVersionUseMarkerName)
	markCacheVersionUsed(cacheDir, "search", searchSnapshotCacheVersion, now)
	cachePruneExpectKept(t, marker)

	young := now.Add(-cacheVersionUseMarkInterval / 2).Truncate(time.Second)
	cachePruneBackdate(t, marker, young)
	markCacheVersionUsed(cacheDir, "search", searchSnapshotCacheVersion, now)
	if info, err := os.Stat(marker); err != nil || !info.ModTime().Equal(young) {
		t.Fatalf("a marker inside the interval was rewritten: %v (err=%v)", info, err)
	}

	cachePruneBackdate(t, marker, now.Add(-2*cacheVersionUseMarkInterval))
	markCacheVersionUsed(cacheDir, "search", searchSnapshotCacheVersion, now)
	if info, err := os.Stat(marker); err != nil || now.Sub(info.ModTime()) > time.Minute {
		t.Fatalf("a marker older than the interval was not refreshed: %v (err=%v)", info, err)
	}
}

// TestPruneStaleCacheVersionsRefusesRedirectingComponents pins that neither a
// symlinked family directory nor a symlinked version directory is followed:
// what they point at, inside or outside the cache root, is left intact.
func TestPruneStaleCacheVersionsRefusesRedirectingComponents(t *testing.T) {
	old := cachePruneIdleTime()

	t.Run("family", func(t *testing.T) {
		cacheDir := t.TempDir()
		outside := t.TempDir()
		victim := cachePruneDir(t, outside, ".", olderSearchVersion, old)
		if err := os.Symlink(outside, filepath.Join(cacheDir, "search")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		PruneStaleCacheVersions(cacheDir)
		markCacheVersionUsed(cacheDir, "search", olderSearchVersion, time.Now())

		cachePruneExpectKept(t, victim, filepath.Join(victim, "entry.json.gz"), filepath.Join(cacheDir, "search"))
		cachePruneExpectGone(t, filepath.Join(victim, cacheVersionUseMarkerName))
	})

	t.Run("version", func(t *testing.T) {
		cacheDir := t.TempDir()
		outside := t.TempDir()
		victim := cachePruneDir(t, outside, ".", "target", old)
		inside := cachePruneDir(t, cacheDir, "records", "operator-data", old)
		if err := os.MkdirAll(filepath.Join(cacheDir, "search"), 0o755); err != nil {
			t.Fatal(err)
		}
		escaping := filepath.Join(cacheDir, "search", olderSearchVersion)
		staying := filepath.Join(cacheDir, "search", "search-snapshot-v2-1")
		if err := os.Symlink(victim, escaping); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.Symlink(inside, staying); err != nil {
			t.Fatal(err)
		}

		PruneStaleCacheVersions(cacheDir)
		markCacheVersionUsed(cacheDir, "search", olderSearchVersion, time.Now())

		cachePruneExpectKept(t, escaping, staying,
			filepath.Join(victim, "entry.json.gz"), filepath.Join(inside, "entry.json.gz"))
		cachePruneExpectGone(t, filepath.Join(victim, cacheVersionUseMarkerName))
	})
}

// TestPruneStaleCacheVersionsSkipsAVersionWithAHeldLock pins the build-lock
// rule: a lock file another holder has locked must not be unlinked, so its
// whole version directory waits. A lock file nobody holds is only a leftover,
// and does not keep the directory.
func TestPruneStaleCacheVersionsSkipsAVersionWithAHeldLock(t *testing.T) {
	cacheDir := t.TempDir()
	old := cachePruneIdleTime()
	version := cachePruneDir(t, cacheDir, "search", olderSearchVersion, time.Time{})
	lockPath := filepath.Join(version, "key.build.lock")
	holder, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := tryLockBuildLockFile(holder); err != nil {
		t.Skipf("file locks unavailable: %v", err)
	}
	cachePruneBackdate(t, version, old)

	PruneStaleCacheVersions(cacheDir)
	cachePruneExpectKept(t, version, lockPath, filepath.Join(version, "entry.json.gz"))
	// The probe must not have taken the lock away from its holder.
	other, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := tryLockBuildLockFile(other); err != errBuildLockBusy {
		t.Fatalf("lock is no longer held after a prune pass: %v", err)
	}
	_ = other.Close()

	if err := unlockBuildLockFile(holder); err != nil {
		t.Fatal(err)
	}
	cachePruneBackdate(t, version, old)
	PruneStaleCacheVersions(cacheDir)
	cachePruneExpectGone(t, version)
}

// TestPruneStaleCacheVersionsIgnoresAnAbsentCache pins that pruning never turns
// an absent or caller-supplied empty cache root into an error or a surprise,
// and never creates one.
func TestPruneStaleCacheVersionsIgnoresAnAbsentCache(t *testing.T) {
	PruneStaleCacheVersions("")
	absent := filepath.Join(t.TempDir(), "does-not-exist")
	PruneStaleCacheVersions(absent)
	noteCacheVersionUse(absent, "search", searchSnapshotCacheVersion)
	cachePruneExpectGone(t, absent)

	empty := t.TempDir()
	PruneStaleCacheVersions(empty)
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("pruning an empty cache root created %v (err=%v)", entries, err)
	}
}

// TestCacheVersionsArePrunedOnlyByCommandsThatUseTheCache pins where pruning
// runs: a working-tree query and a cache-disabled query never touch the cache,
// so they leave even an idle older version alone, while a committed-tree
// preindex reclaims it and records its own use.
func TestCacheVersionsArePrunedOnlyByCommandsThatUseTheCache(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	cacheDir := t.TempDir()
	old := cachePruneIdleTime()
	staleSearch := cachePruneDir(t, cacheDir, "search", olderSearchVersion, old)
	staleRecords := cachePruneDir(t, cacheDir, "records", olderRecordsVersion, old)

	options := ProviderSnapshotOptions{Profile: ProfileFast}
	worktree := options
	worktree.Worktree = true
	if _, _, err := loadOrBuildSearchGraphSnapshot(t.Context(), repo, "test-version", worktree, cacheDir, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrBuildSearchGraphSnapshot(t.Context(), repo, "test-version", options, cacheDir, true); err != nil {
		t.Fatal(err)
	}
	cachePruneExpectKept(t, staleSearch, staleRecords)

	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, cacheDir); err != nil {
		t.Fatal(err)
	}
	cachePruneExpectGone(t, staleSearch, staleRecords)

	// The first run created the version directory, so there was nothing to mark
	// yet; a later process (a fresh throttle) marks it on a plain hit.
	cacheVersionMaintenance.Clear()
	if _, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, cacheDir); err != nil || !hit {
		t.Fatalf("second preindex: hit=%v err=%v", hit, err)
	}
	cachePruneExpectKept(t, filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, cacheVersionUseMarkerName))
}
