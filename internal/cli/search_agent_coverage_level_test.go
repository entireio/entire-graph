package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// "Coverage: degraded" used to print whenever a response carried any partial
// failure: seven failures among a thousand files, or a repository whose only
// failures were minified JSON fixtures, opened every answer with it. The graph
// already has a degradation rule, completeness.health.status, and the line now
// follows it. The count and the itemized list are unchanged.
func TestAgentCoverageLevelFollowsHealthStatus(t *testing.T) {
	t.Parallel()

	fileFailures := []sem.PartialFailure{
		{Code: "E_MINIFIED", Severity: "warning", FilePath: "benchmarks/agent-brain/a.json"},
		{Code: "E_PARSE_ERROR", Severity: "warning", FilePath: "pkg/a.go"},
		{Code: "E_FILE_TOO_LARGE", Severity: "warning", FilePath: "dist/app.min.js"},
	}
	with := func(extra ...sem.PartialFailure) []sem.PartialFailure {
		return append(append([]sem.PartialFailure{}, fileFailures...), extra...)
	}
	cases := []struct {
		name     string
		status   string
		failures []sem.PartialFailure
		level    string
		marker   string
	}{
		{"health ok", "ok", fileFailures, "notice", "!N"},
		{"health degraded", "degraded", fileFailures, "degraded", "!D"},
		// The compact marker has two states; unsafe shares !D.
		{"health unsafe", "unsafe", fileFailures, "unsafe", "!D"},
		{"a producer without health reads as ok", "", fileFailures, "notice", "!N"},
		// Health has no numerator for a failure with no file bound, and the
		// repo-ignore shortfalls join the response after health is computed.
		{"health ok plus a shortfall that names a path", "ok",
			with(sem.PartialFailure{Code: "E_REPO_IGNORE_UNREADABLE", Severity: "warning", FilePath: "vendor/x.json"}), "degraded", "!D"},
		{"health ok plus a shortfall with no path", "ok",
			with(sem.PartialFailure{Code: "E_REPO_IGNORE_GIT_UNAVAILABLE", Severity: "warning"}), "degraded", "!D"},
		{"health ok plus a spent analysis budget", "ok",
			with(sem.PartialFailure{Code: sem.AnalysisBudgetExceededCode, Severity: "warning"}), "degraded", "!D"},
		{"a snapshot-scoped failure never softens unsafe", "unsafe",
			with(sem.PartialFailure{Code: "E_REPO_IGNORE_GIT_UNAVAILABLE", Severity: "warning"}), "unsafe", "!D"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := sem.SearchResponse{PartialFailures: tc.failures}
			response.Completeness.Health.Status = tc.status
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
			if !strings.Contains(string(full), "- partial ") {
				t.Fatalf("failures are no longer listed:\n%s", full)
			}
			if !strings.HasPrefix(string(compact), tc.marker+" ") {
				t.Fatalf("compact marker: want %q, got %q", tc.marker, compact)
			}
		})
	}
}
