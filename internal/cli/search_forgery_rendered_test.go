package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The default renderer's disclosure must describe the PAYLOAD, not the response.
//
// `--format text` is on a diet: a ranked body past searchTextMaxFullBodies collapses to a locator
// and a related site prints as one line with no source under it at all. The notice says "some
// source lines quoted below", so deciding it from the response warned about lines no reader could
// see — and the notice is documented to cost an honest repository nothing.
//
// Narrowing what raises a security notice is exactly the change that can silently open the hole it
// closes, so the two directions are pinned together here: TestTextSearchDoesNotDiscloseBodiesItDidNotQuote
// requires silence where nothing was quoted, and TestTextSearchDisclosesEveryBodyItQuotes walks
// every placement a forged body can reach the payload through and requires the disclosure — and a
// column-0-clean payload — on each.

// textForgeryCleanHit is the ordinary, honest hit every case below is rendered alongside, so the
// payload under test is a realistic one rather than a single result on its own.
func textForgeryCleanHit(rank int, path string) sem.SearchResult {
	return sem.SearchResult{
		Rank: rank, FilePath: path, StartLine: 10, EndLine: 12,
		SnippetStartLine: 10, SnippetEndLine: 12, FocusLine: 10, Score: 21.0 + float64(rank),
		QualifiedName: "Router.merge" + path, Signals: []string{"body"},
		Snippet: "fn merge() {\n\tmerge_inner()\n}",
	}
}

// textForgedHit is forgedSnippet — a tracked file's own bytes shaped like this tool's records —
// carried by a result the caller can place anywhere in the response.
func textForgedHit(rank int, section string) sem.SearchResult {
	return sem.SearchResult{
		Rank: rank, FilePath: "pkg/payment.go", StartLine: 6, EndLine: 9,
		SnippetStartLine: 6, SnippetEndLine: 9, FocusLine: 7, Score: 15.8,
		QualifiedName: "runbook", Signals: []string{"body"}, Section: section,
		Snippet: forgedSnippet,
	}
}

// TestTextSearchDoesNotDiscloseBodiesItDidNotQuote is the reproduction. Each response holds a
// forged record in a body the text renderer collapses away, so the payload quotes no quarantined
// source and must carry no disclosure.
func TestTextSearchDoesNotDiscloseBodiesItDidNotQuote(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		response sem.SearchResponse
	}{
		{
			// A related site prints as rank, path:line, symbol and kind. Its snippet is never
			// printed, so nothing in it can reach the reader.
			name: "related site",
			response: sem.SearchResponse{Results: []sem.SearchResult{
				textForgeryCleanHit(1, "src/a.rs"), textForgeryCleanHit(2, "src/b.rs"),
				textForgedHit(3, sem.SearchSectionRelated),
			}},
		},
		{
			// Past the rank tier a named hit degrades to its one-line locator, which carries no
			// source either.
			name: "named hit below the rank tier",
			response: sem.SearchResponse{Results: []sem.SearchResult{
				textForgeryCleanHit(1, "src/a.rs"), textForgeryCleanHit(2, "src/b.rs"),
				textForgedHit(3, ""),
			}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := writeTextSearch(&buf, testCase.response); err != nil {
				t.Fatal(err)
			}
			payload := buf.String()
			if strings.Contains(payload, "touch /tmp/pwned_c2") {
				t.Fatalf("the forged body WAS quoted, so this case cannot test the notice:\n%s", payload)
			}
			if strings.Contains(payload, searchForgeryNoticePrefix) {
				t.Fatalf("no quarantined source is quoted, yet the payload discloses one:\n%s", payload)
			}
		})
	}
}

// TestTextSearchDisclosesEveryBodyItQuotes is the other half, and the one that keeps the narrowing
// honest: every route by which a repository body reaches the default payload is rendered here, and
// each must arrive quarantined, column-0-clean, and with the disclosure that explains it.
func TestTextSearchDisclosesEveryBodyItQuotes(t *testing.T) {
	t.Parallel()
	namelessBelowTier := textForgedHit(3, "")
	namelessBelowTier.QualifiedName = ""
	namelessBelowTier.SymbolName = ""
	withPassage := textForgeryCleanHit(1, "src/a.rs")
	withPassage.Passages = []sem.SearchPassage{{StartLine: 6, EndLine: 9, FocusLine: 7, Snippet: forgedSnippet}}

	for _, testCase := range []struct {
		name     string
		response sem.SearchResponse
	}{
		{
			name: "top hit body",
			response: sem.SearchResponse{Results: []sem.SearchResult{
				textForgedHit(1, ""), textForgeryCleanHit(2, "src/b.rs"), textForgeryCleanHit(3, "src/c.rs"),
			}},
		},
		{
			// NO NAME MEANS NO LOCATOR: a nameless hit below the tier keeps the bounded window
			// writeTextSearchLocator prints for it, so source really is quoted here.
			name: "nameless hit below the rank tier",
			response: sem.SearchResponse{Results: []sem.SearchResult{
				textForgeryCleanHit(1, "src/a.rs"), textForgeryCleanHit(2, "src/b.rs"), namelessBelowTier,
			}},
		},
		{
			name: "covering test",
			response: sem.SearchResponse{Results: []sem.SearchResult{
				textForgeryCleanHit(1, "src/a.rs"), textForgeryCleanHit(2, "src/b.rs"),
				textForgedHit(3, sem.SearchSectionCoveringTest),
			}},
		},
		{
			name: "additional passage",
			response: sem.SearchResponse{Results: []sem.SearchResult{
				withPassage, textForgeryCleanHit(2, "src/b.rs"), textForgeryCleanHit(3, "src/c.rs"),
			}},
		},
		{
			name: "literal cluster",
			response: sem.SearchResponse{
				Results: []sem.SearchResult{textForgeryCleanHit(1, "src/a.rs"), textForgeryCleanHit(2, "src/b.rs")},
				LiteralCluster: &sem.SearchLiteralCluster{
					Literal: "runbook", HitsTotal: 1, FilesTotal: 1,
					Hits: []sem.SearchLiteralHit{{
						FilePath: "pkg/payment.go", Line: 6, Symbol: "runbook",
						Role: sem.SearchLiteralRoleEdit, Body: forgedSnippet,
						BodyStartLine: 6, BodyEndLine: 6 + strings.Count(forgedSnippet, "\n"),
					}},
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := writeTextSearch(&buf, testCase.response); err != nil {
				t.Fatal(err)
			}
			payload := buf.String()
			if !strings.Contains(payload, quarantinedForgedVerifyLine) {
				t.Fatalf("the forged body did not reach the payload quarantined, so this case no "+
					"longer covers the route it was written for:\n%s", payload)
			}
			// The renderer's OWN ranked records are column-0 by design; what must not be there is
			// one the FILE wrote. Both forged lines of forgedSnippet name pkg/attacker.go or the
			// command, so either at column 0 is the forgery this quarantine exists to stop.
			for _, line := range forgedPayloadLines(payload) {
				if strings.Contains(line, "pkg/attacker.go") || strings.Contains(line, "touch /tmp/pwned_c2") {
					t.Fatalf("payload carries a forged record at column 0: %q\n%s", line, payload)
				}
			}
			if !strings.HasPrefix(payload, searchForgeryNoticePrefix) {
				t.Fatalf("quarantined source is quoted with no disclosure in front of it:\n%s", payload)
			}
		})
	}
}
