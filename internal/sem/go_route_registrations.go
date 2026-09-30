package sem

import (
	"go/ast"
	"go/scanner"
	"go/token"
	"path"
	"regexp"
	"sort"
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
	// inline registrations have a handler this pass cannot name as a symbol
	// (a closure, a call, a middleware chain): their route is attributed to
	// the enclosing symbol.
	inline   bool
	receiver goRouteBinding
}

// goRouteInline is a registration with an inline handler and its composed
// route, at a byte offset into the file.
type goRouteInline struct {
	Route  string
	Offset int
}

// goRouteDetail is everything the Go route pass knows about one file:
// registrations with a named handler, registrations with an inline handler,
// and the byte spans of every route argument it processed (placed, omitted or
// composed). The pattern-level route literal fallback must not read those
// spans: their route is decided here.
type goRouteDetail struct {
	regs   []goHTTPRouteRegistration
	inline []goRouteInline
	masks  [][2]int
}

var goRouteMethodNames = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
	"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true, "Head": true, "Options": true,
	"CONNECT": true, "TRACE": true, "Connect": true, "Trace": true, "Any": true, "All": true,
}

// goRouteCallHintRe gates the parse: a file with no call that could register a
// route is never parsed.
var goRouteCallHintRe = regexp.MustCompile(`\b(?:HandleFunc|Handle|MethodFunc|Method|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|CONNECT|TRACE|Get|Post|Put|Patch|Delete|Head|Options|Connect|Trace|Any|All)\s*\(`)

func goRouteRegistrations(content string, constants map[string]string) []goHTTPRouteRegistration {
	return goRouteRegistrationsDetailed(content, constants).regs
}

func goRouteRegistrationsDetailed(content string, constants map[string]string) goRouteDetail {
	if !goRouteCallHintRe.MatchString(content) {
		return goRouteDetail{}
	}
	receivers := resolveGoRouteReceivers(content, constants)
	switch {
	case receivers.exhausted:
		// No receiver in the file is trusted; the route arguments are still
		// known, so the literal fallback does not re-emit them either.
		return goRouteDetail{masks: receivers.masks}
	case receivers.parsed:
		detail := goRouteEmit(receivers.regs, constants)
		detail.masks = receivers.masks
		return detail
	default:
		return goRouteEmit(goRouteFallbackRegistrations(content), constants)
	}
}

func goRouteEmit(candidates []goRouteCandidate, constants map[string]string) goRouteDetail {
	sorted := append([]goRouteCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].order != sorted[j].order {
			return sorted[i].order < sorted[j].order
		}
		return sorted[i].offset < sorted[j].offset
	})
	var detail goRouteDetail
	for _, candidate := range sorted {
		route, ok := goRouteCandidateRoute(candidate, constants)
		if !ok {
			// The receiver's prefix at this call is not determined: emitting
			// the bare path, or another binding's prefix, would invent a route.
			continue
		}
		if candidate.inline {
			detail.inline = append(detail.inline, goRouteInline{Route: route, Offset: candidate.offset})
			continue
		}
		if candidate.handler == "" {
			continue
		}
		detail.regs = append(detail.regs, goHTTPRouteRegistration{
			Route:        route,
			Handler:      candidate.handler,
			EvidenceKind: candidate.evidence,
			Detail:       route + " -> " + candidate.handler,
			offset:       candidate.offset,
		})
	}
	return detail
}

// goRouteCandidateRoute is the full route a registration serves, or false
// when any part of it (receiver, where the router instance is served, the
// path argument) is not determined.
func goRouteCandidateRoute(candidate goRouteCandidate, constants map[string]string) (string, bool) {
	receiver := candidate.receiver
	if receiver.kind == goRouteUnknown {
		return "", false
	}
	framework := receiver.framework
	literal, ok := goRouteLiteral(framework, candidate.route, constants)
	if !ok {
		return "", false
	}
	prefix := receiver.prefix
	if receiver.kind == goRouteKnown && receiver.origin != nil {
		// Where the router instance itself is served: a composed Mount, or
		// unknown when it escaped to code that may mount it.
		base, ok := receiver.origin.prefix(0)
		if !ok {
			return "", false
		}
		if base != "" {
			if len(base)+len(prefix) > goRouteMaxPrefixBytes {
				return "", false
			}
			prefix = goRouteJoin(framework, base, prefix)
		}
	}
	return goRouteFinal(framework, goRouteJoin(framework, prefix, literal)), true
}

