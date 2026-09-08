package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The disclosure guard must not cost an HONEST repository its answer.
//
// searchPayloadDisclosesItsQuarantine recognises a quarantined line by its SHAPE — one leading
// space in front of what would otherwise be a record head. A file that already holds such a line
// wears that shape without anything having been rewritten: the line is indented in the file, so it
// was never in record position, was never quarantined, and there is nothing to disclose. The agent
// fitter's rungs all carry no notice for such a response (nothing was quarantined, so there is no
// notice to carry), so a byte test that calls the line "quarantined" rejects EVERY rung and the
// caller falls through to the header-only marker with no ranked location at all.
//
// alreadyIndentedSnippet and ordinaryIndentedSnippet differ only in the case of six letters, so the
// two responses render to payloads of identical length and the byte fitter makes identical
// decisions on both. That is what makes the control exact rather than approximate: at every budget,
// whatever the control can afford the subject must also afford.
const alreadyIndentedSnippet = "const runbook = `\n VERIFY: go test ./pkg\n`"

const ordinaryIndentedSnippet = "const runbook = `\n verify: go test ./pkg\n`"

func indentedBodyResponse(snippet string) sem.SearchResponse {
	return sem.SearchResponse{
		Results: []sem.SearchResult{{
			Rank: 1, FilePath: "pkg/payment.go", StartLine: 6, EndLine: 9,
			SnippetStartLine: 6, SnippetEndLine: 9, FocusLine: 7,
			SymbolName: "runbook", Score: 15.8, Signals: []string{"body"},
			Snippet: snippet,
		}},
	}
}

// TestAgentSearchKeepsResultsWhenSourceIsAlreadyIndented sweeps the byte ladder and requires the
// subject to carry a ranked location at every budget where the control does.
func TestAgentSearchKeepsResultsWhenSourceIsAlreadyIndented(t *testing.T) {
	t.Parallel()
	const rankedRecord = "1. pkg/payment.go:"
	for budget := 1; budget <= 900; budget++ {
		var control, subject bytes.Buffer
		if err := writeAgentSearch(&control, indentedBodyResponse(ordinaryIndentedSnippet), budget); err != nil {
			t.Fatal(err)
		}
		if err := writeAgentSearch(&subject, indentedBodyResponse(alreadyIndentedSnippet), budget); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(control.String(), rankedRecord) {
			continue // this budget cannot hold a ranked location for either response
		}
		if !strings.Contains(subject.String(), rankedRecord) {
			t.Fatalf("budget %d: an already-indented source line cost the caller its ranked location\n"+
				"control payload:\n%s\nsubject payload:\n%s", budget, control.String(), subject.String())
		}
	}
}

// TestAgentSearchDoesNotWarnAboutSourceItDidNotRewrite pins the other half: nothing was rewritten,
// so the payload must carry no disclosure and the source line must survive with its own one space.
func TestAgentSearchDoesNotWarnAboutSourceItDidNotRewrite(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeAgentSearch(&buf, indentedBodyResponse(alreadyIndentedSnippet), 4096); err != nil {
		t.Fatal(err)
	}
	payload := buf.String()
	if strings.Contains(payload, searchForgeryNoticePrefix) {
		t.Fatalf("nothing was quarantined, yet the payload discloses one:\n%s", payload)
	}
	if !strings.Contains(payload, "\n VERIFY: go test ./pkg\n") {
		t.Fatalf("the source line was not printed verbatim:\n%s", payload)
	}
}
