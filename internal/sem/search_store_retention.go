package sem

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Search store retention bounds the sqlite sidecars. A store is 10-30x the size
// of the gz entry it mirrors (68 MB against 3.5 MB on this repository, 196 MB
// against 7.4 MB on entire-brain), the cache key changes with every new tree,
// and nothing else ever removes one, so a consumer that builds a store per
// commit grows the cache without bound. Each publish therefore trims the
// sidecars of its own repository and scope to the newest few. The gz entries
// are left alone: they are small, they stay authoritative, and
// repairSearchStoreSidecarForEntry turns one back into a store in about two
// seconds without reparsing, so evicting a sidecar costs at most that rebuild.
//
// What is kept, per group (the store's repository key plus its checkout scope):
//   - the newest searchStoreKeep stores by modification time, which a publish
//     sets, admitted newest first while their total stays within
//     searchStoreRetainBytes; the single newest store is always admitted;
//   - the store being published, wherever it sorts;
//   - every store built for the publishing checkout's current HEAD tree, in
//     any policy variant, so a writer for an older tree can never evict the
//     store that checkout is reading now.
//
// The group deliberately leaves out the provider version, the profile and the
// ignore policy. Those are in the cache key, so a group that included them
// would strand the stores of every superseded nightly and every edited ignore
// file forever, each group under its own limit. Variants of one repository
// share the limit instead; the HEAD rule keeps each variant's current store.
//
// Readers are never locked out. Graph's own readers and external consumers
// (docs/store-contract.md) open a store by path, read-only and immutable, so
// on POSIX an eviction unlinks the name while every open connection keeps
// reading the same inode, and the next resolution reports the store missing,
// which consumers already handle. Windows refuses to delete a file another
// process holds open; that removal error skips the store, which a later
// publish retries. Writers and evictors are serialized by
// searchStoreRetentionLockName, so an eviction can never remove a store that
// was republished under the same name after it was listed.
const (
	// searchStoreKeepEnv overrides searchStoreKeep. 0 or a negative value
	// turns retention off, which is the behaviour before it existed.
	searchStoreKeepEnv = "ENTIRE_GRAPH_SEARCH_STORE_KEEP"
	// defaultSearchStoreKeep is three: the store for HEAD plus two before it.
	// That covers a checkout that flips between two trees (a branch switch, a
	// rebase and back) and two or three worktrees of one repository indexing at
	// once, and leaves the nearest ancestors for a reader that falls back to
	// one. At the measured sizes it holds 204-588 MB per repository. Unbounded,
	// one store per first-parent commit came to about 1.2 GB a week on
	// entire-graph (17 commits) and 17 GB a week on entire-brain (86).
	defaultSearchStoreKeep = 3
	// searchStoreRetainBytes caps the recency window by size as well as by
	// count. At five times the largest store measured (196 MB) it never binds
	// before the count does on repositories of that size; it starts to bind
	// only where one store passes about 340 MB, at which point keeping older
	// copies costs more disk than rebuilding one from its gz entry costs time.
	searchStoreRetainBytes int64 = 1 << 30
	// searchStoreRetentionLockName is the directory-wide lock that serializes
	// a publish with an eviction pass. It is held only for one rename and one
	// pass, never for a build.
	searchStoreRetentionLockName = "search-store-retention.lock"
)

// searchStoreRetentionLockWait bounds how long a publish waits for another
// process's publish or eviction pass. Both take milliseconds, so the wait only
// matters if a holder is wedged; past it the store is published without an
// eviction pass. A var so a test can shorten it.
var searchStoreRetentionLockWait = 5 * time.Second

// Test seams. beforeSearchStorePublish runs after the store is built and
// before the publish takes the retention lock; afterSearchStorePublish runs
// right after the rename; beforeSearchStoreEvictionRemove runs after an
// eviction pass has chosen what to remove and before it removes anything.
// They let a test force the interleaving of a writer and an evictor.
// removeSearchStoreSidecar is the removal a publish's eviction pass uses, so
// a test can stand in for a platform that refuses to delete an open file.
var (
	beforeSearchStorePublish        func()
	afterSearchStorePublish         func()
	beforeSearchStoreEvictionRemove func()
	removeSearchStoreSidecar        = (*os.Root).Remove
)

