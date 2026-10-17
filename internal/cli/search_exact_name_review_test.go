package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// Java/C#: the index's SymbolStartLine is the first ANNOTATION line, so the "declaration line" the
// exact answer guarantees can be `@Override`, without the name. The ordinary answer showed the real
// signature at budgets where the exact answer shows only the annotation.
func TestRev302AnnotationDeclLineLocatesLess(t *testing.T) {
	response := exactNameFixture("toString", 1)
	row := &response.Results[0]
	row.Snippet = "    @Override\n    @SuppressWarnings(\"unchecked\")\n    public String toString() {\n        return \"Svc(\" + value + \")\";\n    }\n"
	row.FilePath = "b/Svc.java"
	row.SnippetStartLine, row.StartLine, row.SymbolStartLine, row.FocusLine = 6, 6, 6, 8
	row.SymbolEndLine, row.SnippetEndLine, row.EndLine = 10, 10, 10
	sig := "    public String toString() {"
	lost := 0
	first := 0
	for budget := 1; budget <= 4096; budget++ {
		before := renderAgentSearchForTest(t, response, budget, false)
		after := renderAgentSearchForTest(t, response, budget, true)
		if strings.Contains(before, sig+"\n") && !strings.Contains(after, sig+"\n") {
			lost++
			if first == 0 {
				first = budget
				t.Logf("budget %d: signature shown before, not after\n--- before ---\n%s\n--- after ---\n%s", budget, before, after)
			}
		}
	}
	if lost > 0 {
		t.Errorf("signature line lost at %d budgets (first %d)", lost, first)
	}
}

// VERIFY present before, absent after, at tight budgets.
func TestRev302VerifyLoss(t *testing.T) {
	for _, ranks := range [][]int{{1}, {2}, {2, 6, 9}} {
		response := exactNameFixture("resolveMessageRef", ranks...)
		lost, first, strict := 0, 0, 0
		for budget := 1; budget <= 4096; budget++ {
			before := renderAgentSearchForTest(t, response, budget, false)
			after := renderAgentSearchForTest(t, response, budget, true)
			if strings.Contains(before, "VERIFY: go test") && !strings.Contains(after, "VERIFY: go test") {
				rows, _ := agentExactNameRows(orderAgentSearchResults(response.Results), response.Query)
				all := true
				for _, r := range rows {
					all = all && exactNameDeclShown(before, r)
				}
				if all {
					strict++
				}
				lost++
				if first == 0 {
					first = budget
					t.Logf("ranks %v budget %d\n--- before ---\n%s\n--- after ---\n%s", ranks, budget, before, after)
				}
			}
		}
		if lost > 0 {
			t.Errorf("ranks %v: VERIFY lost at %d budgets (first %d); %d of them where before ALSO showed every exact decl", ranks, lost, first, strict)
		}
	}
}

// A second definition of the name that the index did not surface as its own symbol (a closure var,
// a pseudo-definition) is visible in a NON-exact row's snippet. The exact answer hides that row.
func TestRev302HiddenSecondDefinition(t *testing.T) {
	response := exactNameFixture("resolveMessageRef", 1)
	row := &response.Results[1]
	row.Snippet = "// Helper2 documents row 2.\n//\n//\nfunc (r *recv2) Helper2(ctx context.Context, input string) error {\n\tresolveMessageRef := func(ref string) string { return ref }\n\t_ = resolveMessageRef\n\treturn nil\n}\n"
	row.FocusLine = row.SymbolStartLine + 1
	second := "\tresolveMessageRef := func(ref string) string { return ref }"
	for _, budget := range []int{4096, 8192, 24576} {
		before := renderAgentSearchForTest(t, response, budget, false)
		after := renderAgentSearchForTest(t, response, budget, true)
		if strings.Contains(before, second) && !strings.Contains(after, second) {
			t.Errorf("budget %d: second definition shown before, hidden after", budget)
		}
	}
}

// One-line declarations (no body to fill the share): probes whether the VERIFY re-offer's
// `budget-slack` vs `budget` mutant (M13) is really equivalent.
func TestRev302OneLineLatencyIndependence(t *testing.T) {
	timings := []sem.SearchStats{
		{IndexCacheHit: true, IndexLatencyMS: 1, QueryLatencyMS: 1, PreselectLatencyMS: 1, TotalLatencyMS: 1},
		{IndexCacheHit: true, IndexLatencyMS: 99999999, QueryLatencyMS: 99999999, PreselectLatencyMS: 99999999, TotalLatencyMS: 99999999},
	}
	for _, ranks := range [][]int{{1}, {2, 6, 9}} {
		base := exactNameFixture("resolveMessageRef", ranks...)
		for i := range base.Results {
			r := &base.Results[i]
			line := strings.Split(r.Snippet, "\n")[3]
			r.Snippet = strings.TrimSuffix(line, "{") + "{ return nil }\n"
			r.SnippetStartLine, r.StartLine, r.FocusLine = r.SymbolStartLine, r.SymbolStartLine, r.SymbolStartLine
			r.SymbolEndLine, r.SnippetEndLine, r.EndLine = r.SymbolStartLine, r.SymbolStartLine, r.SymbolStartLine
		}
		for budget := 1; budget <= 2048; budget++ {
			var want string
			for i, stats := range timings {
				response := base
				response.Stats = stats
				got := renderAgentSearchForTest(t, response, budget, true)
				if len(got) > budget {
					t.Fatalf("over budget")
				}
				_, body := splitAgentHeader(got)
				if i == 0 {
					want = body
					continue
				}
				if body != want {
					t.Fatalf("ranks %v budget %d: latency-dependent\n--- fast ---\n%s\n--- slow ---\n%s", ranks, budget, want, body)
				}
			}
		}
	}
}
