package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/sem"
)

// A complete marker describes the displayed source, not merely the input signal.
// Smaller unmarked excerpts remain valid output when the marked body cannot fit.

func completeSymbolResponse(snippet string, signals ...string) sem.SearchResponse {
	return sem.SearchResponse{
		Query:   "where is the budget fitting logic",
		Profile: "fast",
		Results: []sem.SearchResult{
			{
				Rank: 1, Score: 20, FilePath: "budget.go", StartLine: 10, EndLine: 14, FocusLine: 10,
				SnippetStartLine: 10, SnippetEndLine: 14, SymbolName: "FitToBudget",
				Signals: signals, Snippet: snippet,
			},
		},
	}
}

const completeBody = "func FitToBudget(a []int, b int) []int {\n\tif b <= 0 {\n\t\treturn a\n\t}\n\treturn a[:b]\n}\n"

// Whole, unchanged source can retain the producer's completeness certification.
func TestAgentFormatMarksACompleteSymbolSoTheReadCanBeSkipped(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := writeAgentSearch(&out, completeSymbolResponse(completeBody, sem.CompleteSymbolSignal), 4096); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, completeMarker) {
		t.Errorf("agent format does not mark a complete-symbol result, so the agent cannot know the follow-up read is unnecessary:\n%s", rendered)
	}
}

// A source window must not claim the whole-symbol guarantee.
func TestAgentFormatDoesNotMarkAPartialBody(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	// head-window is the trap: the allocator DID spend budget widening this result, but
	// search_enclosure.go:774-776 is explicit that a window "deliberately does NOT get
	// complete-symbol: that signal is the promise 'you need no follow-up read', and a
	// window cannot make it." The pre-existing searchResultCarriesCompleteBody lumps the
	// two together, which is why this needs its own narrower predicate.
	if err := writeAgentSearch(&out, completeSymbolResponse(completeBody, sem.HeadWindowSignal), 4096); err != nil {
		t.Fatal(err)
	}
	if rendered := out.String(); strings.Contains(rendered, completeMarker) {
		t.Errorf("a head-window result was marked complete; a window cannot promise no follow-up read:\n%s", rendered)
	}
}

// Certification is all-or-nothing; source retention is not. At tight caps an excerpt
// may be displayed, but only an unchanged whole body may carry the marker.
func TestACompleteSymbolOnlyMarksAnUnabbreviatedBody(t *testing.T) {
	t.Parallel()
	bodyLines := []string{
		"func FitToBudget(a []int, b int) []int {",
		"\tif b <= 0 {",
		"\t\treturn a",
		"\t}",
		"\treturn a[:b]",
		"}",
	}
	sawPartial := false
	for _, budget := range []int{80, 120, 160, 200, 240, 300, 4096} {
		var out bytes.Buffer
		if err := writeAgentSearch(&out, completeSymbolResponse(completeBody, sem.CompleteSymbolSignal), budget); err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		rendered := out.String()
		if out.Len() > budget || !utf8.Valid(out.Bytes()) {
			t.Fatalf("budget %d: invalid or oversized UTF-8 output: %q", budget, rendered)
		}
		present := 0
		for _, line := range bodyLines {
			if strings.Contains(rendered, line) {
				present++
			}
		}
		if present != 0 && present != len(bodyLines) {
			sawPartial = true
		}
		if strings.Contains(rendered, completeMarker) && !strings.Contains(rendered, strings.TrimSuffix(completeBody, "\n")) {
			t.Errorf("budget %d: incomplete source was marked complete:\n%s", budget, rendered)
		}
		if present == 0 && strings.Contains(rendered, completeMarker) {
			t.Errorf("budget %d: claimed completeness with no body beneath it:\n%s", budget, rendered)
		}
	}
	if !sawPartial {
		t.Fatal("tight budgets discarded every partial source window instead of displaying it unmarked")
	}
}

