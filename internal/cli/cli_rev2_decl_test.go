package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// REV2-A: a wrong finder pick inside the first 16 lines, the real def BELOW the 16-line head bound
// (a long decorator stack). The old window showed the def; does the new block keep it?
func TestRev2LongDecoratorStackWrongPick(t *testing.T) {
	lines := []string{"@retry(times=3)  # handle() is retried on 5xx"}
	for i := 0; i < 17; i++ {
		lines = append(lines, fmt.Sprintf("@click.option(\"--opt%02d\", default=%d, help=\"option %02d\")", i, i, i))
	}
	def := len(lines)
	lines = append(lines, "def handle(opt00, opt01, opt02):")
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("    step_%02d = compute(%d)", i, i))
	}
	first := 10
	res := sem.SearchResult{Rank: 1, Score: 20, FilePath: "app/cli.py", StartLine: first, EndLine: first + len(lines) - 1,
		FocusLine: first + def + 6, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + len(lines) - 1, SymbolName: "handle", QualifiedName: "handle",
		Signals: []string{"complete-symbol"}, Snippet: strings.Join(lines, "\n")}
	idx, ok := agentSearchDeclIndex(lines, first, first, first+len(lines)-1, "handle")
	t.Logf("finder premise: picked %d ok=%v (%q)", idx, ok, lines[idx])
	lost, exercised := 0, 0
	for budget := 60; budget <= 4000; budget++ {
		view := agentSearchBlockViewOf(res)
		plain, _, _ := agentSearchFocusWindow(view, budget)
		block := agentSearchPrimaryBlock(res, budget)
		if !blockShowsLine(plain, lines[def]) {
			continue
		}
		exercised++
		if !blockShowsLine(block, lines[def]) {
			if lost == 0 {
				t.Logf("budget %d\nplain:\n%s\nnew:\n%s", budget, plain, block)
			}
			lost++
		}
	}
	t.Logf("exercised %d budgets", exercised)
	if lost > 0 {
		t.Errorf("DEFECT never-locate-less: %d budgets drop `def handle` the plain window showed", lost)
	}
}

// REV2-B: C# property whose type equals its name; the finder picks `new Options()` below the real
// declaration. Heads should keep `public Options Options` whenever the plain window showed it.
func TestRev2CSharpLazyPropertyBlock(t *testing.T) {
	lines := []string{"    public Options Options", "    {", "        get", "        {"}
	lines = append(lines, "            if (_options == null) _options = new Options();")
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("            _options.Set%02d(Load(%d));", i, i))
	}
	lines = append(lines, "            return _options;", "        }", "    }")
	first := 40
	res := sem.SearchResult{Rank: 1, Score: 20, FilePath: "src/Cfg.cs", StartLine: first, EndLine: first + len(lines) - 1,
		FocusLine: first + 25, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + len(lines) - 1, SymbolName: "Options", QualifiedName: "Cfg.Options",
		Signals: []string{"complete-symbol"}, Snippet: strings.Join(lines, "\n")}
	lost, wrongOnly := 0, 0
	for budget := 60; budget <= 3000; budget++ {
		view := agentSearchBlockViewOf(res)
		plain, _, _ := agentSearchFocusWindow(view, budget)
		block := agentSearchPrimaryBlock(res, budget)
		if blockShowsLine(plain, lines[0]) && !blockShowsLine(block, lines[0]) {
			lost++
		}
		if string(block) != string(plain) && blockShowsLine(block, lines[4]) && !blockShowsLine(block, lines[0]) {
			if wrongOnly == 0 {
				t.Logf("budget %d: block presents the wrong line as the declaration:\n%s", budget, block)
			}
			wrongOnly++
		}
	}
	t.Logf("lost=%d, changed blocks printing wrong decl without real one=%d", lost, wrongOnly)
	if lost > 0 {
		t.Errorf("DEFECT never-locate-less: %d budgets", lost)
	}
}

