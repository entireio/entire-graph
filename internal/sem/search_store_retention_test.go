package sem

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const retentionTestVersion = "retention-test"

// retentionTestCache builds one small real snapshot and returns a factory for
// caches that differ only in the identity retention groups and protects by.
func retentionTestCache(t *testing.T) func(repoKey, scope, tree string) *cachedSearchSnapshot {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "widget.go"), []byte("package sample\n\nfunc Render() string { return \"x\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "store-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	base := newCachedSearchSnapshot("store-test", "commit", "tree", ProviderSnapshotOptions{}, snapshot, t.Context())
	return func(repoKey, scope, tree string) *cachedSearchSnapshot {
		cache := base
		cache.Tree = tree
		cache.RepoScope = scope
		cache.Snapshot.Header.RepoKey = repoKey
		return &cache
	}
}

func retentionTestKey(index int) string {
	return fmt.Sprintf("%064x", index)
}

// publishRetentionTestStore writes the gz entry and publishes its sidecar the
// way production does, and returns the sidecar's path.
func publishRetentionTestStore(t *testing.T, cacheDir, key string, cache *cachedSearchSnapshot, digest string) string {
	t.Helper()
	entry, err := newCacheEntry(cacheDir, "search", retentionTestVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(entry, *cache); err != nil {
		t.Fatal(err)
	}
	if err := writeSearchStoreSidecar(t.Context(), entry, digest, cache); err != nil {
		t.Fatal(err)
	}
	return retentionTestStorePath(cacheDir, key)
}

func retentionTestStorePath(cacheDir, key string) string {
	return filepath.Join(cacheDir, "search", retentionTestVersion, key+".sqlite")
}

func ageRetentionTestStore(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func retentionTestStores(t *testing.T, cacheDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(cacheDir, "search", retentionTestVersion, "*.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	sort.Strings(names)
	return names
}

func retentionTestNames(keys ...string) []string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key+".sqlite")
	}
	sort.Strings(names)
	return names
}

func openRetentionTestDirectory(t *testing.T, cacheDir string) (*os.Root, string) {
	t.Helper()
	entry, err := newCacheEntry(cacheDir, "search", retentionTestVersion, retentionTestKey(0))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := openCacheEntryDirectory(entry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory, filepath.Join(cacheDir, "search", retentionTestVersion)
}

// Writing K+1 stores into one repository/scope group leaves the newest K. A
// store of another group is never touched, however old, and every gz entry
// survives: retention trims sidecars only.
func TestSearchStoreRetentionKeepsNewestKPerGroup(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "2")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()
	base := time.Now().Add(-time.Hour)

	other := publishRetentionTestStore(t, cacheDir, retentionTestKey(100), newCache("gh/acme/other", "prefix:", "tree-o"), "digest-o")
	ageRetentionTestStore(t, other, base)
	for index := 1; index <= 2; index++ {
		path := publishRetentionTestStore(t, cacheDir, retentionTestKey(index), newCache("gh/acme/widget", "prefix:", fmt.Sprintf("tree-%d", index)), "digest")
		ageRetentionTestStore(t, path, base.Add(time.Duration(index)*time.Minute))
	}
	if got, want := retentionTestStores(t, cacheDir), retentionTestNames(retentionTestKey(1), retentionTestKey(2), retentionTestKey(100)); !equalStrings(got, want) {
		t.Fatalf("premise: K stores must all survive, got %v want %v", got, want)
	}

	publishRetentionTestStore(t, cacheDir, retentionTestKey(3), newCache("gh/acme/widget", "prefix:", "tree-3"), "digest")
	if got, want := retentionTestStores(t, cacheDir), retentionTestNames(retentionTestKey(2), retentionTestKey(3), retentionTestKey(100)); !equalStrings(got, want) {
		t.Fatalf("after K+1 stores: got %v, want the newest two of the group plus the other group's store %v", got, want)
	}
	for _, index := range []int{1, 2, 3, 100} {
		if _, err := os.Stat(filepath.Join(cacheDir, "search", retentionTestVersion, retentionTestKey(index)+".json.gz")); err != nil {
			t.Fatalf("gz entry %d must never be evicted: %v", index, err)
		}
	}
}

