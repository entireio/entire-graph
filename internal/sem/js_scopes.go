package sem

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	treesittertsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	treesitterts "github.com/smacker/go-tree-sitter/typescript/typescript"
)

// jsScanState is the single per-file scope view used by JavaScript/TypeScript
// call resolution. It is derived from one tree-sitter parse of the file, so
// regex literals, template strings, JSX, and comments are ordinary AST nodes
// rather than text that scanners must mask heuristically:
//
//   - namespaces: every namespace/module declaration with its body range and
//     full dotted path.
//   - bindings: every lexical binding that can shadow a namespace receiver —
//     parameters (function scope), let/const including arbitrarily nested
//     destructuring (block scope), for-of/for-in heads (loop scope), catch
//     parameters, var declarations (hoisted to the enclosing function), and
//     function/class/enum declarations that do not merge with a same-scope
//     namespace of the same name.
//   - calls: every dotted call site (`A.B.f(...)`, `new A.B.T(...)`) whose
//     receiver is a plain identifier chain, with its byte position, so
//     namespace classification can use the call's own lexical context.
type jsScanState struct {
	namespaces   []jsNamespaceScope
	namespaceSet map[string]bool
	bindings     []jsBindingScope
	calls        []jsDottedCall
	lineStarts   []int
	contentLen   int
	parsed       bool
	// depthTruncated reports that a relation-phase walk stopped at
	// maxParseWalkDepth (parser.go). The scope state stays usable — everything
	// above the limit was collected — but it is incomplete, so the provider
	// records it as an E_PARSE_DEPTH_EXCEEDED partial failure rather than
	// letting a silently short scope scan look like a complete one.
	depthTruncated bool
}

type jsNamespaceScope struct {
	Name      string
	StartLine int
	EndLine   int
	startByte int
	endByte   int
	// declScope{Start,End} is the lexical scope surrounding the declaration,
	// used to decide TypeScript declaration merging (`function B() {}` next to
	// `namespace B {}` merges; the function name must not shadow the namespace).
	declScopeStart int
	declScopeEnd   int
}

type jsBindingScope struct {
	Name      string
	startByte int
	endByte   int
}

type jsDottedCall struct {
	parts     []string
	startByte int
	line      int
}

// newJSScanState parses content with the same grammar/masking choices the
// symbol parser makes for path (tsx vs typescript vs javascript, Flow
// sniffing) and derives the scope state from the tree. A failed parse yields
// an empty state — no namespace calls are classified for that file — and a
// non-nil error so the caller can record a partial failure instead of
// silently degrading (a timeout wraps context.DeadlineExceeded).
func newJSScanState(ctx context.Context, path, content string) (*jsScanState, error) {
	// jsScanGrammar masks the source before any parse happens, which is the
	// same "a later stage was reached unbudgeted" shape as the parse itself.
	// Guard the whole scan on entry, not just the parse inside it.
	if err := ctx.Err(); err != nil {
		return emptyJSScanState(content), fmt.Errorf("tree-sitter scope parse stopped by caller: %w", err)
	}
	grammar, parseSrc := jsScanGrammar(path, content)
	return buildJSScanState(ctx, grammar, parseSrc, content)
}

// newJSScanStateForContent serves content-only callers (no path available):
// it prefers the TypeScript grammar and falls back to TSX when the parse
// fails outright.
func newJSScanStateForContent(ctx context.Context, content string) *jsScanState {
	if ctx.Err() != nil {
		return emptyJSScanState(content)
	}
	state, _ := buildJSScanState(ctx, treesitterts.GetLanguage(), []byte(maskTypeScriptUnsupportedSyntax(content)), content)
	if state.parsed {
		return state
	}
	state, _ = buildJSScanState(ctx, treesittertsx.GetLanguage(), []byte(content), content)
	return state
}

// emptyJSScanState is the "nothing was scanned" state every failure path
// returns: no namespaces, no bindings, no calls, but with the line index the
// callers' position helpers need.
func emptyJSScanState(content string) *jsScanState {
	return &jsScanState{
		namespaceSet: map[string]bool{},
		lineStarts:   jsLineStarts(content),
		contentLen:   len(content),
	}
}

func jsScanGrammar(path, content string) (*sitter.Language, []byte) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts":
		return treesitterts.GetLanguage(), []byte(maskTypeScriptUnsupportedSyntax(content))
	case ".tsx":
		return treesittertsx.GetLanguage(), []byte(content)
	default:
		if looksLikeFlowJavaScript(content) {
			return treesittertsx.GetLanguage(), []byte(maskFlowJavaScriptUnsupportedSyntax(content))
		}
		if strings.EqualFold(filepath.Ext(path), ".jsx") {
			return treesittertsx.GetLanguage(), []byte(content)
		}
		return javascript.GetLanguage(), []byte(content)
	}
}

