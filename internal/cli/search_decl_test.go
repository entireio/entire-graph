package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// declFixture is one undocumented callable with a long body whose focus line sits well below its
// declaration: the shape where a focus-centred window cut the declaration.
type declFixture struct {
	lang, path, symbol string
	lead               []string // annotations / decorators the index counts as the symbol's start
	signature          []string // the declaration; signature[0] names the symbol
	bodyIndent         string
	close              string
}

var declFixtures = []declFixture{
	{lang: "go", path: "internal/store/batch.go", symbol: "processBatch",
		signature: []string{"func (s *Store) processBatch(ctx context.Context, items []Item) error {"}, bodyIndent: "\t", close: "}"},
	{lang: "java", path: "src/main/java/org/acme/Batch.java", symbol: "processBatch",
		lead:      []string{"    @Override", `    @SuppressWarnings("unchecked")`},
		signature: []string{"    public void processBatch(List<Item> items) throws IOException {"}, bodyIndent: "        ", close: "    }"},
	{lang: "csharp", path: "src/Acme/Batch.cs", symbol: "ProcessBatch",
		lead:      []string{`    [Obsolete("use ProcessAll")]`},
		signature: []string{"    public void ProcessBatch(IList<Item> items)", "    {"}, bodyIndent: "        ", close: "    }"},
	{lang: "python", path: "acme/batch.py", symbol: "process_batch",
		lead:      []string{"    @retry(times=3)"},
		signature: []string{"    def process_batch(self, items):"}, bodyIndent: "        ", close: ""},
	{lang: "ts", path: "src/batch.ts", symbol: "processBatch",
		signature: []string{"export function processBatch(", "  items: Item[],", "  options: BatchOptions,", "): void {"}, bodyIndent: "  ", close: "}"},
	{lang: "rust", path: "src/batch.rs", symbol: "process_batch",
		lead:      []string{"#[inline]"},
		signature: []string{"pub fn process_batch(items: &[Item]) -> Result<(), Error> {"}, bodyIndent: "    ", close: "}"},
}

// declFocusOffset is how far below the declaration the focus line sits.
const declFocusOffset = 25

// build returns the result for the fixture's callable, whose snippet is the whole callable starting
// at file line `first` with `bodyLines` body lines, the focus declFocusOffset lines below the
// declaration, and the named line's text.
func (f declFixture) build(first, bodyLines int) (sem.SearchResult, string) {
	var lines []string
	lines = append(lines, f.lead...)
	lines = append(lines, f.signature...)
	declIndex := len(f.lead)
	for i := 0; i < bodyLines; i++ {
		// Varying line lengths so byte budgets do not land on a uniform grid.
		lines = append(lines, fmt.Sprintf("%sstep%02d := compute(items[%d], %s)", f.bodyIndent, i, i, strings.Repeat("w", i%7)))
	}
	if f.close != "" {
		lines = append(lines, f.close)
	}
	focus := first + declIndex + declFocusOffset
	lines[declIndex+declFocusOffset] = f.bodyIndent + "flushPendingWrites(queueDepth, retryLimit, flushPendingWrites)"
	end := first + len(lines) - 1
	return sem.SearchResult{
		Rank: 1, Score: 40, FilePath: f.path,
		StartLine: first, EndLine: end, FocusLine: focus,
		SnippetStartLine: first, SnippetEndLine: end,
		SymbolStartLine: first, SymbolEndLine: end,
		SymbolName: f.symbol, QualifiedName: f.symbol, Kind: "function",
		Signals: []string{"complete-symbol"},
		Snippet: strings.Join(lines, "\n"),
	}, lines[declIndex]
}

// blockShowsLine reports whether a rendered block prints `line` as one of its body lines.
func blockShowsLine(block []byte, line string) bool {
	body := string(block)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i:] // skip the header
	} else {
		return false
	}
	return strings.Contains(body+"\n", "\n"+line+"\n")
}

// blockBody splits a block's printed lines (after the header) into source lines and elision lines.
func blockBody(block []byte) (source []string, elisions int) {
	text := strings.TrimSuffix(string(block), "\n")
	parts := strings.Split(text, "\n")
	for _, line := range parts[1:] {
		if strings.HasPrefix(line, "... ") && strings.HasSuffix(line, " elided") {
			elisions++
			continue
		}
		source = append(source, line)
	}
	return source, elisions
}

