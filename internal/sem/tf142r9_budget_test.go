package sem

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tf142r9LaggingClock is the timer-lag condition, made deterministic.
//
// It reads the real clock exactly once -- when the provider derives the budget
// deadline from it -- and returns a time two hours PAST that deadline on every
// later poll. Paired with a budget an hour long, that puts the run in the exact
// state a Windows runner is in for one system tick after the deadline passes:
// the work context's own timer is nowhere near firing, so ctx.Err() is nil,
// while the clock says the budget is already gone.
//
// No assertion built on it depends on how fast the machine is or on what the
// platform clock can resolve. The budget is expired by construction.
func tf142r9LaggingClock(budget time.Duration) func() time.Time {
	var polls atomic.Int64
	base := time.Now()
	return func() time.Time {
		if polls.Add(1) == 1 {
			return base
		}
		return base.Add(budget + 2*time.Hour)
	}
}

// tf142r9Repo is deliberately several small files rather than one big one: the
// property under test is that no file is admitted after the ceiling, and one
// file cannot distinguish "stopped at the right file" from "stopped at all".
func tf142r9Repo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	for i := range 12 {
		writeFile(t, repo, fmt.Sprintf("pkg%02d.js", i),
			fmt.Sprintf("function helper%d(x) {\n  return x + %d;\n}\n\nfunction caller%d(y) {\n  return helper%d(y);\n}\n", i, i, i, i))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// TestTF142R9FileWorkersStopOnTheClockNotOnTheTimer reproduces the finding at
// provider_parallel.go:187 (and the actionable half of the one at
// provider.go:1157).
//
// Round eight closed the observation-lag class for the relation phase, the
// warm-cache derivation and every content scan, but the PARSE phase was left
// observing the context alone: the pipeline stops handing out files on
// ctx.Done, processProviderFile gates its read on ctx.Err(), and it accepts the
// parsed file on ctx.Err(). All three lag the deadline by one timer
// granularity, so for that whole window files are still read, parsed, and
// emitted -- with their symbols -- past the advertised ceiling.
//
// The assertion is on records retained, not on elapsed time.
func TestTF142R9FileWorkersStopOnTheClockNotOnTheTimer(t *testing.T) {
	repo := tf142r9Repo(t)
	cacheDir := t.TempDir()

	const budget = time.Hour
	snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:     ProfileFull,
		MaxDuration: budget,
		nowFn:       tf142r9LaggingClock(budget),
	}, cacheDir, false)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	if snapshot.Header.Stats.Files != 0 || snapshot.Header.Stats.Symbols != 0 {
		t.Fatalf("the file workers admitted %d file(s) and %d symbol(s) on a budget the clock had already passed: "+
			"the parse phase observes the context's timer, not the deadline",
			snapshot.Header.Stats.Files, snapshot.Header.Stats.Symbols)
	}
	// A truncated run must stay self-describing and uncacheable. This is the
	// round-one HIGH finding's failure mode, and a clock-triggered stop is
	// exactly the shape that can reopen it: the derivation stops while
	// workCtx.Err() is still nil.
	if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("a run stopped by the clock must report E_ANALYSIS_BUDGET_EXCEEDED, got %#v", snapshot.Header.PartialFailures)
	}
	if level := snapshot.Header.Stats.CompletenessLevel; level == "ok" {
		t.Fatalf("a budget-truncated snapshot reported completeness_level=%q", level)
	}
	if got := f06fixCacheFileCount(t, cacheDir); got != 0 {
		t.Fatalf("a budget-truncated snapshot was persisted: %d cache file(s)", got)
	}
}

