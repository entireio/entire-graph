package cli

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

var declAdvHeader = regexp.MustCompile(`^(?:\d+\. )?(\S+?):(\d+)(?:-(\d+))?`)

// declAdvCheck verifies a rendered block: budget, header range == printed+elided lines, every
// printed source line equals the file line its position implies.
func declAdvCheck(t *testing.T, tag string, block []byte, result sem.SearchResult, budget int) {
	t.Helper()
	if budget > 0 && len(block) > budget {
		t.Fatalf("%s: over budget %d > %d", tag, len(block), budget)
	}
	if len(block) == 0 {
		return
	}
	text := strings.TrimSuffix(string(block), "\n")
	parts := strings.Split(text, "\n")
	m := declAdvHeader.FindStringSubmatch(parts[0])
	if m == nil {
		t.Fatalf("%s: unparsable header %q", tag, parts[0])
	}
	if len(parts) == 1 {
		return
	}
	a, _ := strconv.Atoi(m[2])
	b := a
	if m[3] != "" {
		b, _ = strconv.Atoi(m[3])
	}
	// The minimal rung (`path:N *`) names the first printed line and claims no range: its lines are
	// checked like any other rung's, only the range end is not.
	minimal := strings.HasSuffix(parts[0], " *") && !strings.HasPrefix(parts[0], fmt.Sprint(result.Rank)+". ")
	file := strings.Split(result.Snippet, "\n")
	line := a
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, "... ") && strings.HasSuffix(p, " elided") {
			var n int
			fmt.Sscanf(p, "... %d", &n)
			line += n
			continue
		}
		idx := line - result.SnippetStartLine
		if idx < 0 || idx >= len(file) || file[idx] != p {
			t.Fatalf("%s: printed line at implied file line %d does not match file:\n%s", tag, line, block)
		}
		line++
	}
	if !minimal && line-1 != b {
		t.Fatalf("%s: header range %d-%d but printed+elided covers %d-%d:\n%s", tag, a, b, a, line-1, block)
	}
}

// Random fuzz: budget, header-range accuracy, never-locate-less.
func TestAgentBlockAdvFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(303))
	names := []string{"foo", "naïve", "Größe", "processBatch"}
	iters := 3000
	if testing.Short() {
		iters = 400
	}
	for iter := 0; iter < iters; iter++ {
		name := names[rng.Intn(len(names))]
		n := 3 + rng.Intn(80)
		lines := make([]string, n)
		for i := range lines {
			switch rng.Intn(6) {
			case 0:
				lines[i] = "\t" + strings.Repeat("x", rng.Intn(200)) + "\r"
			case 1:
				lines[i] = "  héllo wörld 日本語 " + strings.Repeat("é", rng.Intn(20))
			case 2:
				lines[i] = ""
			default:
				lines[i] = fmt.Sprintf("\tv%d := call(%d)", i, rng.Intn(1000))
			}
		}
		declIdx := rng.Intn(min(4, n))
		if rng.Intn(4) == 0 {
			lines[0] = fmt.Sprintf(`@Named("%s")`, name)
		}
		lines[declIdx] = "func " + name + "(" + strings.Repeat("a int, ", rng.Intn(40)) + ") {"
		var merged []int
		first := 1 + rng.Intn(500)
		for k := 0; k < rng.Intn(3); k++ {
			j := declIdx + 1 + rng.Intn(n-declIdx)
			if j < n {
				lines[j] = "func other" + fmt.Sprint(k) + "() {"
				merged = append(merged, first+j)
			}
		}
		focus := rng.Intn(n)
		res := sem.SearchResult{
			Rank: 1 + rng.Intn(9), Score: 10, FilePath: "a/b/" + strings.Repeat("d", rng.Intn(60)) + ".go",
			StartLine: first, EndLine: first + n - 1, FocusLine: first + focus,
			SnippetStartLine: first, SnippetEndLine: first + n - 1,
			SymbolStartLine: first, SymbolEndLine: first + n - 1, SymbolName: name, QualifiedName: name,
			Signals: []string{"complete-symbol"}, MergedDeclLines: merged,
			Snippet: strings.Join(lines, "\n"),
		}
		for b := 0; b < 20; b++ {
			budget := rng.Intn(3000)
			block := agentSearchPrimaryBlock(res, budget, false)
			tag := fmt.Sprintf("iter %d budget %d", iter, budget)
			declAdvCheck(t, tag, block, res, budget)
			view := agentSearchBlockViewOf(res)
			plain, _, _ := agentSearchFocusWindow(view, budget)
			// never-locate-less for every line the plain window printed that is a decl
			for _, want := range append([]int{first + declIdx}, merged...) {
				if blockShowsLine(plain, lines[want-first]) && !blockShowsLine(block, lines[want-first]) {
					t.Fatalf("%s: lost decl line %d that the plain window showed\nplain:\n%s\nnew:\n%s", tag, want, plain, block)
				}
			}
		}
	}
}

