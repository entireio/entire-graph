package sem

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
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

// Every assertion in this file is on observable behaviour, never on the
// ParseStatus.Partial field this change adds, so the file compiles unmodified
// against the parent commit and each pre-fix failure is a RUNTIME failure —
// a fatal abort, a suppressed delta, or a miscounted completeness — rather than
// a build error.
//
// A Go stack overflow is a FATAL process abort: recover() cannot catch it and
// the test binary dies with no result, so a test that merely calls the walker
// cannot report the defect it exists to pin. Each depth test below therefore
// re-executes this same test binary as a child process running only itself,
// and the parent asserts on the child's exit status.
//
// depthChildEnv both tells the child it is the child and stops the parent's own
// `go test` invocation from recursing.
const depthChildEnv = "EG_SEM_DEPTH_WALK_CHILD"

// depthChildMaxStack lowers the child's goroutine stack ceiling from Go's 1 GiB
// default. The defect is unbounded recursion, not a particular byte count: an
// unguarded walker overflows whatever ceiling it is given, and a guarded one
// stops at maxParseWalkDepth (~3.8 MB of frames) under any ceiling above that.
// Lowering it makes each abort reproducible with a ~1 MB fixture and a few
// hundred MB of RSS instead of the full-scale numbers measured against the
// parent commit at the 1 GiB default:
//
//	firstNameDescendant                6,000,000 levels / 12,024,026 bytes
//	unwrapRustItemWrapperMacros.func1  5,000,000 levels / 10,020,051 bytes
//
// Both of those exceed defaultMaxParseBytes (4 MiB), which the provider applies
// but Analyze does not: AnalyzeGitRange hands git blob content straight to
// ParseWithStatus with no size cap (analyze.go), so `graph diff` reaches those
// sizes on a real repository. ProviderSnapshotOptions.MaxParseBytes < 0 removes
// the cap on the provider path too (provider.go).
const depthChildMaxStack = 24 << 20

// deepWalkDepth is the nesting used by every child below: deep enough that an
// unbounded walker overflows depthChildMaxStack with margin (each of these
// walkers was measured aborting at 200,000 levels against a 16 MiB ceiling),
// small enough that the fixture stays a couple of MB.
const deepWalkDepth = 500_000

// inDepthChild reports whether this process is the re-executed child, and if so
// applies the lowered stack ceiling.
func inDepthChild() bool {
	if os.Getenv(depthChildEnv) != "1" {
		return false
	}
	debug.SetMaxStack(depthChildMaxStack)
	return true
}

// runDepthChild re-runs `name` in a child process and fails if the child does
// not exit 0. A fatal stack overflow exits 2 with `fatal error: stack overflow`
// on stderr, which is reported verbatim so the failure names the walker.
func runDepthChild(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.v=true", "-test.count=1")
	cmd.Env = append(os.Environ(), depthChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	t.Fatalf("child process running %s did not complete: %v\n--- child output ---\n%s", name, err, childFailureExcerpt(string(out)))
}

// childFailureExcerpt keeps the child's diagnosis without its 1000-frame dump:
// everything up to the fatal line, then the first frames belonging to this
// package. A stack-overflow trace begins with runtime frames, so those package
// frames are what name the walker that recursed.
func childFailureExcerpt(out string) string {
	const framePrefix = "github.com/entireio/entire-graph/internal/sem."
	var head, frames []string
	fatal := false
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !fatal {
			head = append(head, line)
			if strings.HasPrefix(line, "fatal error:") || strings.HasPrefix(line, "panic:") {
				fatal = true
			}
			if len(head) > 8 && !fatal {
				head = head[:8]
				break
			}
			continue
		}
		if strings.HasPrefix(line, framePrefix) && len(frames) < 3 {
			frames = append(frames, line)
		}
	}
	if len(frames) > 0 {
		head = append(head, "...", "recursing frames:")
		head = append(head, frames...)
	}
	return strings.Join(head, "\n")
}

