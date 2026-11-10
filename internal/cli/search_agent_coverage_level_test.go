package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// "Coverage: degraded" tells a reader that a fact the answer could have used may
// be missing. On a repository whose only partial failures were 46 minified JSON
// benchmark fixtures and one oversized JSON snapshot, every query printed it,
// although no JSON file holds a code fact and every Go and Python file had
// parsed. The level now depends on what a failure can reach; the count and the
// itemized list are unchanged.
func TestAgentCoverageLevelIgnoresDataFormatFailures(t *testing.T) {
	t.Parallel()

	dataOnly := []sem.PartialFailure{
		{Code: "E_MINIFIED", Severity: "warning", FilePath: "benchmarks/agent-brain/a.json"},
		{Code: "E_MINIFIED", Severity: "warning", FilePath: ".entire/runners/b.json"},
		{Code: "E_FILE_TOO_LARGE", Severity: "warning", FilePath: "SNAPSHOT.json"},
		{Code: "E_PARSE_ERROR", Severity: "warning", FilePath: ".github/workflows/ci.yml"},
	}
	cases := []struct {
		name     string
		failures []sem.PartialFailure
		level    string
		marker   string
	}{
		{"data and config formats only", dataOnly, "notice", "!N"},
		{"plus one Go parse failure", append(append([]sem.PartialFailure{}, dataOnly...),
			sem.PartialFailure{Code: "E_PARSE_ERROR", Severity: "warning", FilePath: "pkg/a.go"}), "degraded", "!D"},
		{"plus a shortfall that names a JSON path", append(append([]sem.PartialFailure{}, dataOnly...),
			sem.PartialFailure{Code: "E_REPO_IGNORE_UNREADABLE", Severity: "warning", FilePath: "vendor/x.json"}), "degraded", "!D"},
		{"plus a shortfall with no path", append(append([]sem.PartialFailure{}, dataOnly...),
			sem.PartialFailure{Code: "E_REPO_IGNORE_GIT_UNAVAILABLE", Severity: "warning"}), "degraded", "!D"},
		{"plus a file of unknown language", append(append([]sem.PartialFailure{}, dataOnly...),
			sem.PartialFailure{Code: "E_PARSE_ERROR", Severity: "warning", FilePath: "x.qqq"}), "degraded", "!D"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := sem.SearchResponse{PartialFailures: tc.failures}
			full, compact := agentSearchDiagnostics(response)
			wantLine := "Coverage: " + tc.level + " ("
			if !strings.HasPrefix(string(full), wantLine) {
				t.Fatalf("want %q, got:\n%s", wantLine, full)
			}
			// The failures are still counted and itemized as before.
			wantCount := fmt.Sprintf("; %d partial failures)", len(tc.failures))
			if !strings.Contains(string(full), wantCount) {
				t.Fatalf("coverage line lost the failure count %q:\n%s", wantCount, full)
			}
			if !strings.Contains(string(full), "- partial E_MINIFIED: benchmarks/agent-brain/a.json") {
				t.Fatalf("data-format failure is no longer listed:\n%s", full)
			}
			if !strings.HasPrefix(string(compact), tc.marker+" ") {
				t.Fatalf("compact marker: want %q, got %q", tc.marker, compact)
			}
		})
	}
}

// A budget too small for the coverage line falls back to a bare marker. That
// rung decided its own level ("any partial failure is !D"), so the same response
// read notice in the roomy form and degraded in the tiny one. Both now use the
// one level function.
func TestAgentTinyBudgetMarkerFollowsTheCoverageLevel(t *testing.T) {
	t.Parallel()

	result := []sem.SearchResult{{
		Rank: 1, FilePath: "src/service.go", StartLine: 1, EndLine: 900,
		FocusLine: 42, SnippetStartLine: 40, SnippetEndLine: 44,
		SymbolName: "serve", Snippet: "func serve() {\n\tprepare()\n\trun()\n\tcleanup()\n}",
	}}
	cases := []struct {
		name     string
		failures []sem.PartialFailure
		level    string
		marker   string
	}{
		{"data-format failures only", []sem.PartialFailure{
			{Code: "E_MINIFIED", Severity: "warning", FilePath: "fixtures/a.json"},
			{Code: "E_FILE_TOO_LARGE", Severity: "warning", FilePath: "SNAPSHOT.json"},
		}, "notice", "!N"},
		{"one source parse failure", []sem.PartialFailure{
			{Code: "E_MINIFIED", Severity: "warning", FilePath: "fixtures/a.json"},
			{Code: "E_PARSE_ERROR", Severity: "warning", FilePath: "pkg/a.go"},
		}, "degraded", "!D"},
		{"a shortfall with no path", []sem.PartialFailure{
			{Code: "E_REPO_IGNORE_GIT_UNAVAILABLE", Severity: "warning"},
		}, "degraded", "!D"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := sem.SearchResponse{
				Results:         result,
				PartialFailures: tc.failures,
				Completeness: sem.CompletenessReport{Languages: map[string]sem.LanguageCompleteness{
					"Go": {Files: 3, Symbols: 8},
				}},
			}
			var roomy bytes.Buffer
			if err := writeAgentSearch(&roomy, response, 4096); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(roomy.String(), "Coverage: "+tc.level+" (") {
				t.Fatalf("roomy output: want Coverage: %s, got:\n%s", tc.level, roomy.String())
			}
			for _, capBytes := range []int{2, 8, 12, 24} {
				var tiny bytes.Buffer
				if err := writeAgentSearch(&tiny, response, capBytes); err != nil {
					t.Fatal(err)
				}
				if tiny.Len() > capBytes {
					t.Fatalf("cap %d: output used %d bytes: %q", capBytes, tiny.Len(), tiny.String())
				}
				other := "!D"
				if tc.marker == "!D" {
					other = "!N"
				}
				if !strings.Contains(tiny.String(), tc.marker) || strings.Contains(tiny.String(), other) {
					t.Fatalf("cap %d: want marker %s to match the full line's %s, got %q", capBytes, tc.marker, tc.level, tiny.String())
				}
			}
		})
	}
}
