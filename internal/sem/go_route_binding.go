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
// unconditional chi or fiber Mount with a static prefix of an instance created in
// this file is composed; any other Mount, and any escape of a mountable
// instance (returned, passed to a call other than a serve or Handle call,
// stored in a field, composite or package variable, addressed, or declared
// at package level), makes all of its routes unknown.
//
// gorilla r.PathPrefix("/a").Subrouter() is a router under /a. HandleFunc
// and Handle(p, HandlerFunc(h)) resolve their receiver like router methods.
//
// http.StripPrefix is never composed: every router instance inside its
// handler argument is unknown, and a ServeMux instance that escapes (the usual
// way it reaches a StripPrefix elsewhere) is unknown too.
//
// KNOWN LIMITATIONS: a parameter of an unclassified local type, or of a
// framework root type (*http.ServeMux, *gin.Engine, *echo.Echo), keeps the
// root heuristic: a StripPrefix in its caller is not visible. A gin, echo,
// gorilla or httprouter instance that escapes stays root unless this file
// wraps it in StripPrefix. A Group prefix without a leading slash (gin's
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
	// framework whose path-joining rules apply to this router ("" when
	// unclassified).
	framework string
	// httpClient and stripPrefixMiddleware are non-router facts carried by
	// the same lexical binding walk. They keep masking and middleware
	// classification attached to a declaration/use, rather than to a bare
	// name across the whole file.
	httpClient            bool
	stripPrefixMiddleware bool
	// stripPrefixSource is the outer binding a closure will read when it
	// eventually runs. It is set only on closure-capture proxies (and aliases
	// copied from them), never on ordinary same-scope aliases.
	stripPrefixSource *goRouteBinding
	// stripPrefixJoins are bounded value-provenance tokens for uncertain loop
	// entry values. Identifier reads and aliases copy them; definite assignments
	// replace them. They are distinct from the live closure source above.
	stripPrefixJoins []*goRouteRewriteJoin
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
	// served: this file serves the instance itself (ListenAndServe, an
	// http.Server Handler, app.Listen, r.Run, e.Start). A served top-level
	// router is not also mounted, so its escapes to read-only helpers (route
	// docs, walkers, test requests) do not make it unknown.
	served bool
	region int
	// mounts counts in-file Mount calls with a composable prefix; the prefix
	// is mountPrefix under mountParent (nil for a root).
	mounts      int
	mountParent *goRouteOrigin
	mountPrefix string
}

// prefix is the path the instance is served under.
func (o *goRouteOrigin) prefix(depth int) (string, bool) {
	if o.opaque || (o.mountable && o.escaped && !o.served) || o.mounts > 1 || (o.served && o.mounts > 0) || depth > 16 {
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
	return goRouteJoin(o.framework, base, o.mountPrefix), true
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

type goRouteRewriteJoin struct {
	name    string
	binding *goRouteBinding
	may     bool
	// observed holds router origins whose middleware Use reads a value carrying
	// this join token. Dependencies form a bounded monotone OR graph, allowing
	// aliases and nested loops to preserve provenance without name-wide taint.
	observed   map[*goRouteOrigin]bool
	deps       map[*goRouteRewriteJoin]bool
	dependents map[*goRouteRewriteJoin]bool
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
	// masks are the byte spans of every route argument the pass decides.
	masks [][2]int
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
	file, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		// Fragments without a package clause are still worth resolving. A file
		// that does not parse either way keeps the name-based fallback.
		const clause = "package routes\n"
		fset = token.NewFileSet()
		file, err = parser.ParseFile(fset, "", clause+content, parser.SkipObjectResolution|parser.ParseComments)
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
		receivers.masks = resolver.maskSpans()
		receivers.exhausted = true
		return receivers
	}
	receivers.masks = resolver.maskSpans()
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

	// paramFunc names the top-level function whose parameters walkFunc is
	// about to declare ("" for closures), and paramRecv its receiver type.
	paramFunc string
	paramRecv string
	// paramOrigins are the router instances of framework-root-typed
	// parameters, keyed by function name and parameter index; paramArgs are
	// the values same-file call sites pass for them. A parameter any caller
	// passes a non-root value (a StripPrefix'd or mounted router, a group, an
	// unknown value) is unknown.
	paramOrigins map[goRouteParamKey]*goRouteOrigin
	paramArgs    map[goRouteParamKey][]goRouteBinding
	// paramValueUse names functions referenced other than as a call: their
	// callers' arguments are not visible.
	paramValueUse map[string]bool
	// rootParamFuncs names functions with a framework-root-typed parameter.
	rootParamFuncs map[string]bool
	// readOnlyParams: per top-level function, the parameters used only as a
	// method receiver or field base. Passing a router there is not an escape.
	readOnlyParams map[string][]bool
	// stripPrefixWatchers records router origins whose Use call reads a
	// closure-captured middleware binding. A later rewrite value assigned to
	// that exact outer binding makes those origins opaque.
	stripPrefixWatchers map[*goRouteBinding]map[*goRouteOrigin]bool
}

// goRouteParamKey names a parameter: the receiver type of a method ("" for a
// package-level function, "*" at a call site whose receiver type is not
// visible), the function name, and the parameter index.
type goRouteParamKey struct {
	recv  string
	fn    string
	index int
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
	goRouteMaxPrefixBytes      = 2048
	goRouteStripSourceMaxDepth = 16
	goRouteStripJoinMaxDeps    = 16
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
		r.taintParams()
		return true
	}
	// A package variable written by some function holds whatever the last
	// executed writer left there; no other function can rely on it. Walk again
	// with those bindings unknown (the writing function still sees its own
	// straight-line writes).
	r.pkgUnknown = r.pkgWritten
	r.collecting = false
	r.walkFile()
	r.taintParams()
	return true
}

