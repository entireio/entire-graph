package sem

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// juliaLocalBindingNames returns every name that Julia's syntax binds inside
// one callable. The same-container CALLS fallback is intentionally fail-closed:
// a parse failure returns nil, which disables that fallback for the callable.
func juliaLocalBindingNames(ctx context.Context, source string) map[string]struct{} {
	sourceBytes := []byte(source)
	headerSource := maskJuliaLiteralsAndComments(source)
	spec, ok := languageForPath("scope.jl")
	if !ok {
		return nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(spec.grammar)
	ctx, cancel := context.WithTimeout(ctx, treeSitterParseTimeout)
	defer cancel()
	tree, err := parser.ParseCtx(ctx, nil, sourceBytes)
	if tree != nil {
		defer tree.Close()
	}
	if err != nil || ctx.Err() != nil || tree == nil {
		return nil
	}
	root := tree.RootNode()
	if !validNode(root) || root.HasError() {
		return nil
	}

	type walkItem struct {
		node    *sitter.Node
		binding bool
	}
	bindings := map[string]struct{}{}
	stack := []walkItem{{node: root}}
	pushChildren := func(node *sitter.Node, start int, binding func(int, *sitter.Node) bool) {
		for i := int(node.NamedChildCount()) - 1; i >= start; i-- {
			child := node.NamedChild(i)
			stack = append(stack, walkItem{node: child, binding: binding != nil && binding(i, child)})
		}
	}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := item.node
		if !validNode(node) {
			continue
		}
		if item.binding {
			switch node.Type() {
			case "identifier":
				if name := strings.TrimSpace(node.Content(sourceBytes)); name != "" {
					bindings[name] = struct{}{}
				}
				continue
			case "typed_expression":
				stack = append(stack, walkItem{node: node.NamedChild(0), binding: true})
				continue
			case "argument_list", "open_tuple", "parenthesized_expression", "splat_expression", "tuple_expression":
				pushChildren(node, 0, func(int, *sitter.Node) bool { return true })
				continue
			}
		}
		switch node.Type() {
		case "assignment", "compound_assignment_expression", "for_binding", "let_binding":
			pushChildren(node, 1, nil)
			stack = append(stack, walkItem{node: node.NamedChild(0), binding: true})
			continue
		case "catch_clause", "let_statement":
			keyword := strings.TrimSuffix(node.Type(), "_statement")
			keyword = strings.TrimSuffix(keyword, "_clause")
			headerEnd := juliaHeaderBindingEnd(node, headerSource, keyword)
			pushChildren(node, 0, func(_ int, child *sitter.Node) bool {
				return int(child.StartByte()) < headerEnd
			})
			continue
		case "local_statement":
			pushChildren(node, 0, func(_ int, child *sitter.Node) bool {
				return child.Type() != "assignment" && child.Type() != "compound_assignment_expression"
			})
			continue
		case "do_clause", "arrow_function_expression":
			pushChildren(node, 1, nil)
			stack = append(stack, walkItem{node: node.NamedChild(0), binding: true})
			continue
		}
		pushChildren(node, 0, nil)
	}
	return bindings
}

// juliaHeaderBindingEnd separates comma-continued catch/let headers from
// their flattened bodies. Literal/comment masking makes delimiters in comments
// harmless while preserving tree-sitter byte offsets.
func juliaHeaderBindingEnd(node *sitter.Node, source, keyword string) int {
	cursor := int(node.StartByte()) + len(keyword)
	headerEnd := 0
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if strings.Contains(child.Type(), "comment") {
			continue
		}
		start := int(child.StartByte())
		if cursor < 0 || cursor > start || start > len(source) {
			break
		}
		gap := source[cursor:start]
		if headerEnd == 0 {
			if strings.ContainsAny(gap, ";\r\n") {
				break
			}
		} else {
			comma := strings.IndexByte(gap, ',')
			semicolon := strings.IndexByte(gap, ';')
			if comma < 0 || semicolon >= 0 && semicolon < comma {
				break
			}
		}
		headerEnd = int(child.EndByte())
		cursor = headerEnd
		if keyword == "catch" {
			break
		}
	}
	return headerEnd
}