// The store being published is kept even when a newer store already fills the
// window (another writer's, or one with a skewed clock), and every store built
// for the publishing checkout's current HEAD tree survives a publish for
// another tree.
func TestSearchStoreRetentionNeverEvictsPublishedOrHeadStore(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "1")
	newCache := retentionTestCache(t)

	t.Run("published", func(t *testing.T) {
		cacheDir := t.TempDir()
		newer := publishRetentionTestStore(t, cacheDir, retentionTestKey(1), newCache("gh/acme/widget", "prefix:", "tree-1"), "digest")
		ageRetentionTestStore(t, newer, time.Now().Add(time.Hour))
		publishRetentionTestStore(t, cacheDir, retentionTestKey(2), newCache("gh/acme/widget", "prefix:", "tree-2"), "digest")
		if got, want := retentionTestStores(t, cacheDir), retentionTestNames(retentionTestKey(1), retentionTestKey(2)); !equalStrings(got, want) {
			t.Fatalf("the newest store and the one just published must both survive: got %v want %v", got, want)
		}
	})

	t.Run("head", func(t *testing.T) {
		repo := t.TempDir()
		initDiffPolicyRepo(t, repo)
		if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", "main.go")
		git(t, repo, "commit", "-q", "-m", "seed")
		headTree := gitInput(t, repo, "", "rev-parse", "HEAD^{tree}")
		inRepo := func(tree string) *cachedSearchSnapshot {
			cache := newCache("gh/acme/widget", "prefix:", tree)
			cache.Snapshot.Header.RepoRoot = repo
			return cache
		}
		cacheDir := t.TempDir()
		base := time.Now().Add(-time.Hour)
		head := publishRetentionTestStore(t, cacheDir, retentionTestKey(1), inRepo(headTree), "digest")
		ageRetentionTestStore(t, head, base)
		older := publishRetentionTestStore(t, cacheDir, retentionTestKey(2), inRepo("tree-older"), "digest")
		ageRetentionTestStore(t, older, base.Add(time.Minute))
		publishRetentionTestStore(t, cacheDir, retentionTestKey(3), inRepo("tree-elsewhere"), "digest")
		if got, want := retentionTestStores(t, cacheDir), retentionTestNames(retentionTestKey(1), retentionTestKey(3)); !equalStrings(got, want) {
			t.Fatalf("HEAD's store must survive a publish for another tree: got %v want %v", got, want)
		}
	})
}

// The recency window is a prefix of the newest stores bounded by count and by
// bytes, and the single newest store is admitted even when it alone exceeds
// the byte budget.
func TestSearchStoreRetentionByteBudget(t *testing.T) {
	now := time.Now()
	candidate := func(name string, age time.Duration, size int64) searchStoreCandidate {
		return searchStoreCandidate{name: name, size: size, modTime: now.Add(-age)}
	}
	retention := searchStoreRetention{keep: 3, maxBytes: 1000}

	keep, evict := planSearchStoreEviction([]searchStoreCandidate{
		candidate("c.sqlite", 3*time.Minute, 100),
		candidate("a.sqlite", time.Minute, 600),
		candidate("b.sqlite", 2*time.Minute, 500),
	}, retention)
	if !equalStrings(keep, []string{"a.sqlite"}) || !equalStrings(evict, []string{"b.sqlite", "c.sqlite"}) {
		t.Fatalf("budget: kept %v evicted %v; want the window closed at the first store over budget", keep, evict)
	}

	keep, evict = planSearchStoreEviction([]searchStoreCandidate{
		candidate("big.sqlite", time.Minute, 5000),
		candidate("small.sqlite", 2*time.Minute, 10),
	}, retention)
	if !equalStrings(keep, []string{"big.sqlite"}) || !equalStrings(evict, []string{"small.sqlite"}) {
		t.Fatalf("newest over budget: kept %v evicted %v; the newest store is always kept", keep, evict)
	}
}

