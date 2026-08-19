package sem

import (
	"slices"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

// nestedSource builds `prefix` + `depth` nested open/close pairs around `inner`,
// breaking a line every 500 columns.
//
// The line breaks are load-bearing, not cosmetic: without them the whole file is
// one enormous line and looksMinified (similarity.go:195, threshold 5000 columns)
// classifies it as a bundled asset, so provider_parallel.go skips parsing and the
// walkers are never reached. Two bytes per nesting level also keeps a very deep
// tree far below defaultMaxParseBytes (provider.go:37, 4 MiB) — the two guards
// that a caller might assume already bound AST depth, and that this input slips
// past on purpose.
//
// The fixtures below are pure in-memory source text, so nothing here depends on
// symlinks, device files, path separators or filesystem case: they behave
// identically on windows-latest and need no runtime.GOOS guard.
func nestedSource(prefix string, open, close byte, depth int, inner, suffix string) string {
	var b strings.Builder
	b.Grow(len(prefix) + 2*depth + len(inner) + len(suffix) + 2*(depth/500) + 2)
	b.WriteString(prefix)
	for i := 0; i < depth; i++ {
		b.WriteByte(open)
		if i%500 == 499 {
			b.WriteByte('\n')
		}
	}
	b.WriteString(inner)
	for i := 0; i < depth; i++ {
		b.WriteByte(close)
		if i%500 == 499 {
			b.WriteByte('\n')
		}
	}
	b.WriteString(suffix)
	return b.String()
}

func assertReachesTheParser(t *testing.T, src string) {
	t.Helper()
	if len(src) >= defaultMaxParseBytes {
		t.Fatalf("fixture must stay under the %d-byte parser input cap, got %d bytes", defaultMaxParseBytes, len(src))
	}
	if looksMinified(src) {
		t.Fatal("fixture must not look minified, or provider_parallel.go skips parsing and never reaches the walkers")
	}
}

// TestParseDeeplyNestedSourceIsBoundedNotFatal pins CWE-674 in walkEntitiesScoped
// (parser.go), which recursed once per AST level with no bound. A source file of
// deeply nested parentheses sits under both the 4 MiB parser input cap and
// looksMinified's column guard, so nothing rejected it, and the walk overflowed
// the goroutine stack. A Go stack overflow is a fatal, unrecoverable process
// abort — recover() cannot catch it — so every verb built on the parser (search,
// symbols, index, snapshot, edges, def, impact, neighbors) died on that input.
//
// Not parallel: the fixture builds a ~1.8 MB source and a ~900k-node tree-sitter
// tree, and the sibling depth tests do the same; running them concurrently
// multiplies peak memory for no coverage gain.
func TestParseDeeplyNestedSourceIsBoundedNotFatal(t *testing.T) {
	const depth = 900_000 // above the ~700k frames that exhaust Go's 1 GB goroutine stack
	src := nestedSource("x = ", '(', ')', depth, "1", "\n")
	assertReachesTheParser(t, src)

	entities, language, status := TreeSitterParser{}.ParseWithStatus("deep.py", src)

	if language != "Python" {
		t.Fatalf("language = %q, want Python", language)
	}
	if !status.ParseError || status.Code != "E_PARSE_DEPTH_EXCEEDED" {
		t.Fatalf("status = %+v, want ParseError with code E_PARSE_DEPTH_EXCEEDED", status)
	}
	if !strings.Contains(status.Detail, "5000") {
		t.Fatalf("detail %q must name the depth limit so the operator can act on it", status.Detail)
	}
	_ = entities
}

// TestParseNestingUnderTheWalkLimitIsNotTruncated guards the other side of the
// bound: real code nested below the limit must parse exactly as before, with no
// partial failure invented. The deepest named-AST nesting measured over 55,900
// real source files across 21 languages was 464, so nothing legitimate comes
// near the 5000-level limit.
func TestParseNestingUnderTheWalkLimitIsNotTruncated(t *testing.T) {
	t.Parallel()
	src := nestedSource("def outer():\n    return ", '(', ')', 4900, "1", "\n") // below the 5000-level limit
	assertReachesTheParser(t, src)

	entities, _, status := TreeSitterParser{}.ParseWithStatus("shallow.py", src)

	if status.ParseError {
		t.Fatalf("nesting under the limit must not report a failure, got %+v", status)
	}
	if !hasEntityNamed(entities, "outer") {
		t.Fatalf("outer must still be extracted, got %v", entityNames(entities))
	}
}

// TestParseTruncatedWalkStillEmitsShallowSymbols pins that the bound truncates
// rather than discards: declarations above the limit keep their symbols, so a
// generated file with one pathological expression stays searchable.
func TestParseTruncatedWalkStillEmitsShallowSymbols(t *testing.T) {
	t.Parallel()
	src := nestedSource("def outer():\n    return ", '(', ')', 6000, "1", "\n") // above the 5000-level limit
	assertReachesTheParser(t, src)

	entities, _, status := TreeSitterParser{}.ParseWithStatus("truncated.py", src)

	if status.Code != "E_PARSE_DEPTH_EXCEEDED" {
		t.Fatalf("status = %+v, want code E_PARSE_DEPTH_EXCEEDED", status)
	}
	if !hasEntityNamed(entities, "outer") {
		t.Fatalf("symbols above the depth limit must survive truncation, got %v", entityNames(entities))
	}
}

// TestInitializerTypeBodiesWalkIsBudgeted isolates the second unbounded walker.
// walkEntitiesScoped stops at a class field (fieldEntities returns ok and the
// walk returns without descending), so its own budget is never spent here: the
// only recursion into the deep initializer is initializerTypeBodies' inner walk,
// which was bounded on the node types it collects, never on depth. Budgeting
// walkEntitiesScoped alone leaves this input walking unbounded and silently
// reports a clean parse.
func TestInitializerTypeBodiesWalkIsBudgeted(t *testing.T) {
	t.Parallel()
	src := nestedSource("class C {\n  x = ", '(', ')', 6000, "1", ";\n}\n") // above the 5000-level limit
	assertReachesTheParser(t, src)

	entities, language, status := TreeSitterParser{}.ParseWithStatus("field.ts", src)

	if language != "TypeScript" {
		t.Fatalf("language = %q, want TypeScript", language)
	}
	if status.Code != "E_PARSE_DEPTH_EXCEEDED" {
		t.Fatalf("status = %+v, want code E_PARSE_DEPTH_EXCEEDED", status)
	}
	if !hasEntityNamed(entities, "C") {
		t.Fatalf("the class above the deep initializer must still be extracted, got %v", entityNames(entities))
	}
}

// TestCollectParseErrorDetailsIsBoundedNotFatal isolates the third unbounded
// walker. collectParseErrorDetails runs on every HasError file — exactly the
// adversarial input class — and was bounded on the number of RESULTS it
// collects, never on depth, so a tree whose only error nodes sit at the bottom
// made it descend every level. It overflows at ~2M levels, which fits in a
// 4,008,006-byte file, still under the 4 MiB parser input cap.
//
// The call is direct because the shape that reaches it end-to-end is
// language-specific, while the defect is not: any grammar that nests this deep
// and reports an error drives the same walk from parser.go:313 (the YAML branch)
// and parser.go:398.
//
// Not parallel: builds a ~4 MB source and a ~2M-node tree.
func TestCollectParseErrorDetailsIsBoundedNotFatal(t *testing.T) {
	const depth = 2_000_000
	src := nestedSource("x = ", '(', ')', depth, "$", "\n")
	assertReachesTheParser(t, src)

	spec, ok := languageForContent("deep.py", src)
	if !ok || spec.grammar == nil {
		t.Fatal("python grammar must be available")
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(spec.grammar)
	tree := parser.Parse(nil, []byte(src))
	if tree == nil {
		t.Fatal("parse returned no tree")
	}
	defer tree.Close()
	root := tree.RootNode()
	if !root.HasError() {
		t.Fatal("fixture must produce error nodes, or the detail walk is never driven")
	}

	// Must return instead of aborting the process.
	collectParseErrorDetails(root, []byte(src), 5, 0)
}

// TestCollectParseErrorDetailsStillReportsShallowErrors pins that the depth bound
// did not silence the diagnostic it protects.
func TestCollectParseErrorDetailsStillReportsShallowErrors(t *testing.T) {
	t.Parallel()
	const src = "def f(:\n    return 1\n"
	spec, ok := languageForContent("broken.py", src)
	if !ok || spec.grammar == nil {
		t.Fatal("python grammar must be available")
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(spec.grammar)
	tree := parser.Parse(nil, []byte(src))
	defer tree.Close()

	details := collectParseErrorDetails(tree.RootNode(), []byte(src), 5, 0)

	if len(details) == 0 {
		t.Fatal("a shallow syntax error must still produce a detail line")
	}
}

// TestSnapshotReportsDepthTruncationWithoutDegradingCompleteness pins the whole
// thread end to end: the parser's status becomes a PartialFailure with the same
// shape E_PARSE_TIMEOUT gets, the file record and the rest of the repo survive,
// and the new code is registered in intentionalSkipFailureCodes so one
// pathological file does not drag the repo's completeness to "degraded"
// (completenessLevel's `failures*4 > files` fall-through returns "degraded" for a
// single counted failure in a small repo).
func TestSnapshotReportsDepthTruncationWithoutDegradingCompleteness(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "deep.py", nestedSource("def outer():\n    return ", '(', ')', 6000, "1", "\n"))
	writeFile(t, repo, "ordinary.py", "def ordinary():\n    return 1\n")

	var symbols []string
	var summary SnapshotSummary
	err := StreamSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{}, func(rec any) error {
		switch r := rec.(type) {
		case SymbolRecord:
			symbols = append(symbols, r.Name)
		case SnapshotSummary:
			summary = r
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var failure *PartialFailure
	for i := range summary.PartialFailures {
		if summary.PartialFailures[i].Code == "E_PARSE_DEPTH_EXCEEDED" {
			failure = &summary.PartialFailures[i]
		}
	}
	if failure == nil {
		t.Fatalf("no E_PARSE_DEPTH_EXCEEDED partial failure; got %+v", summary.PartialFailures)
	}
	if failure.Severity != "warning" {
		t.Fatalf("severity = %q, want warning (matching the other parser codes)", failure.Severity)
	}
	if failure.FilePath != "deep.py" {
		t.Fatalf("file_path = %q, want deep.py", failure.FilePath)
	}
	if failure.EffectOnCompleteness == "" || failure.Detail == "" {
		t.Fatalf("effect and detail must be populated, got %+v", *failure)
	}
	if summary.Stats.CompletenessLevel != "ok" {
		t.Fatalf("completeness_level = %q, want ok: a depth-truncated file is a policy skip, not a coverage gap", summary.Stats.CompletenessLevel)
	}
	for _, want := range []string{"outer", "ordinary"} {
		if !slices.Contains(symbols, want) {
			t.Fatalf("symbols = %v, want %q still emitted", symbols, want)
		}
	}
}

// TestDepthTruncationIsExcludedFromCompletenessCount pins why
// E_PARSE_DEPTH_EXCEEDED is registered in intentionalSkipFailureCodes: if it were
// counted, one truncated file would take a 100-file repo to "degraded" through
// completenessLevel's fall-through, and a small repo to "unsafe".
func TestDepthTruncationIsExcludedFromCompletenessCount(t *testing.T) {
	t.Parallel()
	if got := completenessFailureCount([]PartialFailure{{Code: "E_PARSE_DEPTH_EXCEEDED"}}); got != 0 {
		t.Fatalf("completenessFailureCount = %d, want 0: a depth-truncated file is a policy skip", got)
	}
	if got := completenessLevel(1, 100, 100, 5); got != "degraded" {
		t.Fatalf("a counted failure in a 100-file repo yields %q, want degraded (the outcome the allow-list entry avoids)", got)
	}
	if got := completenessLevel(1, 2, 2, 1); got != "unsafe" {
		t.Fatalf("a counted failure in a 2-file repo yields %q, want unsafe", got)
	}
}

func entityNames(entities []Entity) []string {
	names := make([]string, 0, len(entities))
	for _, entity := range entities {
		names = append(names, entity.Name)
	}
	return names
}

func hasEntityNamed(entities []Entity, name string) bool {
	for _, entity := range entities {
		if entity.Name == name {
			return true
		}
	}
	return false
}
