package sem

import (
	"context"
	"os"
	"path/filepath"
)

// headIndexedProfiles are the profiles whose committed-tree entry counts as "HEAD is indexed".
// `index` builds full by default and `query --head` builds fast by default, so either one means a
// later lookup at this HEAD is served from the cache rather than built.
var headIndexedProfiles = []Profile{ProfileFull, ProfileFast}

// HeadIndexed reports whether a committed-tree search cache entry exists for HEAD under any of
// headIndexedProfiles, with the repository's default policy inputs (its .graphignore, no explicit
// ignore or include files). It is the status line's indexed check, so it must stay cheap: it
// resolves HEAD and the entry keys, then only stats each entry's path. It never opens or hashes an
// entry or its sidecar, which is why DescribeSearchStore (a SHA-256 of the whole entry) is not used.
//
// An unborn or unresolvable HEAD, or metadata git may not be run on, is the "not indexed" state
// rather than an error: there is nothing at HEAD to have indexed. An error is returned only when the
// policy inputs or the key cannot be derived.
//
// Entry paths come from newCacheEntry, the helper every search cache reader and writer addresses
// entries through, so a change to the on-disk layout changes this check with it.
func HeadIndexed(ctx context.Context, repo, providerVersion, cacheDir string) (bool, error) {
	if cacheDir == "" {
		return false, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return false, err
	}
	_, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return false, nil //nolint:nilerr // an unresolvable HEAD is a state, not a failure
	}
	policy, err := CaptureProviderCachePolicy(absRepo, ProviderSnapshotOptions{})
	if err != nil {
		return false, err
	}
	repositoryKey := repoKey(ctx, absRepo)
	for _, profile := range headIndexedProfiles {
		options := policy
		options.Profile = profile
		key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, options, ctx)
		if err != nil {
			return false, err
		}
		entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
		if err != nil {
			return false, err
		}
		if entry.exists() {
			return true, nil
		}
	}
	return false, nil
}

// exists stats the entry through the same confined root its readers open it through. Anything that
// stops the stat (a missing cache directory or entry, a component that escapes the root) is
// absence: a reader would miss on the same entry, so it is not indexed.
func (entry cacheEntry) exists() bool {
	root, err := os.OpenRoot(entry.root)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Stat(entry.relative)
	return err == nil && info.Mode().IsRegular()
}