// taintParams makes a framework-root-typed parameter unknown when a same-file
// caller passes it anything but a root router served at "/" (for example a
// router it wraps in http.StripPrefix), or when the function is used as a
// value. Parameters forwarded to other functions propagate, so this iterates.
func (r *goRouteResolver) taintParams() {
	for changed, rounds := true, 0; changed && rounds < 16; rounds++ {
		changed = false
		for key, origin := range r.paramOrigins {
			if origin.opaque {
				continue
			}
			bad := r.paramValueUse[key.fn]
			args := r.paramArgs[key]
			if key.recv != "" {
				// Method calls whose receiver type is not visible may reach
				// any method of that name.
				args = append(append([]goRouteBinding(nil), args...), r.paramArgs[goRouteParamKey{recv: "*", fn: key.fn, index: key.index}]...)
			}
			for _, arg := range args {
				if !goRouteIsServedRoot(arg) {
					bad = true
				}
			}
			if bad {
				origin.opaque = true
				changed = true
			}
		}
	}
}

// goRouteIsServedRoot reports a known router value with no prefix whose
// instance is served at the root.
func goRouteIsServedRoot(value goRouteBinding) bool {
	if value.kind != goRouteKnown || value.prefix != "" {
		return false
	}
	if value.origin == nil {
		return true
	}
	base, ok := value.origin.prefix(0)
	return ok && base == ""
}

func (r *goRouteResolver) walkFile() {
	r.uses = map[int]goRouteBinding{}
	r.regs = r.regs[:0]
	r.origins = map[token.Pos]*goRouteOrigin{}
	r.paramOrigins = map[goRouteParamKey]*goRouteOrigin{}
	r.paramArgs = map[goRouteParamKey][]goRouteBinding{}
	r.escaped = map[*goRouteBinding]bool{}
	r.stripPrefixWatchers = map[*goRouteBinding]map[*goRouteOrigin]bool{}
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
			r.paramFunc = decl.Name.Name
			r.paramRecv = goRouteRecvTypeName(decl.Recv)
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
	for origin := range r.stripPrefixWatchers[binding] {
		r.step(1)
		if value.stripPrefixMiddleware {
			origin.opaque = true
			continue
		}
		for _, join := range value.stripPrefixJoins {
			r.step(1)
			r.observeStripPrefixJoin(join, origin)
		}
	}
	r.noteGroup(name, value)
}

// ultimateStripPrefixSource follows closure-capture proxies to the exact
// outer binding whose future value they observe. Proxies only point outward,
// but keep a depth bound so malformed or future cyclic state stays safe.
func (r *goRouteResolver) ultimateStripPrefixSource(binding *goRouteBinding) (*goRouteBinding, bool) {
	for depth := 0; depth < goRouteStripSourceMaxDepth; depth++ {
		r.step(1)
		if binding == nil {
			return nil, false
		}
		if binding.stripPrefixSource == nil {
			return binding, true
		}
		binding = binding.stripPrefixSource
	}
	return nil, false
}

func (r *goRouteResolver) watchStripPrefixSource(source *goRouteBinding, origin *goRouteOrigin) {
	source, ok := r.ultimateStripPrefixSource(source)
	if !ok {
		// A malformed dependency cannot justify a bare public route.
		origin.opaque = true
		return
	}
	if source.stripPrefixMiddleware {
		origin.opaque = true
		return
	}
	for _, join := range source.stripPrefixJoins {
		r.step(1)
		r.observeStripPrefixJoin(join, origin)
	}
	watchers := r.stripPrefixWatchers[source]
	if watchers == nil {
		watchers = map[*goRouteOrigin]bool{}
		r.stripPrefixWatchers[source] = watchers
	}
	r.step(1)
	watchers[origin] = true
}

func (r *goRouteResolver) addStripPrefixJoinDependency(join, dependency *goRouteRewriteJoin) {
	if join == nil || dependency == nil || join == dependency || join.deps[dependency] {
		return
	}
	r.step(1)
	if len(join.deps) >= goRouteStripJoinMaxDeps {
		// Too many alternatives cannot justify retaining a bare public route.
		r.resolveStripPrefixJoin(join)
		return
	}
	join.deps[dependency] = true
	dependency.dependents[join] = true
	if dependency.may {
		r.resolveStripPrefixJoin(join)
	}
}

// resolveStripPrefixJoin propagates a newly proven MAY-rewrite fact through
// the bounded dependency graph. Each join changes state at most once.
func (r *goRouteResolver) resolveStripPrefixJoin(join *goRouteRewriteJoin) {
	queue := []*goRouteRewriteJoin{join}
	for len(queue) > 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		r.step(1)
		if current == nil || current.may {
			continue
		}
		current.may = true
		for origin := range current.observed {
			r.step(1)
			origin.opaque = true
		}
		for dependent := range current.dependents {
			r.step(1)
			queue = append(queue, dependent)
		}
	}
}

