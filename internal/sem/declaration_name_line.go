package sem

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// Name lines
// ==========
//
// A symbol's StartLine is where its declaration node starts, which for Java, C#, Rust, Python,
// Kotlin and TypeScript is often an annotation, attribute or decorator. Renderers that must show
// "the line that names the symbol" used to find it by scanning text (DeclarationLineIndex), and
// every review round found a new shape that scan misread: an annotation argument spelled like the
// name, a multi-line raw string, a block comment, a property whose type is spelled like its name.
//
// The parser already knows the answer. nameLine records it from the parse tree, choosing the
// BINDING token the way the name itself was chosen:
//
//  1. the grammar's `name` (or `property`) field when it holds the name;
//  2. for a C-family declaration, the identifier its `declarator` chain binds (the rule
//     cFamilyDeclaratorName uses), trying every declarator of a multi-name declaration. When the
//     node has declarators and none binds the name, the answer is unknown (0): a return-type tag
//     spelled like the function (`struct item` above `item(void)`) is not the binding;
//  3. otherwise the first identifier leaf spelled exactly like the name outside comments, string
//     literals, annotations, decorators and attributes, and before its body.
//
// Tree-sitter tokenises literals, comments and identifiers exactly, so none of the text
// heuristic's lexical failure classes can occur here.
//
// Extractors that have no parse node (regex fallbacks for SQL, GraphQL, JS export lists, ...) leave
// it 0, and consumers fall back to the text heuristic for exactly those symbols.

// nameLineVisitBudget bounds the leaf search for one declaration. The name precedes the body, and
// the search never enters the body, so a real header is far below this; the budget only stops a
// pathological header (a generated parameter list thousands of nodes long) from costing more.
const nameLineVisitBudget = 4096

// declarationNameLine returns the 1-based line of the token naming a declaration `name` whose parse
// node is node, or 0 when the tree does not show one.
func declarationNameLine(node *sitter.Node, src []byte, name string) int {
	if !validNode(node) {
		return 0
	}
	short := nameLineShortName(name)
	if short == "" {
		return 0
	}
	body := signatureBodyBoundary(node)
	limit := int(node.EndByte())
	if validNode(body) && int(body.StartByte()) > int(node.StartByte()) {
		limit = int(body.StartByte())
	}
	for _, field := range []string{"name", "property"} {
		child := node.ChildByFieldName(field)
		if !validNode(child) || int(child.StartByte()) >= limit {
			continue
		}
		budget := nameLineVisitBudget
		if leaf := nameLineLeaf(child, src, short, limit, 0, &budget); validNode(leaf) {
			return int(leaf.StartPoint().Row) + 1
		}
		content := strings.TrimSpace(child.Content(src))
		if content == name || content == short {
			return int(child.StartPoint().Row) + 1
		}
	}
	if line, had := nameLineFromDeclarators(node, src, short); had {
		return line
	}
	budget := nameLineVisitBudget
	if leaf := nameLineLeaf(node, src, short, limit, 0, &budget); validNode(leaf) {
		return int(leaf.StartPoint().Row) + 1
	}
	return 0
}

// nameLineFromDeclarators returns the line of the identifier that one of node's `declarator`
// children binds, when that identifier is spelled short. had reports whether node has any
// declarator child at all: a C-family declaration whose declarators do not bind short is
// ambiguous, and the caller must not fall back to a spelling search.
func nameLineFromDeclarators(node *sitter.Node, src []byte, short string) (line int, had bool) {
	for i := 0; i < int(node.ChildCount()); i++ {
		if node.FieldNameForChild(i) != "declarator" {
			continue
		}
		had = true
		if binding := declaratorBindingNode(node.Child(i)); validNode(binding) && declaratorBinds(binding, src, short) {
			return int(binding.StartPoint().Row) + 1, true
		}
	}
	return 0, had
}

