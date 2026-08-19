package sem

import (
	"testing"
	"time"
)

// A budget of one nanosecond is expired by the time the first unit of work
// starts, so every assertion below is an INVARIANT ("this run was truncated"),
// never a stopwatch reading. Nothing on a real machine parses a file inside a
// nanosecond, so the outcome does not depend on how loaded the runner is.
const tf142r5ExpiredBudget = time.Nanosecond

// TestTF142R5TruncatedSnapshotNeverReportsCompletenessOK reproduces the finding
// on the summary. `completeness_level` is the machine-readable field a consumer
// checks before trusting a graph. When the wall-clock budget expires before the
// first file is retained, the summary carries E_ANALYSIS_BUDGET_EXCEEDED but
// `completenessLevel` still returns "ok", because its `files == 0` arm reads an
// empty graph as "genuinely empty repository". A consumer keying on the level
// alone therefore treats a graph missing the ENTIRE repository as complete.
func TestTF142R5TruncatedSnapshotNeverReportsCompletenessOK(t *testing.T) {
	repo := budgetBombRepo(t)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:     ProfileFull,
		MaxDuration: tf142r5ExpiredBudget,
	})
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("expected a truncated snapshot, got %#v", snapshot.Header.PartialFailures)
	}
	if level := snapshot.Header.Stats.CompletenessLevel; level == "ok" {
		t.Fatalf("a snapshot truncated by its budget reported completeness_level=%q with %d file(s) and %d symbol(s): "+
			"a machine consumer would trust a graph missing the whole repository",
			level, snapshot.Header.Stats.Files, snapshot.Header.Stats.Symbols)
	}
}

// TestTF142R5CompletenessLevelUnaffectedWithoutTruncation is the other
// direction: narrowing "ok" must not swallow the legitimate cases. A genuinely
// empty scope and a clean full parse both still report "ok".
func TestTF142R5CompletenessLevelUnaffectedWithoutTruncation(t *testing.T) {
	if got := completenessLevel(nil, 0, 0, 0); got != "ok" {
		t.Fatalf("an empty scope with no truncation must stay %q, got %q", "ok", got)
	}
	if got := completenessLevel([]PartialFailure{{Code: "E_FILE_TOO_LARGE"}}, 100, 100, 4000); got != "ok" {
		t.Fatalf("a clean parse with only intentional skips must stay %q, got %q", "ok", got)
	}
}

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

// TestTF142R5SelectiveDerivationObservesTheBudget reproduces the finding on the
// warm-cache path. LoadOrBuildProviderSnapshot honours MaxDuration on a cold
// cache (it falls through to BuildProviderSnapshotWithOptions), but when a
// complete committed-tree snapshot is already cached it derives the OnlyFiles
// view instead -- and that derivation re-runs the whole relation phase on the
// CALLER's context only. Same call, same options, budget honoured or ignored
// depending on cache state.
func TestTF142R5SelectiveDerivationObservesTheBudget(t *testing.T) {
	repo := tf142r5SelectiveRepo(t)
	cacheDir := t.TempDir()

	// Warm the complete committed-tree entry with no budget at all.
	if _, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir, false); err != nil {
		t.Fatalf("warm the complete snapshot: %v", err)
	}
	warmed := f06fixCacheFileCount(t, cacheDir)
	if warmed == 0 {
		t.Fatal("the complete snapshot was not cached, so the derivation path under test is never reached")
	}

	selective := ProviderSnapshotOptions{
		Profile:     ProfileFull,
		OnlyFiles:   []string{"deep.js"},
		MaxDuration: tf142r5ExpiredBudget,
	}
	snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", selective, cacheDir, false)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("the warm-cache selective derivation ignored MaxDuration: got %d relation(s) and failures %#v",
			len(snapshot.Relations), snapshot.Header.PartialFailures)
	}
	if got := f06fixCacheFileCount(t, cacheDir); got != warmed {
		t.Fatalf("a budget-truncated selective view was persisted: cache went from %d to %d file(s)", warmed, got)
	}
}

// TestTF142R5SelectiveDerivationUnbudgetedStillComplete is the widening
// direction: an unbudgeted selective query must still derive the full view from
// the cached complete snapshot, report no truncation, and be cached.
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
	if got := f06fixCacheFileCount(t, cacheDir); got <= warmed {
		t.Fatalf("the untruncated selective view should be persisted: cache stayed at %d file(s)", got)
	}
}
