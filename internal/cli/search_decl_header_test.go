package cli

import (
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// declHeaderFirstLine parses a block header into the file line of its first printed line and, when
// the header names a range, its last. Rich and compact rungs: `N. path:A-B ...` or `N. path:A ...`.
// Minimal rung: `path:A *`.
var (
	declHeaderRanked  = regexp.MustCompile(`^\d+\. (\S+):(\d+)(?:-(\d+))? `)
	declHeaderMinimal = regexp.MustCompile(`^(\S+):(\d+) \*$`)
)

// declRecoverLineNumbers reads a block the way an agent would, with nothing but its text, and
// returns the file line of every printed source line. ok is false when the header does not parse.
func declRecoverLineNumbers(block []byte) (numbers []int, sources []string, last int, ok bool) {
	parts := strings.Split(strings.TrimSuffix(string(block), "\n"), "\n")
	last = -1
	var line int
	if m := declHeaderRanked.FindStringSubmatch(parts[0]); m != nil {
		line, _ = strconv.Atoi(m[2])
		last = line
		if m[3] != "" {
			last, _ = strconv.Atoi(m[3])
		}
	} else if m := declHeaderMinimal.FindStringSubmatch(parts[0]); m != nil {
		line, _ = strconv.Atoi(m[2])
	} else {
		return nil, nil, 0, false
	}
	for _, p := range parts[1:] {
		var n int
		if strings.HasPrefix(p, "... ") && strings.HasSuffix(p, " elided") {
			fmt.Sscanf(p, "... %d", &n)
			line += n
			continue
		}
		numbers = append(numbers, line)
		sources = append(sources, p)
		line++
	}
	return numbers, sources, last, true
}

// declCheckRecoverable asserts that every printed line of a block — a declaration block or the
// ordinary one, whichever the renderer chose — sits at the file line recoverable from the block's
// own header and elision counts, on every header rung, and that a range header ends at the last
// printed line. Only a bare header with no lines under it is exempt.
func declCheckRecoverable(t *testing.T, tag string, result sem.SearchResult, budget int) []byte {
	t.Helper()
	block := agentSearchPrimaryBlock(result, budget, false)
	if len(block) == 0 || !strings.Contains(strings.TrimSuffix(string(block), "\n"), "\n") {
		return nil
	}
	numbers, sources, last, ok := declRecoverLineNumbers(block)
	if !ok {
		t.Fatalf("%s: unparsable header:\n%s", tag, block)
	}
	file := strings.Split(result.Snippet, "\n")
	for i, number := range numbers {
		index := number - result.SnippetStartLine
		if index < 0 || index >= len(file) || file[index] != sources[i] {
			t.Fatalf("%s: printed line %q is read as file line %d, which is not that line:\n%s", tag, sources[i], number, block)
		}
	}
	if last >= 0 && len(numbers) > 0 && numbers[len(numbers)-1] != last {
		t.Fatalf("%s: header range ends at %d, last printed line is %d:\n%s", tag, last, numbers[len(numbers)-1], block)
	}
	return block
}

// HEADER HONESTY. A declaration block prints the declaration above an elision line and the focus
// window below it; its minimal rung once named the focus line (`path:FOCUS *`) over a first printed
// line that was the declaration, several lines up, so an agent reading `path:131 *` took the
// signature to be at line 131. Every rung must now let the reader recover the exact file line of
// every printed line from the block alone. Swept over every fixture, every budget from a bare
// header to the whole callable, and a random population; the sweep must see the minimal rung.
func TestAgentBlockDeclarationLineNumbersRecoverable(t *testing.T) {
	t.Parallel()
	checked, minimal := 0, 0
	count := func(result sem.SearchResult, budget int, tag string) {
		if block := declCheckRecoverable(t, tag, result, budget); block != nil {
			checked++
			if declHeaderMinimal.MatchString(strings.SplitN(string(block), "\n", 2)[0]) {
				minimal++
			}
		}
	}
	step, iters := 1, 600
	if testing.Short() {
		step, iters = 7, 60
	}
	for _, f := range declFixtures {
		result, _ := f.build(120, 60)
		for budget := 40; budget <= 3000; budget += step {
			count(result, budget, fmt.Sprintf("%s budget %d", f.lang, budget))
		}
	}
	rng := rand.New(rand.NewSource(20260929))
	for iter := 0; iter < iters; iter++ {
		n := 8 + rng.Intn(70)
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("\tv%d := call(%s)", i, strings.Repeat("x", rng.Intn(40)))
		}
		declIndex := rng.Intn(3)
		if declIndex > 0 {
			lines[0] = `@Named("target")`
		}
		lines[declIndex] = "func target(" + strings.Repeat("a int, ", rng.Intn(6)) + ") {"
		first := 1 + rng.Intn(400)
		var merged []int
		if rng.Intn(2) == 0 {
			j := declIndex + 5 + rng.Intn(n-declIndex-5)
			lines[j] = "func absorbed() {"
			merged = append(merged, first+j)
		}
		result := sem.SearchResult{
			Rank: 1 + rng.Intn(9), Score: 10, FilePath: "pkg/" + strings.Repeat("d", rng.Intn(30)) + ".go",
			StartLine: first, EndLine: first + n - 1, FocusLine: first + declIndex + 4 + rng.Intn(n-declIndex-4),
			SnippetStartLine: first, SnippetEndLine: first + n - 1,
			SymbolStartLine: first, SymbolEndLine: first + n - 1, SymbolName: "target", QualifiedName: "target",
			Signals: []string{"complete-symbol"}, MergedDeclLines: merged, Snippet: strings.Join(lines, "\n"),
		}
		for b := 0; b < 25; b++ {
			budget := 30 + rng.Intn(1500)
			count(result, budget, fmt.Sprintf("iter %d budget %d", iter, budget))
		}
	}
	if checked == 0 || minimal == 0 {
		t.Fatalf("the sweep checked %d declaration blocks, %d on the minimal rung; it tests nothing", checked, minimal)
	}
	t.Logf("%d declaration blocks checked, %d on the minimal rung", checked, minimal)
}