// parseInChild runs ParseWithStatus on src and requires a depth-truncation
// status. Reaching the assertion at all is the point: on the parent commit the
// process is gone before it.
func parseInChild(t *testing.T, path, src string) []Entity {
	t.Helper()
	assertReachesTheParser(t, src)
	entities, _, status := TreeSitterParser{}.ParseWithStatus(path, src)
	if status.Code != "E_PARSE_DEPTH_EXCEEDED" {
		t.Fatalf("status = %+v, want code E_PARSE_DEPTH_EXCEEDED", status)
	}
	fmt.Printf("child ok: %s, %d bytes, %d entities\n", path, len(src), len(entities))
	return entities
}

// TestParseDeeplyNestedSourceIsBoundedNotFatal pins CWE-674 in walkEntitiesScoped
// (parser.go), which recursed once per AST level with no bound. A source file of
// deeply nested parentheses sits under both the 4 MiB parser input cap and
// looksMinified's column guard, so nothing rejected it, and the walk overflowed
// the goroutine stack, killing every verb built on the parser (search, symbols,
// index, snapshot, edges, def, impact, neighbors).
func TestParseDeeplyNestedSourceIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestParseDeeplyNestedSourceIsBoundedNotFatal")
		return
	}
	src := nestedSource("x = ", '(', ')', deepWalkDepth, "1", "\n")
	parseInChild(t, "deep.py", src)
}

// TestNameDescentIsBoundedNotFatal pins the walkers reached from entityFromNode
// BEFORE walkEntitiesScoped descends: firstNameDescendant and, on the same C /
// Objective-C path, firstDescendantOfType. Both carry their own recursion over
// the declaration subtree while the entity walk is still one level down, so the
// entity walk's budget cannot protect them and hostile input aborted the process
// without ever reaching that budget. Reproduced against the parent commit with
// this exact shape.
func TestNameDescentIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestNameDescentIsBoundedNotFatal")
		return
	}
	// A deeply parenthesized C function declarator: `int ((((f)))) (void) {…}`.
	src := nestedSource("int ", '(', ')', deepWalkDepth, "f", "(void) { return 1; }\n")
	parseInChild(t, "deep.c", src)
}

// TestRustMacroUnwrapIsBoundedNotFatal pins the walk inside
// unwrapRustItemWrapperMacros, which runs as language PREPROCESSING before the
// guarded entity walk, over its own parse tree. The `cfg_*! {` hint that arms it
// is a regex over the whole file and need not be anywhere near the nesting, so a
// single tokio-shaped line at the top of a deeply nested Rust file aborted the
// process before any guarded walk ran.
func TestRustMacroUnwrapIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestRustMacroUnwrapIsBoundedNotFatal")
		return
	}
	src := "cfg_net! {\n    pub struct S;\n}\n" +
		nestedSource("fn f() -> i32 { ", '(', ')', deepWalkDepth, "1", " }\n")
	parseInChild(t, "deep.rs", src)
}

// TestRAssignmentChainIsBoundedNotFatal pins rAssignedValueKind, which follows an
// R chained assignment (`a <- b <- function() 1`) to its terminal value. The
// chain length is written by the source, and entityFromNode calls this on the
// whole subtree before the entity walk descends into it.
func TestRAssignmentChainIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestRAssignmentChainIsBoundedNotFatal")
		return
	}
	var b strings.Builder
	for i := 0; i < deepWalkDepth; i++ {
		b.WriteString("a <- ")
		if i%500 == 499 {
			b.WriteString("\n")
		}
	}
	b.WriteString("function() 1\n")
	parseInChild(t, "chain.R", b.String())
}

