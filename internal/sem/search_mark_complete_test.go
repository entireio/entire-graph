package sem

import (
	"strings"
	"testing"
)

// THE GAP THIS CLOSES, and why the obvious fixes for it were reverted twice, is documented on
// searchMarkAlreadyCompleteSnippets. In short: a snippet can already contain its entire symbol
// and carry no complete-symbol signal, because the signal is only assigned along the
// enclosure-widening path. The CLI turns that signal into "no follow-up read needed", so
// withholding it where it is true costs a read of source the agent has already been given.

func markCompleteRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	// Small enough that the allocator has no reason to widen anything: the snippet lands on the
	// whole symbol without passing through the path that assigns the signal.
	write(t, repo, "budget/decide.go", `package budget

// DecideRetryBudget reports whether another attempt is permitted.
func DecideRetryBudget(attempt, ceiling int) bool {
	return attempt < ceiling
}
`)
	// A long callable, so a tight budget must clip it and the mark must NOT appear.
	var long strings.Builder
	long.WriteString("package budget\n\n// ExpandRetryBudget grows the permitted attempt count.\nfunc ExpandRetryBudget(attempt int) int {\n")
	for i := 0; i < 300; i++ {
		long.WriteString("\tif attempt == ")
		long.WriteString(strings.Repeat("9", 3))
		long.WriteString(" {\n\t\treturn attempt\n\t}\n")
	}
	long.WriteString("\treturn attempt\n}\n")
	write(t, repo, "budget/expand.go", long.String())
	return repo
}

func markCompleteSearch(t *testing.T, repo string, budget int) []SearchResult {
	t.Helper()
	response, err := SearchRepository(t.Context(), repo, "test",
		"reports whether another attempt is permitted", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5, MaxContextBytes: budget,
		})
	if err != nil {
		t.Fatal(err)
	}
	return response.Results
}

// THE FALSIFIER FOR THE FIX ITSELF: delete searchMarkAlreadyCompleteSnippets' effect and this
// fails. A snippet whose bounds contain the whole symbol must say so.
func TestASnippetContainingItsWholeSymbolIsMarkedComplete(t *testing.T) {
	t.Parallel()
	var covered, marked int
	for _, r := range markCompleteSearch(t, markCompleteRepo(t), 24576) {
		if r.Snippet == "" || r.SymbolStartLine <= 0 ||
			r.SnippetStartLine > r.SymbolStartLine || r.SnippetEndLine < r.SymbolEndLine {
			continue
		}
		covered++
		if hasSearchSignal(r, searchCompleteSymbolSignal) {
			marked++
		}
	}
	if covered == 0 {
		t.Fatal("no result rendered a whole symbol; the fixture is not exercising the pass")
	}
	if marked != covered {
		t.Errorf("%d of %d whole-symbol snippets carry %s; an unmarked whole body costs a "+
			"follow-up read of source the agent already has", marked, covered, searchCompleteSymbolSignal)
	}
}

// The other direction, and the one that must never regress: the signal is a PROMISE that no
// follow-up read is needed. A clipped body carrying it is a false promise, which is worse than
// the waste this pass exists to remove -- so under-marking is the safe direction and is what
// the budget ceiling chooses when it runs out.
func TestAClippedSnippetIsNeverMarkedComplete(t *testing.T) {
	t.Parallel()
	repo := markCompleteRepo(t)
	for _, budget := range []int{400, 800, 1200, 2048, 6000, 24576} {
		for _, r := range markCompleteSearch(t, repo, budget) {
			if !hasSearchSignal(r, searchCompleteSymbolSignal) || r.SymbolStartLine <= 0 {
				continue
			}
			if r.SnippetStartLine > r.SymbolStartLine || r.SnippetEndLine < r.SymbolEndLine {
				t.Errorf("budget=%d: %s is marked %s but its snippet %d-%d does not contain "+
					"its symbol %d-%d", budget, r.SymbolName, searchCompleteSymbolSignal,
					r.SnippetStartLine, r.SnippetEndLine, r.SymbolStartLine, r.SymbolEndLine)
			}
		}
	}
}

// The pass runs after the byte budget has closed, which is the post-allocator growth class of
// issue #208. It prices each mark and stops at the ceiling; this is the check that it does.
func TestMarkingCompleteSnippetsNeverBreachesTheBudget(t *testing.T) {
	t.Parallel()
	repo := markCompleteRepo(t)
	for _, budget := range []int{400, 800, 1200, 2048, 6000, 24576} {
		response, err := SearchRepository(t.Context(), repo, "test",
			"reports whether another attempt is permitted", SearchOptions{
				Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5, MaxContextBytes: budget,
			})
		if err != nil {
			t.Fatalf("budget=%d: %v", budget, err)
		}
		if err := response.Validate(); err != nil {
			t.Errorf("budget=%d: %v", budget, err)
		}
	}
}
