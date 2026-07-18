package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPreindexProviderSnapshotServesSelectiveSearch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for index := 0; index < 12; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%02d.go", index), fmt.Sprintf(
			"package noise\nfunc Noise%d() int { return %d }\n", index, index,
		))
	}
	write(t, repo, "target/needle.go", `package target

// NeedleTarget handles the query-independent preindex request.
func NeedleTarget() bool { return true }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	preindexed, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit the cache")
	}
	if len(preindexed.Files) != 13 {
		t.Fatalf("preindex files = %d, want 13", len(preindexed.Files))
	}

	options := SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 1,
		CacheDir:        cacheDir,
	}
	cached, err := SearchRepository(t.Context(), repo, "test-version", "NeedleTarget preindex request", options)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.Stats.IndexCacheHit {
		t.Fatal("selective search did not reuse the complete preindex cache")
	}
	if cached.Stats.FilesIndexed != 1 {
		t.Fatalf("selective search indexed %d files, want 1", cached.Stats.FilesIndexed)
	}
	if len(cached.Results) == 0 || cached.Results[0].SymbolName != "NeedleTarget" {
		t.Fatalf("preindexed search lost target: %#v", cached.Results)
	}

	options.DisableCache = true
	uncached, err := SearchRepository(t.Context(), repo, "test-version", "NeedleTarget preindex request", options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached.Results, uncached.Results) {
		t.Fatalf("full-cache selective view changed retrieval: cached=%#v uncached=%#v", cached.Results, uncached.Results)
	}

	_, secondHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !secondHit {
		t.Fatal("second preindex did not reuse the complete cache")
	}
}

func TestIndexAllFilesSearchWritesCanonicalFullSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	_, err := SearchRepository(t.Context(), repo, "test-version", "validate token", SearchOptions{
		Profile:       ProfileSyntaxOnly,
		IndexAllFiles: true,
		CacheDir:      cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("index-all-files search did not populate the canonical full-snapshot cache")
	}
}

func TestPreindexProviderSnapshotRejectsWorktreeAndMissingCache(t *testing.T) {
	if _, _, err := PreindexProviderSnapshot(t.Context(), t.TempDir(), "test", ProviderSnapshotOptions{Worktree: true}, t.TempDir()); err == nil {
		t.Fatal("expected worktree preindex to fail")
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), t.TempDir(), "test", ProviderSnapshotOptions{}, ""); err == nil {
		t.Fatal("expected preindex without a cache directory to fail")
	}
}

func TestPreindexProviderSnapshotSurfacesPersistenceFailure(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cacheDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir); err == nil {
		t.Fatal("expected an unwritable cache path to fail preindex")
	}
}

func TestPreindexProviderSnapshotReusesTreeAcrossCommitsWithCurrentProvenance(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	first, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}
	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	second, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("same-tree commit did not reuse preindex")
	}
	if second.Header.Tree != first.Header.Tree || second.Header.Commit == first.Header.Commit {
		t.Fatalf("cache provenance was not refreshed: first=%s/%s second=%s/%s",
			first.Header.Commit, first.Header.Tree, second.Header.Commit, second.Header.Tree,
		)
	}
}
