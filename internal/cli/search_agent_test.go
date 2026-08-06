package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestAgentSearchSurfacesCoverageDiagnosticsWithinExactCap(t *testing.T) {
	response := sem.SearchResponse{
		Results: []sem.SearchResult{{
			Rank: 1, FilePath: "src/service.go", StartLine: 1, EndLine: 900,
			FocusLine: 42, SnippetStartLine: 40, SnippetEndLine: 44,
			SymbolName: "serve", Snippet: "func serve() {\n\tprepare()\n\trun()\n\tcleanup()\n}",
		}},
		Warnings:        []sem.ProviderWarning{{Code: "W_DIRTY", FilePath: "src/service.go"}},
		PartialFailures: []sem.PartialFailure{{Code: "E_PARSE_ERROR", FilePath: "broken.go"}},
		Completeness: sem.CompletenessReport{Languages: map[string]sem.LanguageCompleteness{
			"Go": {Files: 3, Symbols: 8},
		}},
	}

	var roomy bytes.Buffer
	if err := writeAgentSearch(&roomy, response, 512); err != nil {
		t.Fatal(err)
	}
	if roomy.Len() > 512 {
		t.Fatalf("agent output used %d bytes, cap 512", roomy.Len())
	}
	for _, want := range []string{"Coverage: degraded", "warning W_DIRTY: src/service.go", "partial E_PARSE_ERROR: broken.go", "src/service.go:"} {
		if !strings.Contains(roomy.String(), want) {
			t.Fatalf("agent output omitted %q:\n%s", want, roomy.String())
		}
	}

	var tight bytes.Buffer
	if err := writeAgentSearch(&tight, response, 96); err != nil {
		t.Fatal(err)
	}
	if tight.Len() > 96 {
		t.Fatalf("agent output used %d bytes, cap 96: %q", tight.Len(), tight.String())
	}
	if !strings.Contains(tight.String(), "!D W1 F1 L1/3") || !strings.Contains(tight.String(), "src/service.go:42") {
		t.Fatalf("tight output lost coverage marker or focused location: %q", tight.String())
	}
	for _, capBytes := range []int{1, 2, 8, 12} {
		var tiny bytes.Buffer
		if err := writeAgentSearch(&tiny, response, capBytes); err != nil {
			t.Fatal(err)
		}
		if tiny.Len() > capBytes {
			t.Fatalf("tiny agent output used %d bytes, cap %d: %q", tiny.Len(), capBytes, tiny.String())
		}
		if !strings.HasPrefix(tiny.String(), "!") {
			t.Fatalf("tiny output lost reserved degraded marker at cap %d: %q", capBytes, tiny.String())
		}
	}
}

func TestAgentSearchReportsDisplayedSpanAndFocusAfterCompaction(t *testing.T) {
	result := sem.SearchResult{
		Rank: 1, FilePath: "src/worker.go", StartLine: 10, EndLine: 200,
		FocusLine: 103, SnippetStartLine: 100, SnippetEndLine: 106,
		SymbolName: "Work", Snippet: "line100\nline101\nline102\nFOCUS103\nline104\nline105\nline106",
	}
	block := agentSearchBlock(result, 64)
	if len(block) > 64 {
		t.Fatalf("search block used %d bytes, cap 64: %q", len(block), string(block))
	}
	text := string(block)
	if !strings.Contains(text, "src/worker.go:103") || !strings.Contains(text, "FOCUS103") {
		t.Fatalf("tight block lost the focus line: %q", text)
	}
	if strings.Contains(text, ":10-200") || strings.Contains(text, ":100-106") {
		t.Fatalf("header reported stale undisplayed span: %q", text)
	}
}