func (r *goRouteResolver) observeStripPrefixJoin(join *goRouteRewriteJoin, origin *goRouteOrigin) {
	if join == nil {
		return
	}
	r.step(1)
	if join.may {
		origin.opaque = true
		return
	}
	join.observed[origin] = true
}

func (r *goRouteResolver) withStripPrefixJoin(dependencies []*goRouteRewriteJoin, join *goRouteRewriteJoin) ([]*goRouteRewriteJoin, bool) {
	result := make([]*goRouteRewriteJoin, 0, len(dependencies)+1)
	seen := map[*goRouteRewriteJoin]bool{}
	add := func(dependency *goRouteRewriteJoin) bool {
		r.step(1)
		if dependency == nil || seen[dependency] {
			return true
		}
		seen[dependency] = true
		result = append(result, dependency)
		return len(result) <= goRouteStripJoinMaxDeps
	}
	for _, dependency := range dependencies {
		if !add(dependency) {
			return nil, false
		}
	}
	if !add(join) {
		return nil, false
	}
	return result, true
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
		if r.isStripPrefixMiddlewareCall(expr, lookup) {
			return goRouteBinding{kind: goRouteUnknown, stripPrefixMiddleware: true}
		}
		if fun, ok := expr.Fun.(*ast.Ident); ok && fun.Name == "new" && len(expr.Args) == 1 && !expr.Ellipsis.IsValid() {
			if _, declared := lookup(fun.Name); !declared && r.isHTTPClientType(expr.Args[0]) {
				return goRouteBinding{kind: goRouteUnknown, httpClient: true}
			}
		}
		selector, ok := expr.Fun.(*ast.SelectorExpr)
		if !ok {
			if root, ok := r.inPackageConstructorValue(expr, lookup); ok {
				return root
			}
			if root, ok := r.newRouterValue(expr, lookup); ok {
				return root
			}
			// The result of a local function: whatever router it returns, its
			// prefix was set where this pass does not look.
			return goRouteUnknownBinding
		}
		if root, ok := r.constructorValue(expr, selector, lookup); ok {
			return root
		}
		name := selector.Sel.Name
		if name == "Subrouter" {
			return r.subrouterValue(expr, selector, lookup)
		}
		if name != "Group" && name != "Route" && name != "With" && name != "Host" {
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
		case name == "Host":
			// echo e.Host(name) is a group with an empty prefix on that host:
			// the path is unchanged. Any other framework's Host is unknown.
			if parent.kind != goRouteKnown || parent.framework != "echo" {
				return goRouteUnknownBinding
			}
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
		prefix, ok := goRouteLiteral(parent.framework, argument, r.constants)
		if !ok {
			// Group("") is the parent itself: a known empty prefix, not unknown.
			if value, isStatic := staticStringExpressionValue(argument, r.constants); isStatic && value == "" {
				prefix, ok = "", true
			}
		}
		if !ok || parent.kind == goRouteUnknown {
			return goRouteUnknownBinding
		}
		if parent.kind == goRouteKnown && parent.prefix != "" && len(parent.prefix)+len(prefix) > goRouteMaxPrefixBytes {
			return goRouteUnknownBinding
		}
		prefix = goRouteJoin(parent.framework, parent.prefix, prefix)
		return goRouteBinding{kind: goRouteKnown, prefix: prefix, origin: parent.origin, framework: parent.framework}
	case *ast.UnaryExpr:
		if expr.Op == token.AND {
			return r.groupValue(expr.X, lookup)
		}
		return goRouteBinding{}
	case *ast.CompositeLit:
		if r.isHTTPClientType(expr.Type) {
			return goRouteBinding{kind: goRouteUnknown, httpClient: true}
		}
		if r.isFrameworkType(expr.Type, "http", "ServeMux") {
			return goRouteBinding{kind: goRouteKnown, origin: r.originAt(expr.Pos(), "http", true), framework: "http"}
		}
		return goRouteUnknownBinding
	case *ast.SelectorExpr:
		if pkg, ok := expr.X.(*ast.Ident); ok && expr.Sel.Name == "DefaultClient" {
			if _, declared := lookup(pkg.Name); !declared && r.framework(pkg.Name) == "http" {
				return goRouteBinding{kind: goRouteUnknown, httpClient: true}
			}
		}
		// A value read from a field or package whose router prefix this pass
		// does not follow.
		return goRouteUnknownBinding
	case *ast.IndexExpr, *ast.IndexListExpr, *ast.StarExpr, *ast.TypeAssertExpr:
		// A value read from a field, map, slice, pointer or interface: if it
		// is a group, its prefix was set somewhere this pass does not follow.
		return goRouteUnknownBinding
	default:
		return goRouteBinding{}
	}
}