// jsScanParseTimeout bounds the relation-phase scope parse. It is a variable
// (initialized to the shared tree-sitter budget) so tests can exercise the
// timeout path deterministically.
var jsScanParseTimeout = treeSitterParseTimeout

// buildJSScanState parses under the CALLER's context. The parse budget below
// is a cap on top of that context, not a fresh one rooted at
// context.Background: the relation-phase scan is per file, so a scan rooted at
// Background let every remaining file spend up to jsScanParseTimeout after the
// wall-clock budget had already expired.
func buildJSScanState(ctx context.Context, grammar *sitter.Language, parseSrc []byte, content string) (*jsScanState, error) {
	state := emptyJSScanState(content)
	if err := ctx.Err(); err != nil {
		return state, fmt.Errorf("tree-sitter scope parse stopped by caller: %w", err)
	}
	parseCtx, cancel := context.WithTimeout(ctx, jsScanParseTimeout)
	defer cancel()
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(grammar)
	tree, err := parser.ParseCtx(parseCtx, nil, parseSrc)
	if tree != nil {
		defer tree.Close()
	}
	if err != nil || tree == nil {
		if callerErr := ctx.Err(); callerErr != nil {
			return state, fmt.Errorf("tree-sitter scope parse stopped by caller: %w", callerErr)
		}
		if parseCtx.Err() != nil {
			return state, fmt.Errorf("%w: tree-sitter scope parse exceeded %s", context.DeadlineExceeded, jsScanParseTimeout)
		}
		if err == nil {
			err = errors.New("tree-sitter scope parse produced no tree")
		}
		return state, err
	}
	root := tree.RootNode()
	if !validNode(root) {
		return state, errors.New("tree-sitter scope parse produced no root node")
	}
	state.parsed = true
	// Bounding the tree-sitter call bounds the PARSE. Everything below runs on
	// the tree the parse produced, and it is not linear: the merge filter asks
	// namespaceMergesDeclaration once per declaration, and that scans the whole
	// namespace list, so a file with N namespaces and N declarations costs
	// O(N^2). Measured with the real verb on one 16k-declaration TypeScript
	// file, this ran 4.4 s past an already-expired budget. Poll the caller's
	// context through the walk and through the post-walk passes, and return the
	// caller's error so the relation phase records the degraded scan the same
	// way it records a timed-out parse.
	walker := &jsScopeWalker{src: parseSrc, state: state, budget: ctx}
	walker.walk(root, jsScopeContext{
		lexStart:  -1,
		lexEnd:    len(content),
		funcStart: -1,
		funcEnd:   len(content),
	}, 0)
	if walker.stopped {
		return emptyJSScanState(content), fmt.Errorf("tree-sitter scope walk stopped by caller: %w", ctx.Err())
	}
	if ctx.Err() != nil {
		return emptyJSScanState(content), fmt.Errorf("tree-sitter scope walk stopped by caller: %w", ctx.Err())
	}
	for _, scope := range state.namespaces {
		state.namespaceSet[scope.Name] = true
	}
	sort.Slice(state.calls, func(i, j int) bool { return state.calls[i].startByte < state.calls[j].startByte })
	// namespaceMergesDeclaration scans the whole namespace list for every
	// declaration, so this loop is quadratic exactly when the file declares
	// namespaces. Poll every iteration in that case -- the poll is cheap beside
	// the scan it guards -- and every stride otherwise, where the loop is linear
	// and a per-iteration poll would itself become the dominant cost.
	mergeIsSuperlinear := len(state.namespaces) > 0
	for index, declaration := range walker.declarationBindings {
		if (mergeIsSuperlinear || index%jsScanPollStride == 0) && ctx.Err() != nil {
			return emptyJSScanState(content), fmt.Errorf("tree-sitter scope walk stopped by caller: %w", ctx.Err())
		}
		if state.namespaceMergesDeclaration(declaration) {
			continue
		}
		state.bindings = append(state.bindings, jsBindingScope{
			Name:      declaration.name,
			startByte: declaration.scopeStart,
			endByte:   declaration.scopeEnd,
		})
	}
	return state, nil
}

