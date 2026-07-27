package sem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func cacheTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runCacheGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = repo
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "app.go"),
		[]byte("package app\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCacheGit("init")
	runCacheGit("config", "user.name", "Entire Graph Test")
	runCacheGit("config", "user.email", "graph@example.com")
	runCacheGit("add", ".")
	runCacheGit("commit", "-m", "initial")
	return repo
}

// A working-tree snapshot of a CLEAN tree is exactly a snapshot of HEAD's
// content, so it may be cached — that is what stops the relation verbs, which
// index the whole repository, from re-indexing on every call. A dirty tree must
// still bypass the cache entirely.
func TestWorktreeSnapshotCachesOnlyWhileTreeMatchesHEAD(t *testing.T) {
	repo := cacheTestRepo(t)
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}

	first, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("cold cache reported a hit")
	}
	if len(first.Symbols) == 0 {
		t.Fatal("cold build produced no symbols")
	}

	second, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("clean worktree query did not reuse the cached index")
	}
	if len(second.Symbols) != len(first.Symbols) {
		t.Fatalf("cached snapshot differs: %d symbols vs %d", len(second.Symbols), len(first.Symbols))
	}
	// A working-tree snapshot keeps its provenance warning; it must not be
	// silently replaced by a committed-tree entry.
	if !hasWarningCode(second.Header.Warnings, "W_WORKTREE_SNAPSHOT") {
		t.Fatalf("cached worktree snapshot lost its provenance warning: %#v", second.Header.Warnings)
	}

	if err := os.WriteFile(filepath.Join(repo, "app.go"),
		[]byte("package app\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\nfunc Delta() { Beta() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	InvalidateWorktreeCleanVerdicts()
	dirty, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("dirty worktree was served a cached index")
	}
	if len(dirty.Symbols) <= len(first.Symbols) {
		t.Fatalf("dirty-tree edit is not visible: %d symbols vs %d", len(dirty.Symbols), len(first.Symbols))
	}
}

// An untracked file is indexed by a working-tree walk but absent from HEAD, so
// its presence alone must disable caching.
func TestWorktreeSnapshotBypassesCacheForUntrackedFiles(t *testing.T) {
	repo := cacheTestRepo(t)
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}
	if _, _, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "extra.go"), []byte("package app\n\nfunc Extra() { Beta() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	InvalidateWorktreeCleanVerdicts()
	snapshot, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("untracked file was hidden by a cached index")
	}
	if !hasSymbolNamed(snapshot.Symbols, "Extra") {
		t.Fatal("untracked file was not indexed")
	}
}

// Committed-tree and working-tree entries occupy separate key spaces: neither
// may be served for the other, because their provenance differs.
func TestWorktreeAndHeadSnapshotEntriesDoNotShare(t *testing.T) {
	repo := cacheTestRepo(t)
	cacheDir := t.TempDir()
	head := ProviderSnapshotOptions{Worktree: false, Profile: ProfileFull, NoNetwork: true}
	worktree := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}

	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", head, cacheDir, false); err != nil || hit {
		t.Fatalf("cold head build = (hit %v, err %v)", hit, err)
	}
	// The head entry exists now; a worktree query must still build its own.
	InvalidateWorktreeCleanVerdicts()
	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", worktree, cacheDir, false); err != nil || hit {
		t.Fatalf("worktree query reused a committed-tree entry: (hit %v, err %v)", hit, err)
	}
	// And each view now hits its own entry.
	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", head, cacheDir, false); err != nil || !hit {
		t.Fatalf("head re-query = (hit %v, err %v)", hit, err)
	}
	InvalidateWorktreeCleanVerdicts()
	worktreeSnapshot, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", worktree, cacheDir, false)
	if err != nil || !hit {
		t.Fatalf("worktree re-query = (hit %v, err %v)", hit, err)
	}
	if !hasWarningCode(worktreeSnapshot.Header.Warnings, "W_WORKTREE_SNAPSHOT") {
		t.Fatal("worktree entry served without its provenance warning")
	}
}

// A repository with no commits has no tree to key on; caching must simply be off.
func TestWorktreeSnapshotWithoutCommittedHEADIsNotCached(t *testing.T) {
	repo := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = repo
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n\nfunc Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}
	cacheDir := t.TempDir()
	for attempt := 0; attempt < 2; attempt++ {
		InvalidateWorktreeCleanVerdicts()
		if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false); err != nil || hit {
			t.Fatalf("attempt %d = (hit %v, err %v), want (false, nil)", attempt, hit, err)
		}
	}
}

func hasWarningCode(warnings []ProviderWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func hasSymbolNamed(symbols []SymbolRecord, name string) bool {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return true
		}
	}
	return false
}
