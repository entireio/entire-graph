package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The four dense exact-name sweeps are each divided between several top-level tests, so that CI
// can run the parts on different shards. This file is the one declaration of that division:
// exactNameSweepParts maps every such top-level test to the part of its family it sweeps, each
// test takes its work from that map by its own name (exactNameRunSweepPart), and
// TestAgentSearchExactNameSweepPartsCoverEveryFamily fails unless the parts of every family are a
// disjoint, complete cover of the family and the map's keys are exactly the top-level tests that
// run a part. Dropping a test, a language, a layout, a query or a budget residue therefore fails.

const (
	exactNameAnchorsFamily     = "anchors"        // signature sweep: every language case
	exactNameVerifyFamily      = "verify"         // VERIFY sweep: two ten-row fixtures, every language case
	exactNameNonExactFamily    = "non-exact"      // byte-identity sweep: two fixtures x every query
	exactNameLocatesLessFamily = "locates-less"   // never-locates-less sweep: every layout
	exactNameTenExactRows      = "ten-exact-rows" // VERIFY fixture
	exactNameLongBodies        = "long-bodies"    // VERIFY fixture
	exactNameExactFixture      = "exact-name"     // non-exact fixture
	exactNameTimingFixture     = "timing"         // non-exact fixture
)

// exactNameSweepPart is what one top-level test sweeps. Which fields are meaningful depends on
// the family: langs for anchors; langs or a fixture with a budget residue class for verify; a
// fixture and queries for non-exact; layouts for locates-less.
type exactNameSweepPart struct {
	family           string
	langs            []string
	fixture          string
	residue, modulus int
	queries          []string
	layouts          [][]int
}