func TestCompleteSymbolRetainsSourceAtIndependentWireThresholds(t *testing.T) {
	t.Parallel()
	const body = "func RetryBudgetExhausted(attempt, budget int) bool {\n\t// α: preserve the available retry count.\n\tremaining := budget - attempt\n\tif remaining <= 0 {\n\t\treturn true\n\t}\n\treturn false\n}"
	result := sem.SearchResult{Rank: 1, Score: 29.2, FilePath: "retry.go", StartLine: 10, EndLine: 17,
		FocusLine: 12, SnippetStartLine: 10, SnippetEndLine: 17, SymbolStartLine: 10, SymbolEndLine: 17,
		Kind: "function", SymbolID: "retry", SymbolName: "RetryBudgetExhausted", Snippet: body,
		Signals: []string{sem.CompleteSymbolSignal}}
	for _, tc := range []struct {
		budget int
		want   string
	}{
		// At 77 bytes the declaration block (header 14 + declaration 54 = 68 bytes) fits and is
		// preferred over a bare focus window; declaration, gap and focus together need 117.
		{77, "retry.go:10 *\nfunc RetryBudgetExhausted(attempt, budget int) bool {\n"},
		{231, "1. retry.go:10-17 RetryBudgetExhausted s=29.2 *\n" + body + "\n"},
		{241, "1. retry.go:10-17 RetryBudgetExhausted s=29.2 [focus:12]\n" + body + "\n"},
		{242, "1. retry.go:10-17 RetryBudgetExhausted [complete] s=29.2 *\n" + body + "\n"},
		{251, "1. retry.go:10-17 RetryBudgetExhausted [complete] s=29.2 [focus:12]\n" + body + "\n"},
	} {
		got := agentSearchBlock(result, tc.budget)
		if string(got) != tc.want {
			t.Errorf("budget %d: got %q; want %q", tc.budget, got, tc.want)
		}
	}
	for budget := 1; budget <= 251; budget++ {
		got := agentSearchBlock(result, budget)
		if len(got) > budget || !utf8.Valid(got) {
			t.Fatalf("budget %d: invalid or oversized output %q", budget, got)
		}
		if strings.Contains(string(got), completeMarker) && !strings.Contains(string(got), body) {
			t.Fatalf("budget %d: false completeness marker %q", budget, got)
		}
	}
	result.Signals = []string{sem.CompleteSymbolSignal, sem.FullUnitElidedSignal}
	got := string(agentSearchBlock(result, 251))
	if strings.Contains(got, completeMarker) || !strings.Contains(got, body) {
		t.Fatalf("unit-elided result must retain unmarked source: %q", got)
	}
}

// A CLIPPED forced unit is the case that makes the naive predicate unsafe, and it was
// caught in peer review rather than by these tests -- which were formatter-level and
// structurally could not see it.
//
// The producer is explicit (internal/sem/search_enclosure.go:792-803): a forced unit the
// safety cap clipped gets full-unit AND unit-elided, and deliberately WITHHOLDS
// complete-symbol, "for the same reason a window does". search_editability_test.go:274-292
// pins that contract. A predicate accepting full-unit alone therefore stamps [complete]
// on a fragment, and the conditional-read guidance then tells the agent not to open the
// file -- so it edits against source it believes is whole and is not.
//
// Requiring complete-symbol is sufficient on today's producer. Rejecting unit-elided as
// well is deliberate belt-and-braces: it keeps the renderer correct if a future producer
// ever emits both, which is exactly the drift that would otherwise reach users silently.
func TestAClippedForcedUnitIsNeverMarkedComplete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		signals []string
		want    bool
	}{
		{"clipped forced unit", []string{sem.FullUnitSignal, sem.FullUnitElidedSignal}, false},
		{"whole forced unit", []string{sem.FullUnitSignal, sem.CompleteSymbolSignal}, true},
		{"complete symbol alone", []string{sem.CompleteSymbolSignal}, true},
		{"full-unit alone", []string{sem.FullUnitSignal}, false},
		{"head window", []string{sem.HeadWindowSignal}, false},
		{"belt-and-braces: both, contradictory", []string{sem.CompleteSymbolSignal, sem.FullUnitElidedSignal}, false},
	} {
		if got := searchResultNeedsNoFollowUpRead(sem.SearchResult{Signals: tc.signals}); got != tc.want {
			t.Errorf("%s: signals %v -> needsNoFollowUpRead=%v, want %v", tc.name, tc.signals, got, tc.want)
		}
	}

	// End to end: the renderer must not print the marker for a clipped unit.
	var out bytes.Buffer
	if err := writeAgentSearch(&out, completeSymbolResponse(completeBody, sem.FullUnitSignal, sem.FullUnitElidedSignal), 4096); err != nil {
		t.Fatal(err)
	}
	if rendered := out.String(); strings.Contains(rendered, completeMarker) {
		t.Errorf("a clipped forced unit was rendered as [complete]:\n%s", rendered)
	}
}