// TestSearchDefaultContextBytesIsTwentyFourKiB pins the shipped ceiling. It is sized by TURN
// economics — a search that stops one Read short of an edit costs ~42.5k tokens, ~40x the
// entire payload — so it must clear the largest ranked payload plus the complete head bodies
// the allocator may buy. Lowering it back to 16 KiB reintroduces the failure it fixes: a
// large ranking leaves no room to complete even one body. Explicit callers still win.
func TestSearchDefaultContextBytesIsTwentyFourKiB(t *testing.T) {
	if defaultSearchContextBytes != 24*1024 {
		t.Fatalf("defaultSearchContextBytes = %d, want %d", defaultSearchContextBytes, 24*1024)
	}
	flags, _, err := parseSearchFlags([]string{"--query", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if flags.MaxContextBytes != 24*1024 {
		t.Fatalf("default --max-context-bytes = %d, want %d", flags.MaxContextBytes, 24*1024)
	}
	override, _, err := parseSearchFlags([]string{"--query", "x", "--max-context-bytes", "16384"})
	if err != nil {
		t.Fatal(err)
	}
	if override.MaxContextBytes != 16384 {
		t.Fatalf("explicit --max-context-bytes = %d, want 16384", override.MaxContextBytes)
	}
}

// TestSearchMaxContextBytesZeroIsUnbounded keeps `0` meaning "no ceiling", which the whole
// block pipeline already implements (fitSearchResultsToBudget returns early on budget <= 0,
// allocateSearchSnippets only clamps when hardBudget > 0, and the block funders all guard on
// hardBudget > 0). A bulk or JSON consumer asking for the complete payload is the case it
// exists for; only a NEGATIVE value is an error.
func TestSearchMaxContextBytesZeroIsUnbounded(t *testing.T) {
	flags, rest, err := parseSearchFlags([]string{"--query", "x", "--max-context-bytes", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 || flags.MaxContextBytes != 0 {
		t.Fatalf("zero max context flags = %#v, rest = %#v", flags, rest)
	}

	response := sem.SearchResponse{Results: []sem.SearchResult{{
		Rank: 1, FilePath: "src/service.go", StartLine: 1, EndLine: 3,
		FocusLine: 2, SnippetStartLine: 1, SnippetEndLine: 3,
		SymbolName: "serve", Snippet: "line one\nline two\nline three",
	}}}
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, flags.MaxContextBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "line one\nline two\nline three") {
		t.Fatalf("zero context budget truncated agent output:\n%s", out.String())
	}

	if _, _, err := parseSearchFlags([]string{"--query", "x", "--max-context-bytes", "-1"}); err == nil ||
		!strings.Contains(err.Error(), "non-negative integer") {
		t.Fatalf("negative max context bytes error = %v", err)
	}
}

func TestWriteTextSearchTiersRankOneAndTwoFullRestTerse(t *testing.T) {
	snippet := "func serve() {\n\tprepare()\n\trun()\n\tcleanup()\n}"
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{Rank: 1, FilePath: "src/service.go", StartLine: 10, EndLine: 14, FocusLine: 10, Score: 12.5, SymbolName: "serve", Signals: []string{"path", "body"}, Snippet: snippet},
		{Rank: 2, FilePath: "src/other.go", StartLine: 1, EndLine: 3, FocusLine: 1, Score: 9.0, SymbolName: "other", Signals: []string{"body"}, Snippet: "func other() {}"},
		{Rank: 3, FilePath: "src/third.go", StartLine: 20, EndLine: 30, FocusLine: 22, Score: 8.0, QualifiedName: "Third.method", Signals: []string{"body"}, Snippet: "func method() {\n\t// long\n}"},
		{Rank: 4, FilePath: "src/fourth.go", StartLine: 40, EndLine: 44, FocusLine: 0, Score: 7.0, Signals: []string{"body"}, Snippet: "func fourth() {}"},
		// Rank 5 carries the StartLine fallback for a NAMED demotion, which is the shape rank 4 used
		// to cover before a nameless demotion stopped collapsing to coordinates (see
		// sem.SearchLocatorWindow and TestWriteTextSearchKeepsSourceForANamelessLocator).
		{Rank: 5, FilePath: "src/fifth.go", StartLine: 50, EndLine: 54, FocusLine: 0, Score: 6.0, SymbolName: "fifth", Signals: []string{"body"}, Snippet: "func fifth() {}"},
	}}

	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, snippet) {
		t.Fatalf("rank 1 must keep its full snippet:\n%s", out)
	}
	if !strings.Contains(out, "func other() {}") {
		t.Fatalf("rank 2 must keep its full snippet:\n%s", out)
	}
	if strings.Contains(out, "func method()") || strings.Contains(out, "// long") {
		t.Fatalf("rank 3 must NOT carry its snippet:\n%s", out)
	}
	// Terse lines carry no score= (PR #61 review: no consumer; 39% of added bytes).
	// The locator now names the verb that fetches the body it does not carry: redis's agent
	// hand-sed-ranged exactly this shape, and fmt's blind-Read a 200-line window off one.
	if !strings.Contains(out, "3. src/third.go:22 Third.method  [body: def Third.method]\n") {
		t.Fatalf("rank 3 terse line missing/wrong shape (no score expected):\n%s", out)
	}
	if strings.Contains(out, "src/third.go:22 Third.method score=") {
		t.Fatalf("rank 3 terse line must NOT carry a score:\n%s", out)
	}
	// Rank 4 has NO symbol name, so the terse form has nothing to be terse WITH: no name, and
	// therefore no `[body: def NAME]` follow-up either. It keeps the window the ranker already
	// allocated it rather than collapsing to coordinates, capped at sem's own ceiling. The tier is
	// unchanged for every hit that has a name — rank 3 above is still terse.
	if !strings.Contains(out, "4. src/fourth.go:40-40\nfunc fourth() {}\n") {
		t.Fatalf("rank 4 nameless demotion lost its window:\n%s", out)
	}
	if strings.Contains(out, "src/fourth.go:40-40 score=") {
		t.Fatalf("a demoted line must NOT carry a score:\n%s", out)
	}
	if !strings.Contains(out, "5. src/fifth.go:50 fifth  [body: def fifth]\n") {
		t.Fatalf("rank 5 terse line should fall back to StartLine when FocusLine unset (no score):\n%s", out)
	}
	if strings.Contains(out, "func fifth() {}") {
		t.Fatalf("rank 5 must NOT carry its snippet:\n%s", out)
	}
	// PR #61 review: the redundant READ window hint is dropped — the top ranks
	// carry FocusLine in the header + lines=Start-End, which is the region to open.
	if strings.Contains(out, "READ:") {
		t.Fatalf("no READ window hint should be emitted:\n%s", out)
	}
	// The full-snippet header names the range of the source PRINTED UNDER IT — the snippet's own
	// span — not the ranked region's. The two differ (a matched region can open a line or two
	// above the definition, while the snippet is snapped to the symbol's bounds), and a header
	// naming lines the snippet does not contain made one symbol appear to have two different
	// definition lines inside one session. There is therefore no separate `lines=` field: one
	// range, and it is the one on screen. A jump target is redundant here anyway, since the head
	// carries the complete body — which is also why the READ hint was dropped upstream.
	if !strings.Contains(out, "1. src/service.go:10-14 score=12.5000 symbol=serve") {
		t.Fatalf("rank 1 must print the snippet's own range with the score:\n%s", out)
	}
	if strings.Contains(out, "lines=") {
		t.Fatalf("the printed range replaces a second lines= field:\n%s", out)
	}
}