func exactNameSweepParts() map[string]exactNameSweepPart {
	anchors := func(langs ...string) exactNameSweepPart {
		return exactNameSweepPart{family: exactNameAnchorsFamily, langs: langs}
	}
	verifyLangs := func(langs ...string) exactNameSweepPart {
		return exactNameSweepPart{family: exactNameVerifyFamily, langs: langs}
	}
	verifyFixture := func(fixture string, residue, modulus int) exactNameSweepPart {
		return exactNameSweepPart{family: exactNameVerifyFamily, fixture: fixture, residue: residue, modulus: modulus}
	}
	nonExact := func(fixture string, queries []string) exactNameSweepPart {
		return exactNameSweepPart{family: exactNameNonExactFamily, fixture: fixture, queries: queries}
	}
	locatesLess := func(layouts ...[]int) exactNameSweepPart {
		return exactNameSweepPart{family: exactNameLocatesLessFamily, layouts: layouts}
	}
	return map[string]exactNameSweepPart{
		"TestAgentSearchExactNameAnchorsOnTheSignatureGo":         anchors("go"),
		"TestAgentSearchExactNameAnchorsOnTheSignatureJava":       anchors("java"),
		"TestAgentSearchExactNameAnchorsOnTheSignatureCSharp":     anchors("cs"),
		"TestAgentSearchExactNameAnchorsOnTheSignaturePython":     anchors("py"),
		"TestAgentSearchExactNameAnchorsOnTheSignatureRust":       anchors("rs"),
		"TestAgentSearchExactNameAnchorsOnTheSignatureTypeScript": anchors("ts"),

		"TestAgentSearchExactNameNeverDropsVerifyTenExactRows":          verifyFixture(exactNameTenExactRows, 0, 1),
		"TestAgentSearchExactNameNeverDropsVerifyLongBodiesOddBudgets":  verifyFixture(exactNameLongBodies, 1, 2),
		"TestAgentSearchExactNameNeverDropsVerifyLongBodiesEvenBudgets": verifyFixture(exactNameLongBodies, 0, 2),
		"TestAgentSearchExactNameNeverDropsVerifyGo":                    verifyLangs("go"),
		"TestAgentSearchExactNameNeverDropsVerifyJava":                  verifyLangs("java"),
		"TestAgentSearchExactNameNeverDropsVerifyCSharp":                verifyLangs("cs"),
		"TestAgentSearchExactNameNeverDropsVerifyPython":                verifyLangs("py"),
		"TestAgentSearchExactNameNeverDropsVerifyRust":                  verifyLangs("rs"),
		"TestAgentSearchExactNameNeverDropsVerifyTypeScript":            verifyLangs("ts"),

		"TestAgentSearchNonExactQueriesAreByteIdenticalOnExactNameFixturePartialAndCaseQueries":      nonExact(exactNameExactFixture, []string{"resolveMessage", "ResolveMessageRef"}),
		"TestAgentSearchNonExactQueriesAreByteIdenticalOnExactNameFixtureQualifiedAndUnknownQueries": nonExact(exactNameExactFixture, []string{"recv3.Helper3", "UnknownIdentifier"}),
		"TestAgentSearchNonExactQueriesAreByteIdenticalOnExactNameFixtureOtherQueries":               nonExact(exactNameExactFixture, exactNameNonExactSparseQueries()),
		"TestAgentSearchNonExactQueriesAreByteIdenticalOnTimingFixture":                              nonExact(exactNameTimingFixture, exactNameNonExactQueries),

		"TestAgentSearchExactNameNeverLocatesLessOneExactRow":    locatesLess([]int{1}, []int{2}, []int{10}),
		"TestAgentSearchExactNameNeverLocatesLessThreeExactRows": locatesLess([]int{2, 6, 9}),
		"TestAgentSearchExactNameNeverLocatesLessTenExactRows":   locatesLess([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
	}
}

// exactNameVerifyFixtures are the ten-row fixtures of the VERIFY sweep; exactNameNonExactFixtures
// are the fixtures of the byte-identity sweep. A part names its fixture by key.
var exactNameVerifyFixtures = map[string]func() sem.SearchResponse{
	exactNameTenExactRows: func() sem.SearchResponse {
		return exactNameFixture("resolveMessageRef", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	},
	exactNameLongBodies: func() sem.SearchResponse { return exactNameFixtureBody("resolveMessageRef", 40, 2, 6, 9) },
}

var exactNameNonExactFixtures = map[string]func() sem.SearchResponse{
	exactNameExactFixture:  func() sem.SearchResponse { return exactNameFixture("resolveMessageRef", 1, 4) },
	exactNameTimingFixture: timingDeterminismResponse,
}

// exactNameRunSweepPart runs the part of a sweep that the calling top-level test is declared to
// run. A test that is not declared fails rather than sweeping nothing.
func exactNameRunSweepPart(t *testing.T) {
	t.Helper()
	part, ok := exactNameSweepParts()[t.Name()]
	if !ok {
		t.Fatalf("%s is not declared in exactNameSweepParts", t.Name())
	}
	switch part.family {
	case exactNameAnchorsFamily:
		for _, lang := range part.langs {
			exactNameAnchorSweep(t, lang)
		}
	case exactNameVerifyFamily:
		if part.fixture != "" {
			build, known := exactNameVerifyFixtures[part.fixture]
			if !known {
				t.Fatalf("unknown VERIFY fixture %q", part.fixture)
			}
			exactNameVerifySweepBudgets(t, part.fixture, build(), part.residue, part.modulus)
		}
		for _, lang := range part.langs {
			exactNameVerifySweepLang(t, lang)
		}
	case exactNameNonExactFamily:
		build, known := exactNameNonExactFixtures[part.fixture]
		if !known {
			t.Fatalf("unknown non-exact fixture %q", part.fixture)
		}
		exactNameNonExactSweep(t, build(), part.queries)
	case exactNameLocatesLessFamily:
		exactNameNeverLocatesLessSweep(t, part.layouts...)
	default:
		t.Fatalf("%s: unknown sweep family %q", t.Name(), part.family)
	}
}

// exactNameSweepCoverErrors reports every way parts fails to be a disjoint, complete cover of the
// four families. It takes the parts as an argument so the guard can also be shown to reject a
// division with a piece missing, doubled or pointed at the wrong thing.
func exactNameSweepCoverErrors(parts map[string]exactNameSweepPart) []string {
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)

	// once reports each wanted item that is not covered by exactly one part.
	once := func(what string, wanted []string, covered map[string][]string) {
		want := map[string]bool{}
		for _, item := range wanted {
			want[item] = true
			if len(covered[item]) != 1 {
				fail("%s %q is swept by %d tests %v, want exactly one", what, item, len(covered[item]), covered[item])
			}
		}
		for item, by := range covered {
			if !want[item] {
				fail("%s %q, swept by %v, is not part of the family", what, item, by)
			}
		}
	}

	// Languages: every case's language exactly once, in the anchors family and in the verify family.
	var langs []string
	seenLang := map[string]bool{}
	for _, c := range exactNameLangCases {
		if !seenLang[c.lang] {
			seenLang[c.lang] = true
			langs = append(langs, c.lang)
		}
	}
	for _, family := range []string{exactNameAnchorsFamily, exactNameVerifyFamily} {
		covered := map[string][]string{}
		for _, name := range names {
			if part := parts[name]; part.family == family {
				for _, lang := range part.langs {
					covered[lang] = append(covered[lang], name)
				}
			}
		}
		once(family+" language", langs, covered)
	}

	// VERIFY fixtures: every budget of every fixture in exactly one residue class.
	for fixture := range exactNameVerifyFixtures {
		var classes []exactNameSweepPart
		for _, name := range names {
			part := parts[name]
			if part.family != exactNameVerifyFamily || part.fixture != fixture {
				continue
			}
			if part.modulus < 1 || part.residue < 0 || part.residue >= part.modulus {
				fail("%s: residue %d modulo %d is not a residue class", name, part.residue, part.modulus)
				continue
			}
			classes = append(classes, part)
		}
		period := 1
		for _, class := range classes {
			period *= class.modulus
		}
		for budget := 1; budget <= period; budget++ {
			swept := 0
			for _, class := range classes {
				if budget%class.modulus == class.residue {
					swept++
				}
			}
			if swept != 1 {
				fail("VERIFY fixture %q: budgets congruent to %d modulo %d are swept by %d tests, want exactly one", fixture, budget%period, period, swept)
			}
		}
	}
	for _, name := range names {
		part := parts[name]
		switch {
		case part.family == exactNameVerifyFamily && part.fixture != "":
			if _, known := exactNameVerifyFixtures[part.fixture]; !known {
				fail("%s: unknown VERIFY fixture %q", name, part.fixture)
			}
		case part.family == exactNameVerifyFamily && len(part.langs) == 0:
			fail("%s: a VERIFY part sweeps neither a fixture nor a language", name)
		case part.family == exactNameAnchorsFamily && len(part.langs) == 0:
			fail("%s: an anchors part sweeps no language", name)
		}
	}

	// Non-exact: every fixture meets every query exactly once.
	if len(exactNameNonExactSparseQueries())+len(exactNameNonExactDenseQueries) != len(exactNameNonExactQueries) {
		fail("the dense queries are not %d distinct entries of the query table", len(exactNameNonExactDenseQueries))
	}
	var pairs []string
	for fixture := range exactNameNonExactFixtures {
		for _, query := range exactNameNonExactQueries {
			pairs = append(pairs, fixture+" x "+fmt.Sprintf("%q", query))
		}
	}
	coveredPairs := map[string][]string{}
	for _, name := range names {
		if part := parts[name]; part.family == exactNameNonExactFamily {
			for _, query := range part.queries {
				pair := part.fixture + " x " + fmt.Sprintf("%q", query)
				coveredPairs[pair] = append(coveredPairs[pair], name)
			}
		}
	}
	once("non-exact fixture and query", pairs, coveredPairs)

	// Never-locates-less: every layout exactly once.
	var layouts []string
	for _, layout := range exactNameNeverLocatesLessLayouts {
		layouts = append(layouts, fmt.Sprint(layout))
	}
	coveredLayouts := map[string][]string{}
	for _, name := range names {
		if part := parts[name]; part.family == exactNameLocatesLessFamily {
			for _, layout := range part.layouts {
				coveredLayouts[fmt.Sprint(layout)] = append(coveredLayouts[fmt.Sprint(layout)], name)
			}
		}
	}
	once("never-locates-less layout", layouts, coveredLayouts)

	for _, name := range names {
		switch parts[name].family {
		case exactNameAnchorsFamily, exactNameVerifyFamily, exactNameNonExactFamily, exactNameLocatesLessFamily:
		default:
			fail("%s: unknown sweep family %q", name, parts[name].family)
		}
	}
	sort.Strings(errs)
	return errs
}

// exactNameSweepHelpers are the functions that do a sweep's work. Only exactNameRunSweepPart may
// call them from a top-level test's behalf, so that every sweep is one the registry declares.
var exactNameSweepHelpers = map[string]bool{
	"exactNameAnchorSweep": true, "exactNameVerifySweep": true, "exactNameVerifySweepBudgets": true,
	"exactNameVerifySweepLang": true, "exactNameNonExactSweep": true, "exactNameNeverLocatesLessSweep": true,
}

// exactNameTopLevelSweepCalls parses this package's own test files and returns, for every
// top-level Test function, which of the given functions its body calls directly.
func exactNameTopLevelSweepCalls(t *testing.T, callees map[string]bool) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string][]string{}
	parsed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		// Every test file is read, whatever its build constraints: a sweep test that is
		// compiled only on some platforms must still be declared.
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		parsed++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			calls[fn.Name.Name] = append(calls[fn.Name.Name], []string{}...)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && callees[ident.Name] {
						calls[fn.Name.Name] = append(calls[fn.Name.Name], ident.Name)
					}
				}
				return true
			})
		}
	}
	if parsed == 0 || len(calls) == 0 {
		t.Fatalf("found no test functions in %d test files; the guard would check nothing", parsed)
	}
	return calls
}