func (r *goRouteResolver) walkFunc(recv *ast.FieldList, typ *ast.FuncType, body *ast.BlockStmt, outer *goRouteScope, seed *goRouteBinding) {
	scope := newGoRouteScope(outer)
	fn := r.paramFunc
	r.paramFunc = ""
	if seed != nil && (typ.Params == nil || len(typ.Params.List) != 1 || len(typ.Params.List[0].Names) != 1) {
		seed = nil
	}
	for _, fields := range []*ast.FieldList{recv, typ.Params, typ.Results} {
		if fields == nil {
			continue
		}
		index := 0
		for _, field := range fields.List {
			value := r.typeValue(field.Type)
			if fn == "" {
				// A function literal's parameter is bound by whoever calls it
				// (a go/defer statement, an immediate call, a callback). The
				// long-standing root heuristic and framework-root type facts are
				// kept only for top-level declarations. Preserve non-router type
				// facts, but do not give a closure parameter a root route origin.
				value.kind = goRouteUnknown
				value.prefix, value.origin, value.framework = "", nil, ""
			}
			if seed != nil && fields == typ.Params {
				// The router a Route/Group closure is called with.
				value = *seed
			}
			for _, name := range field.Names {
				declared := value
				if fn != "" && fields == typ.Params && value.kind == goRouteKnown {
					// A framework-root-typed parameter: its callers in this file
					// decide whether it is really served at the root.
					declared.origin = r.paramOriginAt(goRouteParamKey{recv: r.paramRecv, fn: fn, index: index}, value.framework)
				}
				r.declare(scope, name.Name, declared)
				index++
			}
			if len(field.Names) == 0 {
				index++
			}
		}
	}
	if r.hasGoto(body) {
		// Statement order no longer describes execution order.
		r.markNames(scope, r.fnWritten, false)
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
		r.branches(ifScope, written, branches, false)
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
		}}, true)
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
		}}, true)
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
	r.branches(scope, written, branches, false)
}

// branches walks code that may run zero, one, or many times, or instead of a
// sibling branch. Every outer variable any branch may write is unknown before
// each branch (a previous iteration or fallthrough may have written it) and
// after all of them (whether it was written depends on the path taken). Each
// branch's own writes are rolled back so siblings never see them.
func (r *goRouteResolver) branches(scope *goRouteScope, written map[string]bool, branches []func(), repeat bool) {
	joins := r.markNames(scope, written, repeat)
	savedRegion := r.region
	for _, branch := range branches {
		mark := len(r.log)
		r.nextRegion++
		r.region = r.nextRegion
		branch()
		r.region = savedRegion
		for _, join := range joins {
			r.step(1)
			if join.binding.stripPrefixMiddleware {
				r.resolveStripPrefixJoin(join)
			}
			for _, dependency := range join.binding.stripPrefixJoins {
				r.step(1)
				r.addStripPrefixJoinDependency(join, dependency)
			}
		}
		r.undo(mark)
	}
	if !repeat {
		for _, join := range joins {
			r.step(1)
			value := *join.binding
			dependencies, ok := r.withStripPrefixJoin(value.stripPrefixJoins, join)
			if !ok {
				value.stripPrefixMiddleware = true
				value.stripPrefixJoins = nil
			} else {
				value.stripPrefixJoins = dependencies
			}
			r.set(join.name, join.binding, value)
		}
	}
}

// markNames makes the visible bindings of written names unknown. An untracked
// binding stays untracked unless some write could carry a group value into it.
func (r *goRouteResolver) markNames(scope *goRouteScope, written map[string]bool, repeat bool) []*goRouteRewriteJoin {
	var joins []*goRouteRewriteJoin
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
		join := &goRouteRewriteJoin{
			name:       name,
			binding:    binding,
			may:        binding.stripPrefixMiddleware,
			observed:   map[*goRouteOrigin]bool{},
			deps:       map[*goRouteRewriteJoin]bool{},
			dependents: map[*goRouteRewriteJoin]bool{},
		}
		for _, dependency := range binding.stripPrefixJoins {
			r.step(1)
			r.addStripPrefixJoinDependency(join, dependency)
		}
		joins = append(joins, join)
		unknown := goRouteUnknownBinding
		// Rewriting is a MAY fact, so an ambiguous write retains prior rewrite
		// evidence. HTTP-client masking is a MUST fact and intentionally clears.
		unknown.stripPrefixMiddleware = binding.stripPrefixMiddleware
		unknown.stripPrefixSource = binding.stripPrefixSource
		unknown.stripPrefixJoins, _ = r.withStripPrefixJoin(binding.stripPrefixJoins, nil)
		if repeat {
			dependencies, ok := r.withStripPrefixJoin(unknown.stripPrefixJoins, join)
			if !ok {
				unknown.stripPrefixMiddleware = true
				unknown.stripPrefixJoins = nil
				r.resolveStripPrefixJoin(join)
			} else {
				unknown.stripPrefixJoins = dependencies
			}
		}
		r.set(name, binding, unknown)
	}
	return joins
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
			r.noteStripPrefix(node, scope)
			r.noteUseStripPrefix(node, scope)
			r.noteServe(node, scope)
			r.noteParamArgs(node, scope)
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
		case *ast.CompositeLit:
			r.noteServerLiteral(node, scope)
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
	type rewriteCapture struct {
		name  string
		outer *goRouteBinding
		proxy *goRouteBinding
		may   bool
		joins []*goRouteRewriteJoin
	}
	var captures []rewriteCapture
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
		// The closure may run after a later outer assignment. Preserve current
		// MAY-rewrite evidence and link only this capture proxy to the exact
		// outer binding whose future value it observes. MUST-client certainty
		// intentionally does not survive the ambiguous capture.
		unknown.stripPrefixMiddleware = binding.stripPrefixMiddleware
		if source, ok := r.ultimateStripPrefixSource(binding); ok {
			unknown.stripPrefixSource = source
		} else {
			unknown.stripPrefixMiddleware = true
		}
		proxy := &unknown
		closureScope.vars[name] = proxy
		captures = append(captures, rewriteCapture{name: name, outer: binding, proxy: proxy})
	}
	mark := len(r.log)
	savedRegion := r.region
	r.nextRegion++
	r.region = r.nextRegion
	r.walkFunc(nil, lit.Type, lit.Body, closureScope, seed)
	r.region = savedRegion
	for i := range captures {
		r.step(1)
		captures[i].may = captures[i].proxy.stripPrefixMiddleware
		captures[i].joins, _ = r.withStripPrefixJoin(captures[i].proxy.stripPrefixJoins, nil)
	}
	r.undo(mark)
	r.markNames(scope, r.writtenNames(lit.Body), false)
	for _, capture := range captures {
		r.step(1)
		value := *capture.outer
		changed := false
		if capture.may && !value.stripPrefixMiddleware {
			value.stripPrefixMiddleware = true
			value.stripPrefixJoins = nil
			changed = true
		}
		if !value.stripPrefixMiddleware {
			for _, join := range capture.joins {
				var ok bool
				value.stripPrefixJoins, ok = r.withStripPrefixJoin(value.stripPrefixJoins, join)
				if !ok {
					value.stripPrefixMiddleware = true
					value.stripPrefixJoins = nil
				}
				changed = true
				if value.stripPrefixMiddleware {
					break
				}
			}
		}
		if changed {
			r.set(capture.name, capture.outer, value)
		}
	}
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
	case path == "net/http/httptest":
		return "httptest"
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
	"httptest": true,
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
		return goRouteBinding{kind: goRouteKnown, framework: "http"}
	}
	return goRouteUnknownBinding
}

