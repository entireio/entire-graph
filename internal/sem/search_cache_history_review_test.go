package sem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSearchCacheRefusesHistoryRefreshAfterMetadataChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git stub")
	}
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	cache := t.TempDir()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "history-test", options, cache)
	if err != nil {
		t.Fatal(err)
	}
	commit, tree := snapshot.Header.Commit, snapshot.Header.Tree
	key, err := searchSnapshotKey(repo, snapshot.Header.RepoKey, "history-test", tree, options, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(cache, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit := loadCachedSearchSnapshot(entry, repo, commit, snapshot.Header.RepoKey, "history-test", tree, options, t.Context()); !hit {
		t.Fatal("control: cached snapshot must be available")
	}
	// An include is refused on every Git version. (A promisor remote no longer
	// works as the trigger: Git 2.45+ honours GIT_NO_LAZY_FETCH, so it is admitted.)
	git(t, repo, "config", "include.path", "../outside")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "history-called")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	stub := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = log ]; then\nprintf reached > " + quote(marker) + "\nexit 37\nfi\ndone\nexec " + quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, hit := loadCachedSearchSnapshot(entry, repo, commit, snapshot.Header.RepoKey, "history-test", tree, options, t.Context()); hit {
		t.Error("unsafe metadata must prevent the history refresh and cache hit")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("history Git subprocess ran after metadata became unsafe")
	}
}

func TestSearchCacheRejectsFailedOrCanceledHistoryRefresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX failing Git stub")
	}
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	validated, err := WithGitMetadataValidationForSetup(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	snapshot, _, err := PreindexProviderSnapshot(validated, repo, "history-test", options, cache)
	if err != nil {
		t.Fatal(err)
	}
	commit, tree, err := resolveCommittedHEAD(validated, repo)
	if err != nil {
		t.Fatal(err)
	}
	key, err := searchSnapshotKey(repo, snapshot.Header.RepoKey, "history-test", tree, options, validated)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(cache, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit := loadCachedSearchSnapshot(entry, repo, commit, snapshot.Header.RepoKey, "history-test", tree, options, validated); !hit {
		t.Fatal("control: history-capable cached snapshot must be a hit")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 37\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	canceled, cancel := context.WithCancel(validated)
	cancel()
	for _, ctx := range []context.Context{validated, canceled} {
		if _, hit := loadCachedSearchSnapshot(entry, repo, commit, snapshot.Header.RepoKey, "history-test", tree, options, ctx); hit {
			t.Fatal("unavailable history was silently dropped while reporting a cache hit")
		}
	}
}

func TestSearchCacheRefreshesHistoryAtTheSameTree(t *testing.T) {
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	cache := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	warm, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, cache)
	if err != nil || hit {
		t.Fatalf("warm: %v %v", hit, err)
	}
	paths := []string{"ledger/ledger.go", "web/client.ts"}
	originals := make(map[string][]byte)
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = content
		write(t, repo, path, string(content)+"\n// history change\n")
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "history change")
	for _, path := range paths {
		write(t, repo, path, string(originals[path]))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "restore tree")
	served, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, cache)
	if err != nil || !hit {
		t.Fatalf("same-tree load: %v %v", hit, err)
	}
	cold, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", options)
	if err != nil {
		t.Fatal(err)
	}
	history := func(s ProviderSnapshot) []RelationRecord {
		var out []RelationRecord
		for _, r := range s.Relations {
			if r.Type == "FILE_CHANGES_WITH" {
				out = append(out, r)
			}
		}
		return out
	}
	if reflect.DeepEqual(history(warm), history(cold)) {
		t.Fatal("fixture histories did not differ")
	}
	if !reflect.DeepEqual(history(served), history(cold)) || served.Header.Stats.Relations != cold.Header.Stats.Relations || !reflect.DeepEqual(served.Header.Completeness.Relations, cold.Header.Completeness.Relations) {
		t.Fatal("cache reused another history or stale counts")
	}
}