// TestJSPatternBindingsAreBoundedNotFatal pins jsPatternBindingNames
// (js_scopes.go), reached from walkEntitiesScoped through jsEntityParameterNames
// whenever a JS/TS callable is extracted. Object and array patterns nest as
// deeply as the source writes them, so a destructuring parameter list was a
// second, independent way to exhaust the stack during a parse.
func TestJSPatternBindingsAreBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestJSPatternBindingsAreBoundedNotFatal")
		return
	}
	var b strings.Builder
	b.WriteString("function g(")
	for i := 0; i < deepWalkDepth; i++ {
		b.WriteString("{a:")
		if i%500 == 499 {
			b.WriteString("\n")
		}
	}
	b.WriteString("z")
	for i := 0; i < deepWalkDepth; i++ {
		b.WriteString("}")
		if i%500 == 499 {
			b.WriteString("\n")
		}
	}
	b.WriteString(") { return 1; }\n")
	parseInChild(t, "pattern.ts", b.String())
}

// deepRelationWalkDepth is the nesting used by the two relation-phase children.
// The relation walkers carry smaller frames than the entity walkers, so they
// need more levels to overflow the same ceiling; measured against the parent
// commit, (*jsScopeWalker).walk survives 100,000 and aborts by 400,000 at a
// 16 MiB ceiling.
const deepRelationWalkDepth = 1_500_000

// TestJSScopeWalkerIsBoundedNotFatal pins the relation-phase scope walker.
// Relation construction REPARSES the file and walks its own tree after the
// entity phase has returned, so the entity walk's budget never reaches it: a
// deeply nested .js/.ts file aborted the process during relation construction
// even though the entity phase had already truncated cleanly and reported it.
//
// Verified against the parent commit through the provider itself, at Go's
// default 1 GiB ceiling: a snapshot over an 8 MB .js file of 4,000,000 nested
// parentheses dies with `fatal error: stack overflow`, frames in
// (*jsScopeWalker).walk, exit 2, 3.4 GB RSS. Under the provider's default 4 MiB
// input cap the largest admissible file survives; the sizes that reach this
// walker come from ProviderSnapshotOptions.MaxParseBytes < 0, the documented
// escape hatch that removes the cap.
func TestJSScopeWalkerIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestJSScopeWalkerIsBoundedNotFatal")
		return
	}
	src := nestedSource("const x = ", '(', ')', deepRelationWalkDepth, "1", ";\n")
	if looksMinified(src) {
		t.Fatal("fixture must not look minified, or the scope scan is never reached")
	}
	// Must return instead of aborting the process.
	state, err := newJSScanState("deep.js", src)
	if err != nil {
		t.Fatalf("scope scan must degrade, not fail: %v", err)
	}
	if !state.parsed {
		t.Fatal("the scope parse itself must still succeed; only the walk is truncated")
	}
	fmt.Printf("child ok: scope scan returned, %d bytes\n", len(src))
}

// TestJSMemberChainIsBoundedNotFatal pins the recursive member-chain helper the
// scope walker calls. A member chain is as long as the source writes it, and
// walk hands jsMemberChainParts the whole chain from its head before descending
// into it, so that recursion is independent of walk's and reaches the stack
// first — measured against the parent commit, it aborts at 100,000 links where
// walk itself still survives.
func TestJSMemberChainIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestJSMemberChainIsBoundedNotFatal")
		return
	}
	var b strings.Builder
	b.WriteString("a")
	for i := 0; i < deepRelationWalkDepth; i++ {
		b.WriteString(".b")
		if i%250 == 249 {
			b.WriteString("\n")
		}
	}
	b.WriteString("(1);\n")
	src := b.String()
	if looksMinified(src) {
		t.Fatal("fixture must not look minified, or the scope scan is never reached")
	}
	state, err := newJSScanState("chain.js", src)
	if err != nil {
		t.Fatalf("scope scan must degrade, not fail: %v", err)
	}
	if !state.parsed {
		t.Fatal("the scope parse itself must still succeed; only the walk is truncated")
	}
	fmt.Printf("child ok: member chain scan returned, %d bytes\n", len(src))
}

