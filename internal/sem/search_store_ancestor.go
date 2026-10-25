package sem

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// The ancestor fallback closes the window between a new commit and the build
// of its store. `graph context --allow-ancestor N` may answer an exact miss
// from the store of one of the N nearest first-parent ancestors, with every
// record that belongs to a file changed since then withheld and the answer
// marked stale. It exists for `graph context` only: `graph store` and the
// store writer stay exact, and nothing on this path can produce a value they
// accept (TestAncestorStoreValuesCannotReachStoreWriterOrDescriptor).

// MaxStoreContextAncestorDistance caps --allow-ancestor. Measured on this
// repository, relations touching files changed since HEAD~5 are 3.5% of the
// graph; at HEAD~10 they are 14.6%.
const MaxStoreContextAncestorDistance = 5

// storeContextAncestorMaxChangedPercent is the largest share of the ancestor
// store's indexed files that may have changed for it to serve.
const storeContextAncestorMaxChangedPercent = 5

// StoreContextStale is the typed disclosure attached to an ancestor-served
// context answer. Its absence means the answer is exact. Field names are a
// consumer contract (docs/store-contract.md); do not rename them.
type StoreContextStale struct {
	// AncestorCommit and AncestorTree identify the first-parent ancestor whose
	// store answered. The response's top-level commit and tree keep their
	// meaning: what the served store records.
	AncestorCommit string `json:"ancestor_commit"`
	AncestorTree   string `json:"ancestor_tree"`
	// CommitsBehind is the first-parent distance from the requested commit,
	// 1..MaxStoreContextAncestorDistance.
	CommitsBehind int `json:"commits_behind"`
	// ChangedFiles is every path that differs between AncestorTree and the
	// requested tree, sorted, never truncated: both sides of a rename, deleted
	// paths, and added paths the ancestor never saw. When the list cannot be
	// complete the ancestor is refused instead.
	ChangedFiles []string `json:"changed_files"`
	// ExcludedRecords counts what the answer withheld because it belongs to a
	// changed file: task-matched symbols and tests in changed files, plus
	// relations of the returned symbols whose other endpoint is in one.
	ExcludedRecords int `json:"excluded_records"`
}

// ancestorSearchStore is the only handle the ancestor path produces. It holds
// a path and a digest to read, never a cacheEntry, a snapshot or a
// descriptor, so it cannot be handed to the store writer or rendered by
// `graph store`.
type ancestorSearchStore struct {
	path        string
	entryDigest string
	stale       StoreContextStale
}

// ancestorStoreRefusal says why no ancestor could serve. Callers outside this
// file never see it: every refusal surfaces as the exact path's own miss.
type ancestorStoreRefusal struct {
	reason string
	cause  error
}

func (refusal *ancestorStoreRefusal) Error() string {
	if refusal.cause == nil {
		return "ancestor store refused: " + refusal.reason
	}
	return fmt.Sprintf("ancestor store refused: %s: %v", refusal.reason, refusal.cause)
}

func (refusal *ancestorStoreRefusal) Unwrap() error { return refusal.cause }

// errStoreContextExclusionUnrepresentable marks an exclusion set the store
// query cannot apply exactly; the ancestor is refused rather than served with
// a filter that might miss.
var errStoreContextExclusionUnrepresentable = errors.New("store context exclusion cannot be represented exactly")

// LoadSearchStoreContextAllowingAncestor answers exactly when it can. On an
// exact miss, and only then, it tries the nearest first-parent ancestor
// within maxBehind (capped at MaxStoreContextAncestorDistance) whose store
// was built with the same key except for the tree. Any refusal returns the
// exact path's own result and error unchanged, so a caller cannot tell a
// refused fallback from not asking for one. maxBehind <= 0 is the exact call.
func LoadSearchStoreContextAllowingAncestor(ctx context.Context, repo, providerVersion, cacheDir, task string, options ProviderSnapshotOptions, limit, testsLimit, relationsLimit, maxBehind int) (SearchStoreContext, error) {
	exact, exactErr := LoadSearchStoreContext(ctx, repo, providerVersion, cacheDir, task, options, limit, testsLimit, relationsLimit)
	if exactErr == nil || maxBehind <= 0 || !errors.Is(exactErr, ErrSearchStoreMissing) {
		return exact, exactErr
	}
	served, err := loadSearchStoreContextFromAncestor(ctx, repo, providerVersion, cacheDir, task, options, limit, testsLimit, relationsLimit, maxBehind)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SearchStoreContext{}, ctxErr
		}
		return exact, exactErr
	}
	return served, nil
}

