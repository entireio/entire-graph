package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A regexp.MustCompile whose pattern is a string literal has nothing to wait
// for: compiling it inside a function body recompiles the same pattern on every
// call. The relation phase did that on paths it runs once per symbol, and
// compilation alone accounted for 7.2 GiB of the 10.5 GiB a full snapshot of a
// 1,376-file repository allocated.
//
// This is not a style rule. Hoisting the literal ones is what makes that number
// go away, and one new in-function literal on a per-symbol path puts it back.
// Patterns built per call, from a symbol name or a caller's string, are outside
// the rule: there is no single value to hoist, so they are left alone.
func TestRegexpLiteralsAreCompiledOnce(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)

	var offenders []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isRegexpLiteralCompile(call) {
					return true
				}
				offenders = append(offenders, fset.Position(call.Pos()).String()+" in "+fn.Name.Name)
				return true
			})
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("regexp literals compiled inside function bodies (hoist each to a package-level var):\n\t%s",
			strings.Join(offenders, "\n\t"))
	}
}

func isRegexpLiteralCompile(call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "MustCompile" && selector.Sel.Name != "Compile") {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "regexp" {
		return false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	return ok && literal.Kind == token.STRING
}
