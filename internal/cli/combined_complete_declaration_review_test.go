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

	// This is the independently known minimum shape in which a minimal location
	// and the declaration itself fit. The renderer may choose any richer header,
	// body window, or exact whole source that fits; it may not lose the declaration.
	declarationFeasible := len("review/complete.go:100 *\n") + len(combinedCompleteDeclarationLine) + 1
	sawMarkedWhole := false
	sawUnmarkedDeclarationWindow := false
	for budget := 1; budget <= combinedCompleteMaxBudget; budget++ {
		block := agentSearchBlock(result, budget)
		if len(block) > budget {
			t.Fatalf("budget %d: block used %d bytes: %q", budget, len(block), block)
		}
		text := string(block)
		marked := strings.Contains(text, combinedCompleteMarker)
		whole := strings.Contains(text, combinedCompleteDeclarationBody)
		if marked && (!whole || strings.Contains(text, " elided")) {
			t.Fatalf("budget %d: partial declaration window carried a completeness marker:\n%s", budget, text)
		}
		if marked {
			sawMarkedWhole = true
		}
		if budget >= declarationFeasible && len(block) > 0 &&
			!strings.Contains(text, combinedCompleteDeclarationLine) {
			t.Fatalf("budget %d: declaration and minimal location fit, but declaration was lost:\n%s", budget, text)
		}
		if strings.Contains(text, combinedCompleteDeclarationLine) &&
			strings.Contains(text, " elided") && !marked {
			sawUnmarkedDeclarationWindow = true
		}
	}
	if !sawMarkedWhole {
		t.Fatal("fixed budget sweep never retained the truthful marked whole body")
	}
	if !sawUnmarkedDeclarationWindow {
		t.Fatal("fixture never exercised an unmarked declaration-plus-window fallback")
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
