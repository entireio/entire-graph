package sem

import (
	"context"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

func retryGoNewExpressionParse(ctx context.Context, parser *sitter.Parser, root *sitter.Node, content string) (*sitter.Tree, *sitter.Node, *ParseStatus) {
	if root == nil || !root.HasError() {
		return nil, root, nil
	}
	if ctx.Err() != nil {
		status := goNewExpressionTimeoutStatus()
		return nil, root, &status
	}
	prepared, changed := prepareGoNewExpressionParseSource(ctx, content)
	if ctx.Err() != nil {
		status := goNewExpressionTimeoutStatus()
		return nil, root, &status
	}
	if !changed {
		return nil, root, nil
	}

	retryTree, err := parser.ParseCtx(ctx, nil, []byte(prepared))
	if ctx.Err() != nil {
		if retryTree != nil {
			retryTree.Close()
		}
		status := goNewExpressionTimeoutStatus()
		return nil, root, &status
	}
	if err != nil || retryTree == nil {
		if retryTree != nil {
			retryTree.Close()
		}
		return nil, root, nil
	}
	retryRoot := retryTree.RootNode()
	if retryRoot == nil || retryRoot.IsNull() || retryRoot.HasError() {
		retryTree.Close()
		return nil, root, nil
	}
	return retryTree, retryRoot, nil
}

func goNewExpressionTimeoutStatus() ParseStatus {
	return ParseStatus{
		ParseError: true,
		Code:       "E_PARSE_TIMEOUT",
		Detail:     "tree-sitter parse exceeded " + treeSitterParseTimeout.String(),
	}
}

// prepareGoNewExpressionParseSource builds a position-preserving parse view for
// the expression form of Go's predeclared new function. The bundled
// tree-sitter grammar accepts new(Type), but interprets the argument as a type
// and reports an error for unambiguously value-shaped arguments such as
// new(value()) or new("literal"). The standard Go parser identifies only
// syntactically valid call expressions; changing the three-byte callee in that
// private view makes tree-sitter retain the complete expression AST while all
// entity and relation text continues to come from the authored source.
//
// Call arguments that can also be types are deliberately left alone. The
// caller adopts this view only when a bounded retry parses without any error,
// so this compatibility path cannot hide unrelated malformed syntax.
func prepareGoNewExpressionParseSource(ctx context.Context, content string) (string, bool) {
	if ctx == nil || ctx.Err() != nil || len(content) > defaultMaxParseBytes || !strings.Contains(content, "new") {
		return content, false
	}

	files := token.NewFileSet()
	file, err := goparser.ParseFile(files, "source.go", content, goparser.SkipObjectResolution|goparser.AllErrors)
	if err != nil || file == nil || ctx.Err() != nil {
		return content, false
	}

	prepared := []byte(content)
	changed := false
	ast.Inspect(file, func(node ast.Node) bool {
		if ctx.Err() != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 || !goDefinitelyValueExpression(call.Args[0]) {
			return true
		}
		callee, ok := call.Fun.(*ast.Ident)
		if !ok || callee.Name != "new" {
			return true
		}
		offset := files.PositionFor(callee.Pos(), false).Offset
		if offset < 0 || offset+len("new") > len(prepared) || string(prepared[offset:offset+len("new")]) != "new" {
			return true
		}
		copy(prepared[offset:offset+len("new")], "n_w")
		changed = true
		return true
	})
	if ctx.Err() != nil || !changed {
		return content, false
	}
	return string(prepared), true
}

// goDefinitelyValueExpression excludes every syntax node that may name or
// construct a type. This is intentionally narrower than the set of all legal
// Go expressions: the compatibility retry addresses the concrete value forms
// the bundled grammar rejects without guessing whether an identifier or
// selector denotes a value.
func goDefinitelyValueExpression(expression ast.Expr) bool {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return goDefinitelyValueExpression(expression.X)
	case *ast.BasicLit, *ast.BinaryExpr, *ast.CallExpr, *ast.CompositeLit,
		*ast.FuncLit, *ast.SliceExpr, *ast.TypeAssertExpr, *ast.UnaryExpr:
		return true
	default:
		return false
	}
}