// goRouteConstructors are the framework calls that return a new root router.
// The value is whether whoever receives that router conventionally serves it
// under a prefix (chi Mount, fiber Mount, http.StripPrefix around a
// ServeMux), which makes an escape of the instance unknown.
var goRouteConstructors = map[string]bool{
	"echo.New":           false,
	"echo.NewWithConfig": false,
	"gin.New":            false,
	"gin.Default":        false,
	"http.NewServeMux":   true,
	"mux.NewRouter":      false,
	"httprouter.New":     false,
	"chi.NewRouter":      true,
	"chi.NewMux":         true,
	"fiber.New":          true,
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
	return goRouteBinding{kind: goRouteKnown, origin: r.originAt(call.Pos(), framework, mountable), framework: framework}, true
}

func (r *goRouteResolver) isFrameworkType(expr ast.Expr, framework, name string) bool {
	selector, ok := goRouteUnparen(expr).(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && r.framework(pkg.Name) == framework
}

func (r *goRouteResolver) isHTTPClientType(expr ast.Expr) bool {
	expr = goRouteUnstar(goRouteUnparen(expr))
	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		if expr.Sel.Name != "Client" {
			return false
		}
		pkg, ok := expr.X.(*ast.Ident)
		return ok && r.framework(pkg.Name) == "http"
	case *ast.Ident:
		return r.file.Name != nil && r.file.Name.Name == "http" && expr.Name == "Client"
	}
	return false
}

func (r *goRouteResolver) isStripPrefixMiddlewareCall(call *ast.CallExpr, lookup goRouteLookup) bool {
	if len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "StripPrefix" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	if _, declared := lookup(pkg.Name); declared {
		return false
	}
	// Keep the existing name-based direct-call contract for fragments without
	// imports. This fact only has an effect when the value is passed to Use.
	return true
}

