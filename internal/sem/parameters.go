package sem

import (
	"strings"

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
	list := parameterListNode(node)
	if list == nil {
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
	for index := 0; index < int(list.NamedChildCount()); index++ {
		names = append(names, parameterBindingNames(list.NamedChild(index), src)...)
	}
	return names, true
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

func parameterListNode(node *sitter.Node) *sitter.Node {
	if !validNode(node) {
		return nil
	}
	if list := node.ChildByFieldName("parameters"); validNode(list) {
		return list
	}
	for index := 0; index < int(node.NamedChildCount()); index++ {
		if child := node.NamedChild(index); validNode(child) && parameterListNodeTypes[child.Type()] {
			return child
		}
	}
	return nil
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
func withSplatSpelling(node *sitter.Node, src []byte, names []string) []string {
	raw := strings.TrimSpace(node.Content(src))
	// Take the sigil from the front of the entry rather than the entry text
	// itself: an annotated splat (`**kwargs: Any`) carries its type in the same
	// node, so the verbatim text is not what the call site writes — but the
	// leading `**` is.
	prefix := raw[:len(raw)-len(strings.TrimLeft(raw, "*"))]
	if prefix == "" && strings.HasPrefix(raw, "...") {
		prefix = "..."
	}
	if prefix == "" {
		return names
	}
	for _, name := range names {
		if spelled := prefix + name; spelled != name {
			names = append(names, spelled)
		}
	}
	return names
}

// identifierDescendants collects the identifier names under a node, skipping
// type positions. It descends because a C declarator wraps its identifier in
// pointer/function/array layers (`*b`, `(*cb)(int)`) and a destructuring
// pattern nests its bindings.
func identifierDescendants(node *sitter.Node, src []byte) []string {
	if !validNode(node) || isTypeNode(node) {
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
		names = append(names, identifierDescendants(node.NamedChild(index), src)...)
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
	for _, char := range name {
		if !(char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9') {
			return ""
		}
	}
	return name
}
