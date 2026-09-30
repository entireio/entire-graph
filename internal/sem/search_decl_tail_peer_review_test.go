package sem

import (
	"strings"
	"testing"
)

// Derived from Claude's TestRev2TersifyDropsRealDeclOnWrongPick, with an
// explicit baseline-visibility assertion so the preservation check cannot
// pass because its fixture stopped exercising the premise.
func TestReview303PeerTersifyKeepsDeclaration(t *testing.T) {
	cases := []struct {
		name, symbol string
		src          string
		real, focus  int
	}{
		{"py-decorator-trailing-comment", "fetch", "@retry  # fetch() may raise\n@lru_cache()\ndef fetch(url, timeout):\n    r = get(url, timeout)\n    return r.json()\n    # end", 2, 2},
		{"cs-lazy-property", "Options", "    public Options Options\n    {\n        get { return _options ??= new Options(); }\n    }\n", 0, 0},
		{"ruby-self-hashkey", "config", "def self.config\n  @config ||= Config.load(config: path)\n  @config.freeze\nend", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := strings.Split(strings.TrimSuffix(c.src, "\n"), "\n")
			first := 100
			r := SearchResult{FilePath: "x", StartLine: first, EndLine: first + len(lines) - 1, FocusLine: first + c.focus,
				SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1, SymbolStartLine: first,
				SymbolEndLine: first + len(lines) - 1, SymbolName: c.symbol, Snippet: strings.Join(lines, "\n")}
			old := tersifySearchResult(r, 2)
			if !strings.Contains(old.Snippet, lines[c.real]) {
				t.Fatalf("fixture does not exercise preservation: old tail %q lacks declaration %q", old.Snippet, lines[c.real])
			}
			got := tersifySearchResultKeepingDeclaration(r, 2)
			if !strings.Contains(got.Snippet, lines[c.real]) {
				t.Errorf("previously visible declaration %q lost: old [%d-%d] %q; new [%d-%d] %q", lines[c.real],
					old.SnippetStartLine, old.SnippetEndLine, old.Snippet, got.SnippetStartLine, got.SnippetEndLine, got.Snippet)
			}
		})
	}
}
