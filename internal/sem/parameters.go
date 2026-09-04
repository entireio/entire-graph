package sem

import (
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
)

// astParameterNames extracts a callable's formal parameter names from the parse
// tree, for every language except JS/TS (which has its own richer extractor in
// jsEntityParameterNames). It reports false when the grammar exposes no
// parameter list it recognizes, leaving the caller on the signature-regex
// fallback in parameterNames.
//
// The regex fallback splits a signature on every comma with no bracket
// awareness, so nested punctuation leaks into the result: a Zig
// `options: struct { scripts: u8, git_commit: u8 }` parameter yields the struct
// FIELD `git_commit` and a bare `}` as if they were parameters, and those
// invented names then fabricate DATA_FLOWS edges claiming a caller parameter was
// forwarded when the identifier was a module-level constant. The parse tree
// draws the boundaries the regex is guessing at — and, for a Go method, also
// excludes the receiver, which no anchor-on-the-first-paren heuristic can do.
func astParameterNames(node *sitter.Node, src []byte) ([]string, bool) {
	lists := parameterListNodes(node)
	if len(lists) == 0 {
		// Swift hangs `parameter` nodes directly off the declaration with no
		// enclosing list node.
		if direct := directParameterChildren(node, src); len(direct) > 0 {
			return direct, true
		}
		return nil, false
	}
	var names []string
	// A Go method's receiver is a genuine input the body forwards
	// (`func (a Author) DisplayName() { return actorDisplayName(a.Login) }`), and
	// the grammar keeps it in its own field outside the parameter list. Dropping
	// it would lose real flows — 61 in cli-cli alone. Receivers conventionally
	// named `self`/`this` are still filtered, in cleanParameterName.
	if receiver := node.ChildByFieldName("receiver"); validNode(receiver) {
		for index := 0; index < int(receiver.NamedChildCount()); index++ {
			names = append(names, parameterBindingNames(receiver.NamedChild(index), src)...)
		}
	}
	for _, entry := range parameterListEntries(lists) {
		names = append(names, parameterBindingNames(entry, src)...)
	}
	return names, true
}

// astSignatureTypeTexts returns the parameter-type and return-type source text
// of a callable, read from the parse tree. It reports false when the grammar
// exposes neither a parameter list nor the bare `parameter` children Swift uses
// in place of one, leaving the caller on splitSignatureTypes.
//
// The signature-string parser it replaces anchors on the FIRST `(` and, for
// languages it has no case for, guesses the return type as the second-to-last
// whitespace-delimited word before that paren. Both assumptions fail loudly:
//
//	Swift `func f(a: Int) -> Result`   -> return type "func"   (the keyword)
//	Zig   `fn f(a: u8) Result`         -> return type "fn"
//	Scala `def f(a: Int): Result`      -> return type "def"
//	Go    `func (r *R) B(i In) Out`    -> params from the RECEIVER, and the
//	                                      method name and param type read as
//	                                      return types
//
// Zig emitted zero RETURNS_TYPE edges across a whole repository as a result.
func astSignatureTypeTexts(node *sitter.Node, src []byte) (string, string, bool) {
	lists := parameterListNodes(node)
	entries := parameterListEntries(lists)
	if len(lists) == 0 {
		for index := 0; index < int(node.NamedChildCount()); index++ {
			if child := node.NamedChild(index); validNode(child) && child.Type() == "parameter" {
				entries = append(entries, child)
			}
		}
		if len(entries) == 0 {
			return "", "", false
		}
	}
	var paramTypes []string
	for _, entry := range entries {
		if text := parameterTypeText(entry, src); text != "" {
			paramTypes = append(paramTypes, text)
		}
	}
	returnText := ""
	if node := callableReturnTypeNode(node, lists); validNode(node) {
		returnText = strings.TrimSpace(node.Content(src))
		// TypeScript keeps the annotation colon inside the node; Rust and Swift
		// keep the arrow outside it, but trim defensively for both.
		returnText = strings.TrimSpace(strings.TrimPrefix(returnText, ":"))
		returnText = strings.TrimSpace(strings.TrimPrefix(returnText, "->"))
	}
	return strings.Join(paramTypes, ", "), returnText, true
}