func linesBytes(lines []string) int {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return total
}

// TestAgentBlockShowsDeclarationBudgetSweep is the dense sweep: every fixture, every budget from a
// bare header to the whole callable. At every budget the block fits, never shows fewer declarations
// than the focus-centred window did, shows the declaration whenever the header and that line fit,
// and keeps the body within one line's worth of what the focus-centred window printed beyond the
// bytes the declaration itself costs.
func TestAgentBlockShowsDeclarationBudgetSweep(t *testing.T) {
	t.Parallel()
	for _, fixture := range declFixtures {
		t.Run(fixture.lang, func(t *testing.T) {
			t.Parallel()
			result, decl := fixture.build(120, 45)
			result = searchResultOnOneLine(result)
			full := len(agentSearchPrimaryBlock(result, 0))
			maxLine := 0
			for _, line := range strings.Split(result.Snippet, "\n") {
				maxLine = max(maxLine, len(line)+1)
			}
			locatedBefore, locatedAfter, bodyBefore, bodyAfter := 0, 0, 0, 0
			for budget := 40; budget <= full+64; budget++ {
				view := agentSearchBlockViewOf(result)
				plain, _, _ := agentSearchFocusWindow(view, budget)
				block := agentSearchPrimaryBlock(result, budget)
				if len(block) > budget {
					t.Fatalf("budget %d: block is %d bytes:\n%s", budget, len(block), block)
				}
				if blockShowsLine(plain, decl) && !blockShowsLine(block, decl) {
					t.Fatalf("budget %d: the focus window showed the declaration and the block does not:\n%s", budget, block)
				}
				minimal := len(fmt.Sprintf("%s:%d *\n", result.FilePath, result.FocusLine)) + len(decl) + 1
				if budget >= minimal && !blockShowsLine(block, decl) {
					t.Fatalf("budget %d: header+declaration fit (%d B) but the block does not show it:\n%s", budget, minimal, block)
				}
				if blockShowsLine(plain, decl) {
					locatedBefore++
				}
				if blockShowsLine(block, decl) {
					locatedAfter++
				}
				plainSource, _ := blockBody(plain)
				source, elisions := blockBody(block)
				bodyBefore += len(plainSource)
				bodyAfter += len(source)
				if plain == nil {
					continue
				}
				// The body may shrink by the declaration's own bytes, its elision line, and one line of
				// granularity (plus a few header digits) — never collapse.
				declCost := 0
				if !blockShowsLine(plain, decl) {
					declCost = len(decl) + 1 + elisions*len("... 99 lines elided\n")
				}
				if linesBytes(source) < linesBytes(plainSource)-declCost-maxLine-8 {
					t.Fatalf("budget %d: body collapsed: %d B printed vs %d B before (decl cost %d):\nBEFORE\n%s\nAFTER\n%s",
						budget, linesBytes(source), linesBytes(plainSource), declCost, plain, block)
				}
			}
			t.Logf("%s: decl shown at %d/%d budgets before, %d after; source lines summed over the sweep %d -> %d",
				fixture.lang, locatedBefore, full+64-40+1, locatedAfter, bodyBefore, bodyAfter)
			if locatedAfter <= locatedBefore {
				t.Fatalf("%s: the sweep located no more often (%d -> %d)", fixture.lang, locatedBefore, locatedAfter)
			}
		})
	}
}

// TestAgentBlockDeclarationStepsOverAnnotations: the printed declaration is the line that names the
// symbol, not the annotation the index counts as the symbol's first line.
func TestAgentBlockDeclarationStepsOverAnnotations(t *testing.T) {
	t.Parallel()
	for _, fixture := range declFixtures {
		if len(fixture.lead) == 0 {
			continue
		}
		result, decl := fixture.build(10, 45)
		block := agentSearchPrimaryBlock(searchResultOnOneLine(result), 700)
		if !blockShowsLine(block, decl) {
			t.Fatalf("%s: declaration not shown:\n%s", fixture.lang, block)
		}
		if blockShowsLine(block, fixture.lead[0]) {
			t.Fatalf("%s: printed the annotation, which the budget should have spent on the named line or body:\n%s", fixture.lang, block)
		}
		if !strings.Contains(string(block), " elided\n") {
			t.Fatalf("%s: a declaration far above the window must be followed by an elision line:\n%s", fixture.lang, block)
		}
		// Under a ranked header rung the printed range starts at the declaration, not the annotation.
		ranked := 0
		for budget := 300; budget < 3000; budget += 7 {
			block := agentSearchPrimaryBlock(searchResultOnOneLine(result), budget)
			header, _, _ := strings.Cut(string(block), "\n")
			if !strings.HasPrefix(header, "1. ") || !strings.Contains(string(block), " elided\n") {
				continue
			}
			ranked++
			if want := fmt.Sprintf(":%d-", result.StartLine+len(fixture.lead)); !strings.Contains(header, want) {
				t.Fatalf("%s: header %q does not start its range at the declaration (%s)", fixture.lang, header, want)
			}
		}
		if ranked == 0 {
			t.Fatalf("%s: no budget produced a ranked header over a declaration and a separate window", fixture.lang)
		}
	}
}