// DEFENCE IN DEPTH. When the finder takes the wrong line for the declaration — here a keyword
// argument `route(target=True)` is definition-shaped and comes first — the line the old window
// showed that really declares the symbol must still be shown, at every budget. Without the carried
// candidate lines the new block, obliged to print the wrong "declaration", slid its window down and
// dropped `def target(self):`.
func TestAgentBlockKeepsCandidateDeclarationTheWindowShowed(t *testing.T) {
	t.Parallel()
	lines := []string{"    route(target=True)", "    def target(self):"}
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("        self.step%02d = compute(%d, %s)", i, i, strings.Repeat("q", i%5)))
	}
	first := 50
	result := sem.SearchResult{
		Rank: 1, Score: 20, FilePath: "app/handlers.py", StartLine: first, EndLine: first + len(lines) - 1,
		FocusLine: first + 9, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + len(lines) - 1, SymbolName: "target", QualifiedName: "Handler.target",
		Signals: []string{"complete-symbol"}, Snippet: strings.Join(lines, "\n"),
	}
	if index, _ := agentSearchDeclIndex(lines, first, first, first+len(lines)-1, "target"); index != 0 {
		t.Fatalf("fixture premise: the finder must take the keyword argument (index 0), took %d", index)
	}
	exercised := 0
	for budget := 40; budget <= 2500; budget++ {
		view := agentSearchBlockViewOf(result)
		plain, left, _ := agentSearchFocusWindow(view, budget)
		block := agentSearchPrimaryBlock(result, budget, false)
		if plain == nil || left == 0 || !blockShowsLine(plain, lines[1]) {
			continue
		}
		exercised++
		if !blockShowsLine(block, lines[1]) {
			t.Fatalf("budget %d: the old window showed the real declaration and the block dropped it\nplain:\n%s\nnew:\n%s", budget, plain, block)
		}
	}
	if exercised == 0 {
		t.Fatal("no budget had the old window show the real declaration without the finder's line")
	}
}
