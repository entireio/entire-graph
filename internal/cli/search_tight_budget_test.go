package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// Telemetry width must never cost the agent its line of source.
//
// The header carries MEASURED latencies, so its width depends on how loaded the machine is:
// "I:miss/16 Q:7 P:16 T:41" is 24 bytes on an idle laptop and "I:miss/158 Q:78 P:194 T:431" is
// 28 on a CI runner. The header ladder is the outermost loop of the fitting search, so those
// bytes used to come out of the RANKING: the same repo, same query and same budget produced a
// payload with the top hit's body on one machine and a bare locator on another. That is what made
// TestSearchCommandAgentFormatKeepsTopLocationUnderTightBudget fail on windows-latest only.
//
// This sweeps budgets ACROSS the width difference, so it fails on any machine if the protection
// regresses, rather than only on the slow one.
func TestTightAgentBudgetKeepsSourceAcrossHeaderWidths(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "a.py", "def target():\n    return True\n")
	for _, budget := range []string{"64", "60", "56", "52", "48"} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			"search", "--repo", repo, "--query", "target", "--format", "agent",
			"--profile", "syntax-only", "--worktree", "--max-context-bytes", budget,
		})
		if err != nil {
			t.Fatalf("budget %s: %v", budget, err)
		}
		got := out.String()
		if out.Len() > 64 {
			t.Fatalf("budget %s: used %d bytes: %q", budget, out.Len(), got)
		}
		if !strings.Contains(got, "a.py:1") {
			t.Fatalf("budget %s: lost the top-ranked location: %q", budget, got)
		}
		if !strings.Contains(got, "target") {
			t.Fatalf("budget %s: telemetry crowded out the source: %q", budget, got)
		}
		// The header must stay machine-readable at every rung: the ladder sheds latency
		// FIELDS, never the "I:<state>/" shape consumers key off. Degrading to the legacy
		// "Index: cache-miss (…)" form to buy those bytes would break every parser.
		if !strings.HasPrefix(got, "I:miss/") {
			t.Fatalf("budget %s: header lost its machine-readable prefix: %q", budget, got)
		}
	}
}

// The locator predicate is what decides whether a plan is "source-bearing", and it is shape
// sensitive: the AGENT format's locator is `path:line *` with no `N. ` rank prefix. Getting this
// wrong made the protection dead code twice.
func TestAgentSearchLocatorRecognition(t *testing.T) {
	t.Parallel()
	locators := []string{"a.py:1 *", "pkg/mod/file.go:2145", "a.py:1 * signals=body"}
	for _, l := range locators {
		if !agentSearchLineIsLocator([]byte(l)) {
			t.Fatalf("%q should be a locator", l)
		}
		if agentSearchBlockCarriesSource([]byte(l + "\n")) {
			t.Fatalf("%q alone must not count as source", l)
		}
	}
	source := []string{"def target():", "    return True", "func Trim() {", "}", "x := map[string]int{}"}
	for _, s := range source {
		if agentSearchLineIsLocator([]byte(s)) {
			t.Fatalf("%q should NOT be a locator", s)
		}
	}
	if !agentSearchBlockCarriesSource([]byte("a.py:1 *\ndef target():\n")) {
		t.Fatal("locator + body must count as source")
	}
}

// A block gives up body lines before it gives up its rank, name and score.
//
// The budget here fits the minimal header over the WHOLE body, and the rich header over all but
// the last line. When span was the outer loop the widest span won under any header, so the head
// result came back as a bare `a.go:1 *` — no rank, no name, no score — to show one more line.
func TestAgentSearchBlockKeepsIdentityBeforeBodyLines(t *testing.T) {
	t.Parallel()
	body := "func Resolve() {\n\tstepOne()\n\tstepTwo()\n\tstepThree()\n}\n"
	result := sem.SearchResult{
		Rank: 1, FilePath: "a.go", SymbolName: "Resolve", Score: 12.5,
		StartLine: 1, EndLine: 5, SnippetStartLine: 1, FocusLine: 1, Snippet: body,
	}
	minimalWhole := len("a.go:1 *\n") + len(body)
	got := string(agentSearchPrimaryBlock(result, minimalWhole))
	if len(got) > minimalWhole {
		t.Fatalf("block used %d bytes, budget %d: %q", len(got), minimalWhole, got)
	}
	if !strings.HasPrefix(got, "1. a.go:1") || !strings.Contains(got, "Resolve") || !strings.Contains(got, "s=12.5") {
		t.Fatalf("head result lost its rank/name/score to buy body lines: %q", got)
	}
	if !strings.Contains(got, "func Resolve() {") {
		t.Fatalf("head result lost its source entirely: %q", got)
	}
	// Non-vacuity: the minimal header over the whole body must really fit this budget, or the
	// old ordering would not have chosen it and this test would prove nothing.
	if minimal := "a.go:1 *\n" + body; len(minimal) > minimalWhole {
		t.Fatalf("fixture no longer exercises the ordering: %d > %d", len(minimal), minimalWhole)
	}
}
