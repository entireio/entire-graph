package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// All content and coordinates are authored. No real repository, transcript,
// cache, subprocess, model, or runStats invocation is needed. This file has no
// dependency on another test file: only the actual JSON renderer and stats path.
func TestReview304DeliveredPassageFollowUpClassification(t *testing.T) {
	response := sem.SearchResponse{
		FormatVersion: sem.SearchFormatVersion,
		Query:         "authored prose",
		RepoRoot:      "/repo",
		Profile:       "syntax-only",
		Results: []sem.SearchResult{{
			Rank: 1, Score: 1, FilePath: "notes.md",
			StartLine: 1, EndLine: 100, FocusLine: 1,
			SnippetStartLine: 1, SnippetEndLine: 1,
			Language: "markdown", Kind: "document", Signals: []string{"prose"},
			Snippet: "primary evidence",
			Passages: []sem.SearchPassage{{
				StartLine: 100, EndLine: 100, FocusLine: 100,
				Snippet: "delivered distant answer",
			}},
		}},
	}
	var output bytes.Buffer
	if err := writeSearchResponse(&output, response, "json", 0); err != nil {
		t.Fatalf("render authored search response: %v", err)
	}
	graphBody := output.String()
	var delivered sem.SearchResponse
	if err := json.Unmarshal(output.Bytes(), &delivered); err != nil {
		t.Fatalf("rendered search response is not complete JSON: %v", err)
	}
	if len(delivered.Results) != 1 || len(delivered.Results[0].Passages) != 1 ||
		delivered.Results[0].SnippetStartLine != 1 || delivered.Results[0].Snippet != "primary evidence" ||
		delivered.Results[0].Passages[0].StartLine != 100 ||
		delivered.Results[0].Passages[0].Snippet != "delivered distant answer" {
		t.Fatal("fixture must actually deliver both authored, separated source slices")
	}

	for _, tc := range []struct {
		name        string
		offset      int
		readBody    string
		wantOverlap int
		wantOther   int
	}{
		{"primary_span_control", 1, "1→primary evidence", 1, 0},
		{"delivered_passage", 100, "100→delivered distant answer", 1, 0},
		{"truly_disjoint_control", 50, "50→not in the graph answer", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := func(tick int, role string, block map[string]any) string {
				t.Helper()
				stamp := time.Date(2026, 9, 29, 12, 0, tick, 0, time.UTC)
				encoded, err := json.Marshal(map[string]any{
					"type": role, "timestamp": stamp.Format(time.RFC3339),
					"message": map[string]any{
						"id": fmt.Sprintf("review-passage-message-%d", tick),
						"role": role, "content": []any{block},
					},
				})
				if err != nil {
					t.Fatalf("encode authored transcript record: %v", err)
				}
				return string(encoded)
			}
			records := []string{
				record(0, "assistant", map[string]any{
					"type": "tool_use", "id": "g1", "name": "Bash",
					"input": map[string]any{
						"command": `entire graph search --repo /repo --format json --single-resolution --document-resolution --query "authored prose"`,
					},
				}),
				record(1, "user", map[string]any{
					"type": "tool_result", "tool_use_id": "g1", "content": graphBody, "is_error": false,
				}),
				record(2, "assistant", map[string]any{
					"type": "tool_use", "id": "r1", "name": "Read",
					"input": map[string]any{"file_path": "/repo/notes.md", "offset": tc.offset, "limit": 1},
				}),
				record(3, "user", map[string]any{
					"type": "tool_result", "tool_use_id": "r1", "content": tc.readBody, "is_error": false,
				}),
			}
			path := filepath.Join(t.TempDir(), "authored-passage.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
				t.Fatalf("write authored transcript: %v", err)
			}
			summary, ok := summariseTranscript(path)
			if !ok || summary.Malformed != 0 || len(summary.Calls) != 2 {
				t.Fatalf("fixture did not decode exactly two calls: ok=%v malformed=%d calls=%d",
					ok, summary.Malformed, len(summary.Calls))
			}
			for _, call := range summary.Calls {
				if !call.HasResult {
					t.Fatalf("fixture lost result for call %q", call.ID)
				}
			}
			collector := newStatsCollector()
			collector.session("authored-passage").merge(summary)
			report := statsResponse{SessionsDirFound: true, Since: "all", DisplacementWindowCalls: 5}
			collector.finish(&report, time.Time{})
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatalf("encode public statistics: %v", err)
			}
			var public map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &public); err != nil {
				t.Fatalf("decode public statistics: %v", err)
			}
			want := map[string]int{
				"sessions":                                    1,
				"graph_locate_calls":                          1,
				"credited_graph_calls":                        1,
				"exploration_calls":                           1,
				"graph_locate_follow_up_read":                  1,
				"graph_locate_follow_up_read_overlapping":      tc.wantOverlap,
				"graph_locate_follow_up_read_different_region": tc.wantOther,
				"graph_locate_follow_up_read_unknown_span":     0,
				"graph_locate_no_follow_up":                    0,
				"graph_locate_requery":                         0,
				"graph_locate_censored":                        0,
				"graph_locate_error":                           0,
				"graph_locate_ineligible":                      0,
			}
			for key, expected := range want {
				raw, exists := public[key]
				var got int
				if !exists || json.Unmarshal(raw, &got) != nil {
					t.Errorf("public integer counter %q missing or malformed", key)
					continue
				}
				if got != expected {
					t.Errorf("%s = %d, want %d for the authored read at line %d", key, got, expected, tc.offset)
				}
			}
			if report.GraphReturnedBytes != int64(len(graphBody)) || report.ExplorationReturnedBytes != int64(len(tc.readBody)) {
				t.Errorf("delivered-result premise lost: graph_bytes=%d read_bytes=%d",
					report.GraphReturnedBytes, report.ExplorationReturnedBytes)
			}
		})
	}
}