// TestSnapshotOverDeeplyNestedJSCompletes is a guard, not a fix pin: it runs the
// whole provider — both phases, including relation construction — over a .js
// file deep enough to truncate, and requires the run to finish and say so. It
// passes on the parent commit too at this size (the abort there needs the
// uncapped path); what it stops is a future regression that reintroduces the
// crash within the default cap.
func TestSnapshotOverDeeplyNestedJSCompletes(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "deep.js", nestedSource("const x = ", '(', ')', 6000, "1", ";\n"))
	writeFile(t, repo, "ordinary.js", "export function ordinary() { return 1; }\n")

	var summary SnapshotSummary
	err := StreamSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{}, func(rec any) error {
		if s, ok := rec.(SnapshotSummary); ok {
			summary = s
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range summary.PartialFailures {
		if f.Code == "E_PARSE_DEPTH_EXCEEDED" && f.FilePath == "deep.js" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a truncated .js must be reported; got %+v", summary.PartialFailures)
	}
	if summary.Stats.CompletenessLevel == "ok" {
		t.Fatal("a repo whose file was parsed incompletely must not report ok")
	}
}

// TestCollectParseErrorDetailsIsBoundedNotFatal isolates the error-detail walk.
// It runs on every HasError file — exactly the adversarial input class — and was
// bounded on the number of RESULTS it collects, never on depth, so a tree whose
// only error nodes sit at the bottom made it descend every level.
//
// The call is direct because the shape that reaches it end to end is
// language-specific, while the defect is not: any grammar that nests this deep
// and reports an error drives the same walk from parser.go's YAML branch and
// from parseErrorDetailWithLineOffset.
func TestCollectParseErrorDetailsIsBoundedNotFatal(t *testing.T) {
	if !inDepthChild() {
		runDepthChild(t, "TestCollectParseErrorDetailsIsBoundedNotFatal")
		return
	}
	src := nestedSource("x = ", '(', ')', deepWalkDepth, "$", "\n")
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
	fmt.Println("child ok: collectParseErrorDetails returned")
}

// TestParseNestingUnderTheWalkLimitIsNotTruncated guards the other side of the
// bound: real code nested below the limit must parse exactly as before, with no
// partial failure invented. The deepest named-AST nesting measured over 56,261
// real source files was 463, so nothing legitimate comes near the 5000-level
// limit (see maxParseWalkDepth in parser.go for the per-walker breakdown).
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

// TestNameDescentUnderTheWalkLimitStillNamesEntities guards the added bounds the
// same way: a C declarator nested below the limit must still resolve its name
// through firstNameDescendant / firstDescendantOfType.
func TestNameDescentUnderTheWalkLimitStillNamesEntities(t *testing.T) {
	t.Parallel()
	src := nestedSource("int ", '(', ')', 400, "f", "(void) { return 1; }\n") // below the 5000-level limit
	assertReachesTheParser(t, src)

	entities, _, status := TreeSitterParser{}.ParseWithStatus("shallow.c", src)

	if status.ParseError {
		t.Fatalf("nesting under the limit must not report a failure, got %+v", status)
	}
	if !hasEntityNamed(entities, "f") {
		t.Fatalf("f must still be named through the bounded descent, got %v", entityNames(entities))
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

// TestInitializerTypeBodiesWalkIsBudgeted isolates the walk that descends a
// field's initializer. walkEntitiesScoped stops at a class field (fieldEntities
// returns ok and the walk returns without descending), so its own budget is
// never spent here: the only recursion into the deep initializer is
// initializerTypeBodies' inner walk, which was bounded on the node types it
// collects, never on depth.
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

// TestDeepNameDescentStillReportsTruncation pins the claim the name-descent
// bounds rely on. firstNameDescendant and firstDescendantOfType truncate
// SILENTLY — they return "no name found" rather than threading a flag back — so
// the file would be silently shortened if nothing else noticed. Nothing else has
// to: any subtree deep enough to exhaust their budget is also descended by
// walkEntitiesScoped (or initializerTypeBodies), whose guard sets the status.
// Without that, this file would report a clean parse while losing its only
// declaration.
func TestDeepNameDescentStillReportsTruncation(t *testing.T) {
	t.Parallel()
	src := nestedSource("int ", '(', ')', 6000, "f", "(void) { return 1; }\n") // above the 5000-level limit
	assertReachesTheParser(t, src)

	_, _, status := TreeSitterParser{}.ParseWithStatus("truncated.c", src)

	if status.Code != "E_PARSE_DEPTH_EXCEEDED" {
		t.Fatalf("status = %+v, want E_PARSE_DEPTH_EXCEEDED even though the name descent truncated silently", status)
	}
}

// TestSnapshotReportsDepthTruncationAndCountsItAgainstCompleteness pins the
// whole thread end to end: the parser's status becomes a PartialFailure with the
// same shape E_PARSE_TIMEOUT gets, the file record and the rest of the repo
// survive, and — unlike E_FILE_TOO_LARGE / E_MINIFIED — the truncation COUNTS,
// because the graph parsed the file and dropped declarations from it. A repo of
// files that each carry a shallow symbol above deeply nested declarations must
// not report "ok".
func TestSnapshotReportsDepthTruncationAndCountsItAgainstCompleteness(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "deep.py", nestedSource("def outer():\n    return ", '(', ')', 6000, "1", "\n"))
	for i := 0; i < 9; i++ {
		writeFile(t, repo, fmt.Sprintf("ordinary%d.py", i), fmt.Sprintf("def ordinary%d():\n    return 1\n", i))
	}

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
	if summary.Stats.CompletenessLevel != "degraded" {
		t.Fatalf("completeness_level = %q, want degraded: one of ten files was parsed incompletely", summary.Stats.CompletenessLevel)
	}
	for _, want := range []string{"outer", "ordinary0"} {
		if !slices.Contains(symbols, want) {
			t.Fatalf("symbols = %v, want %q still emitted", symbols, want)
		}
	}
}

// TestDepthTruncationCountsTowardCompleteness pins why E_PARSE_DEPTH_EXCEEDED is
// NOT in intentionalSkipFailureCodes. That map is for files the parser never
// opened (too large, minified), where there is no gap in understanding to
// report. A depth-truncated file was opened, parsed, and had declarations
// dropped, so the graph is genuinely incomplete for it and completeness must
// count it — otherwise a repo whose every file truncates still reports "ok".
func TestDepthTruncationCountsTowardCompleteness(t *testing.T) {
	t.Parallel()
	if got := completenessFailureCount([]PartialFailure{{Code: "E_PARSE_DEPTH_EXCEEDED"}}); got != 1 {
		t.Fatalf("completenessFailureCount = %d, want 1: a depth-truncated file is a real coverage gap", got)
	}
	// The skips it must not be confused with.
	skips := []PartialFailure{{Code: "E_FILE_TOO_LARGE"}, {Code: "E_MINIFIED"}}
	if got := completenessFailureCount(skips); got != 0 {
		t.Fatalf("completenessFailureCount(skips) = %d, want 0", got)
	}
	if got := completenessLevel(1, 100, 100, 5); got != "degraded" {
		t.Fatalf("one counted failure in a 100-file repo yields %q, want degraded", got)
	}
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

// TestAnalyzeKeepsDeltaForDepthTruncatedFile pins the diff side of the same
// model. A depth-truncated walk yields ParseError with ZERO entities, which is
// the shape Analyze treats as a TOTAL failure and answers by suppressing the
// file's delta entirely. That is wrong here: the tree parsed and the walk
// stopped, so zero entities means "no declarations above the limit", not "no
// signal". ParseStatus.Partial keeps the delta — a Python file whose only
// statement is a deeply parenthesized module-level assignment still reports its
// module-scope change, as it did before any depth limit existed — while the
// warning still tells the caller the comparison was incomplete.
func TestAnalyzeKeepsDeltaForDepthTruncatedFile(t *testing.T) {
	t.Parallel()
	const deep = 6000 // above the 5000-level limit
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "mod.py", nestedSource("x = ", '(', ')', deep, "1", "\n"))
	}, func(r string) {
		write(t, r, "mod.py", nestedSource("x = ", '(', ')', deep, "2", "\n"))
	})

	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}

	var moduleChange bool
	for _, f := range res.Files {
		if f.Path != "mod.py" {
			continue
		}
		for _, c := range f.Changes {
			if c.Kind == moduleKind {
				moduleChange = true
			}
		}
	}
	if !moduleChange {
		t.Fatalf("depth truncation must not suppress the file delta; got files %+v warnings %+v", res.Files, res.Warnings)
	}

	var warning *ProviderWarning
	for i := range res.Warnings {
		if res.Warnings[i].Code == "E_PARSE_DEPTH_EXCEEDED" {
			warning = &res.Warnings[i]
		}
	}
	if warning == nil {
		t.Fatalf("a kept-but-incomplete diff must still be flagged; got %+v", res.Warnings)
	}
	if strings.Contains(warning.EffectOnCompleteness, "suppressed") {
		t.Fatalf("effect = %q, must not claim the diff was suppressed", warning.EffectOnCompleteness)
	}
}

