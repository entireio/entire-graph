package sem

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestGoRouteMaskTraversalSharesResolverBudget(t *testing.T) {
	const content = `package p
 import "net/http"
 func routes() {
  http.HandleFunc("/x", handler)
  http.HandleFunc("/y", handler)
 }
 `
	for _, budget := range []int{0, 1, 8} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "", content, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		r := &goRouteResolver{src: content, file: file, fset: fset, tokFile: fset.File(file.Pos()), imports: goRouteImports(file), budget: budget}
		spans := r.maskSpans()
		if r.steps != budget+1 || len(spans) != 1 || spans[0] != [2]int{0, len(content)} {
			t.Fatalf("budget %d: mask work must stop and suppress literal fallback, steps=%d masks=%v", budget, r.steps, spans)
		}
		receivers := resolveGoRouteReceiversWithBudget(content, nil, budget)
		if !receivers.exhausted || receivers.parsed || len(receivers.masks) != 1 || receivers.masks[0] != [2]int{0, len(content)} {
			t.Fatalf("mask exhaustion must stop semantic resolution: %+v", receivers)
		}
	}
	normal := resolveGoRouteReceivers(content, nil)
	if !normal.parsed || normal.exhausted || len(normal.regs) != 2 || len(normal.masks) != 2 {
		t.Fatalf("ordinary budget must preserve exact route masks and registrations: %+v", normal)
	}
}
