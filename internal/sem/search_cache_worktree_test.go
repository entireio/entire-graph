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

// Worktree queries always rebuild until raw worktree equality can be checked
// without invoking repository-selected Git conversion filters.
func TestWorktreeSnapshotAlwaysBypassesCache(t *testing.T) {
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
	if hit {
		t.Fatal("clean worktree query reused the cache")
	}
	if len(second.Symbols) != len(first.Symbols) {
		t.Fatalf("rebuilt snapshot differs: %d symbols vs %d", len(second.Symbols), len(first.Symbols))
	}
	// A working-tree snapshot keeps its provenance warning; it must not be
	// silently replaced by a committed-tree entry.
	if !hasWarningCode(second.Header.Warnings, "W_WORKTREE_SNAPSHOT") {
		t.Fatalf("rebuilt worktree snapshot lost its provenance warning: %#v", second.Header.Warnings)
	}

	if err := os.WriteFile(filepath.Join(repo, "app.go"),
		[]byte("package app\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\nfunc Delta() { Beta() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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

// Working-tree snapshots always bypass persistence; this case verifies that
// the resulting rebuild observes an untracked source file.
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

// A committed-tree entry must never serve a worktree query, whose mutable
// provenance requires a fresh build.
func TestWorktreeAndHeadSnapshotEntriesDoNotShare(t *testing.T) {
	repo := cacheTestRepo(t)
	cacheDir := t.TempDir()
	head := ProviderSnapshotOptions{Worktree: false, Profile: ProfileFull, NoNetwork: true}
	worktree := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}

	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", head, cacheDir, false); err != nil || hit {
		t.Fatalf("cold head build = (hit %v, err %v)", hit, err)
	}
	// The head entry exists now; a worktree query must still build its own.
	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", worktree, cacheDir, false); err != nil || hit {
		t.Fatalf("worktree query reused a committed-tree entry: (hit %v, err %v)", hit, err)
	}
	// The committed view hits its entry, while the worktree still rebuilds.
	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", head, cacheDir, false); err != nil || !hit {
		t.Fatalf("head re-query = (hit %v, err %v)", hit, err)
	}
	worktreeSnapshot, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", worktree, cacheDir, false)
	if err != nil || hit {
		t.Fatalf("worktree re-query = (hit %v, err %v)", hit, err)
	}
	if !hasWarningCode(worktreeSnapshot.Header.Warnings, "W_WORKTREE_SNAPSHOT") {
		t.Fatal("rebuilt worktree snapshot omitted its provenance warning")
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

// Worktree bypass is unconditional, including when every new path is a file no
// parser opens. Cache behavior must not depend on a path-eligibility heuristic.
func TestWorktreeSnapshotBypassesCacheWithUnindexableUntrackedFiles(t *testing.T) {
	repo := cacheTestRepo(t)
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}

	first, _, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}

	// Every name here carries an extension that maps to no language, proving
	// that unconditional bypass does not depend on whether a parser claims it.
	for name, content := range map[string]string{
		".DS_Store":           "\x00\x00binary junk",
		"entire-graph.bin":    "\x7fELF not source",
		"build.output.tar.gz": "\x1f\x8b",
		"notes.swp":           "vim swap",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("worktree snapshot reused the cache with unindexable untracked files")
	}
	if len(second.Symbols) != len(first.Symbols) {
		t.Fatalf("rebuilt snapshot differs: %d symbols vs %d", len(second.Symbols), len(first.Symbols))
	}

	// An extensionless untracked file also leaves unconditional bypass intact.
	if err := os.WriteFile(filepath.Join(repo, "somebinary"), []byte("\x7fELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("extensionless untracked file was forgiven; shebang scripts would be missed")
	}
	if err := os.Remove(filepath.Join(repo, "somebinary")); err != nil {
		t.Fatal(err)
	}
	// Adding one indexable source verifies that the subsequent rebuild exposes it.
	if err := os.WriteFile(filepath.Join(repo, "extra.go"), []byte("package app\n\nfunc Extra() { Beta() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("an indexable untracked file was hidden by a cached index")
	}
	if !hasSymbolNamed(third.Symbols, "Extra") {
		t.Fatal("indexable untracked file was not indexed")
	}
}
