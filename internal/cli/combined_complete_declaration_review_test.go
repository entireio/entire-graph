package cli

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/sem"
)

const (
	combinedCompleteDeclarationPath = "review/complete.go"
	combinedCompleteDeclarationName = "reconcileBatch"
	combinedCompleteDeclarationLine = "func reconcileBatch(items []Item) error {"
	combinedCompleteDeclarationBody = "func reconcileBatch(items []Item) error {\n" +
		"\tnormalized := normalize(items)\n" +
		"\tif len(normalized) == 0 {\n" +
		"\t\treturn nil\n" +
		"\t}\n" +
		"\tselected := selectReady(normalized)\n" +
		"\tif err := validate(selected); err != nil {\n" +
		"\t\treturn err\n" +
		"\t}\n" +
		"\tcommitBatch(selected)\n" +
		"\treturn nil\n" +
		"}"
	combinedCompleteMarker    = "[complete]"
	combinedCompleteMaxBudget = 768
)

func combinedCompleteDeclarationResult(body string, signals ...string) sem.SearchResult {
	lineCount := len(strings.Split(body, "\n"))
	focusLine := 100 + lineCount - 3
	if focusLine < 100 {
		focusLine = 100
	}
	return sem.SearchResult{
		Rank:             1,
		Score:            42,
		FilePath:         combinedCompleteDeclarationPath,
		Language:         "go",
		Kind:             "function",
		SymbolID:         "review:reconcileBatch",
		SymbolName:       combinedCompleteDeclarationName,
		QualifiedName:    combinedCompleteDeclarationName,
		StartLine:        100,
		EndLine:          100 + lineCount - 1,
		FocusLine:        focusLine,
		SnippetStartLine: 100,
		SnippetEndLine:   100 + lineCount - 1,
		SymbolStartLine:  100,
		SymbolEndLine:    100 + lineCount - 1,
		SymbolNameLine:   100,
		Signals:          signals,
		Snippet:          body,
	}
}

func combinedCompleteDeclarationResponse(result sem.SearchResult) sem.SearchResponse {
	return sem.SearchResponse{
		Query:   "where is batch reconciliation committed",
		Profile: "fast",
		Results: []sem.SearchResult{result},
	}
}

func TestCombinedCompleteDeclarationReviewWholeBeforeWindow(t *testing.T) {
	t.Parallel()
	result := combinedCompleteDeclarationResult(
		combinedCompleteDeclarationBody,
		sem.CompleteSymbolSignal,
	)

	roomy := string(agentSearchBlock(result, 0))
	if !strings.Contains(roomy, combinedCompleteMarker) {
		t.Fatalf("unchanged whole source lost its completeness marker:\n%s", roomy)
	}
	if !strings.Contains(roomy, combinedCompleteDeclarationBody) {
		t.Fatalf("roomy block did not contain the independently authored whole source:\n%s", roomy)
	}
	if strings.Contains(roomy, " elided") {
		t.Fatalf("declaration window ran before the marked-whole candidate:\n%s", roomy)
	}

	// Candidate contract: prefer certified whole source, then preserve useful
	// unmarked source before falling back to a locator. Main's whole-or-locator
	// rule is intentionally replaced; partial output must never keep the marker.
	sawWhole, sawLocator, sawPartial := false, false, false
	for budget := 1; budget <= combinedCompleteMaxBudget; budget++ {
		block := agentSearchBlock(result, budget)
		if len(block) > budget || !utf8.Valid(block) {
			t.Fatalf("invalid block at %d: %q", budget, block)
		}
		text := string(block)
		_, body, _ := strings.Cut(strings.TrimSuffix(text, "\n"), "\n")
		if strings.Contains(text, combinedCompleteMarker) && !strings.Contains(text, combinedCompleteDeclarationBody) {
			t.Fatalf("partial source certified at %d: %s", budget, text)
		}
		sawPartial = sawPartial || body != "" && !strings.Contains(text, combinedCompleteDeclarationBody)
		sawWhole = sawWhole || strings.Contains(text, combinedCompleteDeclarationBody)
		sawLocator = sawLocator || len(block) > 0 && body == ""
	}
	if !sawWhole || !sawLocator || !sawPartial {
		t.Fatal("whole, unmarked partial, and locator paths must execute")
	}
	result.Signals = nil
	declarationFeasible := len("review/complete.go:100 *\n") + len(combinedCompleteDeclarationLine) + 1
	for budget := declarationFeasible; budget <= combinedCompleteMaxBudget; budget++ {
		block := agentSearchBlock(result, budget)
		if len(block) > budget || !strings.Contains(string(block), combinedCompleteDeclarationLine) {
			t.Fatalf("uncertified declaration lost at %d: %s", budget, block)
		}
	}
}

func TestCombinedCompleteDeclarationReviewTransformedAndClippedCaps(t *testing.T) {
	t.Parallel()
	const transformedBody = "func reconcileBatch(items []Item) error {\n" +
		"\tlabel := `A\x7fB`\n" +
		"\tcommitBatch(items)\n" +
		"\treturn nil\n" +
		"}"

	tests := []struct {
		name        string
		body        string
		signals     []string
		wantEscaped bool
	}{
		{
			name:        "terminal-transformed complete source",
			body:        transformedBody,
			signals:     []string{sem.CompleteSymbolSignal},
			wantEscaped: true,
		},
		{
			name: "clipped forced unit with contradictory complete signal",
			body: combinedCompleteDeclarationBody,
			signals: []string{
				sem.FullUnitSignal,
				sem.CompleteSymbolSignal,
				sem.FullUnitElidedSignal,
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := combinedCompleteDeclarationResult(test.body, test.signals...)
			response := combinedCompleteDeclarationResponse(result)
			sawEscapedSource := false
			sawDeclaration := false

			for budget := 1; budget <= combinedCompleteMaxBudget; budget++ {
				var out bytes.Buffer
				if err := writeAgentSearch(&out, response, budget); err != nil {
					t.Fatalf("budget %d: %v", budget, err)
				}
				if out.Len() > budget {
					t.Fatalf("budget %d: escaped payload used %d bytes: %q", budget, out.Len(), out.String())
				}
				if !utf8.Valid(out.Bytes()) {
					t.Fatalf("budget %d: escaped payload is not valid UTF-8: %q", budget, out.Bytes())
				}
				text := out.String()
				if strings.Contains(text, combinedCompleteMarker) {
					t.Fatalf("budget %d: transformed or clipped source was marked complete:\n%s", budget, text)
				}
				if strings.ContainsRune(text, '\x7f') {
					t.Fatalf("budget %d: raw DEL escaped terminal-safe rendering: %q", budget, text)
				}
				if strings.Contains(text, `\x7f`) {
					sawEscapedSource = true
				}
				if strings.Contains(text, combinedCompleteDeclarationLine) {
					sawDeclaration = true
				}
			}
			if test.wantEscaped && !sawEscapedSource {
				t.Fatal("dense cap sweep never retained the transformed source as an escaped literal")
			}
			if !sawDeclaration {
				t.Fatal("dense cap sweep never retained the feasible declaration")
			}
		})
	}
}
