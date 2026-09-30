package sem

import (
	"go/ast"
	"go/scanner"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Go route registrations are read from real call expressions only. A file
// that parses is walked by the receiver resolver (go_route_binding.go), which
// records each registration together with its receiver's prefix state at the
// call. Route-like text inside comments and string literals is never a call
// expression, so it can never produce a route.
//
// A file that does not parse falls back to a token scan (comments and string
// contents are single tokens or skipped, so they still cannot match) with a
// deliberately narrow receiver rule; see goRouteFallbackRegistrations.

type goRouteCandidate struct {
	// order keeps the historical emission order: HandleFunc, then
	// Handle(HandlerFunc(..)), then router methods, then Group(..).METHOD.
	order    int
	offset   int
	route    string
	handler  string
	evidence string
	// receiverAware is false for registrations whose receiver is not
	// consulted.
	receiverAware bool
	receiver      goRouteBinding
}

var goRouteMethodNames = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
	"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true, "Head": true, "Options": true,
}

// goRouteCallHintRe gates the parse: a file with no call that could register a
// route is never parsed.
var goRouteCallHintRe = regexp.MustCompile(`\b(?:HandleFunc|Handle|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Get|Post|Put|Patch|Delete|Head|Options)\s*\(`)

func goRouteRegistrations(content string, constants map[string]string) []goHTTPRouteRegistration {
	if !goRouteCallHintRe.MatchString(content) {
		return nil
	}
	receivers := resolveGoRouteReceivers(content, constants)
	var candidates []goRouteCandidate
	switch {
	case receivers.exhausted:
		// No receiver in the file is trusted.
		return nil
	case receivers.parsed:
		candidates = receivers.regs
	default:
		candidates = goRouteFallbackRegistrations(content)
	}
	return goRouteEmit(candidates, constants)
}

func goRouteEmit(candidates []goRouteCandidate, constants map[string]string) []goHTTPRouteRegistration {
	sorted := append([]goRouteCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].order != sorted[j].order {
			return sorted[i].order < sorted[j].order
		}
		return sorted[i].offset < sorted[j].offset
	})
	var registrations []goHTTPRouteRegistration
	for _, candidate := range sorted {
		routeExpr := candidate.route
		if candidate.receiverAware {
			receiver := candidate.receiver
			if receiver.kind == goRouteUnknown {
				// The receiver's prefix at this call is not determined: emitting
				// the bare path, or another binding's prefix, would invent a route.
				continue
			}
			if receiver.kind == goRouteKnown && receiver.prefix != "" {
				route, ok := staticRouteExpressionValue(routeExpr, constants)
				if !ok {
					continue
				}
				routeExpr = strconv.Quote(joinRoutePaths(receiver.prefix, route))
			}
		}
		route, ok := staticRouteExpressionValue(routeExpr, constants)
		if !ok || candidate.handler == "" {
			continue
		}
		registrations = append(registrations, goHTTPRouteRegistration{
			Route:        route,
			Handler:      candidate.handler,
			EvidenceKind: candidate.evidence,
			Detail:       route + " -> " + candidate.handler,
		})
	}
	return registrations
}

// goRouteHandlerText accepts the handler shapes the registration forms always
// accepted: an identifier or a one-level selector (h, handlers.Show).
func goRouteHandlerText(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		if x, ok := expr.X.(*ast.Ident); ok {
			return x.Name + "." + expr.Sel.Name
		}
	}
	return ""
}

// goRouteHandlerFuncArg unwraps Handle's http.HandlerFunc(h) / HandlerFunc(h).
func goRouteHandlerFuncArg(expr ast.Expr) string {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return ""
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if fun.Name != "HandlerFunc" {
			return ""
		}
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		if !ok || pkg.Name != "http" || fun.Sel.Name != "HandlerFunc" {
			return ""
		}
	default:
		return ""
	}
	return goRouteHandlerText(call.Args[0])
}