func parameterTypeText(entry *sitter.Node, src []byte) string {
	if !validNode(entry) || !isParameterNode(entry) {
		return ""
	}
	if typeNode := entry.ChildByFieldName("type"); validNode(typeNode) {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(typeNode.Content(src)), ":"))
	}
	// Swift reuses the `name` field for the type, so fall back to the first
	// type-shaped child rather than trusting field names here.
	for index := 0; index < int(entry.NamedChildCount()); index++ {
		if child := entry.NamedChild(index); isTypeNode(child) {
			return strings.TrimSpace(child.Content(src))
		}
	}
	return ""
}

// callableReturnTypeNode finds the declared return type. Grammars name the
// field differently (`result`, `return_type`, `returns`, `type`); Swift, Kotlin
// and Dart give it no field at all and leave a bare type node among the
// declaration's children, which is why the field lookup falls back to scanning
// for a type-shaped child outside the parameter list.
//
// That scan cannot simply take the first unfielded type node. A Kotlin
// EXTENSION function puts its receiver type there too, ahead of the name and the
// parameter list — `fun Receiver.load(): Result` gives function_declaration two
// unfielded `user_type` children, `Receiver` then `Result` — so first-wins
// reported the receiver and lost the real return type on every extension
// function in a Kotlin repository.
//
// Position separates them, but only in one direction per language: Kotlin, Scala
// and Zig write the result AFTER the parameter clauses while Dart and Java write
// it before. So prefer a type that starts after the last clause and fall back to
// one before, which keeps `void f(...)` reading `void` and makes
// `fun Receiver.load(): Result` read `Result`. With no clause to order against
// (Swift's list-less form) the first match still wins, exactly as before.
func callableReturnTypeNode(node *sitter.Node, lists []*sitter.Node) *sitter.Node {
	for _, field := range []string{"result", "return_type", "returns", "type"} {
		if found := node.ChildByFieldName(field); validNode(found) {
			return found
		}
	}
	var beforeLists *sitter.Node
	for index := 0; index < int(node.NamedChildCount()); index++ {
		child := node.NamedChild(index)
		if !isTypeNode(child) {
			continue
		}
		if nodeWithinParameterLists(child, lists) {
			continue // a parameter's own type, not the result
		}
		if nodeAfterParameterLists(child, lists) {
			return child
		}
		if beforeLists == nil {
			beforeLists = child
		}
	}
	return beforeLists
}

func nodeWithinParameterLists(node *sitter.Node, lists []*sitter.Node) bool {
	for _, list := range lists {
		if node.StartByte() >= list.StartByte() && node.EndByte() <= list.EndByte() {
			return true
		}
	}
	return false
}

// nodeAfterParameterLists reports whether node sits past every parameter clause.
// With no clauses it is vacuously true, so a grammar that exposes none keeps the
// first-match behavior this scan has always had.
func nodeAfterParameterLists(node *sitter.Node, lists []*sitter.Node) bool {
	for _, list := range lists {
		if node.StartByte() < list.EndByte() {
			return false
		}
	}
	return true
}

// parameterListNodeTypes are the node types grammars use for a formal parameter
// list. Most expose it under the `parameters` field; the ones that do not still
// name the node recognizably.
var parameterListNodeTypes = map[string]bool{
	"parameters":                true, // Python, Rust, Scala, Zig
	"parameter_list":            true, // Go, C, C++, C#, Lua
	"formal_parameters":         true, // Java, PHP, TypeScript
	"formal_parameter_list":     true, // Dart
	"method_parameters":         true, // Ruby
	"function_value_parameters": true, // Kotlin
}

// parameterGroupNodeTypes are wrapper nodes that appear as a list ENTRY but hold
// several parameter entries instead of binding a name themselves. Dart puts its
// positional-optional `[a = 1]` and named `{a: 1}` groups in one such node, with
// each default VALUE as a sibling of the formal parameter it belongs to. Treated
// as a single entry the group has no `name`/`pattern`/`declarator` field, so the
// last-identifier fallback took the final default expression as the parameter:
// `void f(int a, [int b = other])` bound {a, other} — inventing a flow from a
// module-level constant and losing `b` — and `void g([User user = defaultUser])`
// bound {defaultUser} and reported no parameter type at all, dropping PARAM_TYPE
// and the overload resolution that reads it.
var parameterGroupNodeTypes = map[string]bool{
	"optional_formal_parameters": true, // Dart `[a = 1]` and `{a: 1}`
	"named_formal_parameters":    true, // Dart grammars that split the two forms
}

