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

// f06fixTruncationCode is the wire-visible truncation marker, spelled out on
// purpose so the test pins the string consumers match on rather than whatever
// the constant currently holds.
const f06fixTruncationCode = "E_ANALYSIS_BUDGET_EXCEEDED"

// TestF06FixCallerDeadlineIsAnError reproduces the HIGH finding. A caller that
// runs under a context carrying a deadline -- a request timeout, an outer
// errgroup, a parent CLI budget -- must not silently receive an incomplete
// snapshot with a nil error when that deadline passes mid-build.
func TestF06FixCallerDeadlineIsAnError(t *testing.T) {
	repo := budgetBombRepo(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, "test", ProviderSnapshotOptions{})
	if err == nil {
		t.Fatalf("a caller deadline must surface as an error, got a snapshot with %d file(s) and failures %#v",
			len(snapshot.Files), snapshot.Header.PartialFailures)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
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
		PartialFailures: []PartialFailure{{Code: f06fixTruncationCode, Severity: "warning", EffectOnCompleteness: "snapshot truncated"}},
	}
	if err := storeProviderRecordsForTest(t.Context(), repo, "test", tree, "symbols", cacheDir, options, []byte("{}\n"), summary); err != nil {
		t.Fatalf("store: %v", err)
	}
	_, _, hit, err := LoadProviderRecords(t.Context(), repo, "test", tree, tree, "symbols", cacheDir, options)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if hit {
		t.Fatal("a truncated record stream must never be persisted: a later query would serve it as the complete index")
	}
}

// TestF06FixSearchSnapshotPastCallerDeadlineCachesNothing pins the same
// property on the search/preindex path: a build stopped by its caller's
// deadline returns the context's error, and nothing it produced reaches the
// tree-keyed cache, where every later query would be served it as complete.
func TestF06FixSearchSnapshotPastCallerDeadlineCachesNothing(t *testing.T) {
	repo := budgetBombRepo(t)
	cacheDir := t.TempDir()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	snapshot, _, err := LoadOrBuildProviderSnapshot(ctx, repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a search build past its caller's deadline must return context.DeadlineExceeded, got %v (snapshot with %d file(s))", err, len(snapshot.Files))
	}
	if n := f06fixCacheFileCount(t, cacheDir); n != 0 {
		t.Fatalf("a search build stopped by its deadline must cache nothing, found %d file(s) under %s", n, cacheDir)
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
		// Advisory lock files are intentionally permanent; they contain no
		// graph data and must not be unlinked while another process may use them.
		if strings.HasSuffix(entry.Name(), ".build.lock") {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != 0 {
				t.Fatalf("lock file contains unexpected data: %s", entry.Name())
			}
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