// TestWriteTextSearchAlwaysPrintsCompleteBodies guards the one exception to the tiering: the
// allocator spends real budget to return the head as whole callables, and the default renderer
// throwing those away below rank 2 would put the follow-up file read straight back.
func TestWriteTextSearchAlwaysPrintsCompleteBodies(t *testing.T) {
	body := "func rounded(v float64) float64 {\n\treturn math.Floor(v)\n}"
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{Rank: 1, FilePath: "a.go", StartLine: 1, EndLine: 2, FocusLine: 1, Signals: []string{"body"}, Snippet: "one"},
		{Rank: 2, FilePath: "b.go", StartLine: 1, EndLine: 2, FocusLine: 1, Signals: []string{"body"}, Snippet: "two"},
		{Rank: 5, FilePath: "src/round.go", StartLine: 30, EndLine: 32, FocusLine: 31, SymbolName: "rounded",
			Signals: []string{"body", sem.CompleteSymbolSignal}, Snippet: body},
		{Rank: 6, FilePath: "src/other.go", StartLine: 40, EndLine: 42, FocusLine: 41, SymbolName: "other",
			Signals: []string{"body"}, Snippet: "func other() {\n\t// window\n}"},
	}}

	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, body) {
		t.Fatalf("a complete body below the full-snippet tier was collapsed to a locator:\n%s", out)
	}
	if strings.Contains(out, "// window") {
		t.Fatalf("an ordinary window below the tier kept its snippet:\n%s", out)
	}
	if !strings.Contains(out, "6. src/other.go:41 other  [body: def other]\n") {
		t.Fatalf("ordinary rank 6 lost its locator line:\n%s", out)
	}
}