// typeValue is the state of a parameter or variable declared with type expr
// and no value.
func (r *goRouteResolver) typeValue(expr ast.Expr) goRouteBinding {
	if r.isHTTPClientType(expr) {
		return goRouteBinding{kind: goRouteUnknown, httpClient: true}
	}
	expr = goRouteUnparen(expr)
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = goRouteUnparen(star.X)
	}
	switch expr := expr.(type) {
	case *ast.Ident:
		if r.file.Name != nil && goRouteDefaultFrameworkNames[r.file.Name.Name] {
			// Inside a framework's own package its types are unqualified:
			// classify them exactly like pkg.Type (IRoutes in package gin is
			// gin.IRoutes, a group type).
			framework := r.file.Name.Name
			if goRouteRootTypes[framework+"."+expr.Name] {
				return goRouteBinding{kind: goRouteKnown, framework: framework}
			}
			return goRouteUnknownBinding
		}
		if goRouteGroupishTypeName(expr.Name) {
			return goRouteUnknownBinding
		}
		// A local type this pass cannot see into: the long-standing root
		// heuristic.
		return goRouteBinding{}
	case *ast.SelectorExpr:
		pkg, ok := expr.X.(*ast.Ident)
		if ok && goRouteRootTypes[r.framework(pkg.Name)+"."+expr.Sel.Name] {
			return goRouteBinding{kind: goRouteKnown, framework: r.framework(pkg.Name)}
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

// goRouteMountStrips are the frameworks whose Mount serves the mounted
// router's routes under the mount prefix (chi Mount, fiber v2 App.Mount).
var goRouteMountStrips = map[string]bool{"chi": true, "fiber": true}

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
	if sub.kind != goRouteKnown || sub.prefix != "" || !goRouteMountStrips[origin.framework] || origin.region != r.region {
		// A group of the instance, a router whose framework does not strip
		// the mount prefix, or a mount that may run zero or many times.
		origin.opaque = true
		return
	}
	parent := r.groupValue(selector.X, lookup)
	prefix, ok := goRouteLiteral(origin.framework, r.text(call.Args[0]), r.constants)
	if !ok || parent.kind == goRouteUnknown {
		origin.opaque = true
		return
	}
	origin.mounts++
	origin.mountParent = nil
	origin.mountPrefix = prefix
	if parent.kind == goRouteKnown {
		origin.mountParent = parent.origin
		origin.mountPrefix = goRouteJoin(origin.framework, parent.prefix, prefix)
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
	r.computeParamFacts()
	called := map[*ast.SelectorExpr]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		r.step(1)
		switch n := n.(type) {
		case *ast.CallExpr:
			if fun, ok := n.Fun.(*ast.Ident); ok && !n.Ellipsis.IsValid() {
				// A same-file helper that only calls methods on a parameter
				// cannot mount or wrap what it is given there.
				for index, readOnly := range r.readOnlyParams[fun.Name] {
					if readOnly && index < len(n.Args) {
						mark(n.Args[index])
					}
				}
			}
			selector, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			called[selector] = true
			switch selector.Sel.Name {
			case "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS", "Handle", "Mount", "Walk":
				// Serving a router, or handing it to a mux that matches the
				// full path (net/http, chi and gorilla Handle do not strip).
				// Mount is composed or made opaque by noteMount. chi.Walk
				// only reads the routes.
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

// subrouterValue is gorilla's r.PathPrefix("/a").Subrouter(): a router whose
// routes are served under the parent's prefix plus /a. Any other Subrouter
// chain (Host, Methods, Headers, a dynamic or slash-terminated prefix) is
// unknown.
func (r *goRouteResolver) subrouterValue(call *ast.CallExpr, selector *ast.SelectorExpr, lookup goRouteLookup) goRouteBinding {
	if len(call.Args) != 0 {
		return goRouteUnknownBinding
	}
	pathPrefix, ok := selector.X.(*ast.CallExpr)
	if !ok || len(pathPrefix.Args) != 1 || pathPrefix.Ellipsis.IsValid() {
		return goRouteUnknownBinding
	}
	prefixSelector, ok := pathPrefix.Fun.(*ast.SelectorExpr)
	if !ok || prefixSelector.Sel.Name != "PathPrefix" {
		return goRouteUnknownBinding
	}
	var parent goRouteBinding
	switch receiver := prefixSelector.X.(type) {
	case *ast.Ident:
		parent, _ = lookup(receiver.Name)
	case *ast.CallExpr, *ast.ParenExpr:
		parent = r.groupValue(receiver, lookup)
	default:
		return goRouteUnknownBinding
	}
	prefix, ok := goRouteLiteral("mux", r.text(pathPrefix.Args[0]), r.constants)
	if !ok || parent.kind == goRouteUnknown {
		return goRouteUnknownBinding
	}
	if len(parent.prefix)+len(prefix) > goRouteMaxPrefixBytes {
		return goRouteUnknownBinding
	}
	// gorilla composes TrimRight(parent template, "/") + template, so a
	// slash-terminated prefix is composable ("/a/" + "/x" is "/a/x").
	return goRouteBinding{kind: goRouteKnown, prefix: goRouteJoin("mux", parent.prefix, prefix), origin: parent.origin, framework: "mux"}
}

// originAt returns this walk's router instance for the constructor at pos.
func (r *goRouteResolver) originAt(pos token.Pos, framework string, mountable bool) *goRouteOrigin {
	origin := r.origins[pos]
	if origin == nil {
		origin = &goRouteOrigin{framework: framework, mountable: mountable, region: r.region}
		r.origins[pos] = origin
	}
	if r.atPackage {
		// A package variable: a sibling file may mount or wrap it.
		origin.escaped = true
	}
	return origin
}

// noteStripPrefix handles http.StripPrefix(prefix, handler): every router
// instance reachable in handler (directly, wrapped in middleware, or as a
// ServeHTTP method value) is served under a prefix its own registrations do
// not show, so all of its routes are unknown.
func (r *goRouteResolver) noteStripPrefix(call *ast.CallExpr, scope *goRouteScope) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "StripPrefix" || len(call.Args) != 2 {
		return
	}
	ast.Inspect(call.Args[1], func(n ast.Node) bool {
		r.step(1)
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if binding := r.lookup(scope, ident.Name); binding != nil && binding.origin != nil {
			binding.origin.opaque = true
		}
		return true
	})
}

// inPackageConstructorValue recognizes a framework's own constructor called
// unqualified inside that framework's package (New() in package gin).
func (r *goRouteResolver) inPackageConstructorValue(call *ast.CallExpr, lookup goRouteLookup) (goRouteBinding, bool) {
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || r.file.Name == nil {
		return goRouteBinding{}, false
	}
	if _, declared := lookup(fun.Name); declared {
		return goRouteBinding{}, false
	}
	// Only a package named like the framework has a constructor-table entry.
	framework := r.file.Name.Name
	mountable, ok := goRouteConstructors[framework+"."+fun.Name]
	if !ok {
		return goRouteBinding{}, false
	}
	return goRouteBinding{kind: goRouteKnown, origin: r.originAt(call.Pos(), framework, mountable), framework: framework}, true
}

// goRouteServeMethods are router methods that serve the router itself: fiber
// Listen and Test, gin Run, echo Start.
var goRouteServeMethods = map[string]bool{
	"Listen": true, "ListenTLS": true, "ListenMutualTLS": true, "Listener": true, "Test": true,
	"Run": true, "RunTLS": true, "RunUnix": true, "RunListener": true,
	"Start": true, "StartTLS": true, "StartAutoTLS": true, "StartServer": true,
}

// noteServe marks router instances this file serves itself.
func (r *goRouteResolver) noteServe(call *ast.CallExpr, scope *goRouteScope) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	lookup := r.lookupIn(scope)
	markServed := func(expr ast.Expr) {
		ident, ok := goRouteUnparen(expr).(*ast.Ident)
		if !ok {
			return
		}
		if value, _ := lookup(ident.Name); value.kind == goRouteKnown && value.origin != nil && value.prefix == "" {
			value.origin.served = true
		}
	}
	switch selector.Sel.Name {
	case "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS":
		pkg, ok := selector.X.(*ast.Ident)
		if !ok {
			return
		}
		if _, declared := lookup(pkg.Name); declared || r.framework(pkg.Name) != "http" {
			return
		}
		for _, arg := range call.Args {
			markServed(arg)
		}
	case "NewServer", "NewTLSServer", "NewUnstartedServer":
		// httptest.NewServer(r) serves r at the root of a test server.
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || len(call.Args) != 1 {
			return
		}
		if _, declared := lookup(pkg.Name); declared || r.framework(pkg.Name) != "httptest" {
			return
		}
		markServed(call.Args[0])
	default:
		if goRouteServeMethods[selector.Sel.Name] {
			markServed(selector.X)
		}
	}
}

// noteServerLiteral marks the Handler of an http.Server literal as served.
func (r *goRouteResolver) noteServerLiteral(lit *ast.CompositeLit, scope *goRouteScope) {
	if !r.isFrameworkType(goRouteUnstar(lit.Type), "http", "Server") {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Handler" {
			continue
		}
		if ident, ok := goRouteUnparen(kv.Value).(*ast.Ident); ok {
			if value, _ := r.lookupIn(scope)(ident.Name); value.kind == goRouteKnown && value.origin != nil && value.prefix == "" {
				value.origin.served = true
			}
		}
	}
}

// goRouteNewableRouters are router types whose zero value is a usable root
// router, so new(T) constructs one.
var goRouteNewableRouters = map[string]bool{"mux.Router": true, "http.ServeMux": true, "httprouter.Router": true}

// newRouterValue recognizes new(mux.Router) (or new(Router) inside package mux).
func (r *goRouteResolver) newRouterValue(call *ast.CallExpr, lookup goRouteLookup) (goRouteBinding, bool) {
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || fun.Name != "new" || len(call.Args) != 1 {
		return goRouteBinding{}, false
	}
	if _, declared := lookup("new"); declared {
		return goRouteBinding{}, false
	}
	framework, name := "", ""
	switch typ := goRouteUnparen(call.Args[0]).(type) {
	case *ast.SelectorExpr:
		pkg, ok := typ.X.(*ast.Ident)
		if !ok {
			return goRouteBinding{}, false
		}
		framework, name = r.framework(pkg.Name), typ.Sel.Name
	case *ast.Ident:
		if r.file.Name == nil {
			return goRouteBinding{}, false
		}
		framework, name = r.file.Name.Name, typ.Name
	default:
		return goRouteBinding{}, false
	}
	if !goRouteNewableRouters[framework+"."+name] {
		return goRouteBinding{}, false
	}
	return goRouteBinding{kind: goRouteKnown, origin: r.originAt(call.Pos(), framework, framework == "http"), framework: framework}, true
}

func (r *goRouteResolver) paramOriginAt(key goRouteParamKey, framework string) *goRouteOrigin {
	origin := r.paramOrigins[key]
	if origin == nil {
		origin = &goRouteOrigin{framework: framework, region: r.region}
		r.paramOrigins[key] = origin
	}
	return origin
}

// noteParamArgs records what a same-file call passes for framework-root-typed
// parameters.
func (r *goRouteResolver) noteParamArgs(call *ast.CallExpr, scope *goRouteScope) {
	name, recv := "", ""
	lookup := r.lookupIn(scope)
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		name = fun.Name
	case *ast.SelectorExpr:
		name = fun.Sel.Name
		if pkg, ok := fun.X.(*ast.Ident); ok {
			if _, declared := lookup(pkg.Name); !declared {
				if _, imported := r.imports[pkg.Name]; imported {
					// Another package's function, not one declared here.
					return
				}
			}
		}
		recv = goRouteExprTypeName(fun.X)
		if recv == "" {
			recv = "*"
		}
	default:
		return
	}
	if !r.rootParamFuncs[name] {
		return
	}
	for index, arg := range call.Args {
		key := goRouteParamKey{recv: recv, fn: name, index: index}
		value := r.groupValue(arg, lookup)
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			value = goRouteUnknownBinding
		}
		r.paramArgs[key] = append(r.paramArgs[key], value)
	}
}