// TestAgentBlockUnchangedWhenWindowHoldsDeclaration: a block whose focus window already shows the
// declaration is the focus window, byte for byte.
func TestAgentBlockUnchangedWhenWindowHoldsDeclaration(t *testing.T) {
	t.Parallel()
	for _, fixture := range declFixtures {
		result, decl := fixture.build(50, 45)
		result.FocusLine = result.SymbolStartLine + len(fixture.lead) + 2
		result = searchResultOnOneLine(result)
		for budget := 60; budget < 3000; budget += 3 {
			view := agentSearchBlockViewOf(result)
			plain, _, _ := agentSearchFocusWindow(view, budget)
			if !blockShowsLine(plain, decl) {
				continue
			}
			if got := agentSearchPrimaryBlock(result, budget); string(got) != string(plain) {
				t.Fatalf("%s budget %d: block changed although the window held the declaration:\nWANT\n%s\nGOT\n%s", fixture.lang, budget, plain, got)
			}
		}
	}
}

// TestAgentBlockUnchangedWhenWindowHoldsNamedLineButNotContinuation: the window holds the named
// line of a multi-line signature but not its continuation, and a long line above the declaration
// makes a variant with the (short) continuation print more lines. The block must still be the
// window, byte for byte: never-locate-less is about the declaration, and a block that already shows
// it is not re-rendered.
func TestAgentBlockUnchangedWhenWindowHoldsNamedLineButNotContinuation(t *testing.T) {
	t.Parallel()
	lines := []string{
		"@Component({ selector: '" + strings.Repeat("s", 150) + "' })",
		"export function processBatch(",
		"  a: A,",
		"  b: B,",
		"): void {",
	}
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("  step%02d(a, b);", i))
	}
	lines = append(lines, "}")
	check := func(t *testing.T, lines []string, focusIndex, named int) {
		result := sem.SearchResult{
			Rank: 1, Score: 40, FilePath: "src/batch.ts",
			StartLine: 10, EndLine: 10 + len(lines) - 1, FocusLine: 10 + focusIndex,
			SnippetStartLine: 10, SnippetEndLine: 10 + len(lines) - 1,
			SymbolStartLine: 10, SymbolEndLine: 10 + len(lines) - 1, SymbolName: "processBatch", QualifiedName: "processBatch",
			Snippet: strings.Join(lines, "\n"),
		}
		result = searchResultOnOneLine(result)
		checked := 0
		for budget := 40; budget < 1200; budget++ {
			plain, _, _ := agentSearchFocusWindow(agentSearchBlockViewOf(result), budget)
			if !blockShowsLine(plain, lines[named]) || blockShowsLine(plain, lines[named+2]) {
				continue
			}
			checked++
			if got := agentSearchPrimaryBlock(result, budget); string(got) != string(plain) {
				t.Fatalf("budget %d: re-rendered a block that already showed its declaration:\nWANT\n%s\nGOT\n%s", budget, plain, got)
			}
		}
		if checked == 0 {
			t.Fatal("no budget put the named line in the window without its continuation")
		}
	}
	check(t, lines, 1, 1)
	// The focus on the first of three annotations, two long ones between it and the named line: a
	// variant printing the focus, an elision and the short signature would print more lines than the
	// window in a narrow band of budgets. It must not be taken.
	annotated := []string{
		"@Injectable()",
		"@Component({ selector: '" + strings.Repeat("s", 90) + "' })",
		"@Memoize({ key: '" + strings.Repeat("k", 90) + "' })",
		"export function processBatch(",
		"  a: A,",
		"  b: B,",
		"): void {",
	}
	for i := 0; i < 30; i++ {
		annotated = append(annotated, fmt.Sprintf("  step%02d(a, b);", i))
	}
	annotated = append(annotated, "}")
	check(t, annotated, 0, 3)
}