// goRouteLiteral is a static path argument. gin, echo and fiber accept
// relative and empty paths (they join or prepend the slash themselves);
// anything else must be an absolute path.
func goRouteLiteral(framework, expr string, constants map[string]string) (string, bool) {
	value, ok := staticStringExpressionValue(expr, constants)
	if !ok {
		return "", false
	}
	switch framework {
	case "gin", "echo", "fiber":
		return value, true
	}
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	return value, true
}

// goRouteJoin composes a router's prefix with a path the way that framework
// does ("" prefix is the root):
//   - gin: path.Join, keeping a trailing slash of the relative path, with ""
//     meaning the group path itself (gin joinPaths).
//   - echo: verbatim concatenation (g.prefix + path).
//   - fiber: TrimRight(prefix, "/") + path, with "" meaning the group path
//     and a missing leading slash added (fiber getGroupPath).
//   - gorilla: TrimRight(parent template, "/") + template (mux route.go).
//   - chi, net/http and unclassified receivers: joinRoutePaths.
func goRouteJoin(framework, prefix, rel string) string {
	switch framework {
	case "gin":
		abs := prefix
		if abs == "" {
			abs = "/"
		}
		if rel == "" {
			return abs
		}
		joined := path.Join(abs, rel)
		if strings.HasSuffix(rel, "/") && !strings.HasSuffix(joined, "/") {
			joined += "/"
		}
		return joined
	case "echo":
		return prefix + rel
	case "fiber":
		if prefix == "" {
			return rel
		}
		if rel == "" {
			return prefix
		}
		if rel[0] != '/' {
			rel = "/" + rel
		}
		return strings.TrimRight(prefix, "/") + rel
	case "mux":
		if prefix == "" {
			return rel
		}
		return strings.TrimRight(prefix, "/") + rel
	}
	if prefix == "" {
		return rel
	}
	return joinRoutePaths(prefix, rel)
}