// A reader holding a store open keeps reading it across an eviction. POSIX
// unlinks the name and leaves the open inode readable; Windows refuses to
// delete an open file, and the pass skips that store without failing.
func TestSearchStoreRetentionReaderHoldingStoreKeepsReading(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "0")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()
	heldCache := newCache("gh/acme/widget", "prefix:", "tree-1")
	held := publishRetentionTestStore(t, cacheDir, retentionTestKey(1), heldCache, "digest")
	ageRetentionTestStore(t, held, time.Now().Add(-time.Hour))
	publishRetentionTestStore(t, cacheDir, retentionTestKey(2), newCache("gh/acme/widget", "prefix:", "tree-2"), "digest")

	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(held)+"?immutable=1&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// One connection, opened before the eviction by reading only the meta
	// table, so the symbol rows read afterwards come from the open file and
	// not from a fresh open or the connection's page cache.
	db.SetMaxOpenConns(1)
	var version string
	if err := readSearchStoreMetaKey(t.Context(), db, "store_version", &version); err != nil || version != searchStoreVersion {
		t.Fatalf("premise: reader must open the store, got %q (%v)", version, err)
	}

	directory, directoryPath := openRetentionTestDirectory(t, cacheDir)
	result, err := evictSearchStoreSidecars(t.Context(), directory, directoryPath, searchStoreRetention{
		keep: 1, maxBytes: searchStoreRetainBytes,
		group:  searchStoreRetentionGroup("gh/acme/widget", "prefix:"),
		remove: removeSearchStoreSidecar,
	})
	if err != nil {
		t.Fatalf("eviction must not fail on a store a reader holds: %v", err)
	}
	_, statErr := os.Stat(held)
	if runtime.GOOS == "windows" {
		if statErr != nil || result.Skipped[filepath.Base(held)] == nil {
			t.Fatalf("windows: an open store must be skipped and survive, stat=%v result=%+v", statErr, result)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) || len(result.Evicted) != 1 {
		t.Fatalf("posix: the held store's name must be unlinked, stat=%v result=%+v", statErr, result)
	}
	var symbols int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM brain_symbols_v1").Scan(&symbols); err != nil || symbols != len(heldCache.Snapshot.Symbols) || symbols == 0 {
		t.Fatalf("a reader holding the store must keep reading it after eviction: got %d symbols (%v), want %d", symbols, err, len(heldCache.Snapshot.Symbols))
	}
}

// A removal the platform refuses (Windows: another process has the store open)
// skips that store, evicts the rest, and never fails the publish.
func TestSearchStoreRetentionSkipsStoreThePlatformCannotDelete(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "0")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	oldest := publishRetentionTestStore(t, cacheDir, retentionTestKey(1), newCache("gh/acme/widget", "prefix:", "tree-1"), "digest")
	ageRetentionTestStore(t, oldest, base)
	held := publishRetentionTestStore(t, cacheDir, retentionTestKey(2), newCache("gh/acme/widget", "prefix:", "tree-2"), "digest")
	ageRetentionTestStore(t, held, base.Add(time.Minute))

	sharingViolation := errors.New("The process cannot access the file because it is being used by another process.")
	var refused atomic.Int32
	removeSearchStoreSidecar = func(directory *os.Root, name string) error {
		if name == filepath.Base(held) {
			refused.Add(1)
			return &fs.PathError{Op: "remove", Path: name, Err: sharingViolation}
		}
		return directory.Remove(name)
	}
	t.Cleanup(func() { removeSearchStoreSidecar = (*os.Root).Remove })

	// The undeletable store sorts first among those evicted, so the pass has
	// to carry on past the refusal to evict the oldest.
	t.Setenv(searchStoreKeepEnv, "1")
	publishRetentionTestStore(t, cacheDir, retentionTestKey(3), newCache("gh/acme/widget", "prefix:", "tree-3"), "digest")
	if refused.Load() == 0 {
		t.Fatal("premise: the pass never tried to remove the held store")
	}
	if got, want := retentionTestStores(t, cacheDir), retentionTestNames(retentionTestKey(2), retentionTestKey(3)); !equalStrings(got, want) {
		t.Fatalf("the undeletable store must survive and the pass must still evict the rest: got %v want %v", got, want)
	}
}