// TestAgentBlockUndocumentedSymbolWithoutNamedLineIsUnchanged: no line naming the symbol within the
// anchor bound means the block renders as it always has.
func TestAgentBlockNoNamedLineIsUnchanged(t *testing.T) {
	t.Parallel()
	result, _ := declFixtures[0].build(200, 45)
	result.SymbolName = "notInThisSnippet"
	result = searchResultOnOneLine(result)
	for budget := 60; budget < 3000; budget += 5 {
		plain, _, _ := agentSearchFocusWindow(agentSearchBlockViewOf(result), budget)
		if plain == nil {
			plain = fitAgentSearchLocation(result.Rank, result.FilePath, result.FocusLine, result.QualifiedName, "", agentSearchScoreTag(result), budget)
		}
		if got := agentSearchPrimaryBlock(result, budget); string(got) != string(plain) {
			t.Fatalf("budget %d: changed without a named line:\n%s\nvs\n%s", budget, plain, got)
		}
	}
}

// TestAgentBlockDeclarationAloneWhenNoWindowFits: when the header, declaration, elision and focus
// line do not fit together but the header and declaration do, the declaration is printed alone.
func TestAgentBlockDeclarationAloneWhenNoWindowFits(t *testing.T) {
	t.Parallel()
	result, decl := declFixtures[0].build(300, 45)
	result = searchResultOnOneLine(result)
	found := false
	for budget := 40; budget < 400; budget++ {
		view := agentSearchBlockViewOf(result)
		own, _, _ := agentSearchDecls(view)
		withWindow, _ := agentSearchWidestWithDecls(view, []agentSearchDecl{{index: own.index}}, budget)
		alone := agentSearchDeclsOnly(view, []agentSearchDecl{{index: own.index}}, budget)
		if withWindow != nil || alone == nil {
			continue
		}
		found = true
		block := agentSearchPrimaryBlock(result, budget)
		if !blockShowsLine(block, decl) || strings.Contains(string(block), "elided") {
			t.Fatalf("budget %d: want the declaration alone, got:\n%s", budget, block)
		}
		// The header locates the printed line: the declaration's line under a ranked rung, the focus
		// line under the bare locator rung.
		header, _, _ := strings.Cut(string(block), "\n")
		declLine := result.SymbolStartLine
		if !strings.Contains(header, fmt.Sprintf(":%d ", declLine)) && !strings.Contains(header, fmt.Sprintf(":%d *", result.FocusLine)) {
			t.Fatalf("budget %d: header %q names neither the declaration line nor the focus", budget, header)
		}
	}
	if !found {
		t.Fatal("no budget exercised the declaration-alone rung")
	}
}

// TestAgentBlockMultiLineSignature: the continuation of a multi-line signature rides with the
// declaration when the block can afford it without printing fewer lines.
func TestAgentBlockMultiLineSignature(t *testing.T) {
	t.Parallel()
	fixture := declFixtures[4] // ts, 4-line signature
	result, _ := fixture.build(10, 45)
	result = searchResultOnOneLine(result)
	sawAll := false
	for budget := 200; budget < 1500; budget++ {
		block := agentSearchPrimaryBlock(result, budget)
		shown := 0
		for _, line := range fixture.signature[:3] {
			if blockShowsLine(block, line) {
				shown++
			}
		}
		if shown == 3 && strings.Contains(string(block), " elided\n") {
			sawAll = true // the whole unfinished signature, above a separate focus window
		}
		if !blockShowsLine(block, fixture.signature[0]) {
			t.Fatalf("budget %d: named line missing:\n%s", budget, block)
		}
	}
	if !sawAll {
		t.Fatal("the signature continuation was never shown")
	}
}