func jsLineStarts(content string) []int {
	starts := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func (s *jsScanState) lineOf(position int) int {
	return sort.SearchInts(s.lineStarts, position+1)
}

// namespaceMergesDeclaration reports whether a function/class/enum declaration
// merges with a namespace of the same canonical name declared in the same
// lexical scope (TypeScript declaration merging). A dotted namespace also
// implicitly declares each prefix (`namespace A.B` declares `A`), so a
// declaration named A merges with it. Merged declarations must not register a
// shadow binding: the name still resolves to the namespace.
func (s *jsScanState) namespaceMergesDeclaration(declaration jsDeclarationBinding) bool {
	for _, scope := range s.namespaces {
		if (scope.Name == declaration.canonical || strings.HasPrefix(scope.Name, declaration.canonical+".")) &&
			scope.declScopeStart == declaration.scopeStart && scope.declScopeEnd == declaration.scopeEnd {
			return true
		}
	}
	return false
}

// qualifiedNamespaceCall canonicalizes one dotted call site against the file's
// namespaces using the call's own position: an enclosing binding for the
// receiver root suppresses the namespace reading, and a relative receiver
// (`B.f()` inside `namespace A`) canonicalizes through the namespace the call
// lexically sits in — statement-position calls in namespace bodies included.
// Shadowing respects lexical nesting: a binding only wins when its scope is
// nested inside (or equal to) the nearest enclosing scope from which the
// matched namespace is visible, so an inner `namespace B.A` beats a file-wide
// `function A` while a parameter named A still shadows a file-level namespace.
func (s *jsScanState) qualifiedNamespaceCall(call jsDottedCall) string {
	terminal := call.parts[len(call.parts)-1]
	if terminal == "" {
		return ""
	}
	caller := jsNamespaceAtByte(s.namespaces, call.startByte)
	namespace, prefix := jsCanonicalNamespaceWithPrefix(strings.Join(call.parts[:len(call.parts)-1], "."), caller, s.namespaceSet)
	if namespace == "" {
		return ""
	}
	if s.bindingShadowsNamespaceReceiver(call.parts[0], call.startByte, prefix) {
		return ""
	}
	return namespace + "." + terminal
}

// bindingShadowsNamespaceReceiver reports whether a lexical binding for the
// receiver root shadows the namespace reading at position. The namespace
// matched through prefix (the enclosing namespace path the canonicalization
// resolved against) is visible from the innermost enclosing scope named
// prefix — the whole file when prefix is empty — so only bindings whose scope
// starts at or inside that anchor scope are nearer than the namespace and
// shadow it; an outer binding loses to the deeper namespace declaration.
// A dotted declaration (`namespace A.B`) records only its full path, never a
// scope named exactly "A", yet it implicitly declares every ancestor segment
// over the same body range — so any enclosing scope whose path extends the
// prefix anchors the prefix's visibility too.
func (s *jsScanState) bindingShadowsNamespaceReceiver(root string, position int, prefix string) bool {
	anchorStart := -1 // the file-level lexical scope starts before byte 0
	if prefix != "" {
		// Prefix-namespace members are visible from the OUTERMOST enclosing
		// scope whose path extends the prefix; every binding declared at or
		// inside that scope is lexically nearer than the namespace member and
		// must shadow. Containing matches form a nested chain, so the earliest
		// start is the outermost scope — an inner anchor would wrongly exempt
		// bindings declared between the outer and inner scope starts.
		for _, scope := range s.namespaces {
			if scope.Name != prefix && !strings.HasPrefix(scope.Name, prefix+".") {
				continue
			}
			if position <= scope.startByte || position >= scope.endByte {
				continue
			}
			if anchorStart == -1 || scope.startByte < anchorStart {
				anchorStart = scope.startByte
			}
		}
	}
	for _, binding := range s.bindings {
		if binding.Name == root && binding.startByte < position && position < binding.endByte && binding.startByte >= anchorStart {
			return true
		}
	}
	return false
}

// namespaceCallsForSymbol returns the canonical namespace-qualified calls made
// within the symbol's source range (nested callables included, matching the
// symbol's block text).
func (s *jsScanState) namespaceCallsForSymbol(symbol SymbolRecord) map[string]struct{} {
	if s == nil || len(s.namespaces) == 0 {
		return nil
	}
	start, end := s.symbolByteRange(symbol)
	out := map[string]struct{}{}
	first := sort.Search(len(s.calls), func(i int) bool { return s.calls[i].startByte >= start })
	for _, call := range s.calls[first:] {
		if call.startByte >= end {
			break
		}
		if qualified := s.qualifiedNamespaceCall(call); qualified != "" {
			out[qualified] = struct{}{}
		}
	}
	return out
}

// topLevelNamespaceCalls returns the canonical namespace-qualified calls not
// covered by any symbol's source range. Coverage uses symbolByteRange — the
// exact byte partition the per-symbol scan (namespaceCallsForSymbol) uses —
// so a call belongs to a symbol iff its byte offset lies within that symbol's
// range and to the file otherwise: no call site is attributed to nobody (or
// to two owners) when a declaration and a call share one source line. Symbols
// without byte metadata keep the whole-line fallback inside symbolByteRange.
func (s *jsScanState) topLevelNamespaceCalls(symbols []SymbolRecord) map[string]struct{} {
	if s == nil || len(s.namespaces) == 0 {
		return nil
	}
	spans := s.symbolByteSpans(symbols)
	out := map[string]struct{}{}
	for _, call := range s.calls {
		if spans.contains(call.startByte) {
			continue
		}
		if qualified := s.qualifiedNamespaceCall(call); qualified != "" {
			out[qualified] = struct{}{}
		}
	}
	return out
}

type jsByteSpans []struct{ start, end int }

func (spans jsByteSpans) contains(position int) bool {
	for _, span := range spans {
		if position >= span.start && position < span.end {
			return true
		}
	}
	return false
}

func (s *jsScanState) symbolByteSpans(symbols []SymbolRecord) jsByteSpans {
	spans := make(jsByteSpans, 0, len(symbols))
	for _, symbol := range symbols {
		start, end := s.symbolByteRange(symbol)
		if end > start {
			spans = append(spans, struct{ start, end int }{start, end})
		}
	}
	return spans
}

// topLevelBlock returns content with every symbol's source range blanked out
// (spaces preserve byte offsets and line structure), so the generic top-level
// call scan sees exactly the source no per-symbol scan covers. Symbols with
// exact byte metadata mask only their own bytes — a call sharing a line with
// a declaration stays visible to the top-level scan — while symbols without
// byte metadata fall back to masking their whole line range, matching their
// line-based per-symbol blocks.
func (s *jsScanState) topLevelBlock(content string, symbols []SymbolRecord) string {
	masked := []byte(content)
	for _, symbol := range symbols {
		start, end := s.symbolByteRange(symbol)
		start = maxInt(0, start)
		end = minInt(end, len(masked))
		for i := start; i < end; i++ {
			if masked[i] != '\n' {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}

// symbolByteRange prefers the exact tree-sitter declaration range and falls
// back to the symbol's line range for symbols produced by non-AST extractors.
func (s *jsScanState) symbolByteRange(symbol SymbolRecord) (int, int) {
	if symbol.sourceEndByte > symbol.sourceStartByte && symbol.sourceEndByte <= s.contentLen {
		return symbol.sourceStartByte, symbol.sourceEndByte
	}
	startLine := maxInt(1, symbol.StartLine)
	endLine := maxInt(startLine, symbol.EndLine)
	if startLine > len(s.lineStarts) {
		return 0, 0
	}
	start := s.lineStarts[startLine-1]
	end := s.contentLen
	if endLine < len(s.lineStarts) {
		end = s.lineStarts[endLine]
	}
	return start, end
}

type jsScopeContext struct {
	lexStart, lexEnd   int
	funcStart, funcEnd int
	namespace          string
}

type jsDeclarationBinding struct {
	name       string
	canonical  string
	scopeStart int
	scopeEnd   int
}

type jsScopeWalker struct {
	src   []byte
	state *jsScanState
	// declarationBindings holds function/class/enum declaration names pending
	// the namespace-merge filter, which needs the complete namespace list (a
	// merging namespace may be declared later in the file).
	declarationBindings []jsDeclarationBinding
	// budget is the caller's context, polled every jsScanPollStride nodes so a
	// traversal of a huge tree cannot outlive the wall-clock budget. The parse
	// that produced the tree is bounded; the walk over it was not.
	budget  context.Context
	visited int
	stopped bool
}

// jsScanPollStride is how often the scope walk and the post-walk passes consult
// the caller's context. A context read is far more expensive than one node
// visit, so polling per node would tax every unbudgeted index; polling per
// stride keeps the residual at one stride of work.
const jsScanPollStride = 512

// budgetGone reports whether the caller's context has stopped, latching the
// answer so the rest of the traversal unwinds without re-reading the context.
func (w *jsScopeWalker) budgetGone() bool {
	if w.stopped {
		return true
	}
	if w.budget == nil {
		return false
	}
	w.visited++
	if w.visited%jsScanPollStride != 0 {
		return false
	}
	if w.budget.Err() != nil {
		w.stopped = true
	}
	return w.stopped
}

// walk descends the whole relation-phase parse tree, bounded by both the
// caller's wall-clock budget and maxParseWalkDepth.
func (w *jsScopeWalker) walk(node *sitter.Node, ctx jsScopeContext, depth int) {
	if w.budgetGone() {
		return
	}
	if !validNode(node) {
		return
	}
	if depth >= maxParseWalkDepth {
		w.state.depthTruncated = true
		return
	}
	switch node.Type() {
	case "statement_block", "class_body", "switch_body":
		ctx.lexStart, ctx.lexEnd = int(node.StartByte()), int(node.EndByte())
	case "for_statement":
		// A let/const in the head (`for (let i = 0; ...)`) scopes to the loop,
		// not to the surrounding block; the body block re-narrows below.
		ctx.lexStart, ctx.lexEnd = int(node.StartByte()), int(node.EndByte())
	case "internal_module", "module":
		w.enterNamespace(node, &ctx)
	case "function_declaration", "generator_function_declaration", "function_signature":
		w.addDeclarationBinding(node, ctx)
		w.enterFunction(node, &ctx)
	case "function", "function_expression", "generator_function":
		// A named function expression binds its own name inside its body
		// (`(function Utils() { Utils.helper(); })`): the self-name must
		// shadow a same-named namespace within the expression's own range.
		w.addExpressionSelfNameBinding(node)
		w.enterFunction(node, &ctx)
	case "arrow_function", "method_definition":
		w.enterFunction(node, &ctx)
	case "class":
		// Class expression: like a named function expression, the name is a
		// binding scoped to the expression itself.
		w.addExpressionSelfNameBinding(node)
	case "class_declaration", "abstract_class_declaration", "enum_declaration":
		w.addDeclarationBinding(node, ctx)
	case "lexical_declaration":
		w.addDeclaratorBindings(node, ctx.lexStart, ctx.lexEnd)
	case "variable_declaration":
		// var hoists to the enclosing function.
		w.addDeclaratorBindings(node, ctx.funcStart, ctx.funcEnd)
	case "for_in_statement":
		w.addForHeadBindings(node, ctx)
	case "catch_clause":
		if parameter := node.ChildByFieldName("parameter"); validNode(parameter) {
			w.addPatternBindings(parameter, int(node.StartByte()), int(node.EndByte()))
		}
	case "call_expression":
		w.addDottedCall(node.ChildByFieldName("function"), node.ChildByFieldName("arguments"))
	case "new_expression":
		w.addDottedCall(node.ChildByFieldName("constructor"), node.ChildByFieldName("arguments"))
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		w.walk(node.NamedChild(i), ctx, depth+1)
	}
}

func (w *jsScopeWalker) enterNamespace(node *sitter.Node, ctx *jsScopeContext) {
	name := jsNamespaceDeclarationName(node, w.src)
	if name == "" {
		return
	}
	body := node.ChildByFieldName("body")
	if !validNode(body) || body.Type() != "statement_block" {
		return
	}
	full := jsQualifiedNamespaceName(name, ctx.namespace)
	open, close := int(body.StartByte()), int(body.EndByte())-1
	w.state.namespaces = append(w.state.namespaces, jsNamespaceScope{
		Name:           full,
		StartLine:      w.state.lineOf(open),
		EndLine:        w.state.lineOf(close),
		startByte:      open,
		endByte:        close,
		declScopeStart: ctx.lexStart,
		declScopeEnd:   ctx.lexEnd,
	})
	ctx.namespace = full
}

// jsNamespaceDeclarationName returns the dotted identifier path of a
// namespace/module declaration; string-named ambient modules
// (`declare module "pkg"`) declare no lexical namespace and return "".
func jsNamespaceDeclarationName(node *sitter.Node, src []byte) string {
	name := node.ChildByFieldName("name")
	if !validNode(name) {
		return ""
	}
	switch name.Type() {
	case "identifier":
		return name.Content(src)
	case "nested_identifier":
		parts := strings.Split(name.Content(src), ".")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return strings.Join(parts, ".")
	default:
		return ""
	}
}

// jsQualifiedNamespaceName prefixes a nested declaration with its enclosing
// path. Dotted declaration names are parent-relative in TypeScript —
// `namespace A { namespace A.B {} }` declares A.A.B, not A.B — so every
// nested declaration is qualified against the parent path, including names
// that repeat or extend the parent's own name.
func jsQualifiedNamespaceName(name, parent string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func (w *jsScopeWalker) addDeclarationBinding(node *sitter.Node, ctx jsScopeContext) {
	name := node.ChildByFieldName("name")
	if !validNode(name) {
		return
	}
	text := name.Content(w.src)
	if text == "" {
		return
	}
	canonical := text
	if ctx.namespace != "" {
		canonical = ctx.namespace + "." + text
	}
	w.declarationBindings = append(w.declarationBindings, jsDeclarationBinding{
		name:       text,
		canonical:  canonical,
		scopeStart: ctx.lexStart,
		scopeEnd:   ctx.lexEnd,
	})
}

// addExpressionSelfNameBinding registers a named function/class expression's
// own name as a binding scoped to the expression node itself.
func (w *jsScopeWalker) addExpressionSelfNameBinding(node *sitter.Node) {
	name := node.ChildByFieldName("name")
	if !validNode(name) {
		return
	}
	switch name.Type() {
	case "identifier", "type_identifier":
	default:
		return
	}
	text := name.Content(w.src)
	if text == "" {
		return
	}
	w.state.bindings = append(w.state.bindings, jsBindingScope{
		Name:      text,
		startByte: int(node.StartByte()),
		endByte:   int(node.EndByte()),
	})
}

func (w *jsScopeWalker) enterFunction(node *sitter.Node, ctx *jsScopeContext) {
	ctx.funcStart, ctx.funcEnd = int(node.StartByte()), int(node.EndByte())
	parameters := node.ChildByFieldName("parameters")
	if !validNode(parameters) {
		parameters = node.ChildByFieldName("parameter") // single-identifier arrow
	}
	if validNode(parameters) {
		w.addPatternBindings(parameters, ctx.funcStart, ctx.funcEnd)
	}
}

func (w *jsScopeWalker) addDeclaratorBindings(node *sitter.Node, scopeStart, scopeEnd int) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		declarator := node.NamedChild(i)
		if declarator.Type() != "variable_declarator" {
			continue
		}
		w.addPatternBindings(declarator.ChildByFieldName("name"), scopeStart, scopeEnd)
	}
}

// addForHeadBindings registers `for (const {X} of items)` / `for (let k in m)`
// head bindings. The binding scope is the loop statement itself; a bare
// assignment head (`for (x of items)`) declares nothing.
func (w *jsScopeWalker) addForHeadBindings(node *sitter.Node, ctx jsScopeContext) {
	kind := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		switch node.Child(i).Type() {
		case "var", "let", "const":
			kind = node.Child(i).Type()
		}
	}
	if kind == "" {
		return
	}
	start, end := int(node.StartByte()), int(node.EndByte())
	if kind == "var" {
		start, end = ctx.funcStart, ctx.funcEnd
	}
	w.addPatternBindings(node.ChildByFieldName("left"), start, end)
}

func (w *jsScopeWalker) addPatternBindings(pattern *sitter.Node, scopeStart, scopeEnd int) {
	for _, name := range jsPatternBindingNames(pattern, w.src) {
		w.state.bindings = append(w.state.bindings, jsBindingScope{Name: name, startByte: scopeStart, endByte: scopeEnd})
	}
}

// jsPatternBindingNames collects every identifier a binding pattern binds,
// recursing through parameter wrappers, nested object/array destructuring,
// aliases (`key: alias`), defaults, and rest elements. Type annotations and
// default-value expressions are never entered, so their identifiers cannot
// leak in as phantom bindings.
func jsPatternBindingNames(pattern *sitter.Node, src []byte) []string {
	return jsPatternBindingNamesAt(pattern, src, 0)
}

// depth is capped at maxParseWalkDepth (parser.go). Object/array patterns nest
// as deeply as the source writes them, and walkEntitiesScoped reaches this
// through jsEntityParameterNames on a callable's parameter list — a separate
// recursion from its own, started while the walk is still at the declaration,
// so the walk's guard cannot protect it. Against the parent commit
// `function g({a:{a:{…}}}) {}` aborts the process with `fatal error: stack
// overflow`, frames in jsPatternBindingNames; pinned by
// TestJSPatternBindingsAreBoundedNotFatal.
func jsPatternBindingNamesAt(pattern *sitter.Node, src []byte, depth int) []string {
	if !validNode(pattern) || depth >= maxParseWalkDepth {
		return nil
	}
	switch pattern.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		name := pattern.Content(src)
		if name == "" || name == "this" {
			return nil
		}
		return []string{name}
	case "formal_parameters", "object_pattern", "array_pattern":
		var names []string
		for i := 0; i < int(pattern.NamedChildCount()); i++ {
			names = append(names, jsPatternBindingNamesAt(pattern.NamedChild(i), src, depth+1)...)
		}
		return names
	case "required_parameter", "optional_parameter":
		return jsPatternBindingNamesAt(pattern.ChildByFieldName("pattern"), src, depth+1)
	case "assignment_pattern", "object_assignment_pattern":
		return jsPatternBindingNamesAt(pattern.ChildByFieldName("left"), src, depth+1)
	case "pair_pattern":
		return jsPatternBindingNamesAt(pattern.ChildByFieldName("value"), src, depth+1)
	case "rest_pattern":
		var names []string
		for i := 0; i < int(pattern.NamedChildCount()); i++ {
			names = append(names, jsPatternBindingNamesAt(pattern.NamedChild(i), src, depth+1)...)
		}
		return names
	default:
		return nil
	}
}

func (w *jsScopeWalker) addDottedCall(function, arguments *sitter.Node) {
	if !validNode(function) || !validNode(arguments) || arguments.Type() != "arguments" {
		return
	}
	parts, truncated := jsMemberChainPartsAt(function, w.src, 0)
	if truncated {
		w.state.depthTruncated = true
	}
	if len(parts) < 2 {
		return
	}
	start := int(function.StartByte())
	w.state.calls = append(w.state.calls, jsDottedCall{parts: parts, startByte: start, line: w.state.lineOf(start)})
}

// jsMemberChainParts flattens a member expression into its identifier path
// (`A.B.f` -> [A B f]); any non-identifier link (calls, subscripts, `this`,
// parenthesized receivers) disqualifies the chain.
func jsMemberChainParts(node *sitter.Node, src []byte) []string {
	parts, _ := jsMemberChainPartsAt(node, src, 0)
	return parts
}

// depth is capped at maxParseWalkDepth (parser.go). A member chain is as long as
// the source writes it (`a.b.c…z()`), and walk hands this the whole chain from
// its head — before descending into it — so this recursion is independent of
// walk's and reaches the stack first: with the ceiling lowered to 16 MiB a
// 100,000-link chain aborts here while walk itself survives. Truncation reports
// upward rather than silently returning a short path, because a partial chain
// would resolve the call to the WRONG target; the reported chain is either
// complete or refused.
func jsMemberChainPartsAt(node *sitter.Node, src []byte, depth int) (parts []string, truncated bool) {
	if depth >= maxParseWalkDepth {
		return nil, true
	}
	switch node.Type() {
	case "identifier":
		return []string{node.Content(src)}, false
	case "member_expression":
		property := node.ChildByFieldName("property")
		if !validNode(property) || property.Type() != "property_identifier" {
			return nil, false
		}
		object := node.ChildByFieldName("object")
		if !validNode(object) {
			return nil, false
		}
		parts, truncated := jsMemberChainPartsAt(object, src, depth+1)
		if truncated {
			return nil, true
		}
		if parts == nil {
			return nil, false
		}
		return append(parts, property.Content(src)), false
	default:
		return nil, false
	}
}

// jsEntityParameterNames extracts the parameter identifiers of a JS/TS
// callable entity from its declaration node: the real formal_parameters list,
// immune to generic clauses containing function types. For declarator-shaped
// entities (`const f = (a) => ...`) the first function-like descendant carries
// the list.
func jsEntityParameterNames(node *sitter.Node, src []byte) []string {
	function := jsFunctionLikeNode(node)
	if function == nil {
		return nil
	}
	parameters := function.ChildByFieldName("parameters")
	if !validNode(parameters) {
		parameters = function.ChildByFieldName("parameter")
	}
	return jsPatternBindingNames(parameters, src)
}

// jsFunctionLikeNode finds the callable node an entity's declaration wraps.
//
// depth is capped at maxParseWalkDepth (parser.go). It is the sibling descent to
// jsPatternBindingNames on the very same jsEntityParameterNames call, over the
// same attacker-supplied subtree, and is bounded with it: no input was found
// that drives it deep (a declarator whose arrow function is buried under 1M
// parentheses stops being classified as a callable, so this is never called),
// but leaving one of the pair unbounded would only relocate the abort if that
// classification ever changes.
func jsFunctionLikeNode(node *sitter.Node) *sitter.Node {
	return jsFunctionLikeNodeAt(node, 0)
}

func jsFunctionLikeNodeAt(node *sitter.Node, depth int) *sitter.Node {
	if !validNode(node) || depth >= maxParseWalkDepth {
		return nil
	}
	switch node.Type() {
	case "function_declaration", "generator_function_declaration", "function_signature",
		"function", "function_expression", "generator_function", "arrow_function",
		"method_definition", "abstract_method_signature", "method_signature":
		return node
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if found := jsFunctionLikeNodeAt(node.NamedChild(i), depth+1); found != nil {
			return found
		}
	}
	return nil
}

// jsNamespaceScopes returns the lexical ranges of same-file namespaces from a
// standalone parse of content. The TypeScript parser intentionally inventories
// functions inside a namespace as ordinary, unqualified function symbols, so
// call resolution needs the source range to distinguish same-named
// declarations in `namespace A` and `namespace B`.
func jsNamespaceScopes(ctx context.Context, fileContent string) []jsNamespaceScope {
	return newJSScanStateForContent(ctx, fileContent).namespaces
}

// jsCanonicalNamespaceWithPrefix resolves a dotted receiver path against the
// file's namespaces, walking outward from the caller's namespace chain. It
// returns the canonical namespace and the enclosing-namespace prefix the
// match resolved through ("" for a top-level match), which shadow checks use
// as the visibility anchor.
func jsCanonicalNamespaceWithPrefix(path, callerNamespace string, namespaces map[string]bool) (string, string) {
	for prefix := callerNamespace; prefix != ""; {
		candidate := prefix + "." + path
		if namespaces[candidate] {
			return candidate, prefix
		}
		if dot := strings.LastIndex(prefix, "."); dot >= 0 {
			prefix = prefix[:dot]
		} else {
			prefix = ""
		}
	}
	if namespaces[path] {
		return path, ""
	}
	return "", ""
}

// jsNamespaceBySymbolID maps existing symbols to their lexical namespace
// without changing their public identity. Declaration byte offsets distinguish
// nested/reopened namespace members even when several declarations share one
// source line; line ranges remain a fallback for unusual declaration forms.
func jsNamespaceBySymbolID(content string, symbols []SymbolRecord, scopes []jsNamespaceScope) map[string]string {
	stripped := stripCodeLiteralsAndComments(content)
	lineStarts := []int{0}
	for i := 0; i < len(stripped); i++ {
		if stripped[i] == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	cursors := map[int]int{}
	out := map[string]string{}
	for _, symbol := range symbols {
		if symbol.sourceEndByte > symbol.sourceStartByte && symbol.sourceEndByte <= len(stripped) {
			if namespace := jsNamespaceAtByte(scopes, symbol.sourceStartByte); namespace != "" {
				out[symbol.ID] = namespace
			}
			continue
		}
		line := symbol.StartLine
		if line <= 0 || line > len(lineStarts) {
			continue
		}
		start := lineStarts[line-1]
		end := len(stripped)
		if line < len(lineStarts) {
			end = lineStarts[line] - 1
		}
		cursor := cursors[line]
		if cursor < start || cursor >= end {
			cursor = start
		}
		position := jsSymbolDeclarationPosition(stripped, symbol, cursor, end)
		if position < 0 && cursor != start {
			position = jsSymbolDeclarationPosition(stripped, symbol, start, end)
		}
		if position >= 0 {
			cursors[line] = position + len(symbol.Name)
			if namespace := jsNamespaceAtByte(scopes, position); namespace != "" {
				out[symbol.ID] = namespace
			}
			continue
		}
		if namespace := jsNamespaceAtLine(scopes, line); namespace != "" {
			out[symbol.ID] = namespace
		}
	}
	return out
}

func jsSymbolDeclarationPosition(content string, symbol SymbolRecord, start, end int) int {
	if symbol.Name == "" || start < 0 || end <= start || end > len(content) {
		return -1
	}
	name := regexp.QuoteMeta(symbol.Name)
	patterns := []string{}
	switch {
	case symbol.Kind == "function":
		patterns = append(patterns,
			`\b(?:async\s+)?function\s*\*?\s*(`+name+`)\b`,
			`\b(?:const|let|var)\s+(`+name+`)\b`,
		)
	case typeLikeKind(symbol.Kind):
		patterns = append(patterns, `\b(?:abstract\s+)?(?:class|interface|enum|type)\s+(`+name+`)\b`)
	case symbol.Kind == "method":
		patterns = append(patterns, `\b(`+name+`)\s*(?:<[^>\n;{}()]*>)?\s*\(`)
	default:
		patterns = append(patterns, `\b(`+name+`)\b`)
	}
	segment := content[start:end]
	for _, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatchIndex(segment)
		if len(match) >= 4 {
			return start + match[2]
		}
	}
	return -1
}

func jsNamespaceAtByte(scopes []jsNamespaceScope, position int) string {
	name := ""
	bestSpan := -1
	for _, scope := range scopes {
		if position <= scope.startByte || position >= scope.endByte {
			continue
		}
		span := scope.endByte - scope.startByte
		if bestSpan < 0 || span < bestSpan {
			name = scope.Name
			bestSpan = span
		}
	}
	return name
}

func jsNamespaceAtLine(scopes []jsNamespaceScope, line int) string {
	name := ""
	bestSpan := -1
	for _, scope := range scopes {
		if line < scope.StartLine || line > scope.EndLine {
			continue
		}
		span := scope.EndLine - scope.StartLine
		if bestSpan < 0 || span < bestSpan {
			name = scope.Name
			bestSpan = span
		}
	}
	return name
}
