package cli

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// declNameLineFixture is a snippet of `pre` lines of an earlier symbol, then the result's symbol:
// `annos` annotation lines, its name line, and `body` body lines. Every line is unique, so a
// line's text identifies it in a block.
func declNameLineFixture(pre, annos, body, first int) ([]string, sem.SearchResult) {
	var lines []string
	for i := 0; i < pre; i++ {
		lines = append(lines, fmt.Sprintf("        earlier_%02d();", i))
	}
	for i := 0; i < annos; i++ {
		lines = append(lines, fmt.Sprintf("    @Policy%02d(mode = \"x%02d\")", i, i))
	}
	lines = append(lines, "    public void target(int input) {")
	for i := 0; i < body; i++ {
		lines = append(lines, fmt.Sprintf("        step_%02d(input, %d);", i, i))
	}
	lines = append(lines, "    }")
	last := first + len(lines) - 1
	return lines, sem.SearchResult{
		Rank: 1, Score: 20, FilePath: "src/main/java/a/Svc.java", StartLine: first, EndLine: last,
		FocusLine: last - 1, SnippetStartLine: first, SnippetEndLine: last,
		SymbolStartLine: first + pre, SymbolEndLine: last, SymbolNameLine: first + pre + annos,
		SymbolName: "target", QualifiedName: "Svc.target", Signals: []string{"complete-symbol"},
		Snippet: strings.Join(lines, "\n"),
	}
}

// NEVER LOCATE LESS, WITH A PARSER NAME LINE, UNBOUNDED. Every line of the declaration region —
// the symbol's first line down to its name line, annotations included, however many — that the
// ordinary window showed is shown by the new block, on every budget; and a block that differs from
// the ordinary one shows the name line and numbers every printed line. Annotation stacks run past
// the text fallback's 16-line bound, and no annotation mentions the name, so no text heuristic
// could stand in for the parser's region.
func TestAgentBlockParserRegionNeverLess(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(20260929))
	iters := 160
	if testing.Short() {
		iters = 40
	}
	exercised, changed := 0, 0
	for iter := 0; iter < iters; iter++ {
		pre, annos, body := rng.Intn(6), rng.Intn(28), 3+rng.Intn(50)
		first := 10 + rng.Intn(400)
		lines, result := declNameLineFixture(pre, annos, body, first)
		// The focus anywhere in the symbol, annotations included: a window centred in the
		// annotation stack shows region lines without the name line.
		result.FocusLine = first + pre + rng.Intn(annos+1+body)
		name := pre + annos
		for budget := 40; budget <= 2400; budget += 7 + rng.Intn(9) {
			tag := fmt.Sprintf("iter %d (pre %d annos %d body %d) budget %d", iter, pre, annos, body, budget)
			view := agentSearchBlockViewOf(result)
			plain, _, _ := agentSearchFocusWindow(view, budget)
			block := agentSearchPrimaryBlock(result, budget)
			for i := pre; i <= name; i++ {
				if blockShowsLine(plain, lines[i]) {
					exercised++
					if !blockShowsLine(block, lines[i]) {
						t.Fatalf("%s: the ordinary window showed region line %q; the block does not:\nplain:\n%s\nblock:\n%s", tag, lines[i], plain, block)
					}
				}
			}
			if string(block) != string(plain) && strings.Contains(strings.TrimSuffix(string(block), "\n"), "\n") {
				changed++
				if !blockShowsLine(block, lines[name]) {
					t.Fatalf("%s: a changed block does not show the name line:\n%s", tag, block)
				}
			}
			declCheckRecoverable(t, tag, result, budget)
		}
	}
	if exercised == 0 || changed == 0 {
		t.Fatalf("exercised %d region lines, %d changed blocks: the sweep tests nothing", exercised, changed)
	}
}

// The parser's name line is the declaration even far below the text fallback's bound: a block cut
// below a 20-line annotation stack shows the name line.
func TestAgentBlockParserNameLineBeyondScanBound(t *testing.T) {
	t.Parallel()
	lines, result := declNameLineFixture(0, 20, 40, 100)
	name := lines[20]
	shown, fallback := 0, 0
	for budget := 200; budget <= 900; budget += 10 {
		if blockShowsLine(agentSearchPrimaryBlock(result, budget), name) {
			shown++
		}
		textOnly := result
		textOnly.SymbolNameLine = 0
		if blockShowsLine(agentSearchPrimaryBlock(textOnly, budget), name) {
			fallback++
		}
	}
	if shown == 0 || fallback != 0 {
		t.Fatalf("name line shown at %d budgets with the parser line, %d without (want >0 and 0)", shown, fallback)
	}
}