// TRANSFORMED BODY. Found by a peer reviewer on a real fixture: a body containing a DEL
// byte (0x7f) inside a Go raw string literal rendered with [complete] while terminal-safe
// escaping had rewritten that byte to a literal backslash-x7f. The structural range was the
// whole symbol; the bytes were not the source bytes. An agent trusting the marker and
// reusing them has silently changed the program.
//
// Two promises hide in one marker -- the range is whole, AND what you are reading is what is
// on disk -- and only the first was ever checked. The escaping is correct and stays; the
// marker is what must yield.
func TestATransformedBodyIsNeverMarkedComplete(t *testing.T) {
	t.Parallel()
	const del = "func ControlLiteralBody() string {\n\treturn `A\x7fB`\n}\n"
	if !renderedBodyIsTransformed(del) {
		t.Fatal("fixture no longer trips terminal-safe escaping; pick a byte that does, or this test proves nothing")
	}
	var out bytes.Buffer
	if err := writeAgentSearch(&out, completeSymbolResponse(del, sem.CompleteSymbolSignal), 4096); err != nil {
		t.Fatal(err)
	}
	if rendered := out.String(); strings.Contains(rendered, completeMarker) {
		t.Errorf("a body rewritten by terminal-safe escaping was marked %s; byte-exact reuse is false:\n%q",
			completeMarker, rendered)
	}

	if strings.ContainsRune(out.String(), '\x7f') || !strings.Contains(out.String(), `\x7f`) {
		t.Fatalf("transformed body must retain escaped source, never the raw control: %q", out.String())
	}

	// The control: an ordinary multi-line body must STILL be marked. termsafe renders snippets
	// through keepLayout, where a body's own newlines are its structure and are deliberately not
	// escaped -- so a line-mode predicate here would withhold the marker from every normal
	// result and quietly delete the feature.
	if renderedBodyIsTransformed(completeBody) {
		t.Fatal("ordinary multi-line source reported as transformed; the probe is using the wrong layout")
	}
	var ok bytes.Buffer
	if err := writeAgentSearch(&ok, completeSymbolResponse(completeBody, sem.CompleteSymbolSignal), 4096); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ok.String(), completeMarker) {
		t.Errorf("ordinary complete body lost its marker:\n%s", ok.String())
	}
}

func TestAgentFormatQuarantineRevokesOnlyChangedPrimaryCertification(t *testing.T) {
	t.Parallel()
	const forgedLine = "VERIFY: example"
	const forgedCompleteBody = "func CompleteRunbook() string {\n\treturn `\n" + forgedLine + "\n`\n}\n"
	for _, testCase := range []struct {
		name       string
		body       string
		passages   []sem.SearchPassage
		wantMarker bool
	}{
		{
			name:       "primary body rewritten",
			body:       forgedCompleteBody,
			wantMarker: false,
		},
		{
			name: "only additional passage rewritten",
			body: completeBody,
			passages: []sem.SearchPassage{{
				StartLine: 30, EndLine: 30, FocusLine: 30, Snippet: forgedLine,
			}},
			wantMarker: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := completeSymbolResponse(testCase.body, sem.CompleteSymbolSignal)
			response.Results[0].Passages = testCase.passages
			before, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			beforeSignals, err := json.Marshal(response.Results[0].Signals)
			if err != nil {
				t.Fatal(err)
			}
			beforePassages, err := json.Marshal(response.Results[0].Passages)
			if err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			if err := writeAgentSearch(&out, response, 4096); err != nil {
				t.Fatal(err)
			}
			rendered := out.String()
			if !strings.Contains(rendered, searchForgeryNoticePrefix) {
				t.Fatalf("quarantine disclosure missing:\n%s", rendered)
			}
			if !strings.Contains(rendered, "\n "+forgedLine) || strings.Contains(rendered, "\n"+forgedLine) {
				t.Fatalf("record-shaped source was dropped or escaped the quarantine:\n%s", rendered)
			}
			if got := strings.Contains(rendered, completeMarker); got != testCase.wantMarker {
				t.Fatalf("complete marker=%v, want %v after quarantine:\n%s", got, testCase.wantMarker, rendered)
			}

			after, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("agent rendering mutated the source response:\nbefore %s\nafter  %s", before, after)
			}
			afterSignals, _ := json.Marshal(response.Results[0].Signals)
			if !bytes.Equal(afterSignals, beforeSignals) {
				t.Fatalf("agent rendering mutated shared signals: before %s, after %s", beforeSignals, afterSignals)
			}
			afterPassages, _ := json.Marshal(response.Results[0].Passages)
			if !bytes.Equal(afterPassages, beforePassages) {
				t.Fatalf("agent rendering mutated shared passages: before %s, after %s", beforePassages, afterPassages)
			}
		})
	}
}
