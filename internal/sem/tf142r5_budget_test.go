package sem

import (
	"testing"
)

// tf142r5SelectiveRepo is deliberately small. The derivation findings are about
// WHICH CONTEXT the warm-cache path runs on, not about how long the work takes,
// so the fixture only has to be big enough to produce relations.
func tf142r5SelectiveRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "deep.js", "function helper(x) {\n  return x + 1;\n}\n\nfunction caller(y) {\n  return helper(y) + helper(y + 1);\n}\n")
	writeFile(t, repo, "other.js", "function unrelated() {\n  return 2;\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// TestTF142R5SelectiveDerivationUnbudgetedStillComplete is the widening
// direction: an unbudgeted selective query must still derive the full view from
// the cached complete snapshot and report no truncation. A view derived on a
// warm query is re-derived each time rather than persisted, so the cache must
// not grow either.
func TestTF142R5SelectiveDerivationUnbudgetedStillComplete(t *testing.T) {
	repo := tf142r5SelectiveRepo(t)
	cacheDir := t.TempDir()

	if _, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir, false); err != nil {
		t.Fatalf("warm the complete snapshot: %v", err)
	}
	warmed := f06fixCacheFileCount(t, cacheDir)

	selective := ProviderSnapshotOptions{Profile: ProfileFull, OnlyFiles: []string{"deep.js"}}
	snapshot, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", selective, cacheDir, false)
	if err != nil {
		t.Fatalf("unbudgeted selective derivation: %v", err)
	}
	if !hit {
		t.Fatal("the unbudgeted selective query should have been served from the complete snapshot")
	}
	if partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("an unbudgeted derivation must never report truncation: %#v", snapshot.Header.PartialFailures)
	}
	if len(snapshot.Symbols) == 0 || len(snapshot.Relations) == 0 {
		t.Fatalf("the unbudgeted derivation lost the graph: %d symbol(s), %d relation(s)",
			len(snapshot.Symbols), len(snapshot.Relations))
	}
	if got := f06fixCacheFileCount(t, cacheDir); got != warmed {
		t.Fatalf("a warm selective derivation must not persist its view: cache went from %d to %d file(s)", warmed, got)
	}
}
