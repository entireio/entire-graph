package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/sem"
)

// The cap must not make a LARGER budget buy a WORSE answer.
//
// Rendering is fitted to a raw byte budget and escaped afterwards, so a snippet
// of control bytes expands past the cap after it has already been measured. The
// retry loop answered that by dropping whole RESULTS -- and with a single hit
// there is nothing to drop, so the ranked block went to nil, every prefix
// variant failed with it, and the payload fell through to the telemetry tail.
// Measured on this repo before the fix: at 600 bytes the agent was handed
// "Index: cache-miss!N\n" and no location at all, while the SAME query at 400
// bytes returned "1. a.py:1 target s=25.0 [focus:1]" and its source.
//
// The locator is ASCII by construction -- paths and names are escaped by
// searchResultOnOneLine BEFORE they are measured -- so it always fits. What is
// too wide is the snippet, and the snippet is what must shrink.
func TestAgentSearchKeepsTheLocatorWhenEscapingOverrunsTheBudget(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "a.py", "def target():\n    return \""+strings.Repeat("\x1b", 300)+"\"\n")
	for _, budget := range []int{2000, 1200, 900, 700, 600, 500, 400, 300, 250} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			"search", "--repo", repo, "--query", "target", "--format", "agent",
			"--profile", "syntax-only", "--worktree",
			"--max-context-bytes", strconv.Itoa(budget),
		})
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		got := out.String()
		if out.Len() > budget {
			t.Fatalf("budget %d: emitted %d bytes: %q", budget, out.Len(), got)
		}
		if bytes.IndexByte(out.Bytes(), 0x1b) >= 0 {
			t.Fatalf("budget %d: a raw ESC reached the payload", budget)
		}
		if !strings.Contains(got, "a.py:1") {
			t.Fatalf("budget %d: lost the locator to escaped source: %q", budget, got)
		}
	}
}

// The retry has to shrink the per-result BUDGET, not just the result COUNT: a
// snippet whose every line is control bytes expands on every retry, so the loop
// converges only by re-rendering smaller until the block degrades to the
// locator the header can always afford. With one result there is no count left
// to drop, which is why dropping results alone could never fix this shape.
func TestFitAgentSearchResultsShrinksTheRenderBudgetUntilTheEscapedFormFits(t *testing.T) {
	t.Parallel()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = strings.Repeat("\x1b", 60)
	}
	result := sem.SearchResult{
		Rank: 1, Score: 25, FilePath: "a.py",
		StartLine: 1, EndLine: len(lines), FocusLine: 20,
		SnippetStartLine: 1, SnippetEndLine: len(lines),
		SymbolName: "target", Snippet: strings.Join(lines, "\n") + "\n",
	}
	for _, budget := range []int{2000, 1000, 600, 400, 200, 100, 60, 40} {
		fitted := fitAgentSearchResults([]sem.SearchResult{result}, budget)
		if len(fitted) == 0 {
			t.Fatalf("budget %d: dropped the result entirely", budget)
		}
		if len(fitted) > budget {
			t.Fatalf("budget %d: emitted %d bytes: %q", budget, len(fitted), fitted)
		}
		if bytes.IndexByte(fitted, 0x1b) >= 0 {
			t.Fatalf("budget %d: a raw ESC survived", budget)
		}
		// The header degrades from `1. a.py:<span> target s=25.0 [focus:20]`
		// down to the minimal `a.py:20 *`, so assert the locator's file and
		// line rather than any one rung of that ladder.
		if !bytes.Contains(fitted, []byte("a.py:")) {
			t.Fatalf("budget %d: lost the locator: %q", budget, fitted)
		}
	}
}

// rawC1At reports the first index at which a C1 control sits in the payload in
// its two-byte UTF-8 form, or -1. It is written over the BYTES rather than as a
// substring search so the assertion cannot itself carry the control it looks
// for.
func rawC1At(payload []byte) int {
	for i := 0; i+1 < len(payload); i++ {
		if payload[i] == 0xc2 && payload[i+1] >= 0x80 && payload[i+1] <= 0x9f {
			return i
		}
	}
	return -1
}

// Shrinking to fit must never hand back a payload that has stopped being valid
// UTF-8. U+65E5 carries 0x97 inside it, which is a C1 value, and U+009B is CSI
// in its two-byte form: the first has to be stepped over whole and the second
// escaped as a unit, with no partial sequence left where the fit cut.
func TestAgentSearchByteCapNeverSplitsARune(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	body := strings.Repeat("日本", 80)
	write(t, repo, "a.py", "def target():\n    return \""+body+"\"\n")
	for _, budget := range []int{1200, 900, 700, 600, 500, 400, 300, 250} {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{
			"search", "--repo", repo, "--query", "target", "--format", "agent",
			"--profile", "syntax-only", "--worktree",
			"--max-context-bytes", strconv.Itoa(budget),
		})
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		got := out.String()
		if out.Len() > budget {
			t.Fatalf("budget %d: emitted %d bytes: %q", budget, out.Len(), got)
		}
		if !utf8.Valid(out.Bytes()) {
			t.Fatalf("budget %d: payload is not valid UTF-8: %q", budget, got)
		}
		if index := rawC1At(out.Bytes()); index >= 0 {
			t.Fatalf("budget %d: a raw C1 control at byte %d: %q", budget, index, got)
		}
		if !strings.Contains(got, "a.py:1") {
			t.Fatalf("budget %d: lost the locator: %q", budget, got)
		}
	}
}