// A publish that cannot take the retention lock still publishes, and evicts
// nothing: removal is only safe while no other publish can rename under it.
func TestSearchStoreRetentionWithoutLockPublishesButNeverEvicts(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "1")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()
	oldest := publishRetentionTestStore(t, cacheDir, retentionTestKey(1), newCache("gh/acme/widget", "prefix:", "tree-1"), "digest")
	ageRetentionTestStore(t, oldest, time.Now().Add(-time.Hour))

	directory, _ := openRetentionTestDirectory(t, cacheDir)
	holder, err := openCacheLockFileIn(directory, searchStoreRetentionLockName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := tryLockBuildLockFile(holder); err != nil {
		t.Skipf("platform has no cache lock: %v", err)
	}
	previous := searchStoreRetentionLockWait
	searchStoreRetentionLockWait = 50 * time.Millisecond
	t.Cleanup(func() { searchStoreRetentionLockWait = previous })

	publishRetentionTestStore(t, cacheDir, retentionTestKey(2), newCache("gh/acme/widget", "prefix:", "tree-2"), "digest")
	if got, want := retentionTestStores(t, cacheDir), retentionTestNames(retentionTestKey(1), retentionTestKey(2)); !equalStrings(got, want) {
		t.Fatalf("a publish without the lock must publish and evict nothing: got %v want %v", got, want)
	}
}

// A writer republishing a store while another process's eviction pass has
// already chosen to remove that name must win: the pass holds the retention
// lock from listing to removal, so the rename lands only after it. The fixture
// forces the interleaving: the writer reaches its publish while the pass sits
// between choosing and removing.
func TestSearchStoreRetentionRepublishDuringEvictionSurvives(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "1")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	target := publishRetentionTestStore(t, cacheDir, retentionTestKey(1), newCache("gh/acme/widget", "prefix:", "tree-1"), "digest-old")
	ageRetentionTestStore(t, target, base)

	var armed atomic.Bool
	aboutToPublish := make(chan struct{})
	published := make(chan struct{})
	beforeSearchStorePublish = func() {
		if armed.Load() {
			close(aboutToPublish)
		}
	}
	afterSearchStorePublish = func() {
		if armed.Load() {
			close(published)
		}
	}
	writerDone := make(chan error, 1)
	var once sync.Once
	beforeSearchStoreEvictionRemove = func() {
		once.Do(func() {
			armed.Store(true)
			go func() {
				entry, err := newCacheEntry(cacheDir, "search", retentionTestVersion, retentionTestKey(1))
				if err == nil {
					err = writeSearchStoreSidecar(t.Context(), entry, "digest-new", newCache("gh/acme/widget", "prefix:", "tree-1"))
				}
				writerDone <- err
			}()
			select {
			case <-aboutToPublish:
			case <-time.After(10 * time.Second):
			}
			select {
			case <-published:
			case <-time.After(500 * time.Millisecond):
			}
		})
	}
	t.Cleanup(func() {
		beforeSearchStorePublish, afterSearchStorePublish, beforeSearchStoreEvictionRemove = nil, nil, nil
	})

	publishRetentionTestStore(t, cacheDir, retentionTestKey(2), newCache("gh/acme/widget", "prefix:", "tree-2"), "digest")
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the store republished during the eviction pass was removed by it: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(target)+"?immutable=1&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var digest string
	if err := readSearchStoreMetaKey(t.Context(), db, "entry_digest", &digest); err != nil || digest != "digest-new" {
		t.Fatalf("surviving store must be the republished one: digest %q (%v)", digest, err)
	}
}

