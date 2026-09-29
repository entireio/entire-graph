package cli

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// exactNameMinimalHeader is the minimal rung's locator, `path:N *`.
var exactNameMinimalHeader = regexp.MustCompile(`^(\S+):(\d+) \*$`)

// Every exact block printed under the MINIMAL rung numbers its lines correctly from its own text:
// the header names the first printed line and each line below it adds one. A row grown upward
// prints annotations and doc lines above the named line; a header naming the named line misread
// all of them. The sweep must find blocks whose first printed line is not the named line, or it
// tests nothing.
func TestExactNameMinimalRungNamesFirstPrintedLine(t *testing.T) {
	t.Parallel()
	checked, grown := 0, 0
	for _, c := range exactNameLangCases {
		response := exactNameLangResponse(c, 1)
		anchor, ok := agentExactNameAnchor(response.Results[0], c.symbol)
		if !ok {
			t.Fatalf("%s: not anchored", c.name)
		}
		for budget := 1; budget <= 4000; budget++ {
			block := agentExactNameBlock(anchor, 2, budget)
			if block == nil {
				continue
			}
			parts := strings.Split(strings.TrimSuffix(string(block), "\n"), "\n")
			m := exactNameMinimalHeader.FindStringSubmatch(parts[0])
			if m == nil {
				t.Fatalf("%s budget %d: minimal rung header %q", c.name, budget, parts[0])
			}
			line, _ := strconv.Atoi(m[2])
			checked++
			if parts[1] != anchor.lines[anchor.named] {
				grown++
			}
			for k, source := range parts[1:] {
				index := line + k - anchor.first
				if index < 0 || index >= len(anchor.lines) || anchor.lines[index] != source {
					t.Fatalf("%s budget %d: printed line %q read as file line %d:\n%s", c.name, budget, source, line+k, block)
				}
			}
		}
	}
	if checked == 0 || grown == 0 {
		t.Fatalf("checked %d minimal blocks, %d grown above the named line: the sweep tests nothing", checked, grown)
	}
}

// The parser's name line is the anchor whenever the index recorded one inside the row's span; the
// text finder is the fallback only when it is absent or outside the span.
func TestExactNameAnchorPrefersParserNameLine(t *testing.T) {
	t.Parallel()
	// The text finder takes line 0 (`public Options` reads as `Type Name`); the parser says the
	// name token is on line 1. A Kotlin backtick name is a literal to the text finder, which then
	// finds nothing at all.
	cases := []struct {
		name, symbol, file, snippet string
		nameLine                    int // 0-based in the snippet
	}{
		{"split-property", "Options", "src/Cfg.cs", "    public Options\n        Options { get; }\n", 1},
		{"kotlin-backtick", "run", "src/K.kt", "    @Test\n    fun `run`() {\n        check()\n    }\n", 1},
	}
	for _, c := range cases {
		n := strings.Count(c.snippet, "\n")
		base := sem.SearchResult{Rank: 1, Score: 10, FilePath: c.file, StartLine: 10, EndLine: 10 + n - 1, FocusLine: 10,
			SnippetStartLine: 10, SnippetEndLine: 10 + n - 1, SymbolStartLine: 10, SymbolEndLine: 10 + n - 1,
			SymbolName: c.symbol, QualifiedName: c.symbol, Snippet: c.snippet}
		parsed := base
		parsed.SymbolNameLine = 10 + c.nameLine
		anchor, ok := agentExactNameAnchor(parsed, c.symbol)
		if !ok || anchor.named != c.nameLine || !anchor.parsed {
			t.Errorf("%s: with a name line, anchored=%v named=%d parsed=%v; want %d from the parser", c.name, ok, anchor.named, anchor.parsed, c.nameLine)
		}
		outside := base
		outside.SymbolNameLine = 10 + n + 5 // outside the span: not trusted
		if anchor, ok := agentExactNameAnchor(outside, c.symbol); ok && anchor.parsed {
			t.Errorf("%s: a name line outside the span was used", c.name)
		}
		if anchor, ok := agentExactNameAnchor(base, c.symbol); ok && (anchor.parsed || anchor.named == c.nameLine && c.name == "split-property") {
			t.Errorf("%s: without a name line the fallback reported parsed=%v named=%d", c.name, anchor.parsed, anchor.named)
		}
	}
}
