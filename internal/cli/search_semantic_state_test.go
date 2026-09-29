package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// semanticStateResponse is a valid literal response carrying one result and the given channel
// state; an empty status is the unconfigured channel.
func semanticStateResponse(t *testing.T, status string, result sem.SearchResult) sem.SearchResponse {
	t.Helper()
	response := sem.SearchResponse{
		FormatVersion: sem.SearchFormatVersion,
		Query:         "widget", RepoRoot: "/fixture", Profile: "fast",
		Commit: "fixture-commit", Tree: "fixture-tree",
		Results:  []sem.SearchResult{result},
		Warnings: []sem.ProviderWarning{},
		Stats:    sem.SearchStats{ContextBudgetBytes: 4096, CandidatesSelected: 1, SemanticStatus: status},
	}
	if status == sem.SemanticStatusUsed {
		response.Stats.SemanticResults = 1
	}
	if strings.HasPrefix(status, "unavailable:") {
		response.Warnings = []sem.ProviderWarning{{Code: "W_SEMANTIC_UNAVAILABLE", Severity: "warning",
			EffectOnCompleteness: "semantic retrieval skipped; ranking is lexical only", Detail: "SEMANTIC_DETAIL_TEXT"}}
	}
	encoded, err := json.Marshal(response.Results)
	if err != nil {
		t.Fatal(err)
	}
	response.Stats.ResultBytes = len(encoded)
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	return response
}

func semanticStateLexicalRow() sem.SearchResult {
	return sem.SearchResult{
		Rank: 1, Score: 40, FilePath: "src/widget.go", Language: "go",
		StartLine: 1, EndLine: 1, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 1,
		SymbolID: "widget-id", SymbolName: "widget", Kind: "function", SymbolStartLine: 1, SymbolEndLine: 1,
		Snippet: "func widget() {}", Signals: []string{},
	}
}

// Q4: a configured channel's state (used / off / unavailable, as a stable code) is visible in text
// and agent output, including at a tight agent budget; the agent cap is never exceeded; warning
// detail text never leaks into either; an unconfigured payload carries no trace of the channel.
func TestSemanticStateIsVisibleInTextAndAgent(t *testing.T) {
	for _, tc := range []struct {
		status, text, agent string
	}{
		{status: sem.SemanticStatusUsed, text: "semantic: used (1 results, 0 nominated files)", agent: "sem=used/1"},
		{status: sem.SemanticStatusOffFlag, text: "semantic: off:flag", agent: "sem=off:flag"},
		{status: sem.SemanticStatusOffWorktree, text: "semantic: off:worktree", agent: "sem=off:worktree"},
		{status: "unavailable:endpoint", text: "semantic: unavailable:endpoint - results are lexical only", agent: "sem=unavailable:endpoint"},
	} {
		response := semanticStateResponse(t, tc.status, semanticStateLexicalRow())
		var text bytes.Buffer
		if err := writeSearchResponse(&text, response, "text", 4096); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text.String(), tc.text) {
			t.Fatalf("%s: text output lacks %q:\n%s", tc.status, tc.text, text.String())
		}
		for _, budget := range []int{4096, 64, 40} {
			var agent bytes.Buffer
			if err := writeSearchResponse(&agent, response, "agent", budget); err != nil {
				t.Fatal(err)
			}
			if agent.Len() > budget {
				t.Fatalf("%s: agent output %d bytes exceeds budget %d", tc.status, agent.Len(), budget)
			}
			if !strings.Contains(agent.String(), tc.agent) {
				t.Fatalf("%s at budget %d: agent output lacks %q:\n%q", tc.status, budget, tc.agent, agent.String())
			}
			if strings.Contains(agent.String(), "SEMANTIC_DETAIL_TEXT") {
				t.Fatalf("%s: warning detail leaked into agent output", tc.status)
			}
		}
		if strings.Contains(text.String(), "SEMANTIC_DETAIL_TEXT") {
			t.Fatalf("%s: warning detail leaked into text output", tc.status)
		}
	}
	unconfigured := semanticStateResponse(t, "", semanticStateLexicalRow())
	for _, format := range []string{"text", "agent"} {
		var out bytes.Buffer
		if err := writeSearchResponse(&out, unconfigured, format, 4096); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "sem") {
			t.Fatalf("unconfigured %s output mentions the channel: %q", format, out.String())
		}
	}
}

// Q1 rendering: a row the channel synthesized shows its own labelled score, never a lexical 0.
func TestSemanticOnlyRowRendersItsOwnScore(t *testing.T) {
	row := semanticStateLexicalRow()
	row.Score, row.SemanticScore, row.Signals = 0, 0.83, []string{"semantic:embedding"}
	response := semanticStateResponse(t, sem.SemanticStatusUsed, row)
	var text, agent bytes.Buffer
	if err := writeSearchResponse(&text, response, "text", 4096); err != nil {
		t.Fatal(err)
	}
	if err := writeSearchResponse(&agent, response, "agent", 4096); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "semantic_score=0.8300") || strings.Contains(text.String(), "score=0.0000") {
		t.Fatalf("text renders the synthesized row's score wrongly:\n%s", text.String())
	}
	if !strings.Contains(agent.String(), " e=0.83") || strings.Contains(agent.String(), " s=0.0") {
		t.Fatalf("agent renders the synthesized row's score wrongly:\n%s", agent.String())
	}
}
