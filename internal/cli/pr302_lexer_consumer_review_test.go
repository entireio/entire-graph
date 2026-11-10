package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// This file is portable to base841 and cdb: it uses the shared final-payload
// entry point and no new name-line fields or helpers from the repair. The break
// it catches is promoting source inside a literal/comment to an exact definition,
// or suppressing a genuine nested definition after a decorator comment. Parsed
// retrieval is deliberately outside this pure-renderer test's scope.
func TestReview302LexerConsumerExactNameDefinitions(t *testing.T) {
	tests := []struct {
		name, ext      string
		lines          []string
		end            int
		wantExtra      bool
		wantDeclLine   int
		wantDeclSource string
	}{
		{
			name: "python_raw_string_is_not_extra_definition", ext: ".py",
			lines: []string{
				"def fixture():",
				`    sample = r"\"; def run(): pass"`,
				"    return sample",
			},
			end: 22,
		},
		{
			name: "python_genuine_nested_definition_control", ext: ".py",
			lines: []string{
				"def fixture():",
				"    def run():",
				"        return 1",
				"    return run",
			},
			end: 23, wantExtra: true, wantDeclLine: 21, wantDeclSource: "    def run():",
		},
		{
			name: "rust_nested_comment_is_not_extra_definition", ext: ".rs",
			lines: []string{
				"fn fixture() {",
				"    /* outer",
				"    /* inner */ fn run() {}",
				"    */",
				"}",
			},
			end: 24,
		},
		{
			name: "rust_genuine_nested_definition_control", ext: ".rs",
			lines: []string{
				"fn fixture() {",
				"    fn run() {}",
				"    run();",
				"}",
			},
			end: 23, wantExtra: true, wantDeclLine: 21, wantDeclSource: "    fn run() {}",
		},
		{
			name: "decorator_ordinary_comment_control", ext: ".py",
			lines: []string{
				"def fixture():",
				"    @policy(",
				"        enabled=True, # explanatory comment",
				"    )",
				"    def run():",
				"        return 1",
				"    return run",
			},
			end: 26, wantExtra: true, wantDeclLine: 24, wantDeclSource: "    def run():",
		},
		{
			name: "decorator_comment_preserves_real_nested_definition", ext: ".py",
			lines: []string{
				"def fixture():",
				"    @policy(",
				"        enabled=True, # (",
				"    )",
				"    def run():",
				"        return 1",
				"    return run",
			},
			end: 26, wantExtra: true, wantDeclLine: 24, wantDeclSource: "    def run():",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const budget = 4096
			realDecl, realSource, realEnd := "def run():", "def run():\n    pass\n", 2
			if tt.ext == ".rs" {
				realDecl, realSource, realEnd = "fn run() {}", "fn run() {}\n", 1
			}
			realPath := "src/run" + tt.ext
			fixturePath := "fixtures/source" + tt.ext
			response := sem.SearchResponse{
				Query: "run", Profile: "full",
				Results: []sem.SearchResult{
					{
						Rank: 1, Score: 100, FilePath: realPath,
						StartLine: 1, EndLine: realEnd, FocusLine: 1,
						SnippetStartLine: 1, SnippetEndLine: realEnd,
						SymbolStartLine: 1, SymbolEndLine: realEnd,
						SymbolName: "run", QualifiedName: "run", Kind: "function", Snippet: realSource,
					},
					{
						Rank: 2, Score: 50, FilePath: fixturePath,
						StartLine: 20, EndLine: tt.end, FocusLine: 21,
						SnippetStartLine: 20, SnippetEndLine: tt.end,
						SymbolStartLine: 20, SymbolEndLine: tt.end,
						SymbolName: "fixture", QualifiedName: "fixture", Kind: "function",
						Snippet: strings.Join(tt.lines, "\n") + "\n",
					},
				},
			}
			var out bytes.Buffer
			taken, err := writeAgentSearchPayload(&out, response, budget, agentSearchRender{exactName: true, topK: 10})
			if err != nil {
				t.Fatal(err)
			}
			payload := out.String()
			t.Logf("exact=%v bytes=%d/%d\n%s", taken, out.Len(), budget, payload)
			if out.Len() == 0 || out.Len() > budget {
				t.Fatalf("expected nonempty bounded payload, got %d bytes", out.Len())
			}
			// Rejecting every row or declining exact mode cannot satisfy a
			// negative case: the independent real definition must be delivered.
			if !taken || !strings.Contains(payload, realPath+":1") ||
				!strings.Contains(payload, "\n"+realDecl+"\n") {
				t.Fatalf("genuine run declaration did not survive an actual exact-name answer:\n%s", payload)
			}
			if gotExtra := strings.Contains(payload, fixturePath+":"); gotExtra != tt.wantExtra {
				t.Errorf("fixture row admitted as an extra definition = %v; want %v:\n%s", gotExtra, tt.wantExtra, payload)
			}
			if tt.wantExtra {
				// These in-span declarations have no leading doc/annotation
				// growth: their block starts at the authored declaration line.
				locator := fmt.Sprintf("%s:%d", fixturePath, tt.wantDeclLine)
				if !strings.Contains(payload, locator+" ") && !strings.Contains(payload, locator+"-") {
					t.Errorf("genuine nested declaration missing its file coordinate %q:\n%s", locator, payload)
				}
				if !strings.Contains(payload, "\n"+tt.wantDeclSource+"\n") {
					t.Errorf("genuine nested declaration source %q was not printed:\n%s", tt.wantDeclSource, payload)
				}
			}
		})
	}
}
