package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestGoRouteWrittenNamesRespectsLexicalShadowing(t *testing.T) {
	for _, body := range []string{`if ok { g := root.Group("/inner"); g = g.Group("/more") }`, `for ok { var g = root.Group("/inner"); g = root.Group("/new") }`, `switch {case ok: g := root.Group("/inner"); g = root.Group("/new")}`, `if ok {g, err := root.Group("/inner"), nil; _ = err; g = root.Group("/new")}`} {
		f, err := parser.ParseFile(token.NewFileSet(), "", `package p; func f(){`+body+`}`, 0)
		if err != nil {
			t.Fatal(err)
		}
		r := &goRouteResolver{budget: 10000}
		n := f.Decls[0].(*ast.FuncDecl).Body
		if r.writtenNames(n)["g"] {
			t.Errorf("shadow invalidated outer: %s", body)
		}
	}
	f, _ := parser.ParseFile(token.NewFileSet(), "", `package p; func f(){if ok {g = root.Group("/changed")}}`, 0)
	if !(&goRouteResolver{budget: 10000}).writtenNames(f.Decls[0].(*ast.FuncDecl).Body)["g"] {
		t.Fatal("outer assignment was lost")
	}
}