// noteCall records call as a route registration candidate when it has one of
// the registration shapes, with its receiver's state at this point of the walk.
func (r *goRouteResolver) noteCall(call *ast.CallExpr, scope *goRouteScope) {
	if len(call.Args) != 2 || call.Ellipsis.IsValid() {
		return
	}
	var name string
	var receiver ast.Expr
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		name, receiver = fun.Sel.Name, fun.X
	case *ast.Ident:
		name = fun.Name
	default:
		return
	}
	candidate := goRouteCandidate{offset: r.offset(call.Pos()), route: r.text(call.Args[0])}
	switch {
	case name == "HandleFunc":
		candidate.order, candidate.evidence = 0, "go_http_handle_func"
		candidate.handler = goRouteHandlerText(call.Args[1])
	case name == "Handle":
		candidate.order, candidate.evidence = 1, "go_http_handler_func"
		candidate.handler = goRouteHandlerFuncArg(call.Args[1])
	case goRouteMethodNames[name] && receiver != nil:
		candidate.handler = goRouteHandlerText(call.Args[1])
		candidate.receiverAware = true
		switch x := receiver.(type) {
		case *ast.Ident:
			candidate.order, candidate.evidence = 2, "go_router_method"
			candidate.receiver = r.read(r.lookup(scope, x.Name))
		case *ast.CallExpr:
			selector, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Group" {
				return
			}
			if _, ok := selector.X.(*ast.Ident); !ok {
				return
			}
			candidate.order, candidate.evidence = 3, "go_router_group_method"
			candidate.receiver = r.groupValue(x, r.lookupIn(scope))
		default:
			// A field or other selector receiver (s.api.GET) holds a prefix
			// this pass does not track.
			return
		}
	default:
		return
	}
	if candidate.handler == "" {
		return
	}
	r.regs = append(r.regs, candidate)
}

