package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// These are the regression tests for the terminal-injection findings: one per
// reported sink, each driving the renderer that owns the sink with a payload a
// hostile repository could produce.
//
// They assert on the ABSENCE of a raw ESC rather than on an exact rendering, so
// they keep testing the property — "repository bytes never reach the terminal as
// a control sequence" — if the escape's spelling ever changes.

// hostileESC is a repository-supplied value carrying a real terminal sequence: an
// erase-line that would blank what the reader already saw, plus a colour change
// that would outlive the value it is embedded in.
const hostileESC = "evil\x1b[2K\x1b[31m"

func assertNoRawControl(t *testing.T, what, rendered string) {
	t.Helper()
	if strings.ContainsRune(rendered, 0x1b) {
		t.Errorf("%s passed a raw ESC through to the terminal:\n%q", what, rendered)
	}
	if strings.Contains(rendered, "\u009b") {
		t.Errorf("%s passed a raw C1 CSI through to the terminal:\n%q", what, rendered)
	}
	// The value must still be REPORTED, only defanged: dropping it silently would
	// hide the file the reader is being warned about.
	if !strings.Contains(rendered, "evil") {
		t.Errorf("%s dropped the value instead of escaping it:\n%s", what, rendered)
	}
}

// TestWriteTextSearchEscapesSnippetAndPath covers the snippet sink (source lines
// lifted verbatim out of the scanned repository) and the path sink (a Git
// pathname, which may hold any byte but NUL and '/').
func TestWriteTextSearchEscapesSnippetAndPath(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{Results: []sem.SearchResult{{
		Rank: 1, FilePath: "src/" + hostileESC + ".go", StartLine: 1, EndLine: 3, FocusLine: 1,
		Score: 21.0, QualifiedName: "Router.merge", Signals: []string{"body"},
		Snippet: "func merge() {\n\t// " + hostileESC + "\n}",
	}}}
	var buffer bytes.Buffer
	if err := writeTextSearch(&buffer, response); err != nil {
		t.Fatal(err)
	}
	assertNoRawControl(t, "writeTextSearch", buffer.String())
}

// TestWriteTextSearchEscapesTypeCardDeclaration covers the type-card sink, where
// a one-line declaration is read straight off a line of the scanned file.
func TestWriteTextSearchEscapesTypeCardDeclaration(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{TypeCard: []sem.TypeCardEntry{{
		Name: "Router", FilePath: "src/routing/mod.rs", Line: 12,
		Decl: "pub struct Router { /* " + hostileESC + " */ }",
	}}}
	var buffer bytes.Buffer
	if err := writeTextSearch(&buffer, response); err != nil {
		t.Fatal(err)
	}
	assertNoRawControl(t, "writeTextSearchTypeCard", buffer.String())
}

// TestWriteTextSearchEscapesFileOutlinePath covers the file-outline sink, which
// prints repository paths through sem.RenderSearchFileOutline.
func TestWriteTextSearchEscapesFileOutlinePath(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{FileOutlines: []sem.SearchFileOutline{{
		FilePath: "src/" + hostileESC + ".rs", Lines: 40,
	}}}
	var buffer bytes.Buffer
	if err := writeTextSearch(&buffer, response); err != nil {
		t.Fatal(err)
	}
	assertNoRawControl(t, "RenderSearchFileOutline", buffer.String())
}

// TestWriteAgentSearchEscapesSnippetAndPath holds the same line for the format
// the agent guide tells callers to prefer.
func TestWriteAgentSearchEscapesSnippetAndPath(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{Results: []sem.SearchResult{{
		Rank: 1, FilePath: "src/" + hostileESC + ".go", StartLine: 1, EndLine: 3, FocusLine: 1,
		Score: 21.0, QualifiedName: "Router.merge", Signals: []string{"body"},
		Snippet: "func merge() {\n\t// " + hostileESC + "\n}",
	}}}
	var buffer bytes.Buffer
	if err := writeAgentSearch(&buffer, response, 0); err != nil {
		t.Fatal(err)
	}
	assertNoRawControl(t, "writeAgentSearch", buffer.String())
}

// TestWriteDefTextEscapesSourceExcerpt covers the `def` sink: whole source lines
// read off the repository and printed with a line-number gutter.
func TestWriteDefTextEscapesSourceExcerpt(t *testing.T) {
	t.Parallel()
	response := defResponse{
		Query: "Router",
		Declarations: []defDeclaration{{
			Name: "Router", Kind: "struct", FilePath: "src/" + hostileESC + ".rs",
			StartLine: 12, EndLine: 14,
			Signature: "pub struct Router { /* " + hostileESC + " */ }",
		}},
		DeclarationTotal: 1,
	}
	var buffer bytes.Buffer
	if err := writeDefText(&buffer, response, defaultDefContextBytes); err != nil {
		t.Fatal(err)
	}
	assertNoRawControl(t, "writeDefText", buffer.String())
}

// TestSearchTextPathCannotForgeAResultLine covers the other half of the path
// sink. Escaping ESC is not enough on its own: the headers are one record per
// line, so a Git pathname holding a newline would print an entry the ranking
// never produced — with whatever rank and score the attacker chose.
func TestSearchTextPathCannotForgeAResultLine(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{Results: []sem.SearchResult{{
		Rank: 1, FilePath: "a.go\n2. src/attacker-owned.go:1 score=99.0000 signals=body",
		StartLine: 1, EndLine: 1, FocusLine: 1, Score: 21.0,
		QualifiedName: "Router.merge", Signals: []string{"body"},
	}}}
	var buffer bytes.Buffer
	if err := writeTextSearch(&buffer, response); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "2. ") {
			t.Errorf("a newline in the path forged a second ranked entry:\n%s", buffer.String())
		}
	}
}

