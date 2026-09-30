package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
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
// This resolver instead walks the syntax tree once, in statement order, and
// records the prefix state of every identifier at its use site:
//
//   - untracked: never bound to a Group value in scope. Treated as the root
//     router, which is the heuristic every receiver had before (e.GET, r.Get).
//   - known: bound to a Group call whose prefix and parent are both static. A
//     known empty prefix is still known; it is not the same state as unknown.
//   - unknown: the value depends on something this pass does not model. Every
//     registration on an unknown receiver is omitted, never re-rooted.
//
// Supported subset: package-level var initializers (resolved by dependency,
// independent of textual order; cycles and duplicates become unknown),
// straight-line locals in statement order with the RHS evaluated before the
// LHS, lexical block scopes and shadowing. Everything else is conservative:
// a tracked variable written inside a branch, loop, switch, select or closure
// is unknown after it and inside any loop body; closures see outer variables
// written anywhere in the enclosing function as unknown; a function using goto
// is opaque; package variables written from any function are unknown in every
// other function. No type information, provider, or filesystem is involved.

type goRouteBindingKind uint8

const (
	goRouteUntracked goRouteBindingKind = iota
	goRouteKnown
	goRouteUnknown
)

type goRouteBinding struct {
	kind   goRouteBindingKind
	prefix string
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
	uses   map[int]goRouteBinding
	// groupNames are names bound to a Group value anywhere in the file. A
	// receiver the walk did not visit (text in a comment, a selector field, an
	// unparseable file) is omitted when its name is one of these, and treated
	// as the root router otherwise, exactly as before this resolver existed.
	groupNames map[string]bool
}

func (r goRouteReceivers) at(offset int, name string) goRouteBinding {
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
	receivers := goRouteReceivers{groupNames: map[string]bool{}}
	for _, match := range goHTTPRouteRegistrationsGroupRe.FindAllStringSubmatch(content, -1) {
		if len(match) == 4 {
			receivers.groupNames[match[1]] = true
		}
	}
	if !strings.Contains(content, "Group") {
		// No Group call can bind anything: every receiver is untracked.
		return receivers
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
	}
	resolver.resolve()
	receivers.parsed = true
	receivers.uses = resolver.uses
	return receivers
}

type goRouteResolver struct {
	fset      *token.FileSet
	tokFile   *token.File
	src       string
	shift     int
	constants map[string]string
	file      *ast.File

	uses       map[int]goRouteBinding
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

func (r *goRouteResolver) resolve() {
	r.pkgWritten = map[string]bool{}
	r.collecting = true
	r.walkFile()
	if len(r.pkgWritten) == 0 {
		return
	}
	// A package variable written by some function holds whatever the last
	// executed writer left there; no other function can rely on it. Walk again
	// with those bindings unknown (the writing function still sees its own
	// straight-line writes).
	r.pkgUnknown = r.pkgWritten
	r.collecting = false
	r.walkFile()
}

func (r *goRouteResolver) walkFile() {
	r.uses = map[int]goRouteBinding{}
	r.log = r.log[:0]
	pkg := r.packageScope()
	for _, decl := range r.file.Decls {
		mark := len(r.log)
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Body == nil {
				continue
			}
			r.fnWritten = goRouteWrittenNames(decl.Body)
			r.walkFunc(decl.Recv, decl.Type, decl.Body, pkg)
		case *ast.GenDecl:
			if decl.Tok != token.VAR {
				continue
			}
			r.fnWritten = goRouteWrittenNames(decl)
			for _, spec := range decl.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					r.exprs(valueSpec.Values, pkg)
				}
			}
		}
		r.undo(mark)
	}
}

