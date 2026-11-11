package sem

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// goGrammarGapSource is valid Go 1.26: new accepts an expression as well as a
// type. The bundled tree-sitter Go grammar (and upstream master) still requires a
// type there, so it recovers each new(expr) as an ERROR node. go/parser accepts it.
const goGrammarGapSource = `package demo

func onlyInsideNew() *int { return new(g()) }

func control() int { return h() }

func afterBroken() int {
	p := new(0)
	_ = p
	return h()
}
`

const goGrammarGapLib = `package demo

func g() int { return 1 }

func h() int { return 2 }
`

// goInvalidSource is not Go: go/parser rejects the unclosed parameter list.
const goInvalidSource = `package demo

func broken( {
	return h()
}
`

func TestParseStatusDefersGoGrammarGapToGoParser(t *testing.T) {
	t.Parallel()

	entities, language, status := TreeSitterParser{}.ParseWithStatus("demo.go", goGrammarGapSource)
	if language != "Go" {
		t.Fatalf("language = %q", language)
	}
	if !status.ParseError {
		t.Fatal("the bundled grammar now parses new(expr) cleanly; this fixture no longer exercises a grammar gap")
	}
	if !status.GrammarGap || status.Code != GoGrammarGapCode {
		t.Fatalf("valid Go reported as %+v, want a %s grammar gap", status, GoGrammarGapCode)
	}
	if !strings.Contains(status.Detail, "tree-sitter syntax error nodes present") || !strings.HasSuffix(status.Detail, "go/parser accepts the file") {
		t.Fatalf("detail = %q", status.Detail)
	}
	names := map[string]bool{}
	for _, entity := range entities {
		names[entity.Name] = true
	}
	for _, want := range []string{"onlyInsideNew", "control", "afterBroken"} {
		if !names[want] {
			t.Fatalf("entity %s missing from the recovered tree: %v", want, names)
		}
	}

	_, _, invalid := TreeSitterParser{}.ParseWithStatus("broken.go", goInvalidSource)
	if !invalid.ParseError || invalid.GrammarGap || invalid.Code != "E_PARSE_ERROR" {
		t.Fatalf("invalid Go reported as %+v, want E_PARSE_ERROR", invalid)
	}
}

func goGrammarGapRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "demo.go", goGrammarGapSource)
	writeFile(t, repo, "lib.go", goGrammarGapLib)
	writeFile(t, repo, "broken/broken.go", goInvalidSource)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "grammar gap fixture")
	return repo
}

func TestSnapshotReportsGoGrammarGapAsWarning(t *testing.T) {
	t.Parallel()

	repo := goGrammarGapRepo(t)
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "test", opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == "demo.go" {
			t.Fatalf("valid Go still reported as a partial failure: %+v", failure)
		}
	}
	var gap *ProviderWarning
	for index, warning := range snapshot.Header.Warnings {
		if warning.Code == GoGrammarGapCode {
			if gap != nil {
				t.Fatalf("more than one grammar-gap warning: %+v", snapshot.Header.Warnings)
			}
			gap = &snapshot.Header.Warnings[index]
		}
	}
	if gap == nil || gap.FilePath != "demo.go" || gap.Severity != "warning" ||
		!strings.Contains(gap.EffectOnCompleteness, "valid Go") ||
		!strings.Contains(gap.EffectOnCompleteness, "may be missing") {
		t.Fatalf("grammar-gap warning = %+v; warnings %+v", gap, snapshot.Header.Warnings)
	}
	// Truly invalid Go keeps its partial failure.
	brokenFailures := 0
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == "broken/broken.go" && failure.Code == "E_PARSE_ERROR" {
			brokenFailures++
		}
	}
	if brokenFailures != 1 {
		t.Fatalf("invalid Go lost its E_PARSE_ERROR: %+v", snapshot.Header.PartialFailures)
	}
	// Go health counts the invalid file only.
	if goHealth := snapshot.Header.Completeness.Health.Languages["Go"]; goHealth.FlaggedFiles != 1 {
		t.Fatalf("Go health = %+v, want exactly the invalid file flagged", goHealth)
	}
	// The facts around the unparsed constructs survive.
	ids := map[string]string{}
	for _, symbol := range snapshot.Symbols {
		ids[symbol.ID] = symbol.Name
	}
	calls := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" {
			calls[ids[relation.FromID]+"->"+ids[relation.ToID]] = true
		}
	}
	for _, want := range []string{"onlyInsideNew->g", "control->h", "afterBroken->h"} {
		if !calls[want] {
			t.Fatalf("CALLS %s missing: %v", want, calls)
		}
	}

	// A selective view derived from the complete snapshot reports the warning
	// exactly as a cold build of the same view does, and drops it with its file.
	indexed, _, err := PreindexProviderSnapshot(context.Background(), repo, "test", opts, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, only := range [][]string{{"demo.go", "lib.go"}, {"lib.go"}} {
		viewOpts := opts
		viewOpts.OnlyFiles = only
		derived, err := selectiveSearchSnapshotFromFull(context.Background(), repo, "test", viewOpts, indexed)
		if err != nil {
			t.Fatal(err)
		}
		cold, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "test", viewOpts)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cold.Header.Warnings, derived.Header.Warnings) {
			t.Fatalf("view %v: cold warnings %+v, derived %+v", only, cold.Header.Warnings, derived.Header.Warnings)
		}
		hasGap := false
		for _, warning := range derived.Header.Warnings {
			hasGap = hasGap || warning.Code == GoGrammarGapCode
		}
		if hasGap != (len(only) == 2) {
			t.Fatalf("view %v: grammar-gap warning present = %v; %+v", only, hasGap, derived.Header.Warnings)
		}
	}
}

