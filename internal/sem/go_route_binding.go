package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// Go router-group receiver resolution.
//
// A route registered on a group receiver (`g.GET("/x", h)`) is only as good as
// the prefix g holds at that call. The previous implementation kept one prefix
// per variable name for the whole file and iterated it to a fixed point, which
// both failed to terminate (rebound names, naming cycles) and, once bounded,
// handed the final value of a name to every use of it: an earlier registration
// got a later self-update, one function got another function's `g`, an inner
// shadow leaked out, and `g = replacement` kept its stale prefix.
//
// This resolver walks the syntax tree once, in statement order, and records
// the prefix state of every identifier at its use site:
//
//   - untracked: a parameter or variable of a local type this pass cannot
//     see into (func register(root *Router)). Treated as the root router: the
//     long-standing heuristic, kept only for this case.
//   - known: bound to a Group call whose prefix and parent are both static. A
//     known empty prefix is still known; it is not the same state as unknown.
//   - unknown: the value depends on something this pass models as unknown.
//     A registration on an unknown receiver is omitted, and so are its
//     descendants and chained Group calls.
//
// Tracked subset: package-level var initializers (resolved by dependency,
// independent of textual order; cycles and duplicates are unknown);
// straight-line locals in statement order with the RHS evaluated before the
// LHS; lexical block scopes and shadowing (blocks, if/for/switch inits, range
// and type-switch bindings, function parameters). Conservatively unknown: a
// tracked variable written inside a branch, loop, switch, select or closure
// (after it, and throughout a loop body); outer variables inside a closure
// when written anywhere in the enclosing function; any variable whose address
// is taken (from then on); a whole function that uses goto; package variables
// written from any function (in every other function); Group on a field or
// index receiver; field or index receivers of route methods (s.api.GET); and
// every receiver in a file whose walk exceeds its step budget.
//
// A receiver whose origin is not visible is unknown, never the root: a group
// or sub-router parameter, a call result, a field, and a name this file never
// declares (see "Router origins" below).
//
// Route closures (chi, fiber) are walked with their parameter bound to the
// parent's prefix plus the Route prefix; chi Group(func) and With keep the
// parent's prefix. A router instance is served where it is mounted: a single,
// unconditional chi Mount with a static prefix of an instance created in
// this file is composed; any other Mount, and any escape of a mountable
// instance (returned, passed to a call other than a serve or Handle call,
// stored in a field, composite or package variable, addressed, or declared
// at package level), makes all of its routes unknown.
//
// KNOWN LIMITATIONS: a parameter of an unclassified local type keeps the root
// heuristic. gorilla Subrouter and http.StripPrefix prefixes are not
// modelled. A Group prefix without a leading slash (gin's
// Group("v1")) is framework-normalized and is omitted here rather than
// guessed. No type information, provider or filesystem is involved.

type goRouteBindingKind uint8

const (
	goRouteUntracked goRouteBindingKind = iota
	goRouteKnown
	goRouteUnknown
)

type goRouteBinding struct {
	kind   goRouteBindingKind
	prefix string
	// origin is the constructor call a known router value descends from, or
	// nil for a root that cannot be mounted elsewhere (a framework-root
	// parameter, the net/http package).
	origin *goRouteOrigin
}

// goRouteOrigin is one router instance created by a constructor call. Every
// value derived from it (aliases, groups, Route closures) shares it, so what
// happens to the instance anywhere in its lifetime (being mounted under a
// prefix, escaping to code that may mount it) applies to every registration
// on it, before or after.
type goRouteOrigin struct {
	framework string
	// mountable routers (chi, fiber) can be mounted under a prefix by
	// whoever receives them.
	mountable bool
	// escaped: returned, passed, stored or captured by address where it may
	// be mounted. Unknown for a mountable router.
	escaped bool
	// opaque: mounted or wrapped in a way this pass cannot compose.
	opaque bool
	region int
	// mounts counts in-file Mount calls with a composable prefix; the prefix
	// is mountPrefix under mountParent (nil for a root).
	mounts      int
	mountParent *goRouteOrigin
	mountPrefix string
}

// prefix is the path the instance is served under.
func (o *goRouteOrigin) prefix(depth int) (string, bool) {
	if o.opaque || (o.mountable && o.escaped) || o.mounts > 1 || depth > 16 {
		return "", false
	}
	if o.mounts == 0 {
		return "", true
	}
	base := ""
	if o.mountParent != nil {
		parent, ok := o.mountParent.prefix(depth + 1)
		if !ok {
			return "", false
		}
		base = parent
	}
	if len(base)+len(o.mountPrefix) > goRouteMaxPrefixBytes {
		return "", false
	}
	return joinRoutePaths(base, o.mountPrefix), true
}

var goRouteUnknownBinding = goRouteBinding{kind: goRouteUnknown}

type goRouteScope struct {
	parent *goRouteScope
	vars   map[string]*goRouteBinding
}

func newGoRouteScope(parent *goRouteScope) *goRouteScope {
	return &goRouteScope{parent: parent, vars: map[string]*goRouteBinding{}}
}

func (s *goRouteScope) lookup(name string) *goRouteBinding {
	for scope := s; scope != nil; scope = scope.parent {
		if binding, ok := scope.vars[name]; ok {
			return binding
		}
	}
	return nil
}

type goRouteWrite struct {
	binding *goRouteBinding
	old     goRouteBinding
}

// goRouteReceivers answers "what prefix does the receiver identifier at this
// byte offset hold". Offsets are into the original content.
type goRouteReceivers struct {
	parsed bool
	// exhausted means the walk ran out of its step budget: no receiver in the
	// file is trusted, so every group-capable registration is omitted.
	exhausted bool
	uses      map[int]goRouteBinding
	// groupNames are names bound to a Group value anywhere in the file. A
	// receiver the walk did not visit (text in a comment, a selector field, an
	// unparseable file) is omitted when its name is one of these, and treated
	// as the root router otherwise, exactly as before this resolver existed.
	groupNames map[string]bool
	// regs are the route registrations found at real call expressions, with
	// each receiver's state at its call.
	regs []goRouteCandidate
}