// THE SPLIT SWEEPS ARE COMPLETE. The declared parts cover every family exactly once; every
// declared part is a real top-level test that takes its work from the declaration; and no
// top-level test runs a sweep outside it.
func TestAgentSearchExactNameSweepPartsCoverEveryFamily(t *testing.T) {
	t.Parallel()
	parts := exactNameSweepParts()
	for _, problem := range exactNameSweepCoverErrors(parts) {
		t.Error(problem)
	}

	callees := map[string]bool{"exactNameRunSweepPart": true}
	for helper := range exactNameSweepHelpers {
		callees[helper] = true
	}
	calls := exactNameTopLevelSweepCalls(t, callees)
	for name := range parts {
		got, declared := calls[name]
		switch {
		case !declared:
			t.Errorf("%s is declared in exactNameSweepParts but is not a top-level test in this package", name)
		case len(got) != 1 || got[0] != "exactNameRunSweepPart":
			t.Errorf("%s must call exactNameRunSweepPart exactly once and no sweep helper; it calls %v", name, got)
		}
	}
	for name, got := range calls {
		if _, declared := parts[name]; declared {
			continue
		}
		for _, callee := range got {
			t.Errorf("%s calls %s but is not declared in exactNameSweepParts", name, callee)
		}
	}
}

// The guard itself: each way of dropping, doubling or mis-pointing a piece of the division is
// reported by exactNameSweepCoverErrors.
func TestAgentSearchExactNameSweepGuardRejectsBrokenDivisions(t *testing.T) {
	t.Parallel()
	if problems := exactNameSweepCoverErrors(exactNameSweepParts()); len(problems) != 0 {
		t.Fatalf("the declared division is not a cover: %v", problems)
	}
	const (
		anchorsRust   = "TestAgentSearchExactNameAnchorsOnTheSignatureRust"
		verifyPython  = "TestAgentSearchExactNameNeverDropsVerifyPython"
		longEven      = "TestAgentSearchExactNameNeverDropsVerifyLongBodiesEvenBudgets"
		longOdd       = "TestAgentSearchExactNameNeverDropsVerifyLongBodiesOddBudgets"
		oneExactRow   = "TestAgentSearchExactNameNeverLocatesLessOneExactRow"
		partialCase   = "TestAgentSearchNonExactQueriesAreByteIdenticalOnExactNameFixturePartialAndCaseQueries"
		timingFixture = "TestAgentSearchNonExactQueriesAreByteIdenticalOnTimingFixture"
	)
	edit := func(name string, change func(*exactNameSweepPart)) func(map[string]exactNameSweepPart) {
		return func(parts map[string]exactNameSweepPart) {
			part, ok := parts[name]
			if !ok {
				t.Fatalf("%s is not declared", name)
			}
			change(&part)
			parts[name] = part
		}
	}
	broken := map[string]func(map[string]exactNameSweepPart){
		"a language's signature sweep is dropped":   func(parts map[string]exactNameSweepPart) { delete(parts, anchorsRust) },
		"a VERIFY sweep points at another language": edit(verifyPython, func(part *exactNameSweepPart) { part.langs = []string{"ts"} }),
		"the even budgets are dropped":              func(parts map[string]exactNameSweepPart) { delete(parts, longEven) },
		"both halves sweep the odd budgets":         edit(longEven, func(part *exactNameSweepPart) { part.residue = 1 }),
		"a residue class overlaps the whole range":  edit(longOdd, func(part *exactNameSweepPart) { part.residue, part.modulus = 0, 1 }),
		"a layout is dropped":                       edit(oneExactRow, func(part *exactNameSweepPart) { part.layouts = part.layouts[:2] }),
		"a layout is invented":                      edit(oneExactRow, func(part *exactNameSweepPart) { part.layouts = append([][]int{{3}}, part.layouts...) }),
		"a dense query is dropped":                  edit(partialCase, func(part *exactNameSweepPart) { part.queries = part.queries[:1] }),
		"a query is swept twice":                    edit(partialCase, func(part *exactNameSweepPart) { part.queries = append([]string{"compute"}, part.queries...) }),
		"a fixture loses its queries":               func(parts map[string]exactNameSweepPart) { delete(parts, timingFixture) },
		"a part names an unknown fixture":           edit(timingFixture, func(part *exactNameSweepPart) { part.fixture = "elsewhere" }),
		"a part names an unknown family":            edit(anchorsRust, func(part *exactNameSweepPart) { part.family = "other" }),
		"a language is swept twice":                 edit(anchorsRust, func(part *exactNameSweepPart) { part.langs = []string{"rs", "go"} }),
	}
	for label, breakIt := range broken {
		parts := exactNameSweepParts()
		breakIt(parts)
		if problems := exactNameSweepCoverErrors(parts); len(problems) == 0 {
			t.Errorf("%s: the guard reported nothing", label)
		}
	}
}
