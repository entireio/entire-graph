package sem

import (
	"context"
	"strings"
	"time"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

// pythonBareImportScopes is a single AST-derived scope view for one Python
// file. It records whether each callable has at least one bare identifier call
// that is not shadowed at that call site. The raw FFI import exception consults
// only this view; ordinary call resolution continues to use its existing rules.
type pythonBareImportScopes struct {
	imports  map[string]map[string][][]string
	module   map[string][][]string
	complete bool
}

func newPythonBareImportScopes(content string, symbols []SymbolRecord) *pythonBareImportScopes {
	state := &pythonBareImportScopes{imports: map[string]map[string][][]string{}, module: map[string][][]string{}}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(python.GetLanguage())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tree, err := parser.ParseCtx(ctx, nil, []byte(content))
	if err != nil || tree == nil {
		return state
	}
	defer tree.Close()
	root := tree.RootNode()
	if !validNode(root) || root.HasError() {
		return state
	}
	walker := pythonScopeWalker{src: []byte(content), symbols: symbols, state: state, complete: true}
	module := newPythonBindingScope("", nil)
	walker.walk(root, module, 0)
	if !walker.complete {
		return state
	}
	walker.publish(module)
	state.complete = true
	return state
}

func (s *pythonBareImportScopes) importModules(from SymbolRecord, name string) []string {
	sets := s.importModuleSets(from, name)
	var modules []string
	for _, set := range sets {
		modules = append(modules, set...)
	}
	return uniqueStrings(modules)
}

func (s *pythonBareImportScopes) importModuleSets(from SymbolRecord, name string) [][]string {
	if s == nil || !s.complete {
		return nil
	}
	if from.Kind == "file" {
		return s.module[name]
	}
	return s.imports[from.ID][name]
}

type pythonBindingScope struct {
	owner string
	// parent is the scope as code running right now sees it; deferred, set only
	// on a class scope, is the scope as code that runs later -- the bodies of the
	// methods defined in it -- sees it. They differ by the class's own name.
	parent   *pythonBindingScope
	deferred *pythonBindingScope
	class    bool
	comp     bool
	// hiddenName is one binding this view cannot see from hiddenFrom onwards: a
	// definition's own name, invisible to the parts of its own statement that
	// Python evaluates before the name is bound.
	hiddenName string
	hiddenFrom int
	bindings   map[string][]pythonBindingEvent
	locals     map[string]bool // function-local declarations shadow enclosing bindings everywhere
	globals    map[string]bool
	nonlocals  map[string]bool
	calls      []pythonScopedCall
}

type pythonBindingEvent struct {
	byteOffset int
	modules    []string // nil = ordinary assignment/declaration
}

type pythonScopedCall struct {
	name       string
	byteOffset int
}

func (s *pythonBindingScope) importModules(name string, byteOffset int) []string {
	if s == nil {
		return nil
	}
	if s.nonlocals[name] {
		if modules := s.bindingAt(name, byteOffset); modules != nil || s.hasBindingAt(name, byteOffset) {
			return modules
		}
		return s.lexicalParent().importModules(name, byteOffset)
	}
	if s.globals[name] {
		if modules := s.bindingAt(name, byteOffset); modules != nil || s.hasBindingAt(name, byteOffset) {
			return modules
		}
		for module := s.parent; module != nil; module = module.parent {
			if module.parent == nil {
				return module.bindingAt(name, byteOffset)
			}
		}
		return nil
	}
	if s.locals[name] {
		return s.bindingAt(name, byteOffset)
	}
	if modules := s.bindingAt(name, byteOffset); modules != nil || s.hasBindingAt(name, byteOffset) {
		return modules
	}
	return s.lexicalParent().importModules(name, byteOffset)
}

func newPythonBindingScope(owner string, parent *pythonBindingScope) *pythonBindingScope {
	return &pythonBindingScope{owner: owner, parent: parent, bindings: map[string][]pythonBindingEvent{}, locals: map[string]bool{}, globals: map[string]bool{}, nonlocals: map[string]bool{}}
}

func (s *pythonBindingScope) lexicalParent() *pythonBindingScope {
	if s == nil {
		return nil
	}
	p := s.parent
	for p != nil && p.class {
		p = p.parent
	}
	return p
}

// signatureView is the enclosing scope as one nested function's signature sees
// it: the same bindings (a walrus in a default argument really does bind out
// here), reattributed to that function's owner and starting with no calls of
// its own so the enclosing scope's calls are published once.
func (s *pythonBindingScope) signatureView(owner string) *pythonBindingScope {
	view := *s
	view.owner = owner
	view.calls = nil
	return &view
}

// preDefinitionView is the enclosing scope as the parts of a `class` statement
// that run before it finishes see it. Only the class's own name is hidden, and
// the bindings map stays shared, so a walrus in a base really does bind out
// here just as one in a default argument does.
func (s *pythonBindingScope) preDefinitionView(name string, byteOffset int) *pythonBindingScope {
	view := *s
	view.hiddenName = strings.TrimSpace(name)
	view.hiddenFrom = byteOffset
	view.calls = nil
	return &view
}

// lexicalContainer is the scope a nested function body resolves against. It
// runs after its container statements have finished, so it crosses a class
// scope by its deferred parent -- the one that can see the class name.
func (s *pythonBindingScope) lexicalContainer() *pythonBindingScope {
	for s != nil && s.class {
		if s.deferred != nil {
			s = s.deferred
			continue
		}
		s = s.parent
	}
	return s
}

type pythonScopeWalker struct {
	src      []byte
	symbols  []SymbolRecord
	state    *pythonBareImportScopes
	complete bool
}

func (w *pythonScopeWalker) walk(node *sitter.Node, scope *pythonBindingScope, depth int) {
	if !validNode(node) {
		return
	}
	if depth >= maxParseWalkDepth {
		w.complete = false
		return
	}
	switch node.Type() {
	case "function_definition":
		if scope != nil {
			// A `def` does not rebind its own name until the whole statement has
			// run, so the name starts shadowing at the BODY: a default or an
			// annotation that calls a same-named enclosing symbol really does
			// reach that symbol (`def compute(v=compute())` calls the outer
			// `compute`, and raises NameError when there is none). The body is
			// deferred code that runs once the name is bound, so recursion still
			// resolves to the function itself.
			scope.addName(w.fieldName(node, "name"), w.bindingOffset(node, node.ChildByFieldName("body")), false)
		}
		child := w.functionScope(node, scope)
		if child != nil {
			// Default arguments and annotations are evaluated where the `def`
			// runs, not inside the call frame, so they read the ENCLOSING
			// bindings and never the parameters they sit beside. The call scan
			// still credits them to the callable whose signature line holds
			// them, so resolve them against the enclosing scope and publish them
			// under the function's own owner.
			if scope != nil {
				signature := scope.signatureView(child.owner)
				body := node.ChildByFieldName("body")
				for i := 0; i < int(node.NamedChildCount()); i++ {
					if part := node.NamedChild(i); !samePythonNode(part, body) {
						w.walk(part, signature, depth+1)
					}
				}
				w.publish(signature)
			}
			w.walk(node.ChildByFieldName("body"), child, depth+1)
			w.publish(child)
		}
		return
	case "class_definition":
		body := w.targetFieldOrLast(node, "body")
		if scope != nil {
			// The class name is bound only once the whole `class` statement has
			// run. Everything the statement evaluates on the way there -- its
			// bases, the body's own statements, and the signatures of the methods
			// it defines -- still reads the enclosing binding, which the pre-class
			// view supplies. Method BODIES are the exception: they run after the
			// class exists, so they keep the real scope, where the name is bound
			// from the `class` line on. Offsets cannot separate the two because
			// they interleave inside the body, hence the explicit views.
			scope.addName(w.fieldName(node, "name"), int(node.StartByte()), false)
			pre := scope.preDefinitionView(w.fieldName(node, "name"), int(node.StartByte()))
			for i := 0; i < int(node.NamedChildCount()); i++ {
				child := node.NamedChild(i)
				if !samePythonNode(child, body) {
					w.walk(child, pre, depth+1) // decorators and base expressions
				}
			}
			w.publish(pre)
			// Class bindings are isolated, but executable class-body calls retain the
			// enclosing emitted owner rather than leaking class locals outward.
			classScope := newPythonBindingScope(scope.owner, pre)
			classScope.class = true
			classScope.deferred = scope
			w.walk(body, classScope, depth+1)
			w.publish(classScope)
		}
		return
	case "lambda":
		if scope != nil {
			child := newPythonBindingScope(scope.owner, scope)
			parameters := node.ChildByFieldName("parameters")
			if !validNode(parameters) {
				for i := 0; i < int(node.NamedChildCount()); i++ {
					candidate := node.NamedChild(i)
					if candidate.Type() == "lambda_parameters" {
						parameters = candidate
						break
					}
				}
			}
			w.addParameters(child, parameters)
			// A default is evaluated where the `lambda` expression itself runs,
			// not inside the call frame, so it reads the ENCLOSING bindings and
			// never the parameters beside it. CPython is the oracle --
			//
			//	$ python3 -c 'def compute(): return "OUTER-FN"
			//	f = lambda compute=compute(): compute
			//	print(f())'                            -> OUTER-FN
			//	$ python3 -c 'f = lambda a, b=a: b'     -> NameError: name 'a' is not defined
			//
			// -- so walking only the body recorded such a call in no scope at
			// all, and a `complete` view that reports no modules for the name
			// makes importsWithName DELETE the file-level import binding,
			// taking the resolved call edge with it. A lambda owns no symbol of
			// its own, so the enclosing scope is both what the default reads and
			// what the call scan already credits with the call.
			body := node.ChildByFieldName("body")
			for i := 0; i < int(node.NamedChildCount()); i++ {
				if part := node.NamedChild(i); !samePythonNode(part, body) {
					w.walk(part, scope, depth+1)
				}
			}
			w.collectFunctionBindings(body, child, 0)
			w.walk(body, child, depth+1)
			w.publish(child)
		}
		return
	case "list_comprehension", "set_comprehension", "dictionary_comprehension", "generator_expression":
		w.walkComprehension(node, scope, depth+1)
		return
	case "call":
		if scope != nil {
			fn := node.ChildByFieldName("function")
			if validNode(fn) && fn.Type() == "identifier" {
				scope.calls = append(scope.calls, pythonScopedCall{name: fn.Content(w.src), byteOffset: int(fn.StartByte())})
			}
		}
	case "assignment", "augmented_assignment", "annotated_assignment":
		if scope != nil {
			w.addTarget(scope, node.ChildByFieldName("left"))
		}
	case "for_statement":
		if scope != nil {
			w.addTarget(scope, w.targetFieldOrFirst(node, "left"))
		}
	case "with_item":
		if scope != nil {
			w.addTarget(scope, w.asPatternAlias(node))
		}
	case "except_clause", "except_group_clause":
		if scope != nil {
			// The exception expression and the handler body are both ordinary
			// code of this scope, so neither is skipped: the generic child walk
			// below reaches them exactly like any other statement.
			w.addTarget(scope, w.asPatternAlias(node))
		}
	case "named_expression":
		if scope != nil {
			w.addTarget(w.walrusScope(scope), w.targetFieldOrFirst(node, "name"))
		}
	case "delete_statement":
		if scope != nil {
			w.addTarget(scope, node.NamedChild(0))
		}
	case "global_statement":
		if scope != nil {
			w.statementNames(node, scope.globals)
		}
	case "nonlocal_statement":
		if scope != nil {
			w.statementNames(node, scope.nonlocals)
		}
	case "import_statement", "import_from_statement":
		if scope != nil {
			for _, binding := range w.importBindings(node) {
				scope.addImport(binding.name, binding.modules, int(node.StartByte()))
			}
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		w.walk(node.NamedChild(i), scope, depth+1)
	}
}

func samePythonNode(a, b *sitter.Node) bool {
	return validNode(a) && validNode(b) && a.Type() == b.Type() && a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte()
}

func (w *pythonScopeWalker) walkComprehension(node *sitter.Node, parent *pythonBindingScope, depth int) {
	if parent == nil {
		return
	}
	child := newPythonBindingScope(parent.owner, parent)
	child.comp = true
	var result []*sitter.Node
	outermost := true
	for i := 0; i < int(node.NamedChildCount()); i++ {
		part := node.NamedChild(i)
		if part.Type() != "for_in_clause" {
			result = append(result, part)
			continue
		}
		// Each iterable is evaluated before its own target is bound. Later
		// clauses and the result expression see that target in the isolated
		// comprehension scope. The OUTERMOST iterable is the exception: Python
		// evaluates it where the comprehension is written and passes it in, so
		// it reads the enclosing bindings and a target the comprehension binds
		// later never shadows it.
		iterable := child
		if outermost {
			iterable = parent
		}
		outermost = false
		w.walk(part.ChildByFieldName("right"), iterable, depth+1)
		w.addTarget(child, part.ChildByFieldName("left"))
		for j := 0; j < int(part.NamedChildCount()); j++ {
			clause := part.NamedChild(j)
			if clause != part.ChildByFieldName("left") && clause != part.ChildByFieldName("right") {
				w.walk(clause, child, depth+1)
			}
		}
	}
	for _, part := range result {
		w.walk(part, child, depth+1)
	}
	w.publish(child)
}

func (w *pythonScopeWalker) functionScope(node *sitter.Node, parent *pythonBindingScope) *pythonBindingScope {
	start, end := int(node.StartByte()), int(node.EndByte())
	var matched SymbolRecord
	found := false
	for _, symbol := range w.symbols {
		if symbol.Kind != "function" && symbol.Kind != "method" {
			continue
		}
		if symbol.sourceStartByte <= start && end <= symbol.sourceEndByte && (!found || symbol.sourceEndByte-symbol.sourceStartByte < matched.sourceEndByte-matched.sourceStartByte) {
			matched, found = symbol, true
		}
	}
	if !found {
		name := w.fieldName(node, "name")
		line := int(node.StartPoint().Row) + 1
		for _, symbol := range w.symbols {
			if (symbol.Kind != "function" && symbol.Kind != "method") || symbol.Name != name || symbol.StartLine != line {
				continue
			}
			matched, found = symbol, true
			break
		}
	}
	if !found {
		return nil
	}
	scope := newPythonBindingScope(matched.ID, parent.lexicalContainer())
	w.addParameters(scope, node.ChildByFieldName("parameters"))
	w.collectFunctionBindings(node.ChildByFieldName("body"), scope, 0)
	return scope
}

// bindingOffset is where a definition's own name starts shadowing the enclosing
// binding: at its body, because Python evaluates decorators, bases, defaults and
// annotations first and rebinds the name only once the statement has run.
func (w *pythonScopeWalker) bindingOffset(node, body *sitter.Node) int {
	if validNode(body) && body.StartByte() > node.StartByte() {
		return int(body.StartByte())
	}
	return int(node.StartByte())
}

func (w *pythonScopeWalker) publish(scope *pythonBindingScope) {
	for _, call := range scope.calls {
		modules := scope.importModules(call.name, call.byteOffset)
		if len(modules) == 0 {
			continue
		}
		if scope.owner == "" {
			w.state.module[call.name] = appendModuleSet(w.state.module[call.name], modules)
			continue
		}
		if w.state.imports[scope.owner] == nil {
			w.state.imports[scope.owner] = map[string][][]string{}
		}
		w.state.imports[scope.owner][call.name] = appendModuleSet(w.state.imports[scope.owner][call.name], modules)
	}
}

func appendModuleSet(sets [][]string, modules []string) [][]string {
	modules = uniqueStrings(modules)
	for _, set := range sets {
		if len(set) == len(modules) && strings.Join(set, "\x00") == strings.Join(modules, "\x00") {
			return sets
		}
	}
	return append(sets, modules)
}

func (s *pythonBindingScope) addName(name string, byteOffset int, local bool) {
	name = strings.TrimSpace(name)
	if name != "" {
		if local {
			s.locals[name] = true
		}
		s.bindings[name] = append(s.bindings[name], pythonBindingEvent{byteOffset: byteOffset})
	}
}

func (s *pythonBindingScope) addImport(name string, modules []string, byteOffset int) {
	name = strings.TrimSpace(name)
	if name != "" && len(modules) > 0 {
		s.bindings[name] = append(s.bindings[name], pythonBindingEvent{byteOffset: byteOffset, modules: uniqueStrings(modules)})
	}
}

func (w *pythonScopeWalker) walrusScope(scope *pythonBindingScope) *pythonBindingScope {
	for scope != nil && scope.comp {
		scope = scope.parent
	}
	return scope
}

func (s *pythonBindingScope) bindingAt(name string, byteOffset int) []string {
	for events := s.bindings[name]; len(events) > 0; events = events[:len(events)-1] {
		if event := events[len(events)-1]; event.byteOffset <= byteOffset && !s.hides(name, event) {
			return event.modules
		}
	}
	return nil
}

func (s *pythonBindingScope) hasBindingAt(name string, byteOffset int) bool {
	for _, event := range s.bindings[name] {
		if event.byteOffset <= byteOffset && !s.hides(name, event) {
			return true
		}
	}
	return false
}

// hides reports the one binding a pre-definition view cannot see.
func (s *pythonBindingScope) hides(name string, event pythonBindingEvent) bool {
	return s.hiddenName != "" && name == s.hiddenName && event.byteOffset >= s.hiddenFrom
}

func (s *pythonBindingScope) moduleScope() *pythonBindingScope {
	for s.parent != nil {
		s = s.parent
	}
	return s
}

func (w *pythonScopeWalker) fieldName(node *sitter.Node, field string) string {
	child := node.ChildByFieldName(field)
	if !validNode(child) {
		return ""
	}
	return child.Content(w.src)
}

func (w *pythonScopeWalker) addTarget(scope *pythonBindingScope, node *sitter.Node) {
	if !validNode(node) {
		return
	}
	if node.Type() == "identifier" {
		scope.addName(node.Content(w.src), int(node.StartByte()), scope.owner != "")
		return
	}
	// Assignment targets may be destructured, but an attribute or subscription
	// writes through an existing object and binds no identifier in this scope.
	if node.Type() == "attribute" || node.Type() == "subscript" {
		return
	}
	switch node.Type() {
	case "tuple", "list", "pattern_list", "list_pattern", "tuple_pattern", "parenthesized_expression", "starred_expression", "list_splat_pattern", "dictionary_splat_pattern", "as_pattern_target":
		for i := 0; i < int(node.NamedChildCount()); i++ {
			w.addTarget(scope, node.NamedChild(i))
		}
	}
}

func (w *pythonScopeWalker) addParameters(scope *pythonBindingScope, node *sitter.Node) {
	if !validNode(node) {
		return
	}
	if node.Type() == "identifier" {
		scope.addName(node.Content(w.src), int(node.StartByte()), true)
		return
	}
	if node.Type() == "default_parameter" || node.Type() == "typed_parameter" || node.Type() == "typed_default_parameter" || node.Type() == "list_splat_pattern" || node.Type() == "dictionary_splat_pattern" || node.Type() == "parameters" || node.Type() == "lambda_parameters" {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if node.Type() == "default_parameter" || node.Type() == "typed_default_parameter" {
				if i > 0 {
					continue
				}
			}
			w.addParameters(scope, child)
		}
	}
}

// collectFunctionBindings performs Python's function-wide local declaration
// pass before calls are evaluated. Imports still retain ordered runtime events;
// a call before its local import is therefore unresolved rather than inheriting
// an enclosing binding.
func (w *pythonScopeWalker) collectFunctionBindings(node *sitter.Node, scope *pythonBindingScope, depth int) {
	if !validNode(node) || depth >= maxParseWalkDepth {
		if validNode(node) {
			w.complete = false
		}
		return
	}
	switch node.Type() {
	case "function_definition", "class_definition":
		scope.addName(w.fieldName(node, "name"), int(node.StartByte()), true)
		return
	case "lambda":
		return
	case "list_comprehension", "set_comprehension", "dictionary_comprehension", "generator_expression":
		w.collectComprehensionWalrus(node, scope, depth+1)
		return
	case "assignment", "augmented_assignment", "annotated_assignment":
		w.collectTargetNames(node.ChildByFieldName("left"), scope)
	case "for_statement":
		w.collectTargetNames(w.targetFieldOrFirst(node, "left"), scope)
	case "with_item":
		w.collectTargetNames(w.asPatternAlias(node), scope)
	case "except_clause", "except_group_clause":
		w.collectTargetNames(w.asPatternAlias(node), scope)
	case "named_expression":
		w.collectTargetNames(w.targetFieldOrFirst(node, "name"), scope)
	case "delete_statement":
		w.collectTargetNames(node.NamedChild(0), scope)
	case "global_statement":
		w.statementNames(node, scope.globals)
	case "nonlocal_statement":
		w.statementNames(node, scope.nonlocals)
	case "import_statement", "import_from_statement":
		for _, binding := range w.importBindings(node) {
			scope.locals[binding.name] = true
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		w.collectFunctionBindings(node.NamedChild(i), scope, depth+1)
	}
}

func (w *pythonScopeWalker) collectComprehensionWalrus(node *sitter.Node, scope *pythonBindingScope, depth int) {
	if !validNode(node) || depth >= maxParseWalkDepth {
		if validNode(node) {
			w.complete = false
		}
		return
	}
	switch node.Type() {
	case "function_definition", "class_definition", "lambda":
		return
	case "named_expression":
		w.collectTargetNames(w.targetFieldOrFirst(node, "name"), scope)
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		w.collectComprehensionWalrus(node.NamedChild(i), scope, depth+1)
	}
}

func (w *pythonScopeWalker) collectTargetNames(node *sitter.Node, scope *pythonBindingScope) {
	if !validNode(node) {
		return
	}
	if node.Type() == "identifier" {
		scope.locals[node.Content(w.src)] = true
		return
	}
	if node.Type() == "attribute" || node.Type() == "subscript" {
		return
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		w.collectTargetNames(node.NamedChild(i), scope)
	}
}

// asPatternAlias returns the target an `X as t` clause binds. tree-sitter hangs
// that alias off a nested `as_pattern`, so neither `with_item` nor
// `except_clause` carries an alias field of its own; asking them for their last
// named child instead landed on the handler's own block. In the walk that bound
// nothing, and in the function-wide local pass -- which recurses into whatever
// it is handed -- it declared every name written anywhere inside the handler a
// local of the function, reporting the imports they stood for as shadowed.
func (w *pythonScopeWalker) asPatternAlias(node *sitter.Node) *sitter.Node {
	if !validNode(node) {
		return nil
	}
	if node.Type() == "as_pattern" {
		return node.ChildByFieldName("alias")
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if child := node.NamedChild(i); validNode(child) && child.Type() == "as_pattern" {
			return child.ChildByFieldName("alias")
		}
	}
	return nil
}

func (w *pythonScopeWalker) targetFieldOrFirst(node *sitter.Node, field string) *sitter.Node {
	if target := node.ChildByFieldName(field); validNode(target) {
		return target
	}
	return node.NamedChild(0)
}

func (w *pythonScopeWalker) targetFieldOrLast(node *sitter.Node, field string) *sitter.Node {
	if target := node.ChildByFieldName(field); validNode(target) {
		return target
	}
	count := int(node.NamedChildCount())
	if count == 0 {
		return nil
	}
	return node.NamedChild(count - 1)
}

func (w *pythonScopeWalker) statementNames(node *sitter.Node, out map[string]bool) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "identifier" {
			out[child.Content(w.src)] = true
		}
	}
}

type pythonScopedImport struct {
	name    string
	modules []string
}

func (w *pythonScopeWalker) importBindings(node *sitter.Node) []pythonScopedImport {
	text := node.Content(w.src)
	if node.Type() == "import_statement" {
		items := strings.Split(strings.TrimSpace(strings.TrimPrefix(text, "import")), ",")
		out := make([]pythonScopedImport, 0, len(items))
		for _, item := range items {
			module, alias := parsePythonImportItem(item)
			if module == "" {
				continue
			}
			name := alias
			if name == "" {
				name = strings.Split(module, ".")[0] // import a.b binds a
			}
			out = append(out, pythonScopedImport{name: name, modules: []string{module}})
		}
		return out
	}
	if node.Type() != "import_from_statement" {
		return nil
	}
	parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(text, "from")), " import ", 2)
	if len(parts) != 2 {
		return nil
	}
	module := strings.TrimSpace(parts[0])
	var out []pythonScopedImport
	for _, item := range strings.Split(parts[1], ",") {
		name, alias := parsePythonImportItem(item)
		if name == "" || name == "*" {
			continue
		}
		local := alias
		if local == "" {
			local = name
		}
		out = append(out, pythonScopedImport{name: local, modules: []string{module}})
	}
	return out
}
