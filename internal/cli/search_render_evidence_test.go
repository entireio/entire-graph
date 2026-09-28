package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A ranked locator is metadata, even when it includes the target's name.
// Classifying it as source bypasses the renderer's source-protection pass.
func TestAgentSearchRankedLocatorAloneIsNotSource(t *testing.T) {
	for _, line := range []string{
		"1. a.py:1 target s=12.5 *",
		"1. a.py:1 target s=12.5 [focus:1]",
		"1. my file.py:1 target s=12.5 *",
		"1. my file.py:1 target s=12.5 [focus:1]",
		"my file.py:1 *",
	} {
		t.Run(line, func(t *testing.T) {
			if agentSearchBlockCarriesSource([]byte(line + "\n")) {
				t.Fatalf("ranked locator without any body was classified as source: %q", line)
			}
		})
	}
	for _, source := range []string{
		"    return True", " 1. a.py:1 target *", "one. a.py:1 target *",
		"1. not_a_location", "1. my file.py:one target *", "def target():",
		"const value = {x:2 * 3};",
	} {
		if agentSearchLineIsLocator([]byte(source)) {
			t.Fatalf("source or malformed location was classified as metadata: %q", source)
		}
	}
}

func TestAgentSearchHeadSourceUsesAvailableBodyBudget(t *testing.T) {
	const source = "print(\"012345678901234567890123456789\")\n"
	result := sem.SearchResult{
		Rank: 1, FilePath: "a.py", SymbolName: "target", Score: 90,
		StartLine: 1, EndLine: 1, SnippetStartLine: 1, FocusLine: 1, Snippet: source,
	}
	const budget = 50
	if len("a.py:1 *\n"+source) > budget {
		t.Fatal("fixture: minimal header plus source must fit")
	}
	got := fitAgentSearchHeadSource(result, budget)
	if len(got) > budget || !bytes.Contains(got, []byte(source)) {
		t.Fatalf("available bytes must reach source, not an arbitrary smaller cap: %q", got)
	}
}

// Additional passages must not spend the primary identity's floor budget.
func TestAgentSearchAdditionalPassageCannotSpendPrimaryLocationFloor(t *testing.T) {
	name := strings.Repeat("N", 100)
	result := sem.SearchResult{
		Rank: 1, FilePath: "a.go", SymbolName: name, Score: 90,
		StartLine: 1, EndLine: 1, SnippetStartLine: 1, FocusLine: 1, Snippet: "x()\n",
		Passages: []sem.SearchPassage{{StartLine: 10, EndLine: 10, FocusLine: 10, Snippet: "y()\n"}},
	}
	// Literal header is independently assembled from the required public fields.
	wantHeader := "1. a.go:1 " + name + " s=90.0 [focus:1]\n"
	budget := len(wantHeader)
	got := string(agentSearchBlock(result, budget))
	if len(got) > budget {
		t.Fatalf("used %d bytes for budget %d: %q", len(got), budget, got)
	}
	if !strings.HasPrefix(got, "1. a.go:1 "+name+" s=90.0") {
		t.Fatalf("passage spent primary location floor (%d bytes): %q", budget, got)
	}
	roomy := string(agentSearchBlock(result, 512))
	if !strings.Contains(roomy, "x()") || !strings.Contains(roomy, "y()") {
		t.Fatalf("spare capacity must retain both source passages: %q", roomy)
	}
}

// The symbol name also appears in the locator. Require an actual source line,
// not a name match, while exercising the same small-budget command boundary.
func TestSearchTightBudgetPreservesActualSource(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.py", "def target():\n    return True\n")
	for _, budget := range []int{64, 60, 56, 52, 48} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			var out bytes.Buffer
			err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
				"search", "--repo", repo, "--query", "target", "--format", "agent",
				"--profile", "syntax-only", "--worktree", "--max-context-bytes", strconv.Itoa(budget),
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Len() > budget {
				t.Fatalf("used %d bytes for budget %d: %q", out.Len(), budget, out.String())
			}
			if !strings.Contains(out.String(), "def target():") && !strings.Contains(out.String(), "    return True") {
				t.Fatalf("locator name is not source; actual body absent at budget %d: %q", budget, out.String())
			}
		})
	}
}

func TestSearchTightBudgetPreservesSourceForSpacedPath(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "my file.py", "def target():\n    return True\n")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
		"search", "--repo", repo, "--query", "target", "--format", "agent",
		"--profile", "syntax-only", "--worktree", "--max-context-bytes", "64",
	}); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 64 || !strings.Contains(out.String(), "my file.py:1") ||
		!strings.Contains(out.String(), "def target():") {
		t.Fatalf("spaced path hid source loss under the budget: %q", out.String())
	}
}