// goRouteFallbackRegistrations handles a file go/parser rejects. It scans
// tokens, so comments and string contents cannot match, and it accepts a
// registration only on a plain identifier receiver that is either the net/http
// package or declared as a parameter of a type that is not a group, and that
// is never assigned anywhere in the file (so a name bound from a Group call is
// never accepted). No prefix is ever composed, and a Route, Mount, Subrouter
// or StripPrefix call anywhere in the file makes the whole file unresolved.
func goRouteFallbackRegistrations(content string) []goRouteCandidate {
	type tok struct {
		tok token.Token
		lit string
		off int
	}
	var toks []tok
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(content))
	var s scanner.Scanner
	s.Init(file, []byte(content), func(token.Position, string) {}, 0)
	for {
		pos, t, lit := s.Scan()
		if t == token.EOF {
			break
		}
		if t == token.SEMICOLON && lit == "\n" {
			continue
		}
		toks = append(toks, tok{tok: t, lit: lit, off: file.Offset(pos)})
	}
	at := func(i int) tok {
		if i < 0 || i >= len(toks) {
			return tok{tok: token.ILLEGAL}
		}
		return toks[i]
	}
	isIdent := func(i int, name string) bool {
		t := at(i)
		return t.tok == token.IDENT && (name == "" || t.lit == name)
	}
	for i := range toks {
		if isIdent(i, "") && at(i+1).tok == token.LPAREN && at(i-1).tok == token.PERIOD {
			switch toks[i].lit {
			case "Route", "Mount", "Subrouter", "StripPrefix":
				return nil
			}
		}
	}
	// A receiver is acceptable only if it is declared as a parameter
	// (`(name T` or `, name T` followed by `,` or `)`) of a non-group type, and
	// never appears as an assignment target or in a name list.
	paramOK := map[string]bool{}
	tainted := map[string]bool{}
	for i := range toks {
		if !isIdent(i, "") {
			continue
		}
		name := toks[i].lit
		switch at(i + 1).tok {
		case token.ASSIGN, token.DEFINE, token.COMMA:
			tainted[name] = true
			continue
		}
		if prev := at(i - 1); prev.tok == token.VAR || prev.tok == token.RANGE {
			tainted[name] = true
			continue
		}
		if prev := at(i - 1).tok; prev != token.LPAREN && prev != token.COMMA {
			continue
		}
		j := i + 1
		if at(j).tok == token.MUL {
			j++
		}
		if !isIdent(j, "") {
			continue
		}
		pkg, typeName := "", at(j).lit
		j++
		if at(j).tok == token.PERIOD && isIdent(j+1, "") {
			pkg, typeName = typeName, at(j+1).lit
			j += 2
		}
		if next := at(j).tok; next != token.COMMA && next != token.RPAREN {
			continue
		}
		if goRouteFallbackTypeIsRoot(pkg, typeName) {
			paramOK[name] = true
		} else {
			tainted[name] = true
		}
	}
	acceptable := func(name string) bool {
		if tainted[name] {
			return false
		}
		return name == "http" || paramOK[name]
	}
	// routeArg returns the source text of the first argument (up to the first
	// comma at depth zero) and the index of that comma.
	routeArg := func(open int) (string, int) {
		depth := 0
		for k := open + 1; k < len(toks); k++ {
			switch toks[k].tok {
			case token.LPAREN, token.LBRACK, token.LBRACE:
				depth++
			case token.RPAREN, token.RBRACK, token.RBRACE:
				if depth == 0 {
					return "", -1
				}
				depth--
			case token.COMMA:
				if depth == 0 {
					if k == open+1 {
						return "", -1
					}
					return content[toks[open+1].off:toks[k].off], k
				}
			}
		}
		return "", -1
	}
	// handlerAt parses `h` or `x.h` starting at k and returns the text and the
	// index after it.
	handlerAt := func(k int) (string, int) {
		if !isIdent(k, "") {
			return "", k
		}
		if at(k+1).tok == token.PERIOD && isIdent(k+2, "") {
			return at(k).lit + "." + at(k+2).lit, k + 3
		}
		return at(k).lit, k + 1
	}
	var candidates []goRouteCandidate
	for i := range toks {
		if !isIdent(i, "") || at(i+1).tok != token.PERIOD || !isIdent(i+2, "") || at(i+3).tok != token.LPAREN {
			continue
		}
		if at(i-1).tok == token.PERIOD || !acceptable(toks[i].lit) {
			continue
		}
		method := toks[i+2].lit
		route, comma := routeArg(i + 3)
		if comma < 0 {
			continue
		}
		candidate := goRouteCandidate{offset: toks[i].off, route: route}
		k := comma + 1
		switch {
		case method == "HandleFunc":
			candidate.order, candidate.evidence = 0, "go_http_handle_func"
			candidate.handler, k = handlerAt(k)
		case method == "Handle":
			candidate.order, candidate.evidence = 1, "go_http_handler_func"
			if isIdent(k, "http") && at(k+1).tok == token.PERIOD {
				k += 2
			}
			if !isIdent(k, "HandlerFunc") || at(k+1).tok != token.LPAREN {
				continue
			}
			candidate.handler, k = handlerAt(k + 2)
			if at(k).tok != token.RPAREN {
				continue
			}
			k++
		case goRouteMethodNames[method]:
			candidate.order, candidate.evidence = 2, "go_router_method"
			candidate.handler, k = handlerAt(k)
		default:
			continue
		}
		if candidate.handler == "" || at(k).tok != token.RPAREN {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// goRouteFallbackTypeIsRoot reports whether a parameter of this type may be
// taken as the root router in the unparseable-file fallback: a local type
// whose name does not say it is a group, or a framework root type.
func goRouteFallbackTypeIsRoot(pkg, name string) bool {
	if pkg == "" {
		return !goRouteGroupishTypeName(name)
	}
	return goRouteRootTypes[pkg+"."+name]
}

// goRouteRootTypes are framework types that are always the root router.
var goRouteRootTypes = map[string]bool{
	"echo.Echo":         true,
	"gin.Engine":        true,
	"http.ServeMux":     true,
	"httprouter.Router": true,
}

// goRouteGroupishTypeName reports a local type named like a router group.
func goRouteGroupishTypeName(name string) bool {
	return strings.Contains(name, "Group")
}