// computeParamFacts finds functions with framework-root-typed parameters,
// functions referenced other than as a call, and parameters a top-level
// function uses only as a method receiver or field base.
func (r *goRouteResolver) computeParamFacts() {
	r.rootParamFuncs = map[string]bool{}
	r.paramValueUse = map[string]bool{}
	r.readOnlyParams = map[string][]bool{}
	declNames := map[*ast.Ident]bool{}
	for _, decl := range r.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		declNames[fn.Name] = true
		var names []*ast.Ident
		for _, field := range fn.Type.Params.List {
			if r.typeValue(field.Type).kind == goRouteKnown {
				r.rootParamFuncs[fn.Name.Name] = true
			}
			if len(field.Names) == 0 {
				names = append(names, nil)
			}
			names = append(names, field.Names...)
		}
		if fn.Recv != nil || len(names) == 0 {
			continue
		}
		if _, dup := r.readOnlyParams[fn.Name.Name]; dup {
			// Two top-level functions of one name do not compile; trust neither.
			r.readOnlyParams[fn.Name.Name] = nil
			continue
		}
		readOnly := make([]bool, len(names))
		for i, name := range names {
			readOnly[i] = name != nil && name.Name != "_" && r.onlyReceiverUses(fn.Body, name.Name)
		}
		r.readOnlyParams[fn.Name.Name] = readOnly
	}
	calledIdents := map[*ast.Ident]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		r.step(1)
		if call, ok := n.(*ast.CallExpr); ok {
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				calledIdents[fun] = true
			case *ast.SelectorExpr:
				calledIdents[fun.Sel] = true
			}
		}
		if ident, ok := n.(*ast.Ident); ok && r.rootParamFuncs[ident.Name] && !calledIdents[ident] && !declNames[ident] {
			r.paramValueUse[ident.Name] = true
		}
		return true
	})
}