// packageScope resolves package-level var initializers by dependency, so a
// chain declared child-first (legal Go) composes like one declared in order.
func (r *goRouteResolver) packageScope() *goRouteScope {
	inits := map[string]ast.Expr{}
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
				if len(valueSpec.Values) == len(valueSpec.Names) {
					inits[name.Name] = valueSpec.Values[i]
				}
			}
		}
	}
	resolved := map[string]goRouteBinding{}
	visiting := map[string]bool{}
	var resolveName func(name string) goRouteBinding
	resolveName = func(name string) goRouteBinding {
		if !declared[name] {
			return goRouteBinding{}
		}
		if binding, ok := resolved[name]; ok {
			return binding
		}
		if duplicate[name] || visiting[name] {
			return goRouteUnknownBinding
		}
		visiting[name] = true
		binding := goRouteBinding{}
		if value := inits[name]; value != nil {
			binding = r.groupValue(value, resolveName)
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

func (r *goRouteResolver) lookupIn(scope *goRouteScope) func(string) goRouteBinding {
	return func(name string) goRouteBinding {
		if binding := scope.lookup(name); binding != nil {
			return *binding
		}
		return goRouteBinding{}
	}
}

// groupValue is the prefix state an expression produces when stored.
func (r *goRouteResolver) groupValue(expr ast.Expr, lookup func(string) goRouteBinding) goRouteBinding {
	switch expr := expr.(type) {
	case *ast.ParenExpr:
		return r.groupValue(expr.X, lookup)
	case *ast.Ident:
		return lookup(expr.Name)
	case *ast.CallExpr:
		selector, ok := expr.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Group" || len(expr.Args) == 0 || expr.Ellipsis.IsValid() {
			return goRouteBinding{}
		}
		var parent goRouteBinding
		switch receiver := selector.X.(type) {
		case *ast.Ident:
			parent = lookup(receiver.Name)
		case *ast.CallExpr, *ast.ParenExpr:
			parent = r.groupValue(receiver, lookup)
		default:
			// A field or index receiver (s.api.Group) may hold any prefix.
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
			prefix = joinRoutePaths(parent.prefix, prefix)
		}
		return goRouteBinding{kind: goRouteKnown, prefix: prefix}
	default:
		return goRouteBinding{}
	}
}

func (r *goRouteResolver) walkFunc(recv *ast.FieldList, typ *ast.FuncType, body *ast.BlockStmt, outer *goRouteScope) {
	scope := newGoRouteScope(outer)
	for _, fields := range []*ast.FieldList{recv, typ.Params, typ.Results} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				r.declare(scope, name.Name, goRouteBinding{})
			}
		}
	}
	if goRouteHasGoto(body) {
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
			if len(valueSpec.Values) == len(valueSpec.Names) {
				for i, value := range valueSpec.Values {
					values[i] = r.groupValue(value, r.lookupIn(scope))
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
		written := goRouteWrittenNames(stmt.Body)
		branches := []func(){func() { r.walkStmts(stmt.Body.List, newGoRouteScope(ifScope)) }}
		if stmt.Else != nil {
			mergeGoRouteWritten(written, goRouteWrittenNames(stmt.Else))
			branches = append(branches, func() { r.walkStmt(stmt.Else, ifScope) })
		}
		r.branches(ifScope, written, branches)
	case *ast.ForStmt:
		forScope := newGoRouteScope(scope)
		r.walkStmt(stmt.Init, forScope)
		written := goRouteWrittenNames(stmt.Body)
		if stmt.Post != nil {
			mergeGoRouteWritten(written, goRouteWrittenNames(stmt.Post))
		}
		r.branches(forScope, written, []func(){func() {
			r.expr(stmt.Cond, forScope)
			r.walkStmts(stmt.Body.List, newGoRouteScope(forScope))
			r.walkStmt(stmt.Post, forScope)
		}})
	case *ast.RangeStmt:
		r.expr(stmt.X, scope)
		rangeScope := newGoRouteScope(scope)
		written := goRouteWrittenNames(stmt.Body)
		for _, target := range []ast.Expr{stmt.Key, stmt.Value} {
			if target == nil {
				continue
			}
			if stmt.Tok == token.DEFINE {
				if ident, ok := target.(*ast.Ident); ok {
					r.declare(rangeScope, ident.Name, goRouteBinding{})
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
				r.declare(clauseScope, bound, goRouteBinding{})
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
	written := goRouteWrittenNames(body)
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
	for _, branch := range branches {
		mark := len(r.log)
		branch()
		r.undo(mark)
	}
}

// markNames makes the visible bindings of written names unknown. An untracked
// binding stays untracked unless some write could carry a group value into it.
func (r *goRouteResolver) markNames(scope *goRouteScope, written map[string]bool) {
	for name, groupish := range written {
		binding := scope.lookup(name)
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
	if paired {
		for i, value := range stmt.Rhs {
			values[i] = r.groupValue(value, r.lookupIn(scope))
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
			binding := scope.lookup(ident.Name)
			mayCarryGroup := !paired || values[i].kind != goRouteUntracked
			if r.collecting && (binding == nil || r.isPackageBinding(scope, ident.Name)) &&
				(mayCarryGroup || (binding != nil && binding.kind != goRouteUntracked)) {
				r.pkgWritten[ident.Name] = true
			}
			if binding == nil {
				continue
			}
			value := values[i]
			if !paired {
				value = goRouteUnknownBinding
				if binding.kind == goRouteUntracked {
					value = goRouteBinding{}
				}
			}
			r.assignTo(ident.Name, binding, value)
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
	if binding := scope.lookup(ident.Name); binding != nil && binding.kind != goRouteUntracked {
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
		switch node := node.(type) {
		case *ast.FuncLit:
			r.funcLit(node, scope)
			return false
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
			value := goRouteBinding{}
			if binding := scope.lookup(node.Name); binding != nil {
				value = *binding
			}
			r.uses[r.offset(node.Pos())] = value
		}
		return true
	})
}

// funcLit walks a closure. It may run at any later point, any number of times,
// so outer variables written anywhere in the enclosing declaration are unknown
// inside it, and outer variables it writes are unknown once it exists.
func (r *goRouteResolver) funcLit(lit *ast.FuncLit, scope *goRouteScope) {
	closureScope := newGoRouteScope(scope)
	for name, groupish := range r.fnWritten {
		binding := scope.lookup(name)
		if binding == nil || (binding.kind == goRouteUntracked && !groupish) {
			continue
		}
		unknown := goRouteUnknownBinding
		closureScope.vars[name] = &unknown
	}
	mark := len(r.log)
	r.walkFunc(nil, lit.Type, lit.Body, closureScope)
	r.undo(mark)
	r.markNames(scope, goRouteWrittenNames(lit.Body))
}

// goRouteWrittenNames lists identifiers written by plain or compound
// assignment, increment, range-assign or address-of within node. The value is
// true when some write could carry a router group (an identifier, a Group call,
// or an unpaired multi-value assignment).
func goRouteWrittenNames(node ast.Node) map[string]bool {
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
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				return true
			}
			for i, target := range n.Lhs {
				groupish := len(n.Lhs) != len(n.Rhs)
				if !groupish && n.Tok == token.ASSIGN {
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
	case *ast.Ident:
		return true
	case *ast.CallExpr:
		selector, ok := expr.Fun.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == "Group"
	}
	return false
}

func goRouteHasGoto(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
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
