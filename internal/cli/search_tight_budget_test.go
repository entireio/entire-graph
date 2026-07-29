package cli

import (
	"bytes"
	"strings"
	"testing"
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