// Annotation argument naming the symbol: which line is printed as "the declaration"?
func TestAgentBlockAdvAnnotationNamesSymbol(t *testing.T) {
	lines := []string{`    @Named("fooBar")`, `    @Inject`, `    public void fooBar(List<Item> items) {`}
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("        step%02d(items);", i))
	}
	lines = append(lines, "    }")
	res := sem.SearchResult{Rank: 1, Score: 10, FilePath: "src/A.java", StartLine: 10, EndLine: 10 + len(lines) - 1,
		FocusLine: 10 + 40, SnippetStartLine: 10, SnippetEndLine: 10 + len(lines) - 1, SymbolStartLine: 10,
		SymbolEndLine: 10 + len(lines) - 1, SymbolName: "fooBar", QualifiedName: "A.fooBar", Snippet: strings.Join(lines, "\n")}
	block := agentSearchPrimaryBlock(res, 400, false)
	t.Logf("\n%s", block)
	if !blockShowsLine(block, lines[2]) {
		t.Errorf("DEFECT: real declaration line not printed; annotation shown instead")
	}
}

// Absorbed decl in its own gap: how many SOURCE body lines does it displace?
func TestAgentBlockAdvAbsorbedDisplacement(t *testing.T) {
	var lines []string
	lines = append(lines, "func survivor(a int) int {")
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("\tv%02d := a * %d", i, i))
	}
	lines = append(lines, "}")
	abs := len(lines)
	lines = append(lines, "func absorbedHelper(b int) int {")
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("\tw%02d := b + %d", i, i))
	}
	lines = append(lines, "}")
	first := 100
	res := sem.SearchResult{Rank: 1, Score: 50, FilePath: "pkg/m.go", StartLine: first, EndLine: first + len(lines) - 1,
		FocusLine: first + abs + 30, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + 21, SymbolName: "survivor", QualifiedName: "survivor",
		Signals: []string{"complete-symbol", "contiguous-span"}, MergedRanks: []int{1, 2},
		MergedDeclLines: []int{first + abs}, Snippet: strings.Join(lines, "\n")}
	worst := 0
	for budget := 100; budget < 1200; budget++ {
		without := res
		without.MergedDeclLines = nil
		a := agentSearchPrimaryBlock(without, budget, false)
		b := agentSearchPrimaryBlock(res, budget, false)
		if plain, _, _ := agentSearchFocusWindow(agentSearchBlockViewOf(res), budget); blockShowsLine(plain, lines[abs]) {
			// The old window showed the absorbed declaration, so it is not ADDED but kept (never
			// locate less), whatever it costs; `without` is then the block that drops it.
			if !blockShowsLine(b, lines[abs]) {
				t.Fatalf("budget %d: the old window showed the absorbed declaration and the block lost it:\n%s", budget, b)
			}
			continue
		}
		if !blockShowsLine(b, lines[abs]) || blockShowsLine(a, lines[abs]) {
			continue
		}
		sa, _ := blockBody(a)
		sb, _ := blockBody(b)
		// body lines = source lines minus decl lines
		loss := (len(sa) - 1) - (len(sb) - 2)
		if loss > worst {
			worst = loss
			t.Logf("budget %d: body lines %d -> %d (loss %d)\n--- without\n%s--- with\n%s", budget, len(sa)-1, len(sb)-2, loss, a, b)
		}
	}
	if worst > 1 {
		t.Errorf("DEFECT: one absorbed decl displaced %d body lines (claim <= 1)", worst)
	}
}

// Declaration longer than the whole block share; tiny budgets.
func TestAgentBlockAdvHugeDecl(t *testing.T) {
	lines := []string{"func huge(" + strings.Repeat("argument int, ", 100) + ") {"}
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf("\tv%02d := 1", i))
	}
	res := sem.SearchResult{Rank: 1, FilePath: "x.go", StartLine: 1, EndLine: len(lines), FocusLine: 40,
		SnippetStartLine: 1, SnippetEndLine: len(lines), SymbolStartLine: 1, SymbolEndLine: len(lines),
		SymbolName: "huge", Snippet: strings.Join(lines, "\n")}
	for budget := 1; budget < 3000; budget++ {
		block := agentSearchPrimaryBlock(res, budget, false)
		declAdvCheck(t, fmt.Sprint(budget), block, res, budget)
	}
}

