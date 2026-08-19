package sem

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// f06fixBudgetCode is the wire-visible truncation marker, spelled out on
// purpose so the test pins the string consumers match on rather than whatever
// the constant currently holds.
const f06fixBudgetCode = "E_ANALYSIS_BUDGET_EXCEEDED"

func f06fixHasBudgetFailure(failures []PartialFailure) bool {
	for _, failure := range failures {
		if failure.Code == f06fixBudgetCode {
			return true
		}
	}
	return false
}

// TestF06FixCallerDeadlineWithoutBudgetIsAnError reproduces the HIGH finding.
// Callers that never asked for truncation (no MaxDuration) but happen to run
// under a context that carries a deadline -- a request timeout, an outer
// errgroup, a parent CLI budget -- must not silently receive an incomplete
// snapshot with a nil error. Truncation is opt-in.
func TestF06FixCallerDeadlineWithoutBudgetIsAnError(t *testing.T) {
	repo := budgetBombRepo(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, "test", ProviderSnapshotOptions{})
	if err == nil {
		t.Fatalf("a caller deadline with no MaxDuration must surface as an error, got a snapshot with %d file(s) and failures %#v",
			len(snapshot.Files), snapshot.Header.PartialFailures)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// TestF06FixOptedInBudgetStillTruncates is the other half of the split: a
// caller that explicitly set MaxDuration did ask for a truncated result, and
// still gets one.
func TestF06FixOptedInBudgetStillTruncates(t *testing.T) {
	repo := budgetBombRepo(t)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{MaxDuration: time.Second})
	if err != nil {
		t.Fatalf("an explicit MaxDuration must truncate, not fail: %v", err)
	}
	if !f06fixHasBudgetFailure(snapshot.Header.PartialFailures) {
		t.Fatalf("explicit budget must report %s, got %#v", f06fixBudgetCode, snapshot.Header.PartialFailures)
	}
}

// TestF06FixRecordCacheWriterRejectsTruncatedSummary reproduces the HIGH
// finding at the record cache writer. The CLI call site checks the summary, but
// the writer itself did not, so any other caller could poison the tree-keyed
// entry with a truncated graph that later UNBUDGETED queries would serve as
// complete.
func TestF06FixRecordCacheWriterRejectsTruncatedSummary(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "auth.js", "function validateToken(token) {\n  return Boolean(token);\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	cacheDir := t.TempDir()

	tree := f06fixTree(t, repo)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	summary := &SnapshotSummary{
		RecordType:      "summary",
		PartialFailures: []PartialFailure{analysisBudgetFailure(time.Second, 2*time.Second)},
	}
	if err := StoreProviderRecords(t.Context(), repo, "test", tree, "symbols", cacheDir, options, []byte("{}\n"), summary); err != nil {
		t.Fatalf("store: %v", err)
	}
	_, _, hit, err := LoadProviderRecords(t.Context(), repo, "test", tree, "symbols", cacheDir, options)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if hit {
		t.Fatal("a budget-truncated record stream must never be persisted: a later unbudgeted query would serve it as the complete index")
	}
}

// TestF06FixSearchSnapshotCacheRejectsTruncated reproduces the HIGH finding on
// the search/preindex path, which is the one the PR did not cover: it writes
// through writeSearchSnapshot without ever looking at PartialFailures.
func TestF06FixSearchSnapshotCacheRejectsTruncated(t *testing.T) {
	repo := budgetBombRepo(t)
	cacheDir := t.TempDir()

	snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:     ProfileFull,
		MaxDuration: time.Second,
	}, cacheDir, false)
	if err != nil {
		t.Fatalf("budgeted search build: %v", err)
	}
	if !f06fixHasBudgetFailure(snapshot.Header.PartialFailures) {
		t.Fatalf("expected a truncated snapshot, got %#v", snapshot.Header.PartialFailures)
	}
	if n := f06fixCacheFileCount(t, cacheDir); n != 0 {
		t.Fatalf("a budget-truncated search snapshot must not be cached, found %d file(s) under %s", n, cacheDir)
	}
}

func f06fixCacheFileCount(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			count += f06fixCacheFileCount(t, dir+"/"+entry.Name())
			continue
		}
		count++
	}
	return count
}

func f06fixTree(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD^{tree}")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse tree: %v", err)
	}
	return strings.TrimSpace(string(out))
}