// goRouteFinal is the route a router registers for a composed path: gin, echo
// and fiber serve "" at "/" and add a missing leading slash.
func goRouteFinal(framework, route string) string {
	if route == "" {
		route = "/"
	}
	if route[0] != '/' {
		route = "/" + route
	}
	return normalizeRouteParamSyntax(route)
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

// goRouteRouteArgIndex is the index of the path argument of a registration
// call named name, or -1 when name does not register a route.
func goRouteRouteArgIndex(name string) int {
	switch {
	case name == "HandleFunc" || name == "Handle" || goRouteMethodNames[name]:
		return 0
	case name == "Method" || name == "MethodFunc":
		// chi r.Method("GET", "/x", h).
		return 1
	}
	return -1
}

// noteCall records call as a route registration candidate when it has one of
// the registration shapes, with its receiver's state at this point of the walk.
func (r *goRouteResolver) noteCall(call *ast.CallExpr, scope *goRouteScope) {
	if call.Ellipsis.IsValid() {
		return
	}
	// A bare HandleFunc(...) (a local function or a dot import) has no
	// receiver this pass can place.
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	name := selector.Sel.Name
	index := goRouteRouteArgIndex(name)
	if index < 0 || len(call.Args) < index+2 {
		return
	}
	candidate := goRouteCandidate{offset: r.offset(call.Pos()), route: r.text(call.Args[index])}
	chainOrder, chainEvidence := 2, "go_router_method"
	// Only the two-argument forms name their handler; Method/MethodFunc,
	// middleware arguments and wrapped handlers are inline.
	named := index == 0 && len(call.Args) == 2
	switch {
	case name == "HandleFunc" || name == "MethodFunc":
		candidate.order, candidate.evidence = 0, "go_http_handle_func"
		chainOrder, chainEvidence = 0, candidate.evidence
		if named {
			candidate.handler = goRouteHandlerText(call.Args[1])
		}
	case name == "Handle" || name == "Method":
		candidate.order, candidate.evidence = 1, "go_http_handler_func"
		chainOrder, chainEvidence = 1, candidate.evidence
		if named {
			candidate.handler = goRouteHandlerFuncArg(call.Args[1])
		}
	default:
		candidate.order, candidate.evidence = 2, "go_router_method"
		if named {
			candidate.handler = goRouteHandlerText(call.Args[1])
		}
	}
	candidate.inline = candidate.handler == ""
	switch x := selector.X.(type) {
	case *ast.Ident:
		candidate.receiver, _ = r.lookupIn(scope)(x.Name)
	case *ast.CallExpr:
		// A chain: g.Group("/v1").GET, r.With(mw).Get,
		// r.PathPrefix("/a").Subrouter().HandleFunc.
		candidate.order, candidate.evidence = chainOrder, chainEvidence
		if inner, ok := x.Fun.(*ast.SelectorExpr); ok && inner.Sel.Name == "Group" && goRouteMethodNames[name] {
			candidate.order, candidate.evidence = 3, "go_router_group_method"
		}
		candidate.receiver = r.groupValue(x, r.lookupIn(scope))
	default:
		// A field or other selector receiver (s.api.GET, s.mux.HandleFunc)
		// holds a prefix this pass does not track.
		return
	}
	switch candidate.receiver.framework {
	case "http":
		// net/http: only HandleFunc and Handle register; http.Get/Post are
		// client calls and a ServeMux has no verb methods.
		if name != "HandleFunc" && name != "Handle" {
			return
		}
	case "gin", "httprouter":
		// gin and httprouter Handle(method, path, handlers...): the path is
		// the second argument, and a call with no handler registers nothing.
		if name == "Handle" {
			if len(call.Args) < 3 {
				return
			}
			candidate.route = r.text(call.Args[1])
			candidate.handler, candidate.inline = "", true
		}
	}
	r.regs = append(r.regs, candidate)
}

// maskSpans lists the byte spans of every route argument in the file that
// the route pass decides: each registration-shaped call (whatever its
// receiver or handler) and each Group/Route/Mount/PathPrefix/StripPrefix
// prefix (a prefix is not a route). They are keyed by position, so an
// identical literal elsewhere is unaffected.
func (r *goRouteResolver) maskSpans() [][2]int {
	var spans [][2]int
	add := func(arg ast.Expr) {
		start, end := r.offset(arg.Pos()), r.offset(arg.End())
		if start >= 0 && end <= len(r.src) && start < end {
			spans = append(spans, [2]int{start, end})
		}
	}
	// Comments are never routes: a commented-out registration or a doc
	// example must not reach the route literal fallback either.
	for _, group := range r.file.Comments {
		for _, comment := range group.List {
			start, end := r.offset(comment.Pos()), r.offset(comment.End())
			if start >= 0 && end <= len(r.src) && start < end {
				spans = append(spans, [2]int{start, end})
			}
		}
	}
	ast.Inspect(r.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		case *ast.Ident:
			if fun.Name != "HandleFunc" && fun.Name != "Handle" {
				return true
			}
			name = fun.Name
		default:
			return true
		}
		if index := goRouteRouteArgIndex(name); index >= 0 && len(call.Args) >= index+2 {
			add(call.Args[index])
			if name == "Handle" && !goRouteIsPathLiteral(call.Args[0]) {
				// gin/httprouter Handle(method, path, ...).
				add(call.Args[1])
			}
			return true
		}
		switch name {
		case "Group", "Route", "Mount", "PathPrefix", "StripPrefix":
			if len(call.Args) >= 1 && !goRouteIsFuncLit(call.Args[0]) {
				add(call.Args[0])
			}
		}
		return true
	})
	return spans
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

// goRouteIsPathLiteral reports a string literal that starts with a slash.
func goRouteIsPathLiteral(expr ast.Expr) bool {
	lit, ok := goRouteUnparen(expr).(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && len(lit.Value) > 1 && lit.Value[1] == '/'
}