// declaratorBinds reports whether the binding node spells short. An operator binds only when its
// parsed text is the name exactly: C++ sources are parsed with operators masked to same-length
// stand-ins (cFamilyNameEndsAtTokenBoundary), so a member DECLARATION's canonical operator name is
// not in the parsed text and its name line is unknown (0) rather than guessed.
func declaratorBinds(binding *sitter.Node, src []byte, short string) bool {
	text := strings.TrimSpace(binding.Content(src))
	if text == short {
		return true
	}
	if binding.Type() == "destructor_name" {
		// `~K` is named `~K` in a class body and `K` out of line (cFamilyDeclaratorName).
		return strings.TrimPrefix(strings.Join(strings.Fields(text), ""), "~") == strings.TrimPrefix(short, "~")
	}
	return false
}

// declaratorBindingNode follows a C-family declarator chain to the identifier it binds, as
// cFamilyDeclaratorName does: through `declarator` fields (or a declarator attached as a plain
// child, as reference declarators are), qualified and template names, and a `name` field.
func declaratorBindingNode(cur *sitter.Node) *sitter.Node {
	for depth := 0; validNode(cur) && depth < maxParseWalkDepth; depth++ {
		switch cur.Type() {
		case "identifier", "field_identifier", "type_identifier", "property_identifier", "simple_identifier",
			"operator_name", "operator_cast", "destructor_name":
			return cur
		}
		if name := cur.ChildByFieldName("name"); validNode(name) {
			cur = name
			continue
		}
		next := cur.ChildByFieldName("declarator")
		if !validNode(next) {
			next = firstDeclaratorChild(cur)
		}
		cur = next
	}
	return nil
}

// nameLineShortName is the unqualified name: `Outer.Inner.run` and `ns::run` name `run`.
func nameLineShortName(name string) string {
	name = strings.TrimSpace(name)
	if index := strings.LastIndex(name, "::"); index >= 0 {
		name = name[index+2:]
	}
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	return name
}

// nameLineLeaf returns the first node, in source order, that is a name token spelled exactly short,
// starts before limit, and is not inside a comment, literal, annotation, decorator or attribute.
func nameLineLeaf(node *sitter.Node, src []byte, short string, limit, depth int, budget *int) *sitter.Node {
	if !validNode(node) || depth >= maxParseWalkDepth || *budget <= 0 || int(node.StartByte()) >= limit {
		return nil
	}
	*budget--
	if nameLineSkipped(node.Type()) {
		return nil
	}
	if node.NamedChildCount() == 0 || isNameNode(node.Type()) {
		if strings.TrimSpace(node.Content(src)) == short {
			return node
		}
		if node.NamedChildCount() == 0 {
			return nil
		}
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if !validNode(child) || !child.IsNamed() {
			continue
		}
		if leaf := nameLineLeaf(child, src, short, limit, depth+1, budget); validNode(leaf) {
			return leaf
		}
	}
	return nil
}

// nameLineSkipped reports node types whose text is never the declaration's own name: comments,
// literals, and the annotations/decorators/attributes that precede a declaration and may spell the
// name as an argument (`@Policy(run = true)` above `void run()`).
func nameLineSkipped(nodeType string) bool {
	switch nodeType {
	case "modifiers", "type_parameters", "type_parameter_list", "decorator", "annotation",
		"marker_annotation", "attribute_list", "attribute_item", "inner_attribute_item",
		"attribute_group", "attribute_section":
		return true
	}
	return strings.Contains(nodeType, "comment") || strings.Contains(nodeType, "string") ||
		strings.Contains(nodeType, "heredoc") || strings.Contains(nodeType, "annotation") ||
		strings.Contains(nodeType, "decorator")
}

// entityNameLineWithin is entity.nameLine when it lies inside the entity's own line span, else 0: a
// name line outside the span is a parse-metadata mismatch, and a consumer must fall back rather
// than anchor on a line that belongs to another symbol.
func entityNameLineWithin(entity Entity) int {
	if entity.nameLine >= entity.StartLine && entity.nameLine <= entity.EndLine && entity.StartLine > 0 {
		return entity.nameLine
	}
	return 0
}

// NameLine is the parser's line for the token that names the symbol, 0 when unknown.
func (symbol SymbolRecord) NameLine() int {
	return symbol.nameLine
}