// TestDeepAndMalformedIsTotalNotPartial pins the interaction between the two
// status paths. A file that is BOTH deeper than the walk limit AND syntactically
// broken must report a TOTAL parse error, not a partial result: truncation alone
// yields fewer entities that are still correct, but a malformed tree yields
// entities that may be WRONG, so the caller must stay free to suppress the file.
// Marking that case partial let AnalyzeGitRange diff against a zero-entity
// malformed side and emit every symbol on the other side as a phantom removal.
func TestDeepAndMalformedIsTotalNotPartial(t *testing.T) {
	t.Parallel()
	// 6000 levels is above the walk limit; the `$` makes the tree HasError.
	broken := nestedSource("x = ", '(', ')', 6000, "$", "\n")
	assertReachesTheParser(t, broken)
	_, _, status := TreeSitterParser{}.ParseWithStatus("mod.py", broken)
	if status.Code != "E_PARSE_ERROR" {
		t.Fatalf("status = %+v, want E_PARSE_ERROR: a malformed tree dominates depth truncation", status)
	}

	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "mod.py", "def alpha():\n    return 1\n\ndef beta():\n    return 2\n")
	}, func(r string) {
		write(t, r, "mod.py", broken)
	})
	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		for _, c := range f.Changes {
			if c.Type == "removed" && (c.Name == "alpha" || c.Name == "beta") {
				t.Fatalf("phantom %s %q from a malformed zero-entity side; delta must be suppressed: %+v", c.Type, c.Name, res.Files)
			}
		}
	}
	if pfParseWarning(res, "mod.py") == nil {
		t.Fatalf("expected a suppression warning, got %+v", res.Warnings)
	}
}

