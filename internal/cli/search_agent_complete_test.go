package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The complete-symbol signal is the one promise in the payload that can remove a
// follow-up read: search_enclosure.go:775 states its meaning as "you need no
// follow-up read", and CompleteSymbolSignal is exported "for renderers". No renderer
// used it. The agent format -- the one the installed guide asks for -- dropped
// Signals entirely, so the single fact that makes a body worth its bytes never
// reached the agent that had just paid for them.
//
// This matters because the cost only pays off if the read goes away. Measured on this
// tool: it makes +19.6% MORE Read calls than the no-tool baseline while total tool
// calls fall 14.5% (search_span_merge.go:20-23, n=55 paired), and re-reading a file
// the payload already printed is 10.1% of post-payload tool calls (search.go:1245).
// Bodies removed greps and added reads.

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

// A complete body is worth its bytes only if the agent is told it is complete.
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

// The inverse, and the reason the marker cannot simply be printed on every result:
// an unmarked result is an instruction to go and read the file, so marking a partial
// body would send the agent away with an answer it wrongly believes is whole.
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

// CompleteSymbolSignal's exported contract (search_enclosure.go:342-344) says a result
// carrying it "must never be abbreviated on the way out". agentSearchPrimaryBlock
// abbreviated it anyway: its span loop walks the body down line by line until some
// prefix fits the budget. That produces the worst possible output -- bytes are spent
// AND the agent still has to open the file -- and it silently converts a truthful
// promise into a false one, because the marker would then sit above a partial body.
//
// Binary is the only honest rendering: the whole symbol, or a bare locator.
// Swept across budgets, because the interesting failures are at specific caps and a
// single budget misses them. Two invariants, at every cap:
//
//	ALL-OR-NOTHING  every body line is present, or none is -- never a middle.
//	BODY => MARKER  any rendering that carries body lines also says they are complete.
//
// The second is not hypothetical. Before the marker-less rung was refused, a 200-byte
// cap printed the whole symbol under the minimal header `budget.go:10 *` -- complete
// source with nothing vouching for it, which pays the bytes and saves no read. A
// single-budget test at 120 missed it entirely and passed against the unfixed code.
func TestACompleteSymbolIsNeverAbbreviatedIntoATruncatedBody(t *testing.T) {
	t.Parallel()
	bodyLines := []string{
		"func FitToBudget(a []int, b int) []int {",
		"\tif b <= 0 {",
		"\t\treturn a",
		"\t}",
		"\treturn a[:b]",
		"}",
	}
	for _, budget := range []int{80, 120, 160, 200, 240, 300, 4096} {
		var out bytes.Buffer
		if err := writeAgentSearch(&out, completeSymbolResponse(completeBody, sem.CompleteSymbolSignal), budget); err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		rendered := out.String()
		present := 0
		for _, line := range bodyLines {
			if strings.Contains(rendered, line) {
				present++
			}
		}
		if present != 0 && present != len(bodyLines) {
			t.Errorf("budget %d: rendered %d of %d body lines -- a truncated middle of a complete symbol:\n%s",
				budget, present, len(bodyLines), rendered)
		}
		if present > 0 && !strings.Contains(rendered, completeMarker) {
			t.Errorf("budget %d: rendered the body without marking it complete, so the agent re-reads what it already has:\n%s",
				budget, rendered)
		}
		if present == 0 && strings.Contains(rendered, completeMarker) {
			t.Errorf("budget %d: claimed completeness with no body beneath it:\n%s", budget, rendered)
		}
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