// Worst-case timing: long snippet, decl far from focus, long name to defeat the header floor.
func TestAgentBlockAdvTiming(t *testing.T) {
	for _, n := range []int{500, 1000, 2000, 5000} {
		for _, longName := range []bool{false, true} {
			name := "bigFunc"
			if longName {
				name = "bigFunc" + strings.Repeat("X", 150)
			}
			lines := []string{"func " + name + "() {"}
			for i := 1; i < n; i++ {
				if i%3 == 0 {
					lines = append(lines, "")
				} else {
					lines = append(lines, fmt.Sprintf("\tv%d := %d", i, i))
				}
			}
			res := sem.SearchResult{Rank: 1, FilePath: "big.go", StartLine: 1, EndLine: n, FocusLine: n - 5,
				SnippetStartLine: 1, SnippetEndLine: n, SymbolStartLine: 1, SymbolEndLine: n, SymbolName: name,
				QualifiedName: name, Snippet: strings.Join(lines, "\n")}
			for _, budget := range []int{2048, 8192} {
				view := agentSearchBlockViewOf(res)
				t0 := time.Now()
				agentSearchFocusWindow(view, budget)
				plainT := time.Since(t0)
				t0 = time.Now()
				agentSearchPrimaryBlock(res, budget, false)
				allT := time.Since(t0)
				t.Logf("n=%d longName=%v budget=%d plain=%v total=%v (decl overhead %v)", n, longName, budget, plainT, allT, allT-plainT)
			}
		}
	}
}

// Realistic: Flask routes merged into one span; decorator string names the function.
func TestAgentBlockAdvFlaskMergedLosesDef(t *testing.T) {
	lines := []string{`@app.route("/login", methods=["GET", "POST"])`, `def login():`}
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf("    form_field_%02d = request.form.get('f%02d')", i, i))
	}
	lines = append(lines, "    return redirect(url_for('index'))", "", "", `@app.route("/logout")`, `def logout():`)
	absorbedDef := len(lines) - 1
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("    session_cleanup_step_%02d(session)", i))
	}
	first := 40
	res := sem.SearchResult{Rank: 1, Score: 50, FilePath: "app/views.py", StartLine: first, EndLine: first + len(lines) - 1,
		FocusLine: first + absorbedDef + 20, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + 14, SymbolName: "login", QualifiedName: "login",
		Signals: []string{"complete-symbol", "contiguous-span"}, MergedRanks: []int{1, 2},
		MergedDeclLines: []int{first + absorbedDef - 1}, Snippet: strings.Join(lines, "\n")}
	lost := 0
	for budget := 100; budget < 3000; budget++ {
		view := agentSearchBlockViewOf(res)
		plain, _, _ := agentSearchFocusWindow(view, budget)
		block := agentSearchPrimaryBlock(res, budget, false)
		for _, want := range []int{1, absorbedDef} {
			if blockShowsLine(plain, lines[want]) && !blockShowsLine(block, lines[want]) {
				if lost == 0 {
					t.Logf("budget %d lost %q\nplain:\n%s\nnew:\n%s", budget, lines[want], plain, block)
				}
				lost++
			}
		}
		if blockShowsLine(block, lines[0]) && !blockShowsLine(block, lines[1]) {
			if lost == 0 {
				t.Logf("budget %d prints decorator but not def:\n%s", budget, block)
			}
		}
	}
	if lost > 0 {
		t.Errorf("DEFECT: %d budgets lose a def line the plain window showed", lost)
	}
}

// Deterministic never-locate-less violation: annotation argument names the symbol, merged span.
func TestAgentBlockAdvJavaNamedMergedLosesDecl(t *testing.T) {
	lines := []string{`    @Named("fooBar")`, `    public void fooBar(List<Item> items) {`}
	for i := 0; i < 8; i++ {
		lines = append(lines, fmt.Sprintf("        step%02d(items);", i))
	}
	lines = append(lines, "    }", "")
	abs := len(lines)
	lines = append(lines, "    public void bazQux(List<Item> items) {")
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("        other%02d(items);", i))
	}
	lines = append(lines, "    }")
	first := 10
	res := sem.SearchResult{Rank: 1, Score: 50, FilePath: "src/A.java", StartLine: first, EndLine: first + len(lines) - 1,
		FocusLine: first + 5, SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1,
		SymbolStartLine: first, SymbolEndLine: first + 10, SymbolName: "fooBar", QualifiedName: "A.fooBar",
		Signals: []string{"complete-symbol", "contiguous-span"}, MergedRanks: []int{1, 2},
		MergedDeclLines: []int{first + abs}, Snippet: strings.Join(lines, "\n")}
	if os.Getenv("DECL_ADV_NOMERGE") != "" {
		res.MergedDeclLines, res.MergedRanks, res.Signals = nil, nil, []string{"complete-symbol"}
	}
	lost := 0
	for budget := 60; budget < 2000; budget++ {
		view := agentSearchBlockViewOf(res)
		plain, _, _ := agentSearchFocusWindow(view, budget)
		block := agentSearchPrimaryBlock(res, budget, false)
		if blockShowsLine(plain, lines[1]) && !blockShowsLine(block, lines[1]) {
			if lost == 0 {
				t.Logf("budget %d\nplain:\n%s\nnew:\n%s", budget, plain, block)
			}
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("DEFECT never-locate-less: %d budgets drop the real declaration the plain window showed", lost)
	}
}
