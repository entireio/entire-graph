package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// diagnosticCodeShape is what every warning and partial-failure code looks like.
// A prefix constant such as "E_REPO_IGNORE_" ends in an underscore and does not
// match.
var diagnosticCodeShape = regexp.MustCompile(`^[EW]_[A-Z0-9]+(_[A-Z0-9]+)*$`)

// TestEveryDiagnosticCodeHasAnEffectScope makes effect_scope exhaustive by
// construction. It reads every non-test Go file under internal/, collects each
// string literal shaped like a diagnostic code, and requires it to be classified
// in exactly one of the two scope tables. A new code therefore cannot ship
// without someone deciding whether its loss is confined to one file; and a code
// that leaves the source cannot linger in a table.
func TestEveryDiagnosticCodeHasAnEffectScope(t *testing.T) {
	t.Parallel()

	found := map[string]string{}
	root := ".."
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The tables' own file is skipped: its literals would make every table
		// entry look emitted and the stale-entry check vacuous.
		if filepath.Base(path) == "diagnostic_scope.go" && filepath.Base(filepath.Dir(path)) == "sem" {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil || !diagnosticCodeShape.MatchString(value) {
				return true
			}
			if _, seen := found[value]; !seen {
				found[value] = fset.Position(literal.Pos()).String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The scan itself must work: it has to see codes from both packages that
	// emit them, or an empty walk would pass vacuously.
	for _, known := range []string{"E_PARSE_ERROR", GoGrammarGapCode, "W_VALUE_REFERENCES_UNMODELLED", "E_REPO_IGNORE_UNREADABLE"} {
		if _, ok := found[known]; !ok {
			t.Fatalf("source scan missed %s; found %d codes", known, len(found))
		}
	}
	for code, position := range found {
		file, snapshot := fileScopedDiagnosticCodes[code], snapshotScopedDiagnosticCodes[code]
		switch {
		case !file && !snapshot:
			t.Errorf("%s (%s) has no effect scope: add it to fileScopedDiagnosticCodes if its loss is confined to the file it names, otherwise to snapshotScopedDiagnosticCodes", code, position)
		case file && snapshot:
			t.Errorf("%s (%s) is classified as both file- and snapshot-scoped", code, position)
		}
	}
	for _, table := range []map[string]bool{fileScopedDiagnosticCodes, snapshotScopedDiagnosticCodes} {
		for code := range table {
			if _, ok := found[code]; !ok {
				t.Errorf("%s is classified but no source file emits it; remove it from its scope table", code)
			}
		}
	}
}

func TestEffectScopeStampNeverMutatesItsInput(t *testing.T) {
	t.Parallel()

	warnings := []ProviderWarning{{Code: GoGrammarGapCode, FilePath: "a.go"}, {Code: "W_WORKTREE_SNAPSHOT"}}
	stamped := withWarningEffectScopes(warnings)
	if warnings[0].EffectScope != "" || warnings[1].EffectScope != "" {
		t.Fatalf("input mutated: %+v", warnings)
	}
	if stamped[0].EffectScope != EffectScopeFile || stamped[1].EffectScope != EffectScopeSnapshot {
		t.Fatalf("stamped = %+v", stamped)
	}
	failures := []PartialFailure{{Code: "E_MINIFIED", FilePath: "a.json"}, {Code: "E_REPO_IGNORE_UNREADABLE", FilePath: "vendor"}}
	stampedFailures := withFailureEffectScopes(failures)
	if failures[0].EffectScope != "" || stampedFailures[0].EffectScope != EffectScopeFile || stampedFailures[1].EffectScope != EffectScopeSnapshot {
		t.Fatalf("failures %+v stamped %+v", failures, stampedFailures)
	}
	// A wrong stored scope is corrected, not trusted.
	wrong := []PartialFailure{{Code: "E_REPO_IGNORE_UNREADABLE", FilePath: "vendor", EffectScope: EffectScopeFile}}
	if got := withFailureEffectScopes(wrong); got[0].EffectScope != EffectScopeSnapshot {
		t.Fatalf("stored scope trusted: %+v", got)
	}
}
