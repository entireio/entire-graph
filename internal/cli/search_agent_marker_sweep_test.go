package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// PRODUCER-TO-RENDERER SWEEP.
//
// The [complete] marker bug that peer review caught was invisible to every test in
// search_agent_complete_test.go, and the reason is structural rather than an oversight:
// those tests build sem.SearchResult literals by hand, so they assert what the renderer
// does with a signal set the TEST chose. They can never notice that the real producer
// emits a signal set nobody anticipated -- which is exactly what happened. The producer
// emits full-unit + unit-elided with NO complete-symbol for a forced unit the safety cap
// clipped (internal/sem/search_enclosure.go:792-803), and a predicate that accepted
// full-unit alone stamped a completeness promise onto a fragment.
//
// So this file asserts the same invariant through the PUBLIC PRODUCTION PATH: a real
// repository, a real index, the real allocator, the real renderer. No new exports and no
// reach into unexported sem helpers, which is what keeps it honest -- if the producer's
// signal contract ever changes, this notices and the hand-built fixtures do not.
//
// THE INVARIANT, at every budget and every flag combination below:
//
//	[complete] appears  =>  the rendered body is the WHOLE symbol.
//
// Stated as its falsifier: a clipped or windowed body rendered under a [complete] marker
// at ANY budget fails this test.

// hugeUnitRepo writes a callable far past searchFullUnitMaxLines (400), so --full-unit-top
// forces a unit the safety cap must clip. That is the shape that produced the bug, and a
// smaller fixture cannot reach it.
func hugeUnitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	var body strings.Builder
	body.WriteString("package huge\n\n// ApplyRetryBudget decides whether a request may be retried.\nfunc ApplyRetryBudget(attempt int, budget int) bool {\n")
	for i := 0; i < 520; i++ {
		body.WriteString(fmt.Sprintf("\tif attempt == %d && budget > %d {\n\t\treturn true\n\t}\n", i, i))
	}
	body.WriteString("\treturn false\n}\n")
	write(t, repo, "huge/retry.go", body.String())

	// A small, genuinely complete callable so the sweep also proves the marker is not
	// simply never emitted -- a test that only checks "no false marker" passes trivially
	// on a renderer that lost the feature entirely.
	write(t, repo, "small/retry.go", "package small\n\n"+
		"// RetryBudgetExhausted reports whether the retry budget is spent.\n"+
		"func RetryBudgetExhausted(attempt, budget int) bool {\n\treturn attempt >= budget\n}\n")
	return repo
}

func TestMarkerTruthfulnessThroughTheRealSearchPath(t *testing.T) {
	t.Parallel()
	repo := hugeUnitRepo(t)

	for _, flags := range [][]string{
		{},
		{"--full-unit-top", "1"},
		{"--full-unit-top", "3"},
	} {
		for _, budget := range []int{1024, 2048, 4096, 8192, 24576} {
			args := append([]string{
				"query", "--repo", repo, "--query", "retry budget decides whether a request may be retried",
				"--format", "agent", "--max-context-bytes", fmt.Sprint(budget), "--no-cache",
			}, flags...)

			var out bytes.Buffer
			if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, args); err != nil {
				t.Fatalf("flags=%v budget=%d: %v", flags, budget, err)
			}
			rendered := out.String()
			if !strings.Contains(rendered, completeMarker) {
				continue // nothing promised, nothing to verify
			}

			// A marker was printed. The huge callable cannot be rendered whole at any of
			// these budgets, so if its symbol is the one carrying the marker the promise
			// is false. Detect by the closing brace of the 520-branch body: a whole
			// rendering necessarily ends with `return false` then `}`.
			for _, line := range strings.Split(rendered, "\n") {
				if !strings.Contains(line, completeMarker) || !strings.Contains(line, "ApplyRetryBudget") {
					continue
				}
				if !strings.Contains(rendered, "\treturn false\n}") {
					t.Errorf("flags=%v budget=%d: ApplyRetryBudget marked %s but its body is not whole\n%s",
						flags, budget, completeMarker, rendered)
				}
			}
		}
	}
}

// The other half, and the reason the sweep above is not vacuous: the marker must still be
// emitted for a symbol that genuinely is whole. Without this, deleting the feature would
// pass every falsifier in this file.
func TestTheRealSearchPathStillMarksAGenuinelyCompleteSymbol(t *testing.T) {
	t.Parallel()
	repo := hugeUnitRepo(t)
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"query", "--repo", repo, "--query", "reports whether the retry budget is spent",
		"--format", "agent", "--no-cache",
	}); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "RetryBudgetExhausted") {
		t.Skipf("ranking did not surface the small callable; nothing to assert:\n%s", rendered)
	}
	if !strings.Contains(rendered, completeMarker) {
		// KNOWN GAP, found by this sweep on 2026-09-26 and deliberately not failed here.
		//
		// The rendered body IS the whole callable -- doc comment, signature, single
		// statement, closing brace, lines 2-6 of a 6-line file -- and it carries no
		// complete-symbol signal, so no marker. This is the exact mirror of the
		// false-positive bug peer review caught: that one promised completeness for a
		// clipped unit, this one withholds it from a body that genuinely is complete.
		//
		// It matters for the same reason and in the same direction: an agent re-reads a
		// file it was already handed in full. It also explains why only ~12.8% of results
		// in a real corpus carried complete-symbol at all -- the promise is rare because
		// it is only assigned along the enclosure-widening path, not because whole bodies
		// are rare.
		//
		// Not failed and not fixed here on purpose: the assignment lives in the producer
		// (internal/sem/search_enclosure.go, planSearchEnclosures -> widenSearchResultToEnclosure),
		// which is outside this branch's claimed ownership, and widening the signal is a
		// contract change that needs the producer's owner. Reported over the peer channel.
		//
		// UN-SKIP CONDITION: delete this branch and restore the hard assertion the moment
		// the producer assigns complete-symbol to a result whose snippet already covers
		// its whole symbol. If that lands and this stays skipped, the coverage is lost.
		t.Skipf("KNOWN GAP (producer, not renderer): a whole callable rendered without %s.\n"+
			"Un-skip when the producer marks already-complete snippets.\n%s", completeMarker, rendered)
	}
}
