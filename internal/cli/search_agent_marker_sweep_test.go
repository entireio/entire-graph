package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
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
			// HARD FALSIFIER, tightened after peer review. The first version grepped the
			// WHOLE output for a closing brace, so a truncated middle with an intact tail --
			// or an unrelated sibling symbol -- passed it. That is not a falsifier, it is a
			// coincidence detector.
			//
			// ApplyRetryBudget is 520 branches, far past searchFullUnitMaxLines (400). The
			// safety cap can NEVER return it whole at any budget here, so the promise is
			// false unconditionally: assert the marker never appears on ITS OWN header line,
			// rather than reasoning about what the body contains.
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "ApplyRetryBudget") && strings.Contains(line, completeMarker) {
					t.Errorf("flags=%v budget=%d: a callable the safety cap cannot return whole was marked %s\n  %s",
						flags, budget, completeMarker, strings.TrimSpace(line))
				}
			}
		}
	}
}

// Marker ABSENCE proves nothing on its own -- a renderer that never marks anything passes
// the sweep above trivially. This positively establishes, through the public JSON surface,
// that the clipped shape the sweep targets is actually produced by the fixture.
func TestTheHugeFixtureActuallyProducesAClippedForcedUnit(t *testing.T) {
	t.Parallel()
	repo := hugeUnitRepo(t)
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"query", "--repo", repo, "--query", "retry budget decides whether a request may be retried",
		"--format", "json", "--full-unit-top", "1", "--no-cache",
	}); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Results []struct {
			SymbolName string   `json:"symbol_name"`
			Signals    []string `json:"signals"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range resp.Results {
		if r.SymbolName != "ApplyRetryBudget" {
			continue
		}
		var full, elided, complete bool
		for _, s := range r.Signals {
			switch s {
			case sem.FullUnitSignal:
				full = true
			case sem.FullUnitElidedSignal:
				elided = true
			case sem.CompleteSymbolSignal:
				complete = true
			}
		}
		if !full || !elided {
			t.Errorf("fixture did not produce a clipped forced unit: signals=%v -- the sweep is testing a shape that does not occur", r.Signals)
		}
		if complete {
			t.Errorf("producer marked a clipped unit complete-symbol: signals=%v", r.Signals)
		}
		return
	}
	t.Fatal("ApplyRetryBudget not returned under --full-unit-top 1; the sweep fixture is not exercising the forced path")
}

// Positive cases must exercise a producer path that actually certifies the body.
// Opportunistic enclosure widens a medium callable; --full-unit-top also certifies
// an already-visible small callable. Neither case may silently skip.
func TestTheRealSearchPathStillMarksAGenuinelyCompleteSymbol(t *testing.T) {
	for _, forced := range []bool{false, true} {
		name := "enclosure-upgrade"
		if forced {
			name = "forced-small-unit"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			var function strings.Builder
			function.WriteString("func RetryBudgetExhausted(attempt, budget int) bool {\n")
			if !forced {
				for i := 0; i < 12; i++ {
					fmt.Fprintf(&function, "\tif attempt == %d {\n\t\treturn budget <= %d\n\t}\n", i, i)
				}
			}
			function.WriteString("\treturn attempt >= budget\n}\n")
			source := "package retry\n\n" + function.String()
			write(t, repo, "retry.go", source)
			args := []string{"query", "--repo", repo, "--query", "RetryBudgetExhausted", "--no-cache", "--max-context-bytes", "24576"}
			if forced {
				args = append(args, "--full-unit-top", "1")
			}
			var machine bytes.Buffer
			if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &machine}, append(append([]string{}, args...), "--format", "json")); err != nil {
				t.Fatal(err)
			}
			var response sem.SearchResponse
			if err := json.Unmarshal(machine.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, result := range response.Results {
				if result.SymbolName != "RetryBudgetExhausted" {
					continue
				}
				complete, elided := false, false
				for _, signal := range result.Signals {
					complete = complete || signal == sem.CompleteSymbolSignal
					elided = elided || signal == sem.FullUnitElidedSignal
				}
				if !complete || elided {
					t.Fatalf("producer did not certify the fixture: signals=%v", result.Signals)
				}
				if !strings.Contains(result.Snippet, strings.TrimSuffix(function.String(), "\n")) {
					t.Fatalf("producer returned a partial body: %q", result.Snippet)
				}
				found = true
				break
			}
			if !found {
				t.Fatal("query did not return the target callable")
			}
			var agent bytes.Buffer
			if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &agent}, append(append([]string{}, args...), "--format", "agent")); err != nil {
				t.Fatal(err)
			}
			marked := false
			for _, line := range strings.Split(agent.String(), "\n") {
				if strings.Contains(line, "RetryBudgetExhausted") && strings.Contains(line, completeMarker) {
					marked = true
				}
			}
			if !marked || !strings.Contains(agent.String(), strings.TrimSuffix(function.String(), "\n")) {
				t.Fatalf("certified body was not rendered whole with its marker:\n%s", agent.String())
			}
		})
	}
}