// REV2-C: a demoted tail row whose snippet starts at its declaration (sem keeps it there) and
// whose FocusLine lies below the snippet: the ORDINARY block's minimal rung names FocusLine, a line
// that is not printed, so the printed lines' numbers are unrecoverable (new with #303's tersify).
func TestRev2TailRowMinimalRungNamesUnprintedFocus(t *testing.T) {
	result := sem.SearchResult{
		Rank: 7, Score: 30, FilePath: "internal/indexer/indexer.go",
		StartLine: 191, EndLine: 199, FocusLine: 198,
		SnippetStartLine: 194, SnippetEndLine: 195,
		SymbolStartLine: 194, SymbolEndLine: 199, SymbolName: "resolveNamespace", QualifiedName: "Indexer.resolveNamespace",
		Snippet: "func (i *Indexer) resolveNamespace(repoID string) string {\n\tif i.override != \"\" {",
	}
	bad := 0
	for budget := 30; budget <= 400; budget++ {
		block := agentSearchPrimaryBlock(searchResultOnOneLine(result), budget)
		numbers, sources, _, ok := declRecoverLineNumbers(block)
		if !ok || len(numbers) == 0 {
			continue
		}
		file := strings.Split(result.Snippet, "\n")
		for i, n := range numbers {
			idx := n - result.SnippetStartLine
			if idx < 0 || idx >= len(file) || file[idx] != sources[i] {
				if bad == 0 {
					t.Logf("budget %d: line %q read as file line %d:\n%s", budget, sources[i], n, block)
				}
				bad++
				break
			}
		}
	}
	if bad > 0 {
		t.Errorf("DEFECT header recovery: %d budgets print a tail row whose header misnumbers its lines", bad)
	}
}

// REV2-D: #302's exact-name block, minimal rung: `path:NAMED *` over annotation/doc lines above.
func TestRev2ExactNameMinimalRungRecovery(t *testing.T) {
	bad, checked := 0, 0
	for _, c := range exactNameLangCases {
		for rank := 1; rank <= 1; rank++ {
			response := exactNameLangResponse(c, rank)
			anchor, ok := agentExactNameAnchor(response.Results[0], c.symbol)
			if !ok {
				continue
			}
			for step := range anchor.steps {
				for floor := 0; floor < 3; floor++ {
					top, bottom := anchor.steps[step][0], anchor.steps[step][1]
					block := agentExactNameBlock(anchor, floor, 1<<20)
					_ = top
					_ = bottom
					_ = block
				}
			}
			// Render at every budget on the minimal rung and check line recovery.
			for budget := 1; budget <= 2000; budget++ {
				block := agentExactNameBlock(anchor, 2, budget)
				if block == nil {
					continue
				}
				numbers, sources, _, ok := declRecoverLineNumbers(block)
				if !ok {
					continue
				}
				checked++
				for i, n := range numbers {
					idx := n - anchor.first
					if idx < 0 || idx >= len(anchor.lines) || anchor.lines[idx] != sources[i] {
						if sources[0] == termsafeLine(anchor.lines[anchor.named]) {
							t.Logf("%s budget %d: MISNUMBER WITH NAMED FIRST:\n%s", c.name, budget, block)
						}
						if bad < 2 {
							t.Logf("%s budget %d: %q read as line %d:\n%s", c.name, budget, sources[i], n, block)
						}
						bad++
						break
					}
				}
			}
		}
	}
	t.Logf("checked %d minimal-rung exact blocks, %d misnumbered", checked, bad)
	if bad > 0 {
		t.Errorf("DEFECT (known leftover) exact-name minimal rung misnumbers lines in %d blocks", bad)
	}
}