// parameterListNodes returns EVERY formal parameter clause of a callable. Most
// grammars have exactly one, but Scala curries: `def f(a: Int)(b: Int, c: Int)`
// hangs two `parameters` children off the same definition, both under the
// `parameters` field. ChildByFieldName returns only the first, so the extractor
// reported a partial list — and reported it as AST-authoritative, which
// suppressed the signature fallback and dropped every flow and type from the
// later clauses.
func parameterListNodes(node *sitter.Node) []*sitter.Node {
	if !validNode(node) {
		return nil
	}
	// The `parameters` field, when the grammar has one, is authoritative: Go
	// spells its RECEIVER and its multi-value RESULT as `parameter_list` nodes
	// too, and only the field tells the three apart. Every clause carrying the
	// field is taken, which is what makes a curried Scala definition whole.
	var fielded []*sitter.Node
	var typed []*sitter.Node
	for index := 0; index < int(node.ChildCount()); index++ {
		child := node.Child(index)
		if !validNode(child) || !child.IsNamed() {
			continue
		}
		switch field := node.FieldNameForChild(index); field {
		case "parameters":
			fielded = append(fielded, child)
		case "":
			if parameterListNodeTypes[child.Type()] {
				typed = append(typed, child)
			}
		}
	}
	if len(fielded) > 0 {
		return fielded
	}
	return typed
}

// parameterListEntries flattens the clauses into the entries that actually bind
// a parameter, unwrapping the group nodes described on parameterGroupNodeTypes.
// A group's non-parameter children are its default VALUES and are dropped here:
// they are expressions in the enclosing scope, not bindings.
func parameterListEntries(lists []*sitter.Node) []*sitter.Node {
	entries := []*sitter.Node{}
	for _, list := range lists {
		for index := 0; index < int(list.NamedChildCount()); index++ {
			child := list.NamedChild(index)
			if !validNode(child) {
				continue
			}
			if !parameterGroupNodeTypes[child.Type()] {
				entries = append(entries, child)
				continue
			}
			for inner := 0; inner < int(child.NamedChildCount()); inner++ {
				if nested := child.NamedChild(inner); isParameterNode(nested) {
					entries = append(entries, nested)
				}
			}
		}
	}
	return entries
}

func directParameterChildren(node *sitter.Node, src []byte) []string {
	if !validNode(node) {
		return nil
	}
	var names []string
	for index := 0; index < int(node.NamedChildCount()); index++ {
		child := node.NamedChild(index)
		if validNode(child) && child.Type() == "parameter" {
			names = append(names, parameterBindingNames(child, src)...)
		}
	}
	return names
}

// parameterBindingNames returns the identifiers a single parameter entry binds.
// One entry can bind several: Go's `name, urlStr string` is one
// parameter_declaration with two `name` fields, and a destructured JS-style
// pattern binds each of its properties.
func parameterBindingNames(node *sitter.Node, src []byte) []string {
	if !validNode(node) {
		return nil
	}
	if isIdentifierNode(node) {
		if name := cleanParameterName(node.Content(src)); name != "" {
			return []string{name}
		}
		return nil
	}
	if !isParameterNode(node) {
		// A C# `params int[] c` splits the array type out as its own list entry,
		// and a Kotlin default value lands there too. Neither binds a name;
		// descending into them would mistake a type for a parameter.
		return nil
	}
	// Prefer explicit fields: `name` (most grammars), `pattern` (Rust, TS),
	// `declarator` (C/C++). A grammar can repeat `name` on one entry, so collect
	// every child carrying it rather than the first.
	var names []string
	for index := 0; index < int(node.ChildCount()); index++ {
		switch node.FieldNameForChild(index) {
		case "name":
			// Swift labels a parameter's type with `name` as well as its
			// binding (`_ a: Int` carries name="a" AND name="Int"), so a type
			// in that position is not a second binding.
			child := node.Child(index)
			if isTypeNode(child) {
				continue
			}
			if name := cleanParameterName(child.Content(src)); name != "" {
				names = append(names, name)
			}
		case "pattern", "declarator":
			names = append(names, identifierDescendants(node.Child(index), src)...)
		}
	}
	if len(names) > 0 {
		return withSplatSpelling(node, src, names)
	}
	// No field to go on (Kotlin `a: Int`, Swift `_ a: Int`, Dart `int a`, Java
	// `String... c`). The binding is the LAST identifier that is not a type:
	// Swift's external label precedes the internal name, and Dart/Java put the
	// type first.
	if candidates := identifierDescendants(node, src); len(candidates) > 0 {
		return withSplatSpelling(node, src, candidates[len(candidates)-1:])
	}
	return nil
}

