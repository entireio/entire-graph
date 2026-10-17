package sem

import "testing"

// A valid naming coordinate hidden below a prefix snippet is not absent
// metadata: an earlier type use cannot replace it. Expectations are literal
// authored coordinates and text, independent of either declaration finder.
func TestReview303KnownNameLineOutsideSnippet(t *testing.T) {
	cases := []struct {
		name               string
		result             SearchResult
		wantFound          bool
		wantLine           int
		wantParsed         bool
		oldStart, oldEnd   int
		oldSnippet         string
		wantStart, wantEnd int
		wantSnippet        string
	}{
		{
			name: "known_hidden_name_does_not_promote_type_use",
			result: SearchResult{
				FilePath: "Options.cs", Kind: "property", SymbolID: "options-property", SymbolName: "Options",
				StartLine: 1, EndLine: 8, FocusLine: 3,
				SymbolStartLine: 1, SymbolEndLine: 8, SymbolNameLine: 5,
				SnippetStartLine: 1, SnippetEndLine: 3,
				Snippet: "public Options\n\n// allocation policy",
			},
			wantFound:   false,
			oldStart:    2,
			oldEnd:      3,
			oldSnippet:  "\n// allocation policy",
			wantStart:   2,
			wantEnd:     3,
			wantSnippet: "\n// allocation policy",
		},
		{
			name: "visible_authoritative_name_wins_over_type_use",
			result: SearchResult{
				FilePath: "Options.cs", Kind: "property", SymbolID: "options-property", SymbolName: "Options",
				StartLine: 1, EndLine: 8, FocusLine: 8,
				SymbolStartLine: 1, SymbolEndLine: 8, SymbolNameLine: 5,
				SnippetStartLine: 1, SnippetEndLine: 8,
				Snippet: "public Options\n\n// allocation policy\n\nOptions\n{\n    get { return _options; }\n}",
			},
			wantFound:   true,
			wantLine:    5,
			wantParsed:  true,
			oldStart:    7,
			oldEnd:      8,
			oldSnippet:  "    get { return _options; }\n}",
			wantStart:   5,
			wantEnd:     6,
			wantSnippet: "Options\n{",
		},
		{
			name: "absent_metadata_allows_genuine_text_declaration",
			result: SearchResult{
				FilePath: "Worker.java", Kind: "method", SymbolID: "run-method", SymbolName: "run",
				StartLine: 10, EndLine: 13, FocusLine: 13,
				SymbolStartLine: 10, SymbolEndLine: 13, SymbolNameLine: 0,
				SnippetStartLine: 10, SnippetEndLine: 13,
				Snippet: "public void run() {\n    work();\n    done();\n}",
			},
			wantFound:   true,
			wantLine:    10,
			wantParsed:  false,
			oldStart:    12,
			oldEnd:      13,
			oldSnippet:  "    done();\n}",
			wantStart:   10,
			wantEnd:     11,
			wantSnippet: "public void run() {\n    work();",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every positive coordinate in this fixture is valid within the
			// symbol's authored span. Invalid-coordinate policy is out of scope.
			if line := tc.result.SymbolNameLine; line > 0 &&
				(line < tc.result.SymbolStartLine || line > tc.result.SymbolEndLine) {
				t.Fatal("fixture has an invalid symbol name coordinate")
			}
			line, found, parsed := searchResultDeclarationLine(tc.result)
			if found != tc.wantFound {
				t.Errorf("declaration found=%v line=%d parsed=%v, want found=%v", found, line, parsed, tc.wantFound)
			} else if found && (line != tc.wantLine || parsed != tc.wantParsed) {
				t.Errorf("declaration line=%d parsed=%v, want line=%d parsed=%v", line, parsed, tc.wantLine, tc.wantParsed)
			}

			old := tersifySearchResult(tc.result, 2)
			if old.SnippetStartLine != tc.oldStart || old.SnippetEndLine != tc.oldEnd || old.Snippet != tc.oldSnippet {
				t.Fatalf("ordinary tail = [%d,%d] %q, want literal baseline [%d,%d] %q", old.SnippetStartLine, old.SnippetEndLine, old.Snippet, tc.oldStart, tc.oldEnd, tc.oldSnippet)
			}
			if old.SnippetStartLine == tc.result.SnippetStartLine && old.SnippetEndLine == tc.result.SnippetEndLine {
				t.Fatal("fixture did not actually exercise tail clipping")
			}
			got := tersifySearchResultKeepingDeclaration(tc.result, 2)
			if got.SnippetStartLine != tc.wantStart || got.SnippetEndLine != tc.wantEnd || got.Snippet != tc.wantSnippet {
				t.Errorf("declaration-preserving tail = [%d,%d] %q, want [%d,%d] %q", got.SnippetStartLine, got.SnippetEndLine, got.Snippet, tc.wantStart, tc.wantEnd, tc.wantSnippet)
			}
		})
	}
}