func diffWarning(result Result, code, path string) *ProviderWarning {
	for index := range result.Warnings {
		if result.Warnings[index].Code == code && result.Warnings[index].FilePath == path {
			return &result.Warnings[index]
		}
	}
	return nil
}

func TestDiffReportsGoGrammarGapAsWarning(t *testing.T) {
	t.Parallel()

	// demo.go changes, so the diff parses it on both sides; lib.go changes h,
	// which demo.go calls, so the dependents scan parses demo.go too.
	repo := buildLinearRepo(t, func(r string) {
		write(t, r, "demo.go", goGrammarGapSource)
		write(t, r, "lib.go", goGrammarGapLib)
	}, func(r string) {
		write(t, r, "demo.go", strings.Replace(goGrammarGapSource, "func control() int { return h() }", "func control() int { return h() + 1 }", 1))
		write(t, r, "lib.go", strings.Replace(goGrammarGapLib, "return 2", "return 3", 1))
	})
	result, err := AnalyzeGitRange(context.Background(), repo.repo, repo.base, repo.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if warning := diffWarning(result, "E_PARSE_ERROR", "demo.go"); warning != nil {
		t.Fatalf("valid Go still reported as E_PARSE_ERROR in the diff: %+v", warning)
	}
	gaps := 0
	sawKept, sawDependents := false, false
	for _, warning := range result.Warnings {
		if warning.Code != GoGrammarGapCode {
			continue
		}
		gaps++
		if warning.FilePath != "demo.go" || !strings.Contains(warning.EffectOnCompleteness, "valid Go") {
			t.Fatalf("grammar-gap warning = %+v", warning)
		}
		sawKept = sawKept || strings.Contains(warning.EffectOnCompleteness, "diff kept")
		sawDependents = sawDependents || strings.Contains(warning.EffectOnCompleteness, "dependent references")
	}
	if !sawKept || !sawDependents {
		t.Fatalf("want the diff's and the dependents scan's grammar-gap warnings, got %d: %+v", gaps, result.Warnings)
	}
	changed := false
	for _, file := range result.Files {
		for _, change := range file.Changes {
			changed = changed || (file.Path == "demo.go" && change.Name == "control")
		}
	}
	if !changed {
		t.Fatalf("control's change was not reported: %+v", result.Files)
	}

	// Invalid Go keeps E_PARSE_ERROR on the diff path.
	invalid := buildLinearRepo(t, func(r string) {
		write(t, r, "lib.go", goGrammarGapLib)
	}, func(r string) {
		write(t, r, "broken.go", goInvalidSource)
	})
	result, err = AnalyzeGitRange(context.Background(), invalid.repo, invalid.base, invalid.head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diffWarning(result, "E_PARSE_ERROR", "broken.go") == nil || diffWarning(result, GoGrammarGapCode, "broken.go") != nil {
		t.Fatalf("invalid Go: want E_PARSE_ERROR, got %+v", result.Warnings)
	}
}

// goGrammarGapLostSource is valid Go 1.26 whose new(expr) wraps a function
// literal. The bundled grammar's recovery swallows the declaration that
// follows it, so lostAfterNew has no entity although go/parser sees it.
const goGrammarGapLostSource = `package demo

func keptBeforeNew() {}

var f = new(func() int { return 1 }())

func lostAfterNew() {}
`

// A grammar gap is only a warning while the recovered tree still yields every
// declaration. When go/parser sees a function, method or type the recovery has
// no entity for, a symbol is known to be missing and the status must stay the
// counted E_PARSE_ERROR.
func TestParseStatusKeepsParseErrorWhenGrammarGapLosesDeclarations(t *testing.T) {
	t.Parallel()

	entities, _, status := TreeSitterParser{}.ParseWithStatus("lost.go", goGrammarGapLostSource)
	names := map[string]bool{}
	for _, entity := range entities {
		names[entity.Name] = true
	}
	if names["lostAfterNew"] {
		t.Fatal("the bundled grammar now recovers lostAfterNew; this fixture no longer loses a declaration")
	}
	if !names["keptBeforeNew"] {
		t.Fatalf("keptBeforeNew missing: %v", names)
	}
	if !status.ParseError || status.GrammarGap || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("valid Go with a lost declaration reported as %+v, want a counted E_PARSE_ERROR", status)
	}
	if !strings.Contains(status.Detail, "go/parser accepts the file") || !strings.Contains(status.Detail, "recovered no symbol for lostAfterNew") {
		t.Fatalf("detail does not name the lost declaration: %q", status.Detail)
	}
}

func TestGoDeclarationsLost(t *testing.T) {
	t.Parallel()

	const src = `package demo

type T struct{}

type G[K comparable] struct{}

type Alias = int

func (t *T) Method() {}

func (G[K]) Generic() {}

func init() {}

func init() {}

func Free() {}
`
	file, ok := goReferenceParse([]byte(src))
	if !ok {
		t.Fatal("fixture is not valid Go")
	}
	// The entities a clean parse of this exact file produces lose nothing, so
	// the comparison agrees with the entity walk's own naming.
	clean, _, status := TreeSitterParser{}.ParseWithStatus("demo.go", src)
	if status.ParseError {
		t.Fatalf("fixture does not parse cleanly: %+v", status)
	}
	if lost := goDeclarationsLost(file, clean); len(lost) != 0 {
		t.Fatalf("clean parse reported lost declarations: %v", lost)
	}
	if lost := goDeclarationsLost(file, nil); !reflect.DeepEqual(lost, []string{"T", "G", "T.Method", "G.Generic", "init", "init", "Free"}) {
		t.Fatalf("empty recovery lost = %v", lost)
	}
	// One init entity does not stand in for two declarations, and a function
	// does not stand in for a method of the same name.
	partial := []Entity{
		{Kind: "type", Name: "T"},
		{Kind: "type", Name: "G"},
		{Kind: "function", Name: "T.Method"},
		{Kind: "method", Name: "G.Generic"},
		{Kind: "function", Name: "init"},
		{Kind: "function", Name: "Free"},
	}
	if lost := goDeclarationsLost(file, partial); !reflect.DeepEqual(lost, []string{"T.Method", "init"}) {
		t.Fatalf("partial recovery lost = %v", lost)
	}
	if detail := goDeclarationsLostDetail([]string{"a", "b", "c", "d", "e", "f", "g"}); !strings.HasSuffix(detail, "recovered no symbol for a, b, c, d, e and 2 more") {
		t.Fatalf("detail = %q", detail)
	}
}

// In a snapshot the two cases part ways: the fully recovered file is a
// W_GO_GRAMMAR_GAP warning outside completeness, and the file that lost a
// declaration stays in partial_failures, the completeness counts and health.
func TestSnapshotKeepsGoGrammarGapWithLostDeclarationsAsPartialFailure(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "demo.go", goGrammarGapSource)
	writeFile(t, repo, "lib.go", goGrammarGapLib)
	writeFile(t, repo, "lost.go", goGrammarGapLostSource)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "grammar gap fixture")

	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, failure := range snapshot.Header.PartialFailures {
		failed = append(failed, failure.Code+" "+failure.FilePath)
		if failure.FilePath == "lost.go" && (failure.EffectScope != EffectScopeFile || !strings.Contains(failure.Detail, "lostAfterNew")) {
			t.Fatalf("lost.go failure = %+v", failure)
		}
	}
	if !reflect.DeepEqual(failed, []string{"E_PARSE_ERROR lost.go"}) {
		t.Fatalf("partial failures = %v, want only the file that lost a declaration", failed)
	}
	var gaps []string
	for _, warning := range snapshot.Header.Warnings {
		if warning.Code == GoGrammarGapCode {
			gaps = append(gaps, warning.FilePath)
		}
	}
	if !reflect.DeepEqual(gaps, []string{"demo.go"}) {
		t.Fatalf("grammar-gap warnings = %v, want only the fully recovered file", gaps)
	}
	if goHealth := snapshot.Header.Completeness.Health.Languages["Go"]; goHealth.FlaggedFiles != 1 {
		t.Fatalf("Go health = %+v, want the file that lost a declaration flagged", goHealth)
	}
}