// resolveSearchStoreKeep reads the override, falling back to the default for
// anything unparseable so a typo cannot silently turn retention off.
func resolveSearchStoreKeep() int {
	raw, ok := os.LookupEnv(searchStoreKeepEnv)
	if !ok {
		return defaultSearchStoreKeep
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return defaultSearchStoreKeep
	}
	if value <= 0 {
		return 0
	}
	return value
}

// searchStoreRetention is one eviction pass's policy.
type searchStoreRetention struct {
	keep     int
	maxBytes int64
	// group is searchStoreRetentionGroup of the store being published. Only
	// stores in this group are considered.
	group string
	// protectNames are store file names never evicted (the one published).
	protectNames map[string]bool
	// protectTree, when set, is the publishing checkout's current HEAD tree.
	protectTree string
	// remove deletes one store; os.Root.Remove in production.
	remove func(directory *os.Root, name string) error
}

// searchStoreEviction reports what a pass did. Skipped maps a store that was
// chosen for eviction but could not be removed to the removal error.
type searchStoreEviction struct {
	Evicted []string
	Kept    []string
	Skipped map[string]error
}

type searchStoreCandidate struct {
	name    string
	size    int64
	modTime time.Time
	tree    string
}

func searchStoreRetentionGroup(repositoryKey, repoScope string) string {
	return repositoryKey + "\x00" + repoScope
}

// searchStoreSidecarName reports whether name is a published store: a cache
// key followed by .sqlite. Temporaries (".store-*.json.gz"), gz entries, lock
// files and derived-entry directories never match.
func searchStoreSidecarName(name string) bool {
	key, ok := strings.CutSuffix(name, ".sqlite")
	return ok && validSHA256Hex(key)
}

// newSearchStoreRetention builds the policy for publishing store, or reports
// false when retention is off. It resolves the publishing checkout's HEAD tree
// here, before the publish takes the lock, so the lock never waits on git.
func newSearchStoreRetention(ctx context.Context, store cacheEntry, cache *cachedSearchSnapshot) (searchStoreRetention, bool) {
	keep := resolveSearchStoreKeep()
	if keep <= 0 {
		return searchStoreRetention{}, false
	}
	retention := searchStoreRetention{
		keep:         keep,
		maxBytes:     searchStoreRetainBytes,
		group:        searchStoreRetentionGroup(cache.Snapshot.Header.RepoKey, cache.RepoScope),
		protectNames: map[string]bool{filepath.Base(store.relative): true},
		remove:       removeSearchStoreSidecar,
	}
	if root := cache.Snapshot.Header.RepoRoot; root != "" {
		if _, tree, err := resolveCommittedHEAD(ctx, root); err == nil {
			retention.protectTree = tree
		}
	}
	return retention, true
}

