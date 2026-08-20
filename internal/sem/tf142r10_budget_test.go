package sem

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// tf142r10Detail is the exact detail a budget-truncated run must publish.
//
// It is pinned as a literal, not rebuilt from the constructor, because the
// property under test is that the wire bytes are a function of the CONFIGURED
// budget alone. A detail rebuilt from the same code under test would agree with
// itself no matter what it embedded.
const tf142r10Detail = "the 1h0m0s wall-clock budget expired"

// TestTF142R10TruncationDetailCarriesNoMeasuredDuration reproduces the finding
// at provider.go:239.
//
// The E_ANALYSIS_BUDGET_EXCEEDED detail embedded time.Since(started) rounded to
// milliseconds, so two runs that truncate at the SAME point still published
// different NDJSON bytes. That is not cosmetic here: this PR's own verification
// method is byte-identical output comparison ("No-flag output is unchanged byte
// for byte", PR body), and a payload that moves run to run cannot be diffed,
// content-addressed, or reproduced by a reviewer.
//
// Measured on the real verb at 8340ca6a, three runs of
// `entire graph snapshot --repo . --format ndjson --max-seconds 1` over this
// repository. All three produced 8,054,876 bytes and agreed on the first
// 8,053,472 of them, then diverged on exactly the elapsed digits:
//
//	run1 "stopped after 1.248s; wall-clock budget was 1s"  sha 02861e8e...
//	run2 "stopped after 1.229s; wall-clock budget was 1s"  sha df49c610...
//	run3 "stopped after 1.211s; wall-clock budget was 1s"  sha cda7b8eb...
//
// Masking the elapsed made all three shas identical, so the measured duration
// was the SOLE source of nondeterminism in a truncated snapshot.
//
// The assertion is a fixed string, not a comparison of two live runs: two runs
// only disagree when they straddle a millisecond boundary, which would make the
// test a function of how fast this machine is. The clock is injected so the
// truncation point is expired by construction.
func TestTF142R10TruncationDetailCarriesNoMeasuredDuration(t *testing.T) {
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

	var truncation *PartialFailure
	for i := range snapshot.Header.PartialFailures {
		if snapshot.Header.PartialFailures[i].Code == AnalysisBudgetExceededCode {
			truncation = &snapshot.Header.PartialFailures[i]
		}
	}
	if truncation == nil {
		t.Fatalf("a run stopped by the clock must report %s, got %#v",
			AnalysisBudgetExceededCode, snapshot.Header.PartialFailures)
	}
	if truncation.Detail != tf142r10Detail {
		t.Fatalf("the truncation detail must be a function of the configured budget alone.\n got: %q\nwant: %q",
			truncation.Detail, tf142r10Detail)
	}

	// The round-one HIGH finding's failure mode, re-checked because this round
	// changed what a truncated run PUBLISHES: it must still say it is truncated,
	// still report an unsafe completeness level, and still be refused by the
	// cache writers.
	if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("truncation marker lost: %#v", snapshot.Header.PartialFailures)
	}
	if level := snapshot.Header.Stats.CompletenessLevel; level != "unsafe" {
		t.Fatalf("a budget-truncated snapshot reported completeness_level=%q, want \"unsafe\"", level)
	}
	if got := f06fixCacheFileCount(t, cacheDir); got != 0 {
		t.Fatalf("a budget-truncated snapshot was persisted: %d cache file(s)", got)
	}
}

// TestTF142R10TruncatedSummaryIsByteIdenticalAcrossRuns is the end-to-end half:
// the serialized summary of two independent truncated builds of the same tree
// with the same options must be the same bytes.
//
// It is the property a reviewer actually needs (diff two runs, get nothing),
// and it is what a per-field golden cannot promise on its own -- a later field
// carrying a measured value would pass the golden above and fail here.
func TestTF142R10TruncatedSummaryIsByteIdenticalAcrossRuns(t *testing.T) {
	repo := tf142r9Repo(t)

	const budget = time.Hour
	build := func() []byte {
		t.Helper()
		snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile:     ProfileFull,
			MaxDuration: budget,
			nowFn:       tf142r9LaggingClock(budget),
		}, t.TempDir(), false)
		if err != nil {
			t.Fatalf("an explicit budget must truncate, not fail: %v", err)
		}
		if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
			t.Fatalf("fixture did not truncate: %#v", snapshot.Header.PartialFailures)
		}
		encoded, err := json.Marshal(snapshot.Header)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return encoded
	}

	first, second := build(), build()
	if string(first) != string(second) {
		t.Fatalf("two identical truncating runs published different bytes:\n first: %s\nsecond: %s", first, second)
	}
}

// TestTF142R10BudgetFailureDetailIsStableForEveryBudget is the widening
// direction over the one input the detail is allowed to depend on. The
// constructor is total, so the unset-budget case is exercised too even though
// both call sites reach it only with a positive budget.
func TestTF142R10BudgetFailureDetailIsStableForEveryBudget(t *testing.T) {
	for _, budget := range []time.Duration{0, time.Nanosecond, time.Second, 90 * time.Second, time.Hour} {
		first := analysisBudgetFailure(budget)
		second := analysisBudgetFailure(budget)
		if first != second {
			t.Fatalf("budget %s: %#v != %#v", budget, first, second)
		}
		if strings.Contains(first.Detail, "stopped after") {
			t.Fatalf("budget %s: detail still reports a measured duration: %q", budget, first.Detail)
		}
	}
}