// onlyReceiverUses reports whether every use of name in body is the base of a
// selector (a method call or field read), never a value handed elsewhere, an
// assignment target or an address.
func (r *goRouteResolver) onlyReceiverUses(body *ast.BlockStmt, name string) bool {
	bases := map[*ast.Ident]bool{}
	ok := true
	ast.Inspect(body, func(n ast.Node) bool {
		r.step(1)
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if ident, isIdent := n.X.(*ast.Ident); isIdent && n.Sel.Name != "ServeHTTP" {
				bases[ident] = true
			}
		case *ast.Ident:
			if n.Name == name && !bases[n] {
				ok = false
			}
		}
		return true
	})
	return ok
}

// goRouteRecvTypeName is the receiver type name of a method declaration
// ("" for a function).
func goRouteRecvTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return goRouteTypeName(recv.List[0].Type)
}

func goRouteTypeName(expr ast.Expr) string {
	expr = goRouteUnparen(expr)
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = goRouteUnparen(star.X)
	}
	switch expr := expr.(type) {
	case *ast.IndexExpr:
		return goRouteTypeName(expr.X)
	case *ast.IndexListExpr:
		return goRouteTypeName(expr.X)
	case *ast.Ident:
		return expr.Name
	}
	return ""
}

// goRouteExprTypeName is the type of a method call's receiver expression when
// it is syntactically visible (T{}, &T{}, (*T)(nil)); "" otherwise.
func goRouteExprTypeName(expr ast.Expr) string {
	expr = goRouteUnparen(expr)
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = goRouteUnparen(unary.X)
	}
	switch expr := expr.(type) {
	case *ast.CompositeLit:
		return goRouteTypeName(expr.Type)
	case *ast.CallExpr:
		// A conversion (*T)(x) or T(x).
		if len(expr.Args) == 1 {
			if paren, ok := expr.Fun.(*ast.ParenExpr); ok {
				return goRouteTypeName(paren.X)
			}
		}
	}
	return ""
}

// noteUseStripPrefix handles r.Use(middleware.StripPrefix("/api")): the
// router's requests are rewritten before routing, so none of its routes is
// served at its registered path.
func (r *goRouteResolver) noteUseStripPrefix(call *ast.CallExpr, scope *goRouteScope) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Use" {
		return
	}
	lookup := r.lookupIn(scope)
	for _, arg := range call.Args {
		middleware := r.groupValue(arg, lookup)
		router := r.groupValue(selector.X, lookup)
		if router.origin == nil {
			continue
		}
		if middleware.stripPrefixMiddleware {
			router.origin.opaque = true
			continue
		}
		if middleware.stripPrefixSource != nil {
			r.watchStripPrefixSource(middleware.stripPrefixSource, router.origin)
		}
		for _, join := range middleware.stripPrefixJoins {
			r.step(1)
			r.observeStripPrefixJoin(join, router.origin)
		}
	}
}