// REV2-E: #302 exact-name anchor on non-shaped declarations with a shaped use below: prints the
// wrong line as the declaration-only rung. Compare against first-mention (pre-8411) behaviour.
func TestRev2ExactNameAnchorWrongPick(t *testing.T) {
	cases := []struct{ name, file, symbol, snippet string }{
		{"cs-lazy-property", "src/Cfg.cs", "Options", "    public Options Options\n    {\n        get { return _options ??= new Options(); }\n    }\n"},
		{"ruby-self-hashkey", "lib/app.rb", "config", "def self.config\n  @config ||= Config.load(config: path)\nend\n"},
		{"py-decorator-trailing-comment", "app/net.py", "fetch", "@retry  # fetch() may raise\n@lru_cache()\ndef fetch(url):\n    return get(url)\n"},
	}
	for _, c := range cases {
		n := strings.Count(c.snippet, "\n")
		r := sem.SearchResult{Rank: 1, Score: 10, FilePath: c.file, StartLine: 10, EndLine: 10 + n - 1, FocusLine: 10,
			SnippetStartLine: 10, SnippetEndLine: 10 + n - 1, SymbolStartLine: 10, SymbolEndLine: 10 + n - 1,
			SymbolName: c.symbol, QualifiedName: c.symbol, Snippet: c.snippet}
		anchor, ok := agentExactNameAnchor(r, c.symbol)
		if !ok {
			t.Logf("%s: not anchored", c.name)
			continue
		}
		smallest := agentExactNameBlock(anchor, 2, 0)
		t.Logf("%s: named=%d (%q); smallest block:\n%s", c.name, anchor.named, anchor.lines[anchor.named], smallest)
	}
}

func termsafeLine(s string) string { return s }

// REV2-F: an absorbed member whose recorded declaration (sem's finder) is BELOW its real one (C#
// property whose type equals its name, lazy getter). Heads for absorbed members run DOWN from the
// recorded line only, so the real `public Options Options` the plain window showed is unprotected.
func TestRev2AbsorbedWrongPickAboveUnprotected(t *testing.T) {
	var lines []string
	lines = append(lines, "    public void Load(string path)", "    {")
	for i := 0; i < 24; i++ {
		lines = append(lines, fmt.Sprintf("        _cfg%02d = Read(path, %d);", i, i))
	}
	lines = append(lines, "    }", "")
	real := len(lines)
	lines = append(lines, "    public Options Options", "    {")
	wrong := len(lines)
	lines = append(lines, "        get { return _options ??= new Options(); }", "    }", "")
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("    // trailing %02d", i))
	}
	first := 10
	lost, exercised := 0, 0
	for focus := 2; focus < real; focus++ {
		res := sem.SearchResult{Rank: 1, Score: 50, FilePath: "src/Cfg.cs", StartLine: first, EndLine: first + len(lines) - 1,
			FocusLine: first + focus, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
			SymbolStartLine: first, SymbolEndLine: first + real - 2, SymbolName: "Load", QualifiedName: "Cfg.Load",
			Signals: []string{"complete-symbol", "contiguous-span"}, MergedRanks: []int{1, 2},
			MergedDeclLines: rev2Merged(first, real, wrong), Snippet: strings.Join(lines, "\n")}
		for budget := 80; budget <= 2500; budget++ {
			view := agentSearchBlockViewOf(res)
			plain, _, _ := agentSearchFocusWindow(view, budget)
			if !blockShowsLine(plain, lines[real]) {
				continue
			}
			exercised++
			block := agentSearchPrimaryBlock(res, budget)
			if !blockShowsLine(block, lines[real]) {
				if lost == 0 {
					t.Logf("focus %d budget %d\nplain:\n%s\nnew:\n%s", focus, budget, plain, block)
				}
				lost++
			}
		}
	}
	t.Logf("exercised %d", exercised)
	if lost > 0 {
		t.Errorf("DEFECT never-locate-less: %d (focus,budget) pairs drop `public Options Options` the plain window showed", lost)
	}
}

func rev2Merged(first, real, wrong int) []int {
	switch os.Getenv("REV2_MERGED") {
	case "real":
		return []int{first + real}
	case "none":
		return nil
	}
	return []int{first + wrong}
}
