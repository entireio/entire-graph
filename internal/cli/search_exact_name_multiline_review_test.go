package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The mandatory line of an exact answer must be the source declaration, not an
// annotation argument. An intervening long argument makes a wrong anchor visible
// even at a roomy response budget; the ordinary rendering is a positive control.
func TestReview302ExactNameMultilineAnnotationRendering(t *testing.T) {
	for _, tt := range []struct {
		name, argument string
		control        bool
	}{
		{"noncolliding_annotation_control", "    enabled = true,", true},
		{"annotation_argument_is_not_signature", "    run = true,", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const signature = "public void run() {}"
			snippet := "@Policy(\n" + tt.argument + "\n    note = \"" + strings.Repeat("x", 1100) + "\",\n    audit = false\n)\n" + signature + "\n"
			response := sem.SearchResponse{
				Query: "run", Profile: "full",
				Results: []sem.SearchResult{{
					Rank: 1, Score: 100, FilePath: "src/Handler.java",
					SymbolName: "run", QualifiedName: "Handler.run", Kind: "method",
					StartLine: 1, EndLine: 6, FocusLine: 6,
					SnippetStartLine: 1, SnippetEndLine: 6,
					SymbolStartLine: 1, SymbolEndLine: 6, Snippet: snippet,
				}},
				VerifyCommand: &sem.SearchVerifyCommand{Command: "mvn test -Dtest=HandlerTest", Tier: "narrow"},
			}
			before, _ := review302RenderExactName(t, response, false)
			if !exactNameLineShown(before, "src/Handler.java", signature) {
				t.Fatalf("ordinary-render positive control lost the real signature:\n%s", before)
			}
			after, taken := review302RenderExactName(t, response, true)
			if tt.control && !taken {
				t.Fatal("noncolliding annotation control did not exercise exact mode")
			}
			if taken && !exactNameLineShown(after, "src/Handler.java", signature) {
				t.Errorf("exact answer shows annotation text but hides the retrieved declaration:\n%s", after)
			}
			if !taken && after != before {
				t.Error("declined exact mode did not preserve ordinary output")
			}
			if !strings.Contains(after, "VERIFY: mvn test -Dtest=HandlerTest") {
				t.Errorf("render lost VERIFY at a roomy budget:\n%s", after)
			}
		})
	}
}

// A fixture string is not a second definition. The real local declaration
// control prevents a blanket removal of all definitions inside other symbols.
func TestReview302ExactNameMultilineRawStringRendering(t *testing.T) {
	for _, tt := range []struct {
		name, snippet string
		end           int
		wantExtra     bool
	}{
		{
			name:      "genuine_local_declaration_control",
			snippet:   "func TestRun(t *testing.T) {\n    run := func() {}\n    run()\n}\n",
			end:       4,
			wantExtra: true,
		},
		{
			name:    "raw_string_fixture_is_not_definition",
			snippet: "func TestRun(t *testing.T) {\n    src := `\nfunc run() {}\n`\n    _ = src\n}\n",
			end:     6,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := review302RealRunResponse()
			response.Results = append(response.Results, sem.SearchResult{
				Rank: 2, Score: 50, FilePath: "fixtures/parser_test.go",
				SymbolName: "TestRun", QualifiedName: "TestRun", Kind: "function",
				StartLine: 1, EndLine: tt.end, FocusLine: 2,
				SnippetStartLine: 1, SnippetEndLine: tt.end,
				SymbolStartLine: 1, SymbolEndLine: tt.end, Snippet: tt.snippet,
			})
			payload, taken := review302RenderExactName(t, response, true)
			if !taken || !exactNameLineShown(payload, "src/run.go", "func run() {}") {
				t.Fatalf("genuine declaration did not survive an exact answer:\n%s", payload)
			}
			if got := strings.Contains(payload, "fixtures/parser_test.go:"); got != tt.wantExtra {
				t.Errorf("extra definition block present = %v; want %v:\n%s", got, tt.wantExtra, payload)
			}
			if tt.wantExtra && !exactNameLineShown(payload, "fixtures/parser_test.go", "    run := func() {}") {
				t.Errorf("genuine local-definition control is not shown:\n%s", payload)
			}
		})
	}
}

// The query is ASCII, but a longer source identifier may contain Unicode. The
// source identifier must match as a whole; rejecting all second rows is not a fix.
func TestReview302ExactNameIdentifierBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, symbol, snippet string
		wantExtra             bool
	}{
		{"ascii_suffix_control", "runHelper", "func runHelper() {}\n", false},
		{"underscore_suffix_control", "run_2", "func run_2() {}\n", false},
		{"same_name_control", "run", "func run() {}\n", true},
		{"unicode_suffix_is_not_exact", "runé", "func runé() {}\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := review302RealRunResponse()
			response.Results = append(response.Results, sem.SearchResult{
				Rank: 2, Score: 50, FilePath: "other/run.go",
				SymbolName: tt.symbol, QualifiedName: tt.symbol, Kind: "function",
				StartLine: 1, EndLine: 1, FocusLine: 1,
				SnippetStartLine: 1, SnippetEndLine: 1,
				SymbolStartLine: 1, SymbolEndLine: 1, Snippet: tt.snippet,
			})
			payload, taken := review302RenderExactName(t, response, true)
			if !taken || !exactNameLineShown(payload, "src/run.go", "func run() {}") {
				t.Fatalf("genuine declaration did not survive an exact answer:\n%s", payload)
			}
			if got := strings.Contains(payload, "other/run.go:"); got != tt.wantExtra {
				t.Errorf("second exact-definition block present = %v; want %v:\n%s", got, tt.wantExtra, payload)
			}
			if tt.wantExtra && !exactNameLineShown(payload, "other/run.go", "func run() {}") {
				t.Errorf("second genuine exact declaration was not shown:\n%s", payload)
			}
		})
	}
}

func review302RealRunResponse() sem.SearchResponse {
	return sem.SearchResponse{
		Query: "run", Profile: "full",
		Results: []sem.SearchResult{{
			Rank: 1, Score: 100, FilePath: "src/run.go",
			SymbolName: "run", QualifiedName: "run", Kind: "function",
			StartLine: 1, EndLine: 1, FocusLine: 1,
			SnippetStartLine: 1, SnippetEndLine: 1,
			SymbolStartLine: 1, SymbolEndLine: 1, Snippet: "func run() {}\n",
		}},
	}
}

// This invokes the production formatter only: no repository, provider, parser,
// cache, Git process, network, model, or filesystem-backed fixture is involved.
func review302RenderExactName(t *testing.T, response sem.SearchResponse, enabled bool) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	taken, err := writeAgentSearchPayload(&out, response, 4096, agentSearchRender{exactName: enabled, topK: 10})
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() > 4096 {
		t.Fatalf("rendered %d bytes, exceeding the 4096-byte response budget", out.Len())
	}
	return out.String(), taken
}
