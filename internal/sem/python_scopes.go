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
	owner     string
	parent    *pythonBindingScope
	class     bool
	comp      bool
	bindings  map[string][]pythonBindingEvent
	locals    map[string]bool // function-local declarations shadow enclosing bindings everywhere
	globals   map[string]bool
	nonlocals map[string]bool
	calls     []pythonScopedCall
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

func (s *pythonBindingScope) lexicalContainer() *pythonBindingScope {
	for s != nil && s.class {
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
			scope.addName(w.fieldName(node, "name"), int(node.StartByte()), false)
		}
		matched, ok := w.functionSymbol(node)
		if ok && scope != nil {
			// Defaults and annotations execute while the definition is evaluated,
			// before the function body scope (and its local declarations) exists.
			// Attribute their calls to the function while resolving names through
			// the lexical parent; deliberately skip decorators to retain their
			// established handling.
			header := newPythonBindingScope(matched.ID, scope.lexicalContainer())
			w.walk(node.ChildByFieldName("parameters"), header, depth+1)
			w.walk(node.ChildByFieldName("return_type"), header, depth+1)
			w.publish(header)
			child := w.functionScopeForSymbol(node, scope, matched)
			w.walk(node.ChildByFieldName("body"), child, depth+1)
			w.publish(child)
		}
		return
	case "class_definition":
		if scope != nil {
			scope.addName(w.fieldName(node, "name"), int(node.StartByte()), false)
		}
		body := w.targetFieldOrLast(node, "body")
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if !samePythonNode(child, body) {
				w.walk(child, scope, depth+1) // decorators and base expressions
			}
		}
		// Class bindings are isolated, but executable class-body calls retain the
		// enclosing emitted owner rather than leaking class locals outward.
		if scope != nil {
			classScope := newPythonBindingScope(scope.owner, scope)
			classScope.class = true
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
			w.collectFunctionBindings(node.ChildByFieldName("body"), child, 0)
			w.walk(node.ChildByFieldName("body"), child, depth+1)
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
			w.addTarget(scope, w.targetFieldOrLast(node, "alias"))
		}
	case "except_clause":
		if scope != nil {
			w.addTarget(scope, w.targetFieldOrLast(node, "name"))
			w.walk(node.ChildByFieldName("body"), scope, depth+1)
			return // exception type expressions are not handler-body calls
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
	for i := 0; i < int(node.NamedChildCount()); i++ {
		part := node.NamedChild(i)
		if part.Type() != "for_in_clause" {
			result = append(result, part)
			continue
		}
		// Each iterable is evaluated before its own target is bound. Later
		// clauses and the result expression see that target in the isolated
		// comprehension scope.
		w.walk(part.ChildByFieldName("right"), child, depth+1)
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

func (w *pythonScopeWalker) functionSymbol(node *sitter.Node) (SymbolRecord, bool) {
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
		return SymbolRecord{}, false
	}
	return matched, true
}

func (w *pythonScopeWalker) functionScopeForSymbol(node *sitter.Node, parent *pythonBindingScope, matched SymbolRecord) *pythonBindingScope {
	scope := newPythonBindingScope(matched.ID, parent.lexicalContainer())
	w.addParameters(scope, node.ChildByFieldName("parameters"))
	w.collectFunctionBindings(node.ChildByFieldName("body"), scope, 0)
	return scope
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
		if events[len(events)-1].byteOffset <= byteOffset {
			return events[len(events)-1].modules
		}
	}
	return nil
}

func (s *pythonBindingScope) hasBindingAt(name string, byteOffset int) bool {
	for _, event := range s.bindings[name] {
		if event.byteOffset <= byteOffset {
			return true
		}
	}
	return false
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
	case "tuple", "list", "pattern_list", "list_pattern", "tuple_pattern", "parenthesized_expression", "starred_expression", "list_splat_pattern", "dictionary_splat_pattern":
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
		w.collectTargetNames(w.targetFieldOrLast(node, "alias"), scope)
	case "except_clause":
		w.collectTargetNames(w.targetFieldOrLast(node, "name"), scope)
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
	statements := pythonFromImportStatements(text)
	if len(statements) != 1 {
		return nil
	}
	var out []pythonScopedImport
	for _, item := range statements[0].items {
		name, alias := parsePythonImportItem(item)
		if name == "" || name == "*" {
			continue
		}
		local := alias
		if local == "" {
			local = name
		}
		out = append(out, pythonScopedImport{name: local, modules: []string{statements[0].module}})
	}
	return out
}
