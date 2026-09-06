package sem

import (
	"strings"
	"testing"
)

// searchTestRankFixture is the measured mechanism reproduced at fixture scale (sharkdp__bat-2260,
// fmtlib__fmt-2457): a SHORT test file whose body is a dense run of the exact code-like literals an
// issue harvester scraped into the query, next to a longer source file that owns the reported
// behaviour but never mentions those literals. Maximal term density on a minimal document length is
// what lets BM25 plus the capped exact-code-token bonus outrun the flat -12 test-path demotion, so
// the test takes rank 1 even though the demotion fired.
//
// Measured on this fixture at db24f74: test 42.598, best source 24.606 — an 18-point gap, so the
// -12 is demonstrably applied and demonstrably not enough.
func searchTestRankFixture(t *testing.T) (repo string, query string) {
	t.Helper()
	repo = t.TempDir()
	// A build manifest, so VERIFY derivation is live rather than degenerate. Without it the payload
	// reports "none derivable" and the neutrality assertion below would be vacuous.
	writeFile(t, repo, "go.mod", "module example.com/mapping\n\ngo 1.22\n")
	writeFile(t, repo, "src/syntax_mapping.go", `package syntax

import "strings"

// MappingTarget selects the syntax used for a path.
type MappingTarget struct {
	rules  []string
	ignore []string
}

// ResolveSyntax picks a syntax for path, honouring both the explicit map syntax rules and the
// ignored suffix list. When a suffix is ignored the remaining stem is re-resolved.
func (target *MappingTarget) ResolveSyntax(path string) string {
	for _, suffix := range target.ignore {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
		}
	}
	for _, rule := range target.rules {
		if strings.HasPrefix(path, rule) {
			return rule
		}
	}
	return ""
}
`)
	writeFile(t, repo, "tests/integration_test.go", `package tests

import "os"

func rawCommandWithConfig() {
	os.Unsetenv("BAT_PAGER")
	os.Unsetenv("BAT_CACHE_PATH")
	os.Unsetenv("BAT_CONFIG_PATH")
	os.Unsetenv("BAT_OPTS")
	os.Unsetenv("PAGER")
	os.Unsetenv("LESS")
}
`)
	// The query an issue harvester actually produces: the bug sentence plus the environment dump
	// pasted into the report. It carries no test-intent word, so the demotion gate is open.
	query = "BAT_PAGER BAT_CACHE_PATH BAT_CONFIG_PATH BAT_OPTS PAGER LESS " +
		"the options map syntax and ignored suffix cannot work together"
	return repo, query
}

// TestSearchNeverRanksATestFirstWhenAFixSiteExists is the end-to-end regression for the rank-1 test
// defect. It deliberately calls nothing but the public search entry point, so it compiles and runs
// unchanged against a tree without the reordering pass — which is the only way it can prove that
// the pass is what makes it green.
func TestSearchNeverRanksATestFirstWhenAFixSiteExists(t *testing.T) {
	t.Parallel()
	repo, query := searchTestRankFixture(t)
	response, err := SearchRepository(t.Context(), repo, "test-version", query,
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) < 2 {
		t.Fatalf("fixture produced %d results, so it cannot exercise the ordering", len(response.Results))
	}

	// The fixture is only evidence while the test file is genuinely competitive. If some later
	// scoring change sinks it out of the payload this test silently stops testing anything, so it
	// reports that rather than passing.
	testRank, testScore, bestSourceScore := 0, 0.0, 0.0
	for _, result := range response.Results {
		if searchTestArtifactPath(searchLowerPath(result.FilePath)) {
			if testRank == 0 {
				testRank, testScore = result.Rank, result.Score
			}
			continue
		}
		if result.Score > bestSourceScore {
			bestSourceScore = result.Score
		}
	}
	if testRank == 0 {
		t.Fatalf("no test file in the payload, so this fixture no longer exercises the defect: %s",
			searchTestRankOrder(response.Results))
	}
	if testScore <= bestSourceScore {
		t.Fatalf("the test no longer OUTSCORES the source (test %.3f, best source %.3f), so the "+
			"fixture stopped exercising the case where the -12 demotion is not enough: %s",
			testScore, bestSourceScore, searchTestRankOrder(response.Results))
	}

	// The defect: a test file is never a fix site (0/30 gold patches on the measured sample touch
	// one), and rank 1 is the slot the agent reads and edits from.
	if searchTestArtifactPath(searchLowerPath(response.Results[0].FilePath)) {
		t.Fatalf("rank 1 is a test file, which is never a fix site: %s",
			searchTestRankOrder(response.Results))
	}

	// The pass reorders; it never drops. Losing the test is the failure mode a bigger score
	// demotion has, and it is what breaks VERIFY derivation.
	if testRank != 2 {
		t.Fatalf("the demoted test should sit immediately below the best fix site, got rank %d: %s",
			testRank, searchTestRankOrder(response.Results))
	}

	// Scores are NOT rewritten to make the list look monotonic. The score is the caller's only
	// signal for "is this a real hit or is the repo simply missing what I asked for", so a
	// deliberate ordering decision must stay visible in it rather than being papered over.
	if response.Results[1].Score <= response.Results[0].Score {
		t.Fatalf("scores were rewritten to hide the reorder: %s", searchTestRankOrder(response.Results))
	}

	// VERIFY neutrality, the property that makes reordering the right instrument and a larger
	// subtraction the wrong one: the deriver reads the ranked test when the covering-test section
	// declined to reprint it, so demoting by score can convert a correctly scoped command into a
	// wrong one. Moving cannot.
	if response.VerifyCommand == nil {
		t.Fatal("no VERIFY command derived, so the neutrality assertion is vacuous")
	}
	if got := response.VerifyCommand.Command; got != "go test ./tests" {
		t.Fatalf("VERIFY moved off the ranked test's own package: %q", got)
	}
}

func searchTestRankOrder(results []SearchResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		parts = append(parts, result.FilePath)
	}
	return strings.Join(parts, " | ")
}