// Source rescue must not discard identity before trying a shorter telemetry prefix.
func TestAgentSearchPrefersRankedSourceBeforeMinimalRescue(t *testing.T) {
	response := sem.SearchResponse{Results: []sem.SearchResult{{
		Rank: 1, FilePath: "a.py", SymbolName: "target", Score: 90,
		StartLine: 1, EndLine: 2, SnippetStartLine: 1, FocusLine: 1,
		Snippet: "def target():\n    return True\n",
	}}}
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 60); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 60 || !strings.Contains(out.String(), "1. a.py:1 target s=90.0") ||
		!strings.Contains(out.String(), "def target():") {
		t.Fatalf("shorter telemetry can fit ranked source, do not rescue early: %q", out.String())
	}
}

func TestAgentSearchSourceRescueKeepsTailLocations(t *testing.T) {
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{Rank: 1, FilePath: "a.py", SymbolName: "target", Score: 90,
			StartLine: 1, EndLine: 2, SnippetStartLine: 1, FocusLine: 1,
			Snippet: "def target():\n    return True\n"},
		{Rank: 2, FilePath: "b.py", SymbolName: "other", Score: 80,
			StartLine: 1, EndLine: 1, SnippetStartLine: 1, FocusLine: 1,
			Snippet: strings.Repeat("x", 200) + "\n"},
	}}
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 80); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 80 || !strings.Contains(out.String(), "b.py:1") ||
		!strings.Contains(out.String(), "def target():") {
		t.Fatalf("source rescue lost an affordable tail location: %q", out.String())
	}
}

// A wider legacy prefix can accidentally force the ordinary allocator into a
// source-bearing minimal tier. It must not win ahead of the compact-prefix
// source rescue: that makes the public header shape depend on machine latency.
func TestAgentSearchSourceRescuePreservesCompactTelemetry(t *testing.T) {
	response := sem.SearchResponse{
		Results: []sem.SearchResult{{
			Rank: 1, FilePath: "a.py", SymbolName: "target", Score: 25,
			StartLine: 1, EndLine: 2, SnippetStartLine: 1, FocusLine: 1,
			Snippet: "def target():\n    return True\n",
		}},
		Warnings: []sem.ProviderWarning{{Code: "W_DIRTY", FilePath: "a.py"}},
		Completeness: sem.CompletenessReport{Languages: map[string]sem.LanguageCompleteness{
			"Python": {Files: 1, Symbols: 1},
		}},
		Stats: sem.SearchStats{IndexLatencyMS: 139, QueryLatencyMS: 78, PreselectLatencyMS: 194, TotalLatencyMS: 431},
	}
	for _, budget := range []int{64, 60, 56, 52, 48} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			var out bytes.Buffer
			if err := writeAgentSearch(&out, response, budget); err != nil {
				t.Fatal(err)
			}
			if out.Len() > budget || !strings.HasPrefix(out.String(), "I:miss/139") ||
				!strings.Contains(out.String(), "!N W1 F0 L1/1") ||
				!strings.Contains(out.String(), "a.py:1") ||
				!strings.Contains(out.String(), "def target():") {
				t.Fatalf("latency must not change the compact header, coverage, or source at %d bytes: %q", budget, out.String())
			}
		})
	}
}

func TestAgentSearchHeadSourceOutranksUnaffordableTail(t *testing.T) {
	const source = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"
	response := sem.SearchResponse{
		Results: []sem.SearchResult{
			{Rank: 1, FilePath: "a.py", SymbolName: strings.Repeat("N", 100), Score: 25,
				StartLine: 1, EndLine: 1, SnippetStartLine: 1, FocusLine: 1, Snippet: source},
			{Rank: 2, FilePath: strings.Repeat("b", 24) + ".py", SymbolName: "tail", Score: 10,
				StartLine: 1, EndLine: 1, SnippetStartLine: 1, FocusLine: 1,
				Snippet: strings.Repeat("y", 200) + "\n"},
		},
		Warnings: []sem.ProviderWarning{{Code: "W_DIRTY", FilePath: "a.py"}},
		Completeness: sem.CompletenessReport{Languages: map[string]sem.LanguageCompleteness{
			"Python": {Files: 1, Symbols: 1},
		}},
		Stats: sem.SearchStats{IndexLatencyMS: 139, QueryLatencyMS: 78, PreselectLatencyMS: 194, TotalLatencyMS: 431},
	}
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 100); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 100 || !strings.HasPrefix(out.String(), "I:miss/139") ||
		!strings.Contains(out.String(), "!N W1 F0 L1/1") ||
		!strings.Contains(out.String(), source) {
		t.Fatalf("unaffordable tail must not displace all source or compact telemetry: %q", out.String())
	}
}