// TestAgentBlockMergedSpanShowsAbsorbedDeclarations: a merged span shows the declarations of the
// members it absorbed when the block can afford them without printing fewer lines, and never shows
// an absorbed declaration without the survivor's own.
func TestAgentBlockMergedSpanShowsAbsorbedDeclarations(t *testing.T) {
	t.Parallel()
	var lines []string
	lines = append(lines, "func survivor(a int) int {")
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("\tv%02d := a * %d", i, i))
	}
	lines = append(lines, "\treturn flushPendingWrites(queueDepth, retryLimit)", "}", "")
	absorbedAt := len(lines)
	lines = append(lines, "func absorbedHelper(b int) int {")
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("\tw%02d := b + %d", i, i))
	}
	lines = append(lines, "}")
	first := 100
	result := sem.SearchResult{
		Rank: 1, Score: 50, FilePath: "pkg/merge.go",
		StartLine: first, EndLine: first + len(lines) - 1, FocusLine: first + 31,
		SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + 32, SymbolName: "survivor", QualifiedName: "survivor",
		Signals: []string{"complete-symbol", "contiguous-span"}, MergedRanks: []int{1, 2},
		MergedDeclLines: []int{first + absorbedAt},
		Snippet:         strings.Join(lines, "\n"),
	}
	result = searchResultOnOneLine(result)
	absorbedShown := 0
	for budget := 60; budget < 3000; budget++ {
		view := agentSearchBlockViewOf(result)
		plain, _, _ := agentSearchFocusWindow(view, budget)
		block := agentSearchPrimaryBlock(result, budget)
		if len(block) > budget {
			t.Fatalf("budget %d: %d bytes", budget, len(block))
		}
		own, absorbed := blockShowsLine(block, lines[0]), blockShowsLine(block, lines[absorbedAt])
		if absorbed && !own {
			t.Fatalf("budget %d: absorbed declaration shown without the survivor's:\n%s", budget, block)
		}
		if blockShowsLine(plain, lines[absorbedAt]) && !absorbed {
			t.Fatalf("budget %d: the focus window showed the absorbed declaration and the block lost it:\n%s", budget, block)
		}
		if absorbed && !blockShowsLine(plain, lines[absorbedAt]) {
			absorbedShown++
			// It displaced at most one body line per line it added.
			base, _ := agentSearchWidestWithDecls(view, []agentSearchDecl{{index: 0}}, budget)
			baseSource, baseElisions := blockBody(base)
			source, elisions := blockBody(block)
			if len(source)+elisions < len(baseSource)+baseElisions {
				t.Fatalf("budget %d: absorbed declaration cost more lines than it added:\n%s\nvs\n%s", budget, base, block)
			}
		}
	}
	if absorbedShown == 0 {
		t.Fatal("the absorbed declaration was never shown")
	}
}

// TestAgentBlockTailFocusBelowSnippetKeepsHeaderFocus: a demoted row whose snippet starts at its
// declaration and ends above its match still reports where the query matched.
func TestAgentBlockTailFocusBelowSnippetKeepsHeaderFocus(t *testing.T) {
	t.Parallel()
	result := sem.SearchResult{
		Rank: 7, Score: 30, FilePath: "internal/indexer/indexer.go",
		StartLine: 191, EndLine: 199, FocusLine: 198,
		SnippetStartLine: 194, SnippetEndLine: 195,
		SymbolStartLine: 194, SymbolEndLine: 199, SymbolName: "resolveNamespace", QualifiedName: "Indexer.resolveNamespace",
		Snippet: "func (i *Indexer) resolveNamespace(repoID string) string {\n\tif i.override != \"\" {",
	}
	block := string(agentSearchPrimaryBlock(searchResultOnOneLine(result), 4096))
	if !strings.Contains(block, "[focus:198]") || !strings.Contains(block, "func (i *Indexer) resolveNamespace") {
		t.Fatalf("got:\n%s", block)
	}
}

// TestAgentBlockDeclarationDeterministic: the same result renders the same bytes every time.
func TestAgentBlockDeclarationDeterministic(t *testing.T) {
	t.Parallel()
	for _, fixture := range declFixtures {
		result, _ := fixture.build(77, 45)
		result = searchResultOnOneLine(result)
		for _, budget := range []int{150, 333, 700, 1200} {
			first := agentSearchPrimaryBlock(result, budget)
			for i := 0; i < 5; i++ {
				if got := agentSearchPrimaryBlock(result, budget); string(got) != string(first) {
					t.Fatalf("%s budget %d: nondeterministic", fixture.lang, budget)
				}
			}
		}
	}
}
