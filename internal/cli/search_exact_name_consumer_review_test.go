package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The existing draft test checks the selected index and parsed bit. This control
// additionally checks the delivered source/coordinate and actual exact-mode
// admission. The C++ return type calls the zero-argument overload of run; only
// line 11 names the function being declared here.
func TestReview302NameLineVisibleAuthoritativeCoordinates(t *testing.T) {
	row := review302NameLineCPPResult()
	anchor, ok := agentExactNameAnchor(row, "run")
	if !ok || !anchor.parsed {
		t.Fatalf("visible authoritative name line was not admitted as parsed: ok=%v parsed=%v", ok, anchor.parsed)
	}
	want := "src/runner.cpp:11 *\nrun(int retry) {\n"
	if got := string(agentExactNameBlock(anchor, 2, 0)); got != want {
		t.Errorf("named-line payload must identify the declaration, not the return-type call:\ngot: %q\nwant: %q", got, want)
	}
	payload, exact := review302NameLinePayload(t, row, true)
	if !exact || !strings.Contains(payload, "\nrun(int retry) {\n") {
		t.Errorf("visible declaration must support the actual exact-name answer: exact=%v\n%s", exact, payload)
	}
}

// The break this test catches is treating a known-but-unprinted name coordinate
// as absent and substituting a same-name call. Literal source line 10 is a call
// in a C++ return type; the authoritative declaration is line 11 and remains
// inside the symbol's true 10..13 span, but is outside the displayed snippet.
func TestReview302NameLineKnownOutsideSnippetDoesNotPromoteCall(t *testing.T) {
	row := review302NameLineCPPResult()
	row.SnippetEndLine = 10
	row.Snippet = "decltype(run())\n"
	if anchor, ok := agentExactNameAnchor(row, "run"); ok {
		t.Errorf("known declaration at file line 11 is unavailable, but file line %d was admitted instead (parsed=%v):\n%s",
			anchor.first+anchor.named, anchor.parsed, agentExactNameBlock(anchor, 2, 0))
	}
	payload, exact := review302NameLinePayload(t, row, true)
	ordinary, ordinaryExact := review302NameLinePayload(t, row, false)
	if ordinaryExact {
		t.Fatal("disabled exact-name control unexpectedly selected exact mode")
	}
	if exact {
		t.Errorf("exact-name mode promoted a return-type call although the parsed name line is outside the snippet:\n%s", payload)
	}
	if payload != ordinary {
		t.Errorf("missing authoritative source must preserve the ordinary payload unchanged:\nexact-enabled:\n%s\nordinary:\n%s", payload, ordinary)
	}
	// A call can legitimately remain in the ordinary answer. The defect is its
	// promotion to a declaration answer that drops another retrieved result.
	if !strings.Contains(ordinary, "src/other.cpp:") {
		t.Fatal("ordinary control must actually show the other retrieved row")
	}
	if !strings.Contains(payload, "src/other.cpp:") {
		t.Error("false declaration admission omitted the other retrieved row")
	}
}

// Unlike the draft's conditional zero-coordinate assertion, this is nonvacuous:
// an extractor/old index without name-token metadata must still render a genuine
// text-found declaration and successfully take exact-name mode.
func TestReview302NameLineAbsentRetainsTextFallback(t *testing.T) {
	row := sem.SearchResult{
		Rank: 1, Score: 40, FilePath: "src/fallback.go",
		StartLine: 10, EndLine: 12, FocusLine: 11,
		SnippetStartLine: 10, SnippetEndLine: 12,
		SymbolStartLine: 10, SymbolEndLine: 12, SymbolNameLine: 0,
		SymbolName: "run", QualifiedName: "run", Kind: "function", Language: "go",
		Snippet: "func run() {\n\treturn\n}\n",
	}
	anchor, ok := agentExactNameAnchor(row, "run")
	if !ok || anchor.parsed {
		t.Fatalf("absent-coordinate fallback must succeed without claiming parser evidence: ok=%v parsed=%v", ok, anchor.parsed)
	}
	want := "src/fallback.go:10 *\nfunc run() {\n"
	if got := string(agentExactNameBlock(anchor, 2, 0)); got != want {
		t.Errorf("fallback named-line payload:\ngot: %q\nwant: %q", got, want)
	}
	payload, exact := review302NameLinePayload(t, row, true)
	if !exact || !strings.Contains(payload, "\nfunc run() {\n") {
		t.Errorf("absent metadata must retain genuine text-found exact answers: exact=%v\n%s", exact, payload)
	}
}

func review302NameLineCPPResult() sem.SearchResult {
	return sem.SearchResult{
		Rank: 1, Score: 40, FilePath: "src/runner.cpp",
		StartLine: 10, EndLine: 13, FocusLine: 10,
		SnippetStartLine: 10, SnippetEndLine: 13,
		SymbolStartLine: 10, SymbolEndLine: 13, SymbolNameLine: 11,
		SymbolName: "run", QualifiedName: "run", Kind: "function", Language: "cpp",
		Snippet: "decltype(run())\nrun(int retry) {\n    return retry;\n}\n",
	}
}

func review302NameLinePayload(t *testing.T, row sem.SearchResult, enableExact bool) (string, bool) {
	t.Helper()
	response := sem.SearchResponse{
		Query: "run", Profile: "full",
		Results: []sem.SearchResult{row, {
			Rank: 2, Score: 30, FilePath: "src/other.cpp",
			StartLine: 30, EndLine: 32, FocusLine: 31,
			SnippetStartLine: 30, SnippetEndLine: 32,
			SymbolStartLine: 30, SymbolEndLine: 32, SymbolNameLine: 30,
			SymbolName: "other", QualifiedName: "other", Kind: "function", Language: "cpp",
			Snippet: "int other() {\n    return 0;\n}\n",
		}},
	}
	var out bytes.Buffer
	exact, err := writeAgentSearchPayload(&out, response, 4096, agentSearchRender{exactName: enableExact})
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 || out.Len() > 4096 {
		t.Fatalf("expected nonempty payload within 4096 bytes, got %d", out.Len())
	}
	t.Logf("enableExact=%v usedExact=%v bytes=%d\n%s", enableExact, exact, out.Len(), out.String())
	return out.String(), exact
}