// withSplatSpelling keeps the sigil-carrying spelling of a variadic parameter
// alongside its bare name. A Python `**kwargs` parameter is bound as `kwargs`,
// but the forwarding call site writes `f(**kwargs)` verbatim — and the flow
// detectors match argument text against this set literally. Returning only the
// bare name would silently drop every splat-forwarding flow (measured: 108 real
// flows in pandas alone), so carry both spellings.
//
// `&` is in the sigil set for the same reason `*` is: Ruby binds a block
// parameter as `block` but forwards it as `sink(&block)`, and the argument text
// is compared for EQUALITY, so the bare name never matched and `def f(&block)`
// contributed no DATA_FLOWS edge while the neighbouring `def g(*args)` did. The
// extra spelling cannot invent a flow anywhere else: an argument only matches
// when its whole text is exactly `&name`, which no `x & name` expression is.
func withSplatSpelling(node *sitter.Node, src []byte, names []string) []string {
	raw := strings.TrimSpace(node.Content(src))
	// Take the sigil from the front of the entry rather than the entry text
	// itself: an annotated splat (`**kwargs: Any`) carries its type in the same
	// node, so the verbatim text is not what the call site writes — but the
	// leading `**` is.
	prefix := raw[:len(raw)-len(strings.TrimLeft(raw, "*&"))]
	if prefix == "" && strings.HasPrefix(raw, "...") {
		prefix = "..."
	}
	if prefix != "" {
		for _, name := range names {
			if spelled := prefix + name; spelled != name {
				names = append(names, spelled)
			}
		}
		return names
	}
	// Go puts the ellipsis between the name and the type (`args ...string`) and
	// forwards it after the name (`sink(args...)`), so neither the bare name nor
	// a leading-sigil spelling matches the call site and the flow was missed
	// entirely. Carry the trailing spelling — but only where the ellipsis
	// actually follows the name. Java writes `String... c`, attaching it to the
	// TYPE, and calls that parameter as plain `c`; emitting `c...` there would
	// invent a spelling no Java call site uses.
	ellipsis := strings.Index(raw, "...")
	if ellipsis < 0 {
		return names
	}
	for _, name := range names {
		if at := strings.Index(raw, name); at >= 0 && at < ellipsis {
			names = append(names, name+"...")
		}
	}
	return names
}

// identifierDescendants collects the identifier names under a node, skipping
// type positions. It descends because a C declarator wraps its identifier in
// pointer/function/array layers (`*b`, `(*cb)(int)`) and a destructuring
// pattern nests its bindings.
//
// depth is capped at maxParseWalkDepth (parser.go). walkEntitiesScoped reaches
// this walker through astParameterNames on every callable it extracts, BEFORE
// it descends into that callable, so the entity walk's own budget is unspent
// and cannot bound this descent: a parameter pattern nested deeply enough
// exhausted the goroutine stack — a fatal, unrecoverable process abort —
// however shallow the file was elsewhere. Bounding the recursive walker itself,
// rather than either call site, covers both entry points (astParameterNames and
// directParameterChildren, Swift's list-less form).
//
// The budget starts fresh here rather than continuing the entity walk's, for
// the same reason firstNameDescendant's does: this descent begins at one
// parameter entry and only ever looks downward, so it cannot compound with the
// caller's remaining budget the way initializerTypeBodies (whose result the
// entity walk then descends INTO) can.
//
// Truncating drops the bindings nested past the limit, which costs the file
// some DATA_FLOWS edges. It is not silent: a pattern that deep sits inside a
// file the entity walk truncates on the same nesting, so ParseWithStatus
// already reports E_PARSE_DEPTH_EXCEEDED for it.
func identifierDescendants(node *sitter.Node, src []byte) []string {
	return identifierDescendantsAt(node, src, 0)
}