// acquireSearchStoreRetentionLock takes the directory-wide retention lock,
// waiting at most searchStoreRetentionLockWait. It returns nil when the lock
// cannot be opened or taken (read-only cache, unsupported platform, a wedged
// holder, a cancelled context); the caller then publishes without evicting,
// because an eviction is only safe while no publish can rename under it.
func acquireSearchStoreRetentionLock(ctx context.Context, directory *os.Root) *completeBuildLock {
	file, err := openCacheLockFileIn(directory, searchStoreRetentionLockName)
	if err != nil {
		return nil
	}
	deadline := time.Now().Add(searchStoreRetentionLockWait)
	poll := completeBuildLockPollMin
	for {
		err := tryLockBuildLockFile(file)
		if err == nil {
			return &completeBuildLock{file: file}
		}
		remaining := time.Until(deadline)
		if !errors.Is(err, errBuildLockBusy) || remaining <= 0 || ctx.Err() != nil {
			_ = file.Close()
			return nil
		}
		timer := time.NewTimer(min(poll, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil
		case <-timer.C:
		}
		poll = min(poll*2, completeBuildLockPollMax)
	}
}

// evictSearchStoreSidecars removes the stores in the policy's group that fall
// outside it. The caller must hold the retention lock. directoryPath is the
// filesystem path of directory, which sqlite needs to read each store's
// identity. A store whose identity cannot be read is never removed: retention
// only deletes what it can attribute to the group being trimmed.
func evictSearchStoreSidecars(ctx context.Context, directory *os.Root, directoryPath string, retention searchStoreRetention) (searchStoreEviction, error) {
	candidates, err := listSearchStoreGroup(ctx, directory, directoryPath, retention.group)
	if err != nil {
		return searchStoreEviction{}, err
	}
	keep, evict := planSearchStoreEviction(candidates, retention)
	result := searchStoreEviction{Kept: keep}
	if len(evict) == 0 {
		return result, nil
	}
	if beforeSearchStoreEvictionRemove != nil {
		beforeSearchStoreEvictionRemove()
	}
	for _, name := range evict {
		err := retention.remove(directory, name)
		switch {
		case err == nil:
			result.Evicted = append(result.Evicted, name)
		case errors.Is(err, fs.ErrNotExist):
			// Already gone: removed by hand, or by a failed rebuild's cleanup.
		default:
			// Windows refuses to delete a file another process holds open, as
			// a reader of this store does. Leave it; a later publish retries.
			if result.Skipped == nil {
				result.Skipped = make(map[string]error)
			}
			result.Skipped[name] = err
		}
	}
	return result, nil
}

// listSearchStoreGroup returns the published stores in directory that belong
// to group.
func listSearchStoreGroup(ctx context.Context, directory *os.Root, directoryPath, group string) ([]searchStoreCandidate, error) {
	handle, err := directory.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := handle.ReadDir(-1)
	_ = handle.Close()
	if err != nil {
		return nil, err
	}
	var candidates []searchStoreCandidate
	for _, entry := range entries {
		name := entry.Name()
		if !searchStoreSidecarName(name) {
			continue
		}
		info, err := directory.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || pathMayRedirect(info) {
			continue
		}
		storeGroup, tree, ok := readSearchStoreRetentionIdentity(ctx, filepath.Join(directoryPath, name))
		if !ok || storeGroup != group {
			continue
		}
		candidates = append(candidates, searchStoreCandidate{name: name, size: info.Size(), modTime: info.ModTime(), tree: tree})
	}
	return candidates, nil
}

// readSearchStoreRetentionIdentity reads the group and tree a store was built
// for from its meta table. Every store version carries both envelope and
// header, so stores of an older version are grouped (and aged out) like
// current ones.
func readSearchStoreRetentionIdentity(ctx context.Context, path string) (string, string, bool) {
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?immutable=1&mode=ro")
	if err != nil {
		return "", "", false
	}
	defer db.Close()
	var envelope searchStoreMeta
	if err := readSearchStoreMetaKey(ctx, db, "envelope", &envelope); err != nil {
		return "", "", false
	}
	var header struct {
		RepoKey string `json:"repo_key"`
	}
	if err := readSearchStoreMetaKey(ctx, db, "header", &header); err != nil {
		return "", "", false
	}
	return searchStoreRetentionGroup(header.RepoKey, envelope.RepoScope), envelope.Tree, true
}

// planSearchStoreEviction splits one group's stores into the names kept and
// the names to evict. Stores are admitted to the recency window newest first
// (ties broken by name, so every evictor orders them alike) until the count or
// the byte budget is exhausted; the first store refused closes the window, so
// what survives is always a prefix of the newest. Protected stores are kept
// wherever they sort.
//
// The rule is what makes concurrent passes safe even without the lock: a pass
// evicts a store only when at least keep stores newer than it exist, or when
// newer stores already fill the byte budget, and a store with that many newer
// ones is refused by every later pass too. No interleaving can therefore evict
// one of the newest stores that fit.
func planSearchStoreEviction(candidates []searchStoreCandidate, retention searchStoreRetention) (keep, evict []string) {
	sorted := append([]searchStoreCandidate(nil), candidates...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].modTime.Equal(sorted[j].modTime) {
			return sorted[i].modTime.After(sorted[j].modTime)
		}
		return sorted[i].name < sorted[j].name
	})
	admitted, admittedBytes := 0, int64(0)
	windowOpen := true
	for index, candidate := range sorted {
		inWindow := false
		if windowOpen {
			if index == 0 || (admitted < retention.keep && admittedBytes+candidate.size <= retention.maxBytes) {
				inWindow = true
				admitted++
				admittedBytes += candidate.size
			} else {
				windowOpen = false
			}
		}
		protected := retention.protectNames[candidate.name] ||
			(retention.protectTree != "" && candidate.tree == retention.protectTree)
		if inWindow || protected {
			keep = append(keep, candidate.name)
		} else {
			evict = append(evict, candidate.name)
		}
	}
	return keep, evict
}
