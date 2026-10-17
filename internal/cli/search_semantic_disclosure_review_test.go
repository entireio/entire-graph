package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// This is a bounded characterization, not an assertion of new disclosure
// wording. It observes real renderers so the unsettled text/agent contract can
// be decided using delivered bytes. No command, provider, filesystem or HTTP
// entry point is invoked.
func TestSemanticReviewDisclosureRenderingObservation(t *testing.T) {
	const status = "unavailable:review-disclosure-marker"
	const code = "W_SEMANTIC_UNAVAILABLE"
	const detail = "review-semantic-fallback-detail"
	for _, hasHit := range []bool{true, false} {
		name := "hits"
		if !hasHit {
			name = "no_hits"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, format string
				budget       int
			}{
				{name: "text_full", format: "text", budget: 4096},
				{name: "agent_full", format: "agent", budget: 4096},
				{name: "agent_compact", format: "agent", budget: 64},
				{name: "json", format: "json", budget: 4096},
				{name: "ndjson", format: "ndjson", budget: 4096},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response := sem.SearchResponse{
						FormatVersion: sem.SearchFormatVersion,
						Query:         "widget", RepoRoot: "/fixture", Profile: "fast",
						Commit: "fixture-commit", Tree: "fixture-tree",
						Results: []sem.SearchResult{}, Warnings: []sem.ProviderWarning{},
						Stats: sem.SearchStats{ContextBudgetBytes: 4096},
					}
					if hasHit {
						response.Results = []sem.SearchResult{{
							Rank: 1, Score: 40, FilePath: "src/widget.go", Language: "go",
							StartLine: 1, EndLine: 1, FocusLine: 1,
							SnippetStartLine: 1, SnippetEndLine: 1,
							SymbolID: "widget-id", SymbolName: "widget", Kind: "function",
							SymbolStartLine: 1, SymbolEndLine: 1,
							Snippet: "func widget() {}", Signals: []string{},
						}}
						response.Stats.CandidatesSelected = 1
					}
					resultBytes, err := json.Marshal(response.Results)
					if err != nil {
						t.Fatal(err)
					}
					response.Stats.ResultBytes = len(resultBytes)
					if err := response.Validate(); err != nil {
						t.Fatalf("invalid literal renderer fixture: %v", err)
					}

					var control, fallback bytes.Buffer
					if err := writeSearchResponse(&control, response, tc.format, tc.budget); err != nil {
						t.Fatal(err)
					}
					response.Stats.SemanticStatus = status
					response.Warnings = []sem.ProviderWarning{{
						Code: code, Severity: "warning", Detail: detail,
						EffectOnCompleteness: "semantic retrieval skipped; ranking is lexical only",
					}}
					if err := writeSearchResponse(&fallback, response, tc.format, tc.budget); err != nil {
						t.Fatal(err)
					}
					plain, degraded := control.String(), fallback.String()
					for _, marker := range []string{status, code, detail} {
						if strings.Contains(plain, marker) {
							t.Fatalf("unconfigured control unexpectedly contains disclosure marker %q", marker)
						}
					}
					statusVisible := strings.Contains(degraded, status)
					codeVisible := strings.Contains(degraded, code)
					detailVisible := strings.Contains(degraded, detail)
					genericDiagnostic := strings.Contains(degraded, "1 warning") ||
						strings.Contains(degraded, " W1 ") || strings.Contains(degraded, "!N")
					genericOnly := genericDiagnostic && !statusVisible && !codeVisible && !detailVisible
					t.Logf("control_bytes=%d fallback_bytes=%d equal=%t status=%t code=%t detail=%t generic_only=%t",
						len(plain), len(degraded), plain == degraded, statusVisible, codeVisible, detailVisible, genericOnly)
					t.Logf("CONTROL=%q", plain)
					t.Logf("FALLBACK=%q", degraded)

					// Existing guarantees, not a newly imposed text/agent design:
					// machine records remain valid and carry the provided metadata;
					// the agent renderer respects its actual byte ceiling.
					if tc.format == "json" || tc.format == "ndjson" {
						for _, output := range []string{plain, degraded} {
							for _, record := range strings.Split(strings.TrimSpace(output), "\n") {
								if !json.Valid([]byte(record)) {
									t.Fatalf("renderer returned an invalid JSON record: %q", record)
								}
							}
						}
						if !statusVisible || !codeVisible || !detailVisible {
							t.Fatal("machine renderer lost the supplied semantic status/warning metadata")
						}
					}
					if tc.format == "agent" && (len(plain) > tc.budget || len(degraded) > tc.budget) {
						t.Fatalf("agent output exceeds %d-byte budget: control=%d fallback=%d", tc.budget, len(plain), len(degraded))
					}
				})
			}
		})
	}
}