func loadSearchStoreContextFromAncestor(ctx context.Context, repo, providerVersion, cacheDir, task string, options ProviderSnapshotOptions, limit, testsLimit, relationsLimit, maxBehind int) (SearchStoreContext, error) {
	ancestor, err := resolveAncestorSearchStore(ctx, repo, providerVersion, cacheDir, options, maxBehind)
	if err != nil {
		return SearchStoreContext{}, err
	}
	exclude := make(map[string]struct{}, len(ancestor.stale.ChangedFiles))
	for _, path := range ancestor.stale.ChangedFiles {
		exclude[path] = struct{}{}
	}
	result, excluded, err := querySearchStoreContext(ctx, ancestor.path, ancestor.entryDigest, ancestor.stale.AncestorTree,
		task, limit, testsLimit, relationsLimit, exclude)
	if err != nil {
		return SearchStoreContext{}, &ancestorStoreRefusal{reason: "query", cause: err}
	}
	stale := ancestor.stale
	stale.ExcludedRecords = excluded
	result.Stale = &stale
	return result, nil
}

// resolveAncestorSearchStore finds the newest first-parent ancestor, at most
// maxBehind commits back, that has an exact store under the request's key
// with only the tree term replaced. Provider version, profile, scope, file
// caps, built-in rules and the captured ignore/include policy bytes all stay
// the request's, so an ancestor built under another version or policy keys
// elsewhere and is simply not found.
//
// Only the newest ancestor with a store is considered. It serves only when
// the complete list of paths changed between its tree and the requested tree
// is known and is at most storeContextAncestorMaxChangedPercent of the files
// it indexed; anything else is a refusal, never a partial list.
func resolveAncestorSearchStore(ctx context.Context, repo, providerVersion, cacheDir string, options ProviderSnapshotOptions, maxBehind int) (ancestorSearchStore, error) {
	refuse := func(reason string, cause error) (ancestorSearchStore, error) {
		return ancestorSearchStore{}, &ancestorStoreRefusal{reason: reason, cause: cause}
	}
	if maxBehind <= 0 {
		return refuse("disabled", nil)
	}
	maxBehind = min(maxBehind, MaxStoreContextAncestorDistance)
	if cacheDir == "" {
		return refuse("no cache directory", nil)
	}
	options.Worktree = false
	options.OnlyFiles = nil
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return refuse("repository path", err)
	}
	options, err = CaptureProviderCachePolicy(absRepo, options)
	if err != nil {
		return refuse("ignore policy", err)
	}
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return refuse("resolve HEAD", err)
	}
	// The changed-path list must be in the store's own coordinates: relative
	// to the checkout scope the key binds. A scope that fell back to the
	// absolute path (Git could not report a prefix) has no such coordinates.
	prefix, err := gitutil.RepoPrefix(ctx, absRepo)
	if err != nil {
		return refuse("checkout scope", err)
	}
	prefix = filepath.ToSlash(prefix)
	if scope := cacheCheckoutScope(absRepo, ctx); scope != "prefix:"+prefix {
		return refuse("checkout scope", fmt.Errorf("cache scope %q does not match prefix %q", scope, prefix))
	}
	chain, err := gitutil.FirstParentCommitTrees(ctx, absRepo, commit, maxBehind+1)
	if err != nil {
		return refuse("first-parent history", err)
	}
	if len(chain) == 0 || chain[0].Tree != tree {
		return refuse("first-parent history", errors.New("walk does not start at the requested tree"))
	}
	repositoryKey := repoKey(ctx, absRepo)
	for behind := 1; behind < len(chain); behind++ {
		candidate := chain[behind]
		if candidate.Tree == tree {
			// Same key as the exact lookup that just missed.
			continue
		}
		key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, candidate.Tree, options, ctx)
		if err != nil {
			return refuse("ancestor key", err)
		}
		store, db, err := openExactSearchStore(ctx, cacheDir, key, candidate.Tree)
		if errors.Is(err, ErrSearchStoreMissing) {
			continue
		}
		if err != nil {
			return refuse("ancestor store unreadable", err)
		}
		var indexedFiles int
		err = db.QueryRowContext(ctx, `SELECT count(*) FROM files`).Scan(&indexedFiles)
		_ = db.Close()
		if err != nil {
			return refuse("ancestor store unreadable", err)
		}
		changed, err := gitutil.TreeChangedPaths(ctx, absRepo, candidate.Tree, tree, prefix)
		if err != nil {
			return refuse("changed paths", err)
		}
		for _, path := range changed {
			// JSON cannot carry invalid UTF-8 byte for byte, so such a path
			// would reach the consumer altered: the list would not be the list.
			if !utf8.ValidString(path) {
				return refuse("changed paths", fmt.Errorf("path %q is not valid UTF-8", path))
			}
		}
		if len(changed)*100 > storeContextAncestorMaxChangedPercent*indexedFiles {
			return refuse("too many changed files", fmt.Errorf("%d changed of %d indexed", len(changed), indexedFiles))
		}
		if changed == nil {
			changed = []string{}
		}
		return ancestorSearchStore{
			path:        store.path,
			entryDigest: store.entryDigest,
			stale: StoreContextStale{
				AncestorCommit: candidate.Commit,
				AncestorTree:   candidate.Tree,
				CommitsBehind:  behind,
				ChangedFiles:   changed,
			},
		}, nil
	}
	return refuse("no ancestor store", fmt.Errorf("none within %d first-parent commits", maxBehind))
}