// An absorbed member whose declaration line is the parser's carries its region start
// (MergedDeclStarts): every region line the ordinary window showed stays, however far above.
func TestAgentBlockAbsorbedParserRegionNeverLess(t *testing.T) {
	t.Parallel()
	var lines []string
	lines = append(lines, "    public void load(String path) {")
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("        read_%02d(path);", i))
	}
	lines = append(lines, "    }", "")
	regionTop := len(lines)
	for i := 0; i < 18; i++ {
		lines = append(lines, fmt.Sprintf("    @Rule%02d(options = \"o%02d\")", i, i))
	}
	declared := len(lines)
	lines = append(lines, "    public Options options() {", "        return cached;", "    }")
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("    // tail %02d", i))
	}
	first := 50
	exercised := 0
	for focus := 1; focus < regionTop; focus++ {
		result := sem.SearchResult{Rank: 1, Score: 50, FilePath: "src/Cfg.java", StartLine: first, EndLine: first + len(lines) - 1,
			FocusLine: first + focus, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
			SymbolStartLine: first, SymbolEndLine: first + regionTop - 2, SymbolNameLine: first, SymbolName: "load",
			QualifiedName: "Cfg.load", Signals: []string{"complete-symbol", "contiguous-span"}, MergedRanks: []int{1, 2},
			MergedDeclLines: []int{first + declared}, MergedDeclStarts: []int{first + regionTop}, Snippet: strings.Join(lines, "\n")}
		for budget := 80; budget <= 2600; budget += 13 {
			view := agentSearchBlockViewOf(result)
			plain, _, _ := agentSearchFocusWindow(view, budget)
			block := agentSearchPrimaryBlock(result, budget)
			for i := regionTop; i <= declared; i++ {
				if blockShowsLine(plain, lines[i]) {
					exercised++
					if !blockShowsLine(block, lines[i]) {
						t.Fatalf("focus %d budget %d: ordinary window showed %q; block does not:\nplain:\n%s\nblock:\n%s", focus, budget, lines[i], plain, block)
					}
				}
			}
		}
	}
	if exercised == 0 {
		t.Fatal("no budget showed the absorbed region: the sweep tests nothing")
	}
}

// A name line outside the symbol's own span is not trusted: the block falls back to the text
// finder rather than anchoring on a line that belongs to another symbol.
func TestAgentBlockNameLineOutsideSpanIsIgnored(t *testing.T) {
	t.Parallel()
	_, result := declNameLineFixture(2, 3, 20, 100)
	view := agentSearchBlockViewOf(result)
	own, ok, _ := agentSearchDecls(view)
	if !ok || own.index != 5 {
		t.Fatalf("own declaration index %d ok=%v; want 5 from the name line", own.index, ok)
	}
	result.SymbolNameLine = result.SymbolStartLine - 1 // inside the snippet, above the span
	own, ok, _ = agentSearchDecls(agentSearchBlockViewOf(result))
	if ok && own.index == 1 {
		t.Fatalf("a name line outside the span was used as the declaration")
	}
	result.SymbolEndLine = result.SymbolStartLine + 3 // the name line now lies below the span
	result.SymbolNameLine = result.SymbolStartLine + 5
	own, ok, _ = agentSearchDecls(agentSearchBlockViewOf(result))
	if ok && own.index == 7 {
		t.Fatalf("a name line below the span was used as the declaration")
	}
}

// Mentions use Unicode identifier boundaries: a longer identifier does not mention a prefix of it.
func TestAgentSearchLineMentionsUnicodeBoundaries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		line, name string
		want       bool
	}{
		{"func runé() {}", "run", false},
		{"x := érun", "run", false},
		{"@Named(\"run\")", "run", true},
		{"def naïve(x):", "naïve", true},
		{"naïve2(x)", "naïve", false},
	} {
		if got := agentSearchLineMentions(c.line, c.name); got != c.want {
			t.Errorf("%q mentions %q = %v, want %v", c.line, c.name, got, c.want)
		}
	}
}