// TestTF142R9FileWorkersUnbudgetedAreUnchanged is the widening direction. A
// budget that has not run out must cost the parse phase nothing and retain
// every file, or the guard above would be a silent truncation of ordinary runs.
func TestTF142R9FileWorkersUnbudgetedAreUnchanged(t *testing.T) {
	repo := tf142r9Repo(t)

	complete, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("unbudgeted build: %v", err)
	}
	if complete.Header.Stats.Files != 12 {
		t.Fatalf("fixture: expected 12 files, got %d", complete.Header.Stats.Files)
	}

	base := time.Now()
	budgeted, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:     ProfileFull,
		MaxDuration: time.Hour,
		nowFn:       func() time.Time { return base },
	})
	if err != nil {
		t.Fatalf("a live budget must not fail: %v", err)
	}
	if budgeted.Header.Stats.Files != complete.Header.Stats.Files ||
		budgeted.Header.Stats.Symbols != complete.Header.Stats.Symbols ||
		budgeted.Header.Stats.Relations != complete.Header.Stats.Relations {
		t.Fatalf("a live budget changed the result: files %d/%d symbols %d/%d relations %d/%d",
			budgeted.Header.Stats.Files, complete.Header.Stats.Files,
			budgeted.Header.Stats.Symbols, complete.Header.Stats.Symbols,
			budgeted.Header.Stats.Relations, complete.Header.Stats.Relations)
	}
	if partialFailuresTruncated(budgeted.Header.PartialFailures) {
		t.Fatalf("a live budget reported truncation: %#v", budgeted.Header.PartialFailures)
	}
}

// tf142r9LaggingErrContext reports no error for the first tolerate polls and
// context.DeadlineExceeded afterwards. Its Done() channel is nil, so the
// tree-sitter parse inside the unwrap pass is never cancelled and the walk that
// follows it is reached deterministically -- which is the condition the finding
// describes: the deadline expires AFTER the parse.
type tf142r9LaggingErrContext struct {
	context.Context
	polls    atomic.Int64
	tolerate int64
}

func (c *tf142r9LaggingErrContext) Err() error {
	if c.polls.Add(1) <= c.tolerate {
		return nil
	}
	return context.DeadlineExceeded
}

// tf142r9RustLateMacro puts the cfg_net! wrapper AFTER n plain items, so the
// walk must traverse the whole prefix before it can unwrap anything. A walk
// that stops early therefore leaves the source unmasked, and the difference is
// observable in the returned bytes rather than in a stopwatch.
func tf142r9RustLateMacro(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "pub fn plain%d(x: u32) -> u32 { let y = x + %d; y }\n", i, i)
	}
	sb.WriteString("cfg_net! {\npub struct Late { pub a: u32 }\n}\n")
	return sb.String()
}

// TestTF142R9RustUnwrapWalkStopsAfterTheParse reproduces the finding at
// parser.go:736. Handing ctx to the unwrap pass bounds only the tree-sitter
// parse: the recursive AST walk over the tree it produced polled nothing, so a
// budget that expired after the parse still paid for a complete traversal --
// once per unwrap iteration, up to maxUnwrapDepth of them.
func TestTF142R9RustUnwrapWalkStopsAfterTheParse(t *testing.T) {
	t.Parallel()

	content := tf142r9RustLateMacro(400)
	// tolerate=1 is the entry check of the unwrap pass; the next poll is the
	// walk's own, at the first stride boundary.
	stopped := &tf142r9LaggingErrContext{Context: context.Background(), tolerate: 1}
	masked := maskRustUnsupportedSyntax(stopped, content)
	if masked != content {
		t.Fatalf("a context that stopped during the walk still unwrapped %d byte(s) of macro wrapper: the walk polls nothing",
			len(content)-strings.Count(masked, "cfg_net"))
	}
	if stopped.polls.Load() < 2 {
		t.Fatalf("fixture: the walk must poll the context at least once, got %d poll(s)", stopped.polls.Load())
	}
}

// TestTF142R9RustUnwrapUnbudgetedIsUnchanged is the widening direction: with no
// deadline the same input must still be fully unwrapped, byte for byte.
func TestTF142R9RustUnwrapUnbudgetedIsUnchanged(t *testing.T) {
	t.Parallel()

	content := tf142r9RustLateMacro(400)
	masked := maskRustUnsupportedSyntax(context.Background(), content)
	if masked == content {
		t.Fatal("an unbudgeted unwrap pass must still unwrap the cfg_net! wrapper")
	}
	if len(masked) != len(content) {
		t.Fatalf("the mask must be length-preserving: %d vs %d", len(masked), len(content))
	}
	if strings.Contains(masked, "cfg_net") {
		t.Fatal("the wrapper must be blanked")
	}
}