// Concurrent publishers into one group, run under -race in CI's local gate:
// every pass is serialized, nothing fails, no temporary is left behind, and the
// newest K stores by publish time are all present.
func TestSearchStoreRetentionConcurrentPublishers(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "2")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()

	var mu sync.Mutex
	publishedAt := map[string]time.Time{}
	afterSearchStorePublish = func() {
		mu.Lock()
		defer mu.Unlock()
		for index := 1; index <= 6; index++ {
			name := retentionTestKey(index) + ".sqlite"
			if _, seen := publishedAt[name]; seen {
				continue
			}
			if info, err := os.Stat(filepath.Join(cacheDir, "search", retentionTestVersion, name)); err == nil {
				publishedAt[name] = info.ModTime()
			}
		}
	}
	t.Cleanup(func() { afterSearchStorePublish = nil })

	var group sync.WaitGroup
	errs := make(chan error, 6)
	for index := 1; index <= 6; index++ {
		group.Go(func() {
			entry, err := newCacheEntry(cacheDir, "search", retentionTestVersion, retentionTestKey(index))
			if err == nil {
				err = writeSearchStoreSidecar(t.Context(), entry, "digest", newCache("gh/acme/widget", "prefix:", fmt.Sprintf("tree-%d", index)))
			}
			errs <- err
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(publishedAt) != 6 {
		t.Fatalf("premise: every publish must be observed, got %d", len(publishedAt))
	}
	names := make([]string, 0, len(publishedAt))
	for name := range publishedAt {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if !publishedAt[names[i]].Equal(publishedAt[names[j]]) {
			return publishedAt[names[i]].After(publishedAt[names[j]])
		}
		return names[i] < names[j]
	})
	survivors := retentionTestStores(t, cacheDir)
	if len(survivors) < 2 || len(survivors) > 3 {
		t.Fatalf("want K stores plus at most the last publisher's own, got %v", survivors)
	}
	present := map[string]bool{}
	for _, name := range survivors {
		present[name] = true
	}
	for _, name := range names[:2] {
		if !present[name] {
			t.Fatalf("newest store %s was evicted; survivors %v, newest-first %v", name, survivors, names)
		}
	}
	temporaries, _ := filepath.Glob(filepath.Join(cacheDir, "search", retentionTestVersion, ".store-*"))
	if len(temporaries) != 0 {
		t.Fatalf("temporaries left behind: %v", temporaries)
	}
}

// ENTIRE_GRAPH_SEARCH_STORE_KEEP follows the repository's override convention:
// unset or unparseable keeps the default, 0 or negative turns retention off.
func TestSearchStoreRetentionKeepOverride(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  int
	}{
		{set: false, want: defaultSearchStoreKeep},
		{value: "5", set: true, want: 5},
		{value: " 2 ", set: true, want: 2},
		{value: "0", set: true, want: 0},
		{value: "-1", set: true, want: 0},
		{value: "three", set: true, want: defaultSearchStoreKeep},
	} {
		if tc.set {
			t.Setenv(searchStoreKeepEnv, tc.value)
		} else {
			t.Setenv(searchStoreKeepEnv, "")
			if err := os.Unsetenv(searchStoreKeepEnv); err != nil {
				t.Fatal(err)
			}
		}
		if got := resolveSearchStoreKeep(); got != tc.want {
			t.Fatalf("%s=%q (set=%v): got %d want %d", searchStoreKeepEnv, tc.value, tc.set, got, tc.want)
		}
	}

	t.Setenv(searchStoreEnv, "1")
	t.Setenv(searchStoreKeepEnv, "0")
	newCache := retentionTestCache(t)
	cacheDir := t.TempDir()
	for index := 1; index <= 4; index++ {
		publishRetentionTestStore(t, cacheDir, retentionTestKey(index), newCache("gh/acme/widget", "prefix:", fmt.Sprintf("tree-%d", index)), "digest")
	}
	if got := retentionTestStores(t, cacheDir); len(got) != 4 {
		t.Fatalf("retention off must evict nothing, got %v", got)
	}
}