// TestDeepButWellFormedStaysPartial is the other half of the pair: without the
// syntax error the same depth still degrades rather than suppresses, so the
// fix above did not collapse partial results back into total failures.
func TestDeepButWellFormedStaysPartial(t *testing.T) {
	t.Parallel()
	clean := nestedSource("x = ", '(', ')', 6000, "1", "\n")
	assertReachesTheParser(t, clean)
	_, _, status := TreeSitterParser{}.ParseWithStatus("mod.py", clean)
	if status.Code != "E_PARSE_DEPTH_EXCEEDED" {
		t.Fatalf("status = %+v, want E_PARSE_DEPTH_EXCEEDED for a clean but deep tree", status)
	}
}

// TestAnalyzeStillSuppressesTotalParseFailure guards the other side: a genuinely
// unparseable file is not Partial, so it keeps the existing total-failure
// behaviour of suppressing the delta.
func TestAnalyzeStillSuppressesTotalParseFailure(t *testing.T) {
	t.Parallel()
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "seed.txt", "seed\n")
	}, func(r string) {
		write(t, r, "svc.ts", pfBrokenTS)
	})

	res, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if f.Path == "svc.ts" {
			t.Fatalf("a total parse failure must still suppress the delta, got %+v", f)
		}
	}
	if pfParseWarning(res, "svc.ts") == nil {
		t.Fatalf("expected a suppression warning, got %+v", res.Warnings)
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