func (r goRouteReceivers) at(offset int, name string) goRouteBinding {
	if r.exhausted {
		return goRouteUnknownBinding
	}
	if r.parsed {
		if binding, ok := r.uses[offset]; ok {
			return binding
		}
	}
	if r.groupNames[name] {
		return goRouteUnknownBinding
	}
	return goRouteBinding{}
}

func resolveGoRouteReceivers(content string, constants map[string]string) goRouteReceivers {
	return resolveGoRouteReceiversWithBudget(content, constants, goRouteStepBudgetBase+goRouteStepBudgetPerByte*len(content))
}

// goRouteGroupBindingRe finds every name assigned from any .Group( call, with
// any receiver and any arguments, for the name-based fallback.
var goRouteGroupBindingRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=)\s*[A-Za-z_][A-Za-z0-9_.]*\.Group\s*\(`)

func resolveGoRouteReceiversWithBudget(content string, constants map[string]string, budget int) goRouteReceivers {
	receivers := goRouteReceivers{groupNames: map[string]bool{}}
	for _, match := range goRouteGroupBindingRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			receivers.groupNames[match[1]] = true
		}
	}
	fset := token.NewFileSet()
	shift := 0
	file, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution)
	if err != nil {
		// Fragments without a package clause are still worth resolving. A file
		// that does not parse either way keeps the name-based fallback.
		const clause = "package routes\n"
		fset = token.NewFileSet()
		file, err = parser.ParseFile(fset, "", clause+content, parser.SkipObjectResolution)
		if err != nil {
			return receivers
		}
		shift = len(clause)
	}
	resolver := &goRouteResolver{
		fset:       fset,
		tokFile:    fset.File(file.Pos()),
		src:        content,
		shift:      shift,
		constants:  constants,
		file:       file,
		groupNames: receivers.groupNames,
		budget:     budget,
		imports:    goRouteImports(file),
	}
	if !resolver.resolve() {
		receivers.exhausted = true
		return receivers
	}
	receivers.parsed = true
	receivers.uses = resolver.uses
	receivers.regs = resolver.regs
	return receivers
}

type goRouteResolver struct {
	fset      *token.FileSet
	tokFile   *token.File
	src       string
	shift     int
	constants map[string]string
	file      *ast.File

	uses map[int]goRouteBinding
	regs []goRouteCandidate
	// imports maps each import's local name to the router framework it is
	// (goRouteFrameworkForPath), or "" for any other package.
	imports    map[string]string
	log        []goRouteWrite
	groupNames map[string]bool
	// pkgWritten collects package variables assigned from inside a function.
	pkgWritten map[string]bool
	pkgUnknown map[string]bool
	collecting bool
	// fnWritten holds the names assigned anywhere in the enclosing top-level
	// declaration, with whether any such write could carry a group value.
	fnWritten map[string]bool
	opaque    int
	// escaped holds bindings whose address was taken: a write through the
	// pointer can happen at any later point, so every later read is unknown.
	escaped map[*goRouteBinding]bool
	steps   int
	budget  int

	// origins holds this walk's router instances by constructor call.
	origins map[token.Pos]*goRouteOrigin
	// safe identifiers are router uses that cannot mount the router under
	// another prefix: a method receiver, an alias assignment, a serve or
	// Handle argument, an http.Server Handler, a Mount argument (composed or
	// made opaque by noteMount).
	safe map[*ast.Ident]bool
	// atPackage is set while package-level initializers are evaluated.
	atPackage bool
	// region identifies the straight-line region being walked; branches,
	// loop bodies and closures each get a fresh one.
	region     int
	nextRegion int
}

// The walk is linear in the common case but can degrade with nesting depth
// (scope chains, nested regions and closures re-scan their bodies). The
// budget is linear in the input; exceeding it abandons resolution for the
// file and omits its group-capable routes instead of guessing.
const (
	goRouteStepBudgetBase    = 1 << 21
	goRouteStepBudgetPerByte = 64
	// goRouteMaxPrefixBytes caps a composed prefix. Longer ones are unknown,
	// which also bounds the memory of pathological self-extending chains.
	goRouteMaxPrefixBytes = 2048
)

type goRouteBudgetExceeded struct{}

func (r *goRouteResolver) step(n int) {
	r.steps += n
	if r.steps > r.budget {
		panic(goRouteBudgetExceeded{})
	}
}

func (r *goRouteResolver) lookup(scope *goRouteScope, name string) *goRouteBinding {
	for s := scope; s != nil; s = s.parent {
		r.step(1)
		if binding, ok := s.vars[name]; ok {
			return binding
		}
	}
	return nil
}

// read is the value a use of binding observes.
func (r *goRouteResolver) read(binding *goRouteBinding) goRouteBinding {
	if binding == nil {
		return goRouteBinding{}
	}
	if r.escaped[binding] {
		return goRouteUnknownBinding
	}
	return *binding
}

func (r *goRouteResolver) offset(pos token.Pos) int {
	return r.tokFile.Offset(pos) - r.shift
}

func (r *goRouteResolver) text(node ast.Node) string {
	start, end := r.offset(node.Pos()), r.offset(node.End())
	if start < 0 || end > len(r.src) || start > end {
		return ""
	}
	return r.src[start:end]
}

// resolve reports false when the step budget ran out.
func (r *goRouteResolver) resolve() (ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, exceeded := recovered.(goRouteBudgetExceeded); !exceeded {
				panic(recovered)
			}
			ok = false
		}
	}()
	r.computeSafe()
	r.pkgWritten = map[string]bool{}
	r.collecting = true
	r.walkFile()
	if len(r.pkgWritten) == 0 {
		return true
	}
	// A package variable written by some function holds whatever the last
	// executed writer left there; no other function can rely on it. Walk again
	// with those bindings unknown (the writing function still sees its own
	// straight-line writes).
	r.pkgUnknown = r.pkgWritten
	r.collecting = false
	r.walkFile()
	return true
}

func (r *goRouteResolver) walkFile() {
	r.uses = map[int]goRouteBinding{}
	r.regs = r.regs[:0]
	r.origins = map[token.Pos]*goRouteOrigin{}
	r.escaped = map[*goRouteBinding]bool{}
	r.log = r.log[:0]
	r.atPackage = true
	pkg := r.packageScope()
	r.atPackage = false
	for _, decl := range r.file.Decls {
		mark := len(r.log)
		r.nextRegion++
		r.region = r.nextRegion
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Body == nil {
				continue
			}
			r.fnWritten = r.writtenNames(decl.Body)
			r.walkFunc(decl.Recv, decl.Type, decl.Body, pkg, nil)
		case *ast.GenDecl:
			if decl.Tok != token.VAR {
				continue
			}
			r.fnWritten = r.writtenNames(decl)
			r.atPackage = true
			for _, spec := range decl.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					r.exprs(valueSpec.Values, pkg)
				}
			}
			r.atPackage = false
		}
		r.undo(mark)
	}
}

// packageScope resolves package-level var initializers by dependency, so a
// chain declared child-first (legal Go) composes like one declared in order.
func (r *goRouteResolver) packageScope() *goRouteScope {
	inits := map[string]ast.Expr{}
	types := map[string]ast.Expr{}
	unpaired := map[string]bool{}
	duplicate := map[string]bool{}
	declared := map[string]bool{}
	for _, decl := range r.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if name.Name == "_" {
					continue
				}
				if declared[name.Name] {
					duplicate[name.Name] = true
				}
				declared[name.Name] = true
				switch {
				case len(valueSpec.Values) == len(valueSpec.Names):
					inits[name.Name] = valueSpec.Values[i]
				case len(valueSpec.Values) > 0:
					unpaired[name.Name] = true
				case valueSpec.Type != nil:
					types[name.Name] = valueSpec.Type
				}
			}
		}
	}
	resolved := map[string]goRouteBinding{}
	visiting := map[string]bool{}
	var resolveName func(name string) goRouteBinding
	var lookup goRouteLookup = func(name string) (goRouteBinding, bool) {
		if !declared[name] {
			return r.undeclaredValue(name), false
		}
		return resolveName(name), true
	}
	resolveName = func(name string) goRouteBinding {
		if binding, ok := resolved[name]; ok {
			return binding
		}
		if duplicate[name] || visiting[name] {
			return goRouteUnknownBinding
		}
		visiting[name] = true
		binding := goRouteBinding{}
		switch {
		case inits[name] != nil:
			binding = r.groupValue(inits[name], lookup)
		case unpaired[name]:
			// One of several results of a call.
			binding = goRouteUnknownBinding
		case types[name] != nil:
			binding = r.typeValue(types[name])
		}
		delete(visiting, name)
		resolved[name] = binding
		return binding
	}
	scope := newGoRouteScope(nil)
	for name := range declared {
		binding := resolveName(name)
		if r.pkgUnknown[name] {
			binding = goRouteUnknownBinding
		}
		r.noteGroup(name, binding)
		scope.vars[name] = &binding
	}
	for name := range r.pkgUnknown {
		if _, ok := scope.vars[name]; !ok {
			binding := goRouteUnknownBinding
			scope.vars[name] = &binding
		}
	}
	return scope
}

func (r *goRouteResolver) noteGroup(name string, binding goRouteBinding) {
	if binding.kind != goRouteUntracked {
		r.groupNames[name] = true
	}
}

func (r *goRouteResolver) set(name string, binding *goRouteBinding, value goRouteBinding) {
	r.log = append(r.log, goRouteWrite{binding: binding, old: *binding})
	*binding = value
	r.noteGroup(name, value)
}

func (r *goRouteResolver) undo(mark int) {
	for i := len(r.log) - 1; i >= mark; i-- {
		*r.log[i].binding = r.log[i].old
	}
	r.log = r.log[:mark]
}

func (r *goRouteResolver) declare(scope *goRouteScope, name string, value goRouteBinding) {
	if name == "_" {
		return
	}
	if r.opaque > 0 && value.kind == goRouteKnown {
		value = goRouteUnknownBinding
	}
	binding := value
	scope.vars[name] = &binding
	r.noteGroup(name, value)
}

// assignTo applies a plain `=` write. A tracked variable overwritten by a value
// that is not a Group (a parameter, a call result) no longer has any prefix
// this pass can name: it becomes unknown rather than silently re-rooted.
func (r *goRouteResolver) assignTo(name string, binding *goRouteBinding, value goRouteBinding) {
	if value.kind == goRouteUntracked && binding.kind != goRouteUntracked {
		value = goRouteUnknownBinding
	}
	if r.opaque > 0 && value.kind == goRouteKnown {
		value = goRouteUnknownBinding
	}
	r.set(name, binding, value)
}

// goRouteLookup returns a name's state and whether a scope declares it.
type goRouteLookup func(name string) (goRouteBinding, bool)

func (r *goRouteResolver) lookupIn(scope *goRouteScope) goRouteLookup {
	return func(name string) (goRouteBinding, bool) {
		binding := r.lookup(scope, name)
		if binding == nil {
			return r.undeclaredValue(name), false
		}
		return r.read(binding), true
	}
}

// groupValue is the prefix state an expression produces when stored.
func (r *goRouteResolver) groupValue(expr ast.Expr, lookup goRouteLookup) goRouteBinding {
	r.step(1)
	switch expr := expr.(type) {
	case *ast.ParenExpr:
		return r.groupValue(expr.X, lookup)
	case *ast.Ident:
		value, _ := lookup(expr.Name)
		return value
	case *ast.CallExpr:
		selector, ok := expr.Fun.(*ast.SelectorExpr)
		if !ok {
			// The result of a local function: whatever router it returns, its
			// prefix was set where this pass does not look.
			return goRouteUnknownBinding
		}
		if root, ok := r.constructorValue(expr, selector, lookup); ok {
			return root
		}
		name := selector.Sel.Name
		if name != "Group" && name != "Route" && name != "With" {
			return goRouteUnknownBinding
		}
		if name != "With" && (len(expr.Args) == 0 || expr.Ellipsis.IsValid()) {
			return goRouteUnknownBinding
		}
		var parent goRouteBinding
		switch receiver := selector.X.(type) {
		case *ast.Ident:
			parent, _ = lookup(receiver.Name)
		case *ast.CallExpr, *ast.ParenExpr:
			parent = r.groupValue(receiver, lookup)
		default:
			// A field or index receiver (s.api.Group) may hold any prefix.
			return goRouteUnknownBinding
		}
		switch {
		case name == "With":
			// chi With(middlewares...) is the same router with middleware.
			return parent
		case name == "Group" && len(expr.Args) == 1 && goRouteIsFuncLit(expr.Args[0]):
			// chi Group(func(r chi.Router) {...}): same prefix, new middleware
			// stack.
			return parent
		case name == "Route" && (len(expr.Args) < 2 || !goRouteIsFuncLit(expr.Args[1])):
			// chi/fiber Route(prefix, func(r Router) {...}) only.
			return goRouteUnknownBinding
		}
		argument := r.text(expr.Args[0])
		prefix, ok := staticRouteExpressionValue(argument, r.constants)
		if !ok {
			// Group("") is the parent itself: a known empty prefix, not unknown.
			if value, isStatic := staticStringExpressionValue(argument, r.constants); isStatic && value == "" {
				prefix, ok = "", true
			}
		}
		if !ok || parent.kind == goRouteUnknown {
			return goRouteUnknownBinding
		}
		if parent.kind == goRouteKnown && parent.prefix != "" {
			if len(parent.prefix)+len(prefix) > goRouteMaxPrefixBytes {
				return goRouteUnknownBinding
			}
			prefix = joinRoutePaths(parent.prefix, prefix)
		}
		return goRouteBinding{kind: goRouteKnown, prefix: prefix, origin: parent.origin}
	case *ast.UnaryExpr:
		if expr.Op == token.AND {
			return r.groupValue(expr.X, lookup)
		}
		return goRouteBinding{}
	case *ast.CompositeLit:
		if r.isFrameworkType(expr.Type, "http", "ServeMux") {
			return goRouteBinding{kind: goRouteKnown}
		}
		return goRouteUnknownBinding
	case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.StarExpr, *ast.TypeAssertExpr:
		// A value read from a field, map, slice, pointer or interface: if it
		// is a group, its prefix was set somewhere this pass does not follow.
		return goRouteUnknownBinding
	default:
		return goRouteBinding{}
	}
}

func (r *goRouteResolver) walkFunc(recv *ast.FieldList, typ *ast.FuncType, body *ast.BlockStmt, outer *goRouteScope, seed *goRouteBinding) {
	scope := newGoRouteScope(outer)
	if seed != nil && (typ.Params == nil || len(typ.Params.List) != 1 || len(typ.Params.List[0].Names) != 1) {
		seed = nil
	}
	for _, fields := range []*ast.FieldList{recv, typ.Params, typ.Results} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			value := r.typeValue(field.Type)
			if seed != nil && fields == typ.Params {
				// The router a Route/Group closure is called with.
				value = *seed
			}
			for _, name := range field.Names {
				r.declare(scope, name.Name, value)
			}
		}
	}
	if r.hasGoto(body) {
		// Statement order no longer describes execution order.
		r.markNames(scope, r.fnWritten)
		r.opaque++
		defer func() { r.opaque-- }()
	}
	r.walkStmts(body.List, scope)
}

func (r *goRouteResolver) walkStmts(stmts []ast.Stmt, scope *goRouteScope) {
	for _, stmt := range stmts {
		r.walkStmt(stmt, scope)
	}
}

func (r *goRouteResolver) walkStmt(stmt ast.Stmt, scope *goRouteScope) {
	r.step(1)
	switch stmt := stmt.(type) {
	case nil:
	case *ast.AssignStmt:
		r.assign(stmt, scope)
	case *ast.DeclStmt:
		gen, ok := stmt.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			r.exprs(valueSpec.Values, scope)
			values := make([]goRouteBinding, len(valueSpec.Names))
			switch {
			case len(valueSpec.Values) == len(valueSpec.Names):
				for i, value := range valueSpec.Values {
					values[i] = r.groupValue(value, r.lookupIn(scope))
				}
			case len(valueSpec.Values) > 0:
				for i := range values {
					values[i] = goRouteUnknownBinding
				}
			case valueSpec.Type != nil:
				for i := range values {
					values[i] = r.typeValue(valueSpec.Type)
				}
			}
			for i, name := range valueSpec.Names {
				r.declare(scope, name.Name, values[i])
			}
		}
	case *ast.ExprStmt:
		r.expr(stmt.X, scope)
	case *ast.GoStmt:
		r.expr(stmt.Call, scope)
	case *ast.DeferStmt:
		r.expr(stmt.Call, scope)
	case *ast.ReturnStmt:
		r.exprs(stmt.Results, scope)
	case *ast.SendStmt:
		r.expr(stmt.Chan, scope)
		r.expr(stmt.Value, scope)
	case *ast.IncDecStmt:
		r.expr(stmt.X, scope)
		r.invalidateTarget(stmt.X, scope)
	case *ast.BlockStmt:
		r.walkStmts(stmt.List, newGoRouteScope(scope))
	case *ast.LabeledStmt:
		r.walkStmt(stmt.Stmt, scope)
	case *ast.IfStmt:
		ifScope := newGoRouteScope(scope)
		r.walkStmt(stmt.Init, ifScope)
		r.expr(stmt.Cond, ifScope)
		written := r.writtenNames(stmt.Body)
		branches := []func(){func() { r.walkStmts(stmt.Body.List, newGoRouteScope(ifScope)) }}
		if stmt.Else != nil {
			mergeGoRouteWritten(written, r.writtenNames(stmt.Else))
			branches = append(branches, func() { r.walkStmt(stmt.Else, ifScope) })
		}
		r.branches(ifScope, written, branches)
	case *ast.ForStmt:
		forScope := newGoRouteScope(scope)
		r.walkStmt(stmt.Init, forScope)
		written := r.writtenNames(stmt.Body)
		if stmt.Post != nil {
			mergeGoRouteWritten(written, r.writtenNames(stmt.Post))
		}
		r.branches(forScope, written, []func(){func() {
			r.expr(stmt.Cond, forScope)
			r.walkStmts(stmt.Body.List, newGoRouteScope(forScope))
			r.walkStmt(stmt.Post, forScope)
		}})
	case *ast.RangeStmt:
		r.expr(stmt.X, scope)
		rangeScope := newGoRouteScope(scope)
		written := r.writtenNames(stmt.Body)
		for _, target := range []ast.Expr{stmt.Key, stmt.Value} {
			if target == nil {
				continue
			}
			if stmt.Tok == token.DEFINE {
				// An element of a collection: whatever group it is, its prefix
				// is not in this statement.
				if ident, ok := target.(*ast.Ident); ok {
					r.declare(rangeScope, ident.Name, goRouteUnknownBinding)
				}
				continue
			}
			r.expr(target, scope)
			if ident, ok := target.(*ast.Ident); ok {
				written[ident.Name] = true
			}
		}
		r.branches(rangeScope, written, []func(){func() {
			r.walkStmts(stmt.Body.List, newGoRouteScope(rangeScope))
		}})
	case *ast.SwitchStmt:
		switchScope := newGoRouteScope(scope)
		r.walkStmt(stmt.Init, switchScope)
		r.expr(stmt.Tag, switchScope)
		r.clauses(switchScope, stmt.Body, nil)
	case *ast.TypeSwitchStmt:
		switchScope := newGoRouteScope(scope)
		r.walkStmt(stmt.Init, switchScope)
		var bound string
		switch assign := stmt.Assign.(type) {
		case *ast.AssignStmt:
			r.exprs(assign.Rhs, switchScope)
			if len(assign.Lhs) == 1 {
				if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
					bound = ident.Name
				}
			}
		case *ast.ExprStmt:
			r.expr(assign.X, switchScope)
		}
		r.clauses(switchScope, stmt.Body, func(clauseScope *goRouteScope) {
			if bound != "" {
				// The switched value's prefix is not known either.
				r.declare(clauseScope, bound, goRouteUnknownBinding)
			}
		})
	case *ast.SelectStmt:
		r.clauses(scope, stmt.Body, nil)
	}
}

// clauses walks switch/select clauses as alternative branches. A clause ending
// in fallthrough reaches the next one; the pre-marking in branches already
// makes anything the previous clause wrote unknown to it.
func (r *goRouteResolver) clauses(scope *goRouteScope, body *ast.BlockStmt, bind func(*goRouteScope)) {
	written := r.writtenNames(body)
	var branches []func()
	for _, clause := range body.List {
		switch clause := clause.(type) {
		case *ast.CaseClause:
			r.exprs(clause.List, scope)
			branches = append(branches, func() {
				clauseScope := newGoRouteScope(scope)
				if bind != nil {
					bind(clauseScope)
				}
				r.walkStmts(clause.Body, clauseScope)
			})
		case *ast.CommClause:
			branches = append(branches, func() {
				clauseScope := newGoRouteScope(scope)
				r.walkStmt(clause.Comm, clauseScope)
				r.walkStmts(clause.Body, clauseScope)
			})
		}
	}
	r.branches(scope, written, branches)
}

// branches walks code that may run zero, one, or many times, or instead of a
// sibling branch. Every outer variable any branch may write is unknown before
// each branch (a previous iteration or fallthrough may have written it) and
// after all of them (whether it was written depends on the path taken). Each
// branch's own writes are rolled back so siblings never see them.
func (r *goRouteResolver) branches(scope *goRouteScope, written map[string]bool, branches []func()) {
	r.markNames(scope, written)
	savedRegion := r.region
	for _, branch := range branches {
		mark := len(r.log)
		r.nextRegion++
		r.region = r.nextRegion
		branch()
		r.region = savedRegion
		r.undo(mark)
	}
}

// markNames makes the visible bindings of written names unknown. An untracked
// binding stays untracked unless some write could carry a group value into it.
func (r *goRouteResolver) markNames(scope *goRouteScope, written map[string]bool) {
	for name, groupish := range written {
		r.step(1)
		binding := r.lookup(scope, name)
		if binding == nil {
			// Not declared in this file: a package variable from a sibling file.
			if r.collecting && groupish {
				r.pkgWritten[name] = true
			}
			continue
		}
		if binding.kind == goRouteUntracked && !groupish {
			continue
		}
		if r.collecting && r.isPackageBinding(scope, name) {
			r.pkgWritten[name] = true
		}
		r.set(name, binding, goRouteUnknownBinding)
	}
}

func (r *goRouteResolver) isPackageBinding(scope *goRouteScope, name string) bool {
	for s := scope; s != nil; s = s.parent {
		if _, ok := s.vars[name]; ok {
			return s.parent == nil
		}
	}
	return false
}

func (r *goRouteResolver) assign(stmt *ast.AssignStmt, scope *goRouteScope) {
	// Go evaluates every right-hand operand before assigning any left side.
	r.exprs(stmt.Rhs, scope)
	values := make([]goRouteBinding, len(stmt.Lhs))
	paired := len(stmt.Lhs) == len(stmt.Rhs)
	for i := range values {
		if paired {
			values[i] = r.groupValue(stmt.Rhs[i], r.lookupIn(scope))
		} else {
			// One of several results of a call (or a comma-ok form).
			values[i] = goRouteUnknownBinding
		}
	}
	for i, target := range stmt.Lhs {
		ident, ok := target.(*ast.Ident)
		if !ok {
			r.expr(target, scope)
			continue
		}
		if ident.Name == "_" {
			continue
		}
		switch stmt.Tok {
		case token.DEFINE:
			if binding, exists := scope.vars[ident.Name]; exists {
				r.assignTo(ident.Name, binding, values[i])
			} else {
				r.declare(scope, ident.Name, values[i])
			}
		case token.ASSIGN:
			binding := r.lookup(scope, ident.Name)
			if values[i].origin != nil && (binding == nil || r.isPackageBinding(scope, ident.Name)) {
				// Stored where a sibling file may mount it.
				values[i].origin.escaped = true
			}
			mayCarryGroup := !paired || values[i].kind != goRouteUntracked
			if r.collecting && (binding == nil || r.isPackageBinding(scope, ident.Name)) &&
				(mayCarryGroup || (binding != nil && binding.kind != goRouteUntracked)) {
				r.pkgWritten[ident.Name] = true
			}
			if binding == nil {
				continue
			}
			r.assignTo(ident.Name, binding, values[i])
		default:
			r.invalidateTarget(ident, scope)
		}
	}
}

func (r *goRouteResolver) invalidateTarget(target ast.Expr, scope *goRouteScope) {
	ident, ok := target.(*ast.Ident)
	if !ok {
		return
	}
	if binding := r.lookup(scope, ident.Name); binding != nil && binding.kind != goRouteUntracked {
		r.set(ident.Name, binding, goRouteUnknownBinding)
	}
}

func (r *goRouteResolver) exprs(exprs []ast.Expr, scope *goRouteScope) {
	for _, expr := range exprs {
		r.expr(expr, scope)
	}
}

// expr records the state of every identifier used in expr, and walks closures.
func (r *goRouteResolver) expr(expr ast.Expr, scope *goRouteScope) {
	if expr == nil {
		return
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		r.step(1)
		switch node := node.(type) {
		case *ast.UnaryExpr:
			if node.Op == token.AND {
				r.expr(node.X, scope)
				if ident, ok := goRouteUnparen(node.X).(*ast.Ident); ok {
					r.escape(scope, ident.Name)
				}
				return false
			}
		case *ast.FuncLit:
			r.funcLit(node, scope, nil)
			return false
		case *ast.CallExpr:
			r.noteCall(node, scope)
			r.noteMount(node, scope)
			if lit, seed, ok := r.routeClosure(node, scope); ok {
				r.expr(node.Fun, scope)
				for _, arg := range node.Args {
					if arg != lit {
						r.expr(arg, scope)
					}
				}
				r.funcLit(lit, scope, &seed)
				return false
			}
		case *ast.SelectorExpr:
			// Sel is a field or method name, not a variable use.
			r.expr(node.X, scope)
			return false
		case *ast.KeyValueExpr:
			if _, ok := node.Key.(*ast.Ident); !ok {
				r.expr(node.Key, scope)
			}
			r.expr(node.Value, scope)
			return false
		case *ast.Ident:
			value := r.read(r.lookup(scope, node.Name))
			r.uses[r.offset(node.Pos())] = value
			if value.origin != nil && !r.safe[node] {
				// The router itself is passed, returned or stored: whoever
				// receives it may mount it under a prefix.
				value.origin.escaped = true
			}
		}
		return true
	})
}

// funcLit walks a closure. It may run at any later point, any number of times,
// so outer variables written anywhere in the enclosing declaration are unknown
// inside it, and outer variables it writes are unknown once it exists.
func (r *goRouteResolver) funcLit(lit *ast.FuncLit, scope *goRouteScope, seed *goRouteBinding) {
	closureScope := newGoRouteScope(scope)
	// Only names the closure mentions can be read inside it, so iterate those
	// rather than every name written in the enclosing declaration.
	for name := range r.mentionedNames(lit.Body) {
		groupish, written := r.fnWritten[name]
		if !written {
			continue
		}
		binding := r.lookup(scope, name)
		if binding == nil || (binding.kind == goRouteUntracked && !groupish) {
			continue
		}
		unknown := goRouteUnknownBinding
		closureScope.vars[name] = &unknown
	}
	mark := len(r.log)
	savedRegion := r.region
	r.nextRegion++
	r.region = r.nextRegion
	r.walkFunc(nil, lit.Type, lit.Body, closureScope, seed)
	r.region = savedRegion
	r.undo(mark)
	r.markNames(scope, r.writtenNames(lit.Body))
	for name := range r.addressedNames(lit.Body) {
		r.escape(scope, name)
	}
}

// escape records that name's address was taken: its binding is unknown for
// every later read, whatever is later assigned to it directly.
func (r *goRouteResolver) escape(scope *goRouteScope, name string) {
	binding := r.lookup(scope, name)
	if r.collecting && (binding == nil || r.isPackageBinding(scope, name)) {
		r.pkgWritten[name] = true
	}
	if binding == nil {
		return
	}
	r.escaped[binding] = true
}

func (r *goRouteResolver) mentionedNames(node ast.Node) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		r.step(1)
		if ident, ok := n.(*ast.Ident); ok {
			names[ident.Name] = true
		}
		return true
	})
	return names
}

func (r *goRouteResolver) addressedNames(node ast.Node) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		r.step(1)
		if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.AND {
			if ident, ok := goRouteUnparen(unary.X).(*ast.Ident); ok {
				names[ident.Name] = true
			}
		}
		return true
	})
	return names
}

func goRouteUnparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

// goRouteWrittenNames lists identifiers written by plain or compound
// assignment, increment, range-assign or address-of within node. The value is
// true when some write could carry a router group (an identifier, a Group call,
// or an unpaired multi-value assignment).
func (r *goRouteResolver) writtenNames(node ast.Node) map[string]bool {
	written := map[string]bool{}
	if node == nil {
		return written
	}
	note := func(expr ast.Expr, groupish bool) {
		if ident, ok := expr.(*ast.Ident); ok && ident.Name != "_" {
			written[ident.Name] = written[ident.Name] || groupish
		}
	}
	ast.Inspect(node, func(n ast.Node) bool {
		r.step(1)
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE && len(n.Lhs) == 1 {
				// A single-name := always declares a new variable. With several
				// names, any of them may redeclare (write) an existing one.
				return true
			}
			for i, target := range n.Lhs {
				groupish := len(n.Lhs) != len(n.Rhs)
				if !groupish && (n.Tok == token.ASSIGN || n.Tok == token.DEFINE) {
					groupish = goRouteGroupish(n.Rhs[i])
				}
				note(target, groupish)
			}
		case *ast.IncDecStmt:
			note(n.X, false)
		case *ast.RangeStmt:
			if n.Tok == token.ASSIGN {
				note(n.Key, true)
				note(n.Value, true)
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				note(n.X, true)
			}
		}
		return true
	})
	return written
}

func mergeGoRouteWritten(into, from map[string]bool) {
	for name, groupish := range from {
		into[name] = into[name] || groupish
	}
}

func goRouteGroupish(expr ast.Expr) bool {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	switch expr := expr.(type) {
	case *ast.Ident, *ast.CallExpr, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr,
		*ast.StarExpr, *ast.TypeAssertExpr, *ast.CompositeLit:
		return true
	case *ast.UnaryExpr:
		return expr.Op == token.AND
	}
	return false
}

func (r *goRouteResolver) hasGoto(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		r.step(1)
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if branch, ok := n.(*ast.BranchStmt); ok && branch.Tok == token.GOTO {
			found = true
		}
		return !found
	})
	return found
}

// goRouteReceiverIsSelector reports whether the receiver identifier starting
// at offset is the field or method part of a selector (s.api.GET): a value
// read from a struct or package whose prefix this pass does not track.
func goRouteReceiverIsSelector(content string, offset int) bool {
	for i := offset - 1; i >= 0; i-- {
		switch content[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '.':
			return true
		default:
			return false
		}
	}
	return false
}

// Router origins. A receiver the file never binds is not assumed to be the
// root router: only a value whose origin this pass can see is. The root is
// what a framework constructor returns (echo.New, gin.Default, chi.NewRouter,
// ...), the net/http package's DefaultServeMux, or a parameter or variable
// typed as a framework root (*echo.Echo, *gin.Engine, *http.ServeMux,
// *httprouter.Router). A parameter or variable typed as a group or sub-router
// (*echo.Group, *gin.RouterGroup, chi.Router, fiber.Router, *mux.Router, a
// local type named like a group), the result of any other call, and a name
// this file never declares (a sibling file's package variable) are unknown.
// A parameter of any other local type keeps the long-standing root heuristic.

// goRouteFrameworkForPath names the router framework an import path is.
func goRouteFrameworkForPath(path string) string {
	switch {
	case path == "net/http":
		return "http"
	case strings.Contains(path, "labstack/echo"):
		return "echo"
	case strings.Contains(path, "gin-gonic/gin"):
		return "gin"
	case strings.Contains(path, "gofiber/fiber"):
		return "fiber"
	case strings.Contains(path, "go-chi/chi"):
		return "chi"
	case strings.Contains(path, "gorilla/mux"):
		return "mux"
	case strings.Contains(path, "julienschmidt/httprouter"):
		return "httprouter"
	}
	return ""
}

var goRouteDefaultFrameworkNames = map[string]bool{
	"http": true, "echo": true, "gin": true, "fiber": true, "chi": true, "mux": true, "httprouter": true,
}

func goRouteImports(file *ast.File) map[string]string {
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
			if len(parts) > 1 && goRouteVersionSuffixRe.MatchString(name) {
				name = parts[len(parts)-2]
			}
			name = strings.TrimPrefix(name, "go-")
		}
		if name == "_" || name == "." || name == "" {
			continue
		}
		imports[name] = goRouteFrameworkForPath(path)
	}
	return imports
}

var goRouteVersionSuffixRe = regexp.MustCompile(`^v[0-9]+$`)

// framework reports which router framework the package name pkg refers to.
// Callers have already checked that no local variable shadows pkg. Without an
// import of that name (a fragment), the framework's usual name is trusted.
func (r *goRouteResolver) framework(pkg string) string {
	if fw, ok := r.imports[pkg]; ok {
		return fw
	}
	if goRouteDefaultFrameworkNames[pkg] {
		return pkg
	}
	return ""
}

// undeclaredValue is the state of a name no scope in this file declares.
func (r *goRouteResolver) undeclaredValue(name string) goRouteBinding {
	if r.framework(name) == "http" {
		// http.HandleFunc / http.Handle: the DefaultServeMux.
		return goRouteBinding{kind: goRouteKnown}
	}
	return goRouteUnknownBinding
}

// goRouteConstructors are the framework calls that return a new root router.
// The value is whether the framework can mount that router under a prefix
// elsewhere (chi Mount, fiber Mount).
var goRouteConstructors = map[string]bool{
	"echo.New":         false,
	"gin.New":          false,
	"gin.Default":      false,
	"http.NewServeMux": false,
	"mux.NewRouter":    false,
	"httprouter.New":   false,
	"chi.NewRouter":    true,
	"chi.NewMux":       true,
	"fiber.New":        true,
}

func (r *goRouteResolver) constructorValue(call *ast.CallExpr, selector *ast.SelectorExpr, lookup goRouteLookup) (goRouteBinding, bool) {
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return goRouteBinding{}, false
	}
	if _, declared := lookup(pkg.Name); declared {
		// A variable, not the package.
		return goRouteBinding{}, false
	}
	framework := r.framework(pkg.Name)
	mountable, ok := goRouteConstructors[framework+"."+selector.Sel.Name]
	if !ok {
		return goRouteBinding{}, false
	}
	origin := r.origins[call.Pos()]
	if origin == nil {
		origin = &goRouteOrigin{framework: framework, mountable: mountable, region: r.region}
		r.origins[call.Pos()] = origin
	}
	if r.atPackage {
		// A package variable: a sibling file may mount or wrap it.
		origin.escaped = true
	}
	return goRouteBinding{kind: goRouteKnown, origin: origin}, true
}

func (r *goRouteResolver) isFrameworkType(expr ast.Expr, framework, name string) bool {
	selector, ok := goRouteUnparen(expr).(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && r.framework(pkg.Name) == framework
}

// typeValue is the state of a parameter or variable declared with type expr
// and no value.
func (r *goRouteResolver) typeValue(expr ast.Expr) goRouteBinding {
	expr = goRouteUnparen(expr)
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = goRouteUnparen(star.X)
	}
	switch expr := expr.(type) {
	case *ast.Ident:
		if goRouteGroupishTypeName(expr.Name) {
			return goRouteUnknownBinding
		}
		// A local type this pass cannot see into: the long-standing root
		// heuristic.
		return goRouteBinding{}
	case *ast.SelectorExpr:
		pkg, ok := expr.X.(*ast.Ident)
		if ok && goRouteRootTypes[r.framework(pkg.Name)+"."+expr.Sel.Name] {
			return goRouteBinding{kind: goRouteKnown}
		}
		// A framework group or sub-router type, or another package's type.
		return goRouteUnknownBinding
	case *ast.Ellipsis, *ast.IndexExpr, *ast.IndexListExpr:
		return goRouteUnknownBinding
	}
	return goRouteBinding{}
}

func goRouteIsFuncLit(expr ast.Expr) bool {
	_, ok := goRouteUnparen(expr).(*ast.FuncLit)
	return ok
}

// routeClosure recognizes r.Route(prefix, func(sub Router) {...}) and
// r.Group(func(sub Router) {...}) and returns the closure with the router its
// parameter is called with.
func (r *goRouteResolver) routeClosure(call *ast.CallExpr, scope *goRouteScope) (*ast.FuncLit, goRouteBinding, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || call.Ellipsis.IsValid() {
		return nil, goRouteBinding{}, false
	}
	var arg ast.Expr
	switch {
	case selector.Sel.Name == "Route" && len(call.Args) >= 2:
		arg = call.Args[1]
	case selector.Sel.Name == "Group" && len(call.Args) == 1:
		arg = call.Args[0]
	default:
		return nil, goRouteBinding{}, false
	}
	lit, ok := goRouteUnparen(arg).(*ast.FuncLit)
	if !ok {
		return nil, goRouteBinding{}, false
	}
	return lit, r.groupValue(call, r.lookupIn(scope)), true
}

// noteMount handles parent.Mount(prefix, sub) on a router instance created in
// this file. The instance's routes are served under the parent's prefix plus
// prefix when that is a single, unconditional, composable mount; otherwise
// they are unknown.
func (r *goRouteResolver) noteMount(call *ast.CallExpr, scope *goRouteScope) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Mount" || len(call.Args) != 2 || call.Ellipsis.IsValid() {
		return
	}
	ident, ok := goRouteUnparen(call.Args[1]).(*ast.Ident)
	if !ok {
		return
	}
	lookup := r.lookupIn(scope)
	sub, _ := lookup(ident.Name)
	origin := sub.origin
	if origin == nil {
		return
	}
	if sub.kind != goRouteKnown || sub.prefix != "" || origin.framework != "chi" || origin.region != r.region {
		// A group of the instance, a router whose framework does not strip
		// the mount prefix, or a mount that may run zero or many times.
		origin.opaque = true
		return
	}
	parent := r.groupValue(selector.X, lookup)
	prefix, ok := staticRouteExpressionValue(r.text(call.Args[0]), r.constants)
	if !ok || parent.kind == goRouteUnknown {
		origin.opaque = true
		return
	}
	origin.mounts++
	origin.mountParent = nil
	origin.mountPrefix = prefix
	if parent.kind == goRouteKnown {
		origin.mountParent = parent.origin
		origin.mountPrefix = joinRoutePaths(parent.prefix, prefix)
	}
}

// computeSafe marks the identifier uses that hand a router to something that
// cannot mount it under another prefix. Every other use of a router instance
// is an escape.
func (r *goRouteResolver) computeSafe() {
	r.safe = map[*ast.Ident]bool{}
	mark := func(expr ast.Expr) {
		if ident, ok := goRouteUnparen(expr).(*ast.Ident); ok {
			r.safe[ident] = true
		}
	}
	called := map[*ast.SelectorExpr]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		r.step(1)
		switch n := n.(type) {
		case *ast.CallExpr:
			selector, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			called[selector] = true
			switch selector.Sel.Name {
			case "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS", "Handle", "Mount":
				// Serving a router, or handing it to a mux that matches the
				// full path (net/http, chi and gorilla Handle do not strip).
				// Mount is composed or made opaque by noteMount.
				for _, arg := range n.Args {
					mark(arg)
				}
			}
		case *ast.SelectorExpr:
			// r.Get(...), r.Group(...): a method call on the router. A bare
			// r.ServeHTTP method value hands the router out.
			if called[n] || n.Sel.Name != "ServeHTTP" {
				mark(n.X)
			}
		case *ast.CompositeLit:
			if r.isFrameworkType(goRouteUnstar(n.Type), "http", "Server") {
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Handler" {
							mark(kv.Value)
						}
					}
				}
			}
		case *ast.AssignStmt:
			// An alias; a store into a package variable is caught in assign.
			if len(n.Lhs) == len(n.Rhs) {
				for i, lhs := range n.Lhs {
					if _, ok := lhs.(*ast.Ident); ok {
						mark(n.Rhs[i])
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for _, value := range n.Values {
					mark(value)
				}
			}
		}
		return true
	})
}

func goRouteUnstar(expr ast.Expr) ast.Expr {
	if star, ok := expr.(*ast.StarExpr); ok {
		return star.X
	}
	return expr
}