// TestDiffProgressLineEscapesGitFilename covers the --progress sink: Git hands
// back raw pathname bytes in -z mode, and the analysis layer forwards them into
// the event unchanged.
func TestDiffProgressLineEscapesGitFilename(t *testing.T) {
	t.Parallel()
	line := diffProgressLine(sem.AnalyzeProgressEvent{
		Phase: "parse", FilesDone: 3, FilesTotal: 9,
		Path: "src/" + hostileESC + ".go",
	})
	assertNoRawControl(t, "diffProgressLine", line)
	// A newline in the path would forge an entire additional progress line.
	if forged := diffProgressLine(sem.AnalyzeProgressEvent{
		Phase: "parse", Path: "a.go\ngraph diff progress phase=done files=9/9",
	}); strings.Count(forged, "\n") != 0 {
		t.Errorf("a newline in the path forged a second line:\n%q", forged)
	}
}

// TestSearchTextWithoutControlBytesIsUnchanged is the compatibility guard for all
// of the above: wrapping the writers must not perturb ordinary output, because an
// agent copies printed snippets verbatim as edit anchors, and a snippet the
// renderer had rewritten would no longer match the file it came from.
func TestSearchTextWithoutControlBytesIsUnchanged(t *testing.T) {
	t.Parallel()
	response := sectionedSearchResponse()
	var buffer bytes.Buffer
	if err := writeTextSearch(&buffer, response); err != nil {
		t.Fatal(err)
	}
	rendered := buffer.String()
	for _, result := range response.Results {
		// Only the primary group prints bodies; the related and docs groups are
		// locator-only by design, so their snippets are absent for reasons that
		// have nothing to do with escaping.
		if result.Section != "" {
			continue
		}
		if !strings.Contains(rendered, result.Snippet) {
			t.Errorf("snippet for %s is no longer byte-identical in the output:\nwant %q\ngot\n%s",
				result.FilePath, result.Snippet, rendered)
		}
		if !strings.Contains(rendered, result.FilePath) {
			t.Errorf("path %q is no longer byte-identical in the output:\n%s", result.FilePath, rendered)
		}
	}
	if strings.Contains(rendered, `\x`) {
		t.Errorf("clean output gained an escape artifact:\n%s", rendered)
	}
}

// forgedPathName is a pathname that tries to print a record of its own. The
// writer wrap cannot stop it — by the time bytes reach the writer, a snippet's
// newline and a path's are the same byte — so these sinks have to escape by
// value, and these tests are what say so.
const forgedPathName = "a.go\n  src/attacker-owned.go:1 forged"

func assertNoForgedRecord(t *testing.T, what, rendered string) {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "attacker-owned.go") && !strings.Contains(line, "a.go") {
			t.Errorf("%s let a pathname forge its own line:\n%s", what, rendered)
		}
	}
	if !strings.Contains(rendered, "a.go") {
		t.Errorf("%s dropped the path instead of escaping it:\n%s", what, rendered)
	}
}

// TestAgentSearchTypeCardEscapesOneLineFields covers the --format agent twin of
// the text type card. The two renderers print the same fields; only the text one
// was escaped at first, which is exactly the asymmetry this pins shut.
func TestAgentSearchTypeCardEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	assertNoForgedRecord(t, "agentSearchTypeCard", string(agentSearchTypeCard([]sem.TypeCardEntry{{
		Name: "Router", FilePath: forgedPathName, Line: 12,
		Decl: "pub struct Router {" + forgedPathName + "}",
	}})))
}

func TestRenderSignatureTypesEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	assertNoForgedRecord(t, "renderSignatureTypes", string(renderSignatureTypes([]sem.SearchSignatureType{{
		Name: "Router" + forgedPathName, FilePath: forgedPathName, StartLine: 3,
		Fields: []string{"inner" + forgedPathName}, FieldsTotal: 1,
	}})))
}

func TestFormatNeighborEndpointStaysOnOneLine(t *testing.T) {
	t.Parallel()
	rendered := formatNeighborEndpoint(neighborEndpoint{
		Name: "Merge" + forgedPathName, FilePath: forgedPathName, StartLine: 4,
	})
	if strings.Contains(rendered, "\n") {
		t.Errorf("formatNeighborEndpoint returned more than one line: %q", rendered)
	}
	assertNoForgedRecord(t, "formatNeighborEndpoint", rendered)
}

func TestExplainEscapesOneLineFields(t *testing.T) {
	t.Parallel()
	rendered := string(RenderExplain(ExplainResponse{Symbols: []ExplainSymbol{{
		Query: "Helper", Resolved: true, Name: "Helper", Kind: "function",
		FilePath: forgedPathName, StartLine: 3, EndLine: 3,
		Signature: "func Helper() int" + forgedPathName,
	}}}, 0))
	assertNoForgedRecord(t, "RenderExplain", rendered)
	assertNoRawControl(t, "RenderExplain", strings.ReplaceAll(rendered, "a.go", "evil"))
}
