package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The header ladder is tier-major (#295: a block drops body lines before its rank, name and score),
// and the declaration rows still apply under it. When no ranked header fits with any window line
// but a ranked location does, the block is the line that names the callable whenever it fits --
// under the ranked header when that fits, else under the minimal one -- and the bare ranked
// location only when no declaration fits at all.
func TestAgentSearchRankedLocationKeepsTheDeclarationRow(t *testing.T) {
	const decl = "def f():"
	body := "    value = " + strings.Repeat("x", 120)
	lines := []string{decl}
	for i := 0; i < 8; i++ {
		lines = append(lines, body)
	}
	name := "f" + strings.Repeat("_", 40)
	result := sem.SearchResult{
		Rank: 1, Score: 20, FilePath: "a.py", StartLine: 1, EndLine: len(lines),
		SnippetStartLine: 1, SnippetEndLine: len(lines), FocusLine: 6,
		SymbolStartLine: 1, SymbolEndLine: len(lines), SymbolNameLine: 1, SymbolName: name, QualifiedName: name,
		Snippet: strings.Join(lines, "\n") + "\n",
	}
	view := agentSearchBlockViewOf(result)
	compactDecl := agentSearchLocationHeaders(1, "a.py", 1, 1, view.focusLine, view.name, view.tag, view.scored, "")[1] + decl + "\n"
	ranked := fitAgentSearchRankedLocation(1, "a.py", view.focusLine, view.name, view.tag, view.scored, 0)
	compactBody := agentSearchLocationHeaders(1, "a.py", 6, 6, view.focusLine, view.name, view.tag, view.scored, "")[1] + body + "\n"
	if len(compactDecl) >= len(compactBody) {
		t.Fatal("fixture: a ranked declaration must be cheaper than a ranked window line")
	}

	got := string(agentSearchPrimaryBlock(result, len(compactDecl), false))
	if got != compactDecl {
		t.Fatalf("budget %d: got %q, want the declaration under the ranked header %q", len(compactDecl), got, compactDecl)
	}

	// Below the ranked declaration but at or above the ranked location, the declaration still fits
	// under the minimal header and must still be shown (the declaration-row contract).
	tight := len(compactDecl) - 1
	minimalDecl := "a.py:1 *\n" + decl + "\n"
	if len(ranked) > tight || len(minimalDecl) > tight {
		t.Fatalf("fixture: ranked location (%d) and minimal declaration must both fit %d", len(ranked), tight)
	}
	if got = string(agentSearchPrimaryBlock(result, tight, false)); got != minimalDecl {
		t.Fatalf("budget %d: got %q, want the declaration under the minimal header %q", tight, got, minimalDecl)
	}
}
