package cli

import (
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
