package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A locator names a symbol but shows one line of it. Without the symbol's true extent the agent
// cannot tell whether it is holding a 3-line helper or a shard of a 456-line function, and measured
// on jqlang/jq-2235 it swept main.c in three reads totalling 26,873 B to find out — where the
// no-tool baseline used `grep -n` anchors and read 17,536 B.
func TestLocatorAnnouncesEnclosingSymbolExtent(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	writeTextSearchResult(&out, sem.SearchResult{
		Rank: 3, FilePath: "src/main.c", FocusLine: 638, SymbolName: "umain",
		SymbolStartLine: 270, SymbolEndLine: 725,
	}, false)
	got := out.String()
	if !strings.Contains(got, "umain (270-725, 456L)") {
		t.Fatalf("locator must carry the symbol's true extent, got %q", got)
	}
}

// A result that carries the whole body must NOT be annotated: the reader already has every line, and
// a span suffix there would imply something is missing.
func TestCompleteBodyIsNotAnnotatedWithSpan(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	writeTextSearchResult(&out, sem.SearchResult{
		Rank: 1, FilePath: "src/x.c", SymbolName: "whole", Kind: "function",
		StartLine: 10, EndLine: 20, SnippetStartLine: 10, SnippetEndLine: 20,
		SymbolStartLine: 10, SymbolEndLine: 20, Snippet: "line\n",
		Signals: []string{"complete-symbol"},
	}, true)
	if got := out.String(); strings.Contains(got, "body elided") || strings.Contains(got, ", 11L)") {
		t.Fatalf("a complete body must not be annotated as elided, got %q", got)
	}
}

func TestTextSearchRendersAdditionalProsePassageWithExactRange(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	writeTextSearchResult(&out, sem.SearchResult{
		Rank: 1, FilePath: "sessions/focus.md", StartLine: 40, EndLine: 42, FocusLine: 41,
		SnippetStartLine: 40, SnippetEndLine: 42, Snippet: "primary session source",
		Signals: []string{"retrieval_mode=prose-parent"},
		Passages: []sem.SearchPassage{{
			StartLine: 100, EndLine: 101, FocusLine: 101, Snippet: "distant line one\ndistant line two",
		}},
	}, true)
	got := out.String()
	if !strings.Contains(got, "additional sessions/focus.md:100-101") ||
		!strings.Contains(got, "distant line one\ndistant line two") {
		t.Fatalf("text output omitted additional passage or exact range:\n%s", got)
	}
}
