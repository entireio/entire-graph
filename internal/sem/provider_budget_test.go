package sem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// nestedRelationBomb returns a JavaScript source of n nested function
// declarations. Every enclosing function's body textually contains every inner
// function's call sites and URL literals, so the relation phase does O(n^2)
// work re-scanning them: 500 declarations in ~52 KB take tens of seconds and
// emit ~386,000 relations. It is the smallest input that makes the missing
// wall-clock ceiling observable in a test.
func nestedRelationBomb(n int) string {
	var head, tail strings.Builder
	head.WriteString("// nested relation bomb\n")
	for i := range n {
		fmt.Fprintf(&head, "function nestedFn%d(a, b) {\n  const x = fetch(\"https://example.com/v%d/items\");\n  helper%d(a, b);\n", i, i, i)
		tail.WriteString("}\n")
	}
	return head.String() + tail.String()
}

func budgetBombRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "deep.js", nestedRelationBomb(500))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "nested")
	return repo
}

// TestStreamSnapshotContextDeadlineTruncatesInsteadOfFailing pins the same
// degradation for a deadline the CALLER put on the context. Before this change
// the deadline aborted the stream with context.DeadlineExceeded and the caller
// got no records and no summary at all.
func TestStreamSnapshotContextDeadlineTruncatesInsteadOfFailing(t *testing.T) {
	repo := budgetBombRepo(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var summary *SnapshotSummary
	err := StreamSnapshot(ctx, repo, "test", ProviderSnapshotOptions{}, func(record any) error {
		if s, ok := record.(SnapshotSummary); ok {
			s := s
			summary = &s
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a caller deadline must truncate, not fail: got %v", err)
	}
	if summary == nil || !hasBudgetFailure(summary.PartialFailures) {
		t.Fatalf("caller deadline must report %s, got %#v", budgetFailureCode, summary)
	}
}

// TestStreamSnapshotCanceledContextStillReturnsError pins the other half of the
// split: a cancellation is an abort, not a budget. A half-finished snapshot is
// not what an interrupted caller asked for, so it must still be an error.
func TestStreamSnapshotCanceledContextStillReturnsError(t *testing.T) {
	repo := budgetBombRepo(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := StreamSnapshot(ctx, repo, "test", ProviderSnapshotOptions{}, func(record any) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled context must still return context.Canceled, got %v", err)
	}
}

// TestStreamSnapshotWithoutBudgetIsUnchanged pins that the default (no
// MaxDuration, no caller deadline) still produces a complete snapshot with no
// budget failure, on a repository small enough to finish quickly.
func TestStreamSnapshotWithoutBudgetIsUnchanged(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "auth.js", "function validateToken(token) {\n  return Boolean(token);\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	var summary *SnapshotSummary
	symbols := 0
	if err := StreamSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{}, func(record any) error {
		switch r := record.(type) {
		case SnapshotSummary:
			summary = &r
		case SymbolRecord:
			symbols++
		}
		return nil
	}); err != nil {
		t.Fatalf("unbudgeted snapshot: %v", err)
	}
	if symbols == 0 {
		t.Fatal("unbudgeted snapshot emitted no symbols")
	}
	if summary == nil || hasBudgetFailure(summary.PartialFailures) {
		t.Fatalf("unbudgeted snapshot must not report a budget failure, got %#v", summary)
	}
}

// budgetFailureCode is spelled out rather than referenced through
// AnalysisBudgetExceededCode on purpose: the code is a wire-visible contract
// string that snapshot consumers match on, so the test pins the literal it will
// see in JSON, not whatever the constant happens to hold.
const budgetFailureCode = "E_ANALYSIS_BUDGET_EXCEEDED"

func hasBudgetFailure(failures []PartialFailure) bool {
	for _, failure := range failures {
		if failure.Code == budgetFailureCode {
			return true
		}
	}
	return false
}
