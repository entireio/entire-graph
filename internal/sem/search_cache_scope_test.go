package sem

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

// scopeFixture commits a repository whose root and pkg/ subtree index to
// different graphs at one tree: main.go exists only at the root, and pkg/'s
// paths are written relative to pkg/ when it is the --repo. The remote gives
// every checkout and subdirectory one gh/ repository key, the key class the
// records cache keeps whole, so nothing but the scope can separate them.
func scopeFixture(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "remote", "add", "origin", "https://github.com/example/scope.git")
	write(t, repo, "go.mod", "module example.com/m\n\ngo 1.22\n")
	write(t, repo, "pkg/a.go", "package pkg\n\nfunc Alpha(x int) int { return Beta(x) + 1 }\n\nfunc Beta(y int) int { return y * 2 }\n")
	write(t, repo, "main.go", "package main\n\nimport \"example.com/m/pkg\"\n\nfunc main() { println(pkg.Alpha(3)) }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "initial")
}

// scopeWorktrees returns two linked worktrees of one scopeFixture repository.
func scopeWorktrees(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	write(t, first, ".keep", "")
	scopeFixture(t, first)
	second := filepath.Join(parent, "second")
	git(t, first, "worktree", "add", "-q", "--detach", second)
	return first, second
}

// TestCommittedTreeCacheKeysCarryTheCheckoutScope pins the key term itself: the
// root and a subdirectory of one checkout share the tree, the repository key and
// every option, so only the scope term separates them, while the same scope in
// two worktrees must still address one entry.
func TestCommittedTreeCacheKeysCarryTheCheckoutScope(t *testing.T) {
	first, second := scopeWorktrees(t)
	commit, tree, err := resolveCommittedHEAD(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	type keyFunc func(absRepo string) (string, error)
	for name, key := range map[string]keyFunc{
		"search": func(absRepo string) (string, error) {
			return searchSnapshotKey(absRepo, "gh/example/scope", "v", tree, options)
		},
		"records": func(absRepo string) (string, error) {
			return providerRecordsKey(absRepo, "gh/example/scope", "v", commit, tree, "snapshot", options)
		},
	} {
		t.Run(name, func(t *testing.T) {
			mustKey := func(absRepo string) string {
				t.Helper()
				value, err := key(absRepo)
				if err != nil {
					t.Fatal(err)
				}
				return value
			}
			root, sub := mustKey(first), mustKey(filepath.Join(first, "pkg"))
			if root == sub {
				t.Fatalf("the repository root and its pkg/ subdirectory share key %s", root)
			}
			if other := mustKey(filepath.Join(second, "pkg")); other != sub {
				t.Fatalf("pkg/ in two worktrees of one tree got different keys %s and %s; the path-free key no longer shares", sub, other)
			}
			if other := mustKey(second); other != root {
				t.Fatalf("two worktree roots of one tree got different keys %s and %s", root, other)
			}
		})
	}
}

// TestSearchCacheNeverServesOneScopeToAnother is the reported defect end to
// end, in both orders: whichever of the root and pkg/ is indexed first, the
// other is a miss and is served exactly what a cold build of it produces.
// Before the scope term a root query after a pkg/ one was a hit with none of
// the root's symbols, and pkg/ after the root was served root-relative paths.
func TestSearchCacheNeverServesOneScopeToAnother(t *testing.T) {
	repo := t.TempDir()
	scopeFixture(t, repo)
	root, sub := repo, filepath.Join(repo, "pkg")
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	for _, order := range [][2]string{{sub, root}, {root, sub}} {
		t.Run(filepath.Base(order[0])+"-then-"+filepath.Base(order[1]), func(t *testing.T) {
			cacheDir := t.TempDir()
			if _, hit, err := PreindexProviderSnapshot(t.Context(), order[0], "test-version", options, cacheDir); err != nil || hit {
				t.Fatalf("first preindex: hit=%v err=%v", hit, err)
			}
			served, hit, err := PreindexProviderSnapshot(t.Context(), order[1], "test-version", options, cacheDir)
			if err != nil {
				t.Fatal(err)
			}
			if hit {
				t.Fatalf("%s was served the entry %s wrote", order[1], order[0])
			}
			cold, err := BuildProviderSnapshotWithOptions(t.Context(), order[1], "test-version", options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(served, cold) {
				t.Fatalf("%s served a snapshot that differs from its cold build: %d vs %d symbols", order[1], len(served.Symbols), len(cold.Symbols))
			}
			// And through the selective search path the reviewer reproduced with.
			response, err := SearchRepository(t.Context(), order[1], "test-version", "main println Alpha", SearchOptions{
				Profile: ProfileFull, TopK: 10, CacheDir: cacheDir,
			})
			if err != nil {
				t.Fatal(err)
			}
			uncached, err := SearchRepository(t.Context(), order[1], "test-version", "main println Alpha", SearchOptions{
				Profile: ProfileFull, TopK: 10, DisableCache: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(response.Results, uncached.Results) || len(uncached.Results) == 0 {
				t.Fatalf("cached search in %s differs from an uncached one:\ncached=%#v\nuncached=%#v", order[1], response.Results, uncached.Results)
			}
		})
	}
}

// TestSearchCacheSharesOneScopeAcrossWorktrees pins the other half: pkg/ in a
// second worktree is served the entry pkg/ in the first one wrote, rebound to
// its own root, and that is exactly its cold build.
func TestSearchCacheSharesOneScopeAcrossWorktrees(t *testing.T) {
	first, second := scopeWorktrees(t)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	cacheDir := t.TempDir()
	if _, hit, err := PreindexProviderSnapshot(t.Context(), filepath.Join(first, "pkg"), "test-version", options, cacheDir); err != nil || hit {
		t.Fatalf("first preindex: hit=%v err=%v", hit, err)
	}
	served, hit, err := PreindexProviderSnapshot(t.Context(), filepath.Join(second, "pkg"), "test-version", options, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("pkg/ in a second worktree of the same tree missed the entry pkg/ in the first wrote")
	}
	cold, err := BuildProviderSnapshotWithOptions(t.Context(), filepath.Join(second, "pkg"), "test-version", options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(served, cold) {
		t.Fatalf("shared pkg/ entry differs from a cold build in the second worktree:\nserved=%#v\ncold=%#v", served.Header, cold.Header)
	}
}

// TestSearchCacheRestampsOnlyWithinOneScope pins the loader's own check, which
// holds even if an entry reaches a key it does not belong to (a pre-scope
// entry, a hand edit, a key collision): an entry recorded for another scope is
// never adopted by a different checkout.
func TestSearchCacheRestampsOnlyWithinOneScope(t *testing.T) {
	first, second := scopeWorktrees(t)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	cacheDir := t.TempDir()
	firstPkg, secondPkg := filepath.Join(first, "pkg"), filepath.Join(second, "pkg")
	if _, _, err := PreindexProviderSnapshot(t.Context(), firstPkg, "test-version", options, cacheDir); err != nil {
		t.Fatal(err)
	}
	captured, err := CaptureProviderCachePolicy(firstPkg, options)
	if err != nil {
		t.Fatal(err)
	}
	commit, tree, err := resolveCommittedHEAD(t.Context(), firstPkg)
	if err != nil {
		t.Fatal(err)
	}
	key, err := searchSnapshotKey(firstPkg, repoKey(t.Context(), firstPkg), "test-version", tree, captured)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RepoScope != "prefix:pkg/" {
		t.Fatalf("premise lost: the pkg/ entry records scope %q", stored.RepoScope)
	}
	secondKey := repoKey(t.Context(), secondPkg)
	if _, ok := loadCachedSearchSnapshot(entry, secondPkg, commit, secondKey, "test-version", tree, captured); !ok {
		t.Fatal("control: the same-scope entry did not load in the second worktree")
	}
	stored.RepoScope = "prefix:"
	if err := writeSearchSnapshot(entry, stored); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadCachedSearchSnapshot(entry, secondPkg, commit, secondKey, "test-version", tree, captured); ok {
		t.Fatal("an entry recorded for the root scope was restamped for pkg/ in another worktree")
	}
	// Its own checkout needs no restamp and is not affected by the check.
	if _, ok := loadCachedSearchSnapshot(entry, firstPkg, commit, repoKey(t.Context(), firstPkg), "test-version", tree, captured); !ok {
		t.Fatal("the checkout that wrote the entry stopped loading it")
	}
}

// storeScopeRecords stores a one-symbol record stream for repo through the
// records cache and returns the header it wrote.
func storeScopeRecords(t *testing.T, repo, cacheDir string) SnapshotHeader {
	t.Helper()
	ctx := t.Context()
	commit, tree, err := resolveCommittedHEAD(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginProviderRecordsCache(ctx, repo, "test-v1", commit, tree, "snapshot", cacheDir, ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	header := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: "test-v1",
		RepoKey:         transaction.repositoryKey,
		RepoRoot:        transaction.absRepo,
		Commit:          commit,
		Tree:            tree,
		Profile:         string(transaction.options.Profile),
	}
	spool, err := spoolRecords(transaction, encodeRecordStreamForTest(t, header, map[string]string{"record_type": "symbol", "path": "a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Store(spool, nil, header); err != nil {
		t.Fatal(err)
	}
	return header
}

func loadScopeRecords(t *testing.T, repo, cacheDir string) (*ProviderRecordsCacheTransaction, []byte, bool) {
	t.Helper()
	commit, tree, err := resolveCommittedHEAD(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginProviderRecordsCache(t.Context(), repo, "test-v1", commit, tree, "snapshot", cacheDir, ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	records, _, hit := transaction.Load()
	return transaction, records, hit
}

// TestProviderRecordsCacheNeverReplaysOneScopeToAnother is the records-cache
// half of the defect, in both orders: a warm `snapshot --repo X` after a
// `--repo X/pkg` run replayed pkg/'s stream (2 symbols of 3) for the root.
func TestProviderRecordsCacheNeverReplaysOneScopeToAnother(t *testing.T) {
	repo := t.TempDir()
	scopeFixture(t, repo)
	root, sub := repo, filepath.Join(repo, "pkg")
	if repoKey(t.Context(), root) != repoKey(t.Context(), sub) {
		t.Fatalf("premise lost: root and pkg/ must share one repository key, got %q and %q", repoKey(t.Context(), root), repoKey(t.Context(), sub))
	}
	for _, order := range [][2]string{{sub, root}, {root, sub}} {
		t.Run(filepath.Base(order[0])+"-then-"+filepath.Base(order[1]), func(t *testing.T) {
			cacheDir := t.TempDir()
			storeScopeRecords(t, order[0], cacheDir)
			if _, _, hit := loadScopeRecords(t, order[0], cacheDir); !hit {
				t.Fatal("control: the stored stream did not replay in its own checkout")
			}
			if _, records, hit := loadScopeRecords(t, order[1], cacheDir); hit {
				t.Fatalf("%s replayed the stream %s stored:\n%s", order[1], order[0], records)
			}
		})
	}
}

// TestProviderRecordsCacheSharesOneScopeAcrossWorktrees: pkg/ in a second
// worktree replays pkg/'s stream from the first, restamped to its own root;
// and an envelope recorded for another scope is never restamped.
func TestProviderRecordsCacheSharesOneScopeAcrossWorktrees(t *testing.T) {
	first, second := scopeWorktrees(t)
	cacheDir := t.TempDir()
	header := storeScopeRecords(t, filepath.Join(first, "pkg"), cacheDir)
	transaction, records, hit := loadScopeRecords(t, filepath.Join(second, "pkg"), cacheDir)
	if !hit {
		t.Fatal("pkg/ in a second worktree missed the stream pkg/ in the first stored")
	}
	header.RepoRoot = transaction.absRepo
	if want := encodeRecordStreamForTest(t, header, map[string]string{"record_type": "symbol", "path": "a.go"}); !bytes.Equal(records, want) {
		t.Fatalf("replayed stream:\n%s\nwant:\n%s", records, want)
	}

	stored, err := readProviderRecords(transaction.entry)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RepoScope != "prefix:pkg/" {
		t.Fatalf("premise lost: the pkg/ envelope records scope %q", stored.RepoScope)
	}
	stored.RepoScope = "prefix:"
	if err := writeProviderRecords(transaction.entry, stored); err != nil {
		t.Fatal(err)
	}
	if _, records, hit := loadScopeRecords(t, filepath.Join(second, "pkg"), cacheDir); hit {
		t.Fatalf("an envelope recorded for the root scope was restamped for pkg/:\n%s", records)
	}
}