func identifierDescendantsAt(node *sitter.Node, src []byte, depth int) []string {
	if !validNode(node) || isTypeNode(node) || depth >= maxParseWalkDepth {
		return nil
	}
	if isIdentifierNode(node) {
		if name := cleanParameterName(node.Content(src)); name != "" {
			return []string{name}
		}
		return nil
	}
	var names []string
	for index := 0; index < int(node.NamedChildCount()); index++ {
		names = append(names, identifierDescendantsAt(node.NamedChild(index), src, depth+1)...)
	}
	return names
}

func isIdentifierNode(node *sitter.Node) bool {
	if !validNode(node) {
		return false
	}
	nodeType := node.Type()
	if isTypeNode(node) {
		return false
	}
	return strings.HasSuffix(nodeType, "identifier") || nodeType == "variable_name" || nodeType == "name"
}

// isTypeNode marks the identifier-shaped node types that name a TYPE rather than
// a binding, so a `Map<String, Integer> b` parameter cannot contribute `Map`.
func isTypeNode(node *sitter.Node) bool {
	if !validNode(node) {
		return false
	}
	nodeType := node.Type()
	// `_type` covers the long tail across grammars — user_type, dictionary_type,
	// array_type, optional_type, function_type, generic_type — without naming
	// each one.
	if strings.HasSuffix(nodeType, "_type") || nodeType == "type" {
		return true
	}
	switch nodeType {
	case "type_identifier", "scoped_type_identifier", "qualified_type":
		return true
	}
	return false
}

// isParameterNode reports whether a list entry is a parameter binding at all.
// Grammars name these consistently enough to match on the shape of the type
// name: `formal_parameter`, `parameter_declaration`, `optional_parameter`,
// `list_splat_pattern`, `required_parameter`, and so on.
func isParameterNode(node *sitter.Node) bool {
	if !validNode(node) {
		return false
	}
	nodeType := node.Type()
	return strings.Contains(nodeType, "parameter") || strings.Contains(nodeType, "pattern")
}

// cleanParameterName strips the sigils grammars keep in the binding text (PHP's
// `$a`, a splat's `*args`) so names match the identifiers a body actually uses.
func cleanParameterName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.TrimPrefix(name, "...")
	name = strings.TrimLeft(name, "*&$@")
	name = strings.TrimSpace(name)
	if name == "" || name == "self" || name == "this" || name == "_" {
		return ""
	}
	// Reject punctuation the signature regex used to invent (a bare `}`, a
	// stray `]`) without rejecting the many supported languages that allow
	// Unicode identifiers — Go, Kotlin, Swift and Python among them. An ASCII
	// -only rule dropped every parameter of `func f(café string, naïve int)`,
	// and because the parser still reported the list as AST-confirmed, the
	// signature fallback did not run and the function contributed no flows.
	for index, char := range name {
		if char == '_' || unicode.IsLetter(char) || unicode.IsDigit(char) {
			continue
		}
		// A combining mark is part of the letter it follows, not punctuation:
		// `café` written in NFD is `caf` + `e` + U+0301, which both Python and
		// Rust accept as one identifier (XID_Continue includes the mark). macOS
		// filesystems hand back decomposed source routinely, so rejecting it
		// dropped the parameter — and, because the list stayed AST-authoritative,
		// took every flow through it with no fallback and no warning. A LEADING
		// mark cannot start an identifier in any supported language, so it is
		// still refused.
		if index > 0 && unicode.IsMark(char) {
			continue
		}
		return ""
	}
	return name
}
