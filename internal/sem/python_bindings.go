package sem

import (
	"regexp"
	"strings"
)

var (
	// A bare `name = ...` statement, including an annotated one (`name: int =
	// 1`) and a tuple target (`a, b = ...`). The whole left-hand side must be
	// identifiers so `self.name = ...` and `items[0] = ...`, which bind an
	// attribute or an element rather than the name, never match. Matching and
	// discarding the character after the `=` keeps `==` out -- RE2 has no
	// lookahead -- and a comparison operator before it (`!=`, `<=`, `>=`) fails
	// the preceding `\s*`.
	pythonAssignTargetRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)\s*(?::[^=]*)?=(?:[^=]|$)`)
	// `name += ...` and friends. Python requires the name to be local already,
	// so the statement is proof of a binding either way.
	pythonAugmentedAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*(?:\*\*|//|>>|<<|[-+*/%@&|^])=(?:[^=]|$)`)
	// `name := ...` binds in the enclosing scope wherever it appears.
	pythonWalrusRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*:=`)
	// `for a, b in ...`. Only a `for` in this scope's own text reaches this
	// regex: a comprehension's `for` lives inside brackets and is taken out
	// with the rest of the comprehension, which is a scope of its own.
	pythonForTargetRe = regexp.MustCompile(`\bfor\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)\s+in\b`)
	// `with ... as name`, `except ... as name`, `import ... as name`.
	pythonAsTargetRe = regexp.MustCompile(`\bas\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// A nested definition binds its own name in the enclosing body.
	pythonNestedDefRe = regexp.MustCompile(`\b(?:def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// `global name` / `nonlocal name` declare the opposite of a local binding:
	// the name is the module's or the enclosing function's.
	pythonNonLocalDeclRe = regexp.MustCompile(`^(?:global|nonlocal)\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)`)
	// The header of any `def` in the block; the parameter list is read by
	// balancing parentheses from the captured `(`.
	pythonDefHeaderRe = regexp.MustCompile(`\bdef\s+[A-Za-z_][A-Za-z0-9_]*\s*\(`)
	// The header line of a nested `def`/`class`, whose body is its own scope.
	pythonNestedScopeHeaderRe = regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// pythonScopeNestingLimit bounds the recursion into nested scopes. Reaching it
// only costs suppression (the deepest scopes contribute no names), which is the
// safe direction: a missing entry leaves a call to be resolved as before.
const pythonScopeNestingLimit = 16

// pythonLocalBindingNames returns the names Python's syntax binds inside one
// callable's own scope: parameters, assignment targets, loop and
// context-manager variables, exception aliases, walrus targets, and nested
// definitions.
//
// A name bound here is that binding at every call site in the body -- Python
// makes a function-local name local for the whole body -- so it is NOT the
// same-named file-level or imported callable, and a call to it must not be
// resolved to one. Only a block that is itself a callable body may be passed:
// at module scope a `def` binds the very symbol a call is meant to reach.
//
// The answer is per-BODY, so it must not carry a name out of a scope nested
// inside that body. A `lambda`'s parameters, a comprehension's loop variables
// and a nested `def`'s parameters and locals bind only within those, and
// treating them as body-wide silently deletes a valid edge for an unrelated
// call elsewhere in the body (`cb = lambda compute: compute(1)` must leave a
// later `compute(...)` alone). Such a name is reported only when every
// occurrence of it in the body is inside the scope that binds it -- the one
// case where a single set answers correctly for every call site. When the uses
// straddle the boundary the name is left out, because the failure that matters
// here is deleting a real call, not resolving one call too many.
func pythonLocalBindingNames(block string) map[string]struct{} {
	bindings := pythonScopeBindingNames(stripPythonLiteralsAndComments(block), 0)
	if len(bindings) == 0 {
		return nil
	}
	return bindings
}

// pythonScopeBindingNames collects the names one scope's own text binds, having
// first cut out the scopes nested inside it.
func pythonScopeBindingNames(scope string, depth int) map[string]struct{} {
	bindings := map[string]struct{}{}
	if depth > pythonScopeNestingLimit {
		return bindings
	}
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			bindings[name] = struct{}{}
		}
	}
	addList := func(list string) {
		for _, part := range strings.Split(list, ",") {
			add(part)
		}
	}

	inner := pythonInnerScopes(scope)
	own := pythonMaskRanges(scope, inner)

	for _, header := range pythonDefHeaderRe.FindAllStringIndex(own, -1) {
		for _, param := range pythonParameterNames(own[header[1]-1:]) {
			add(param)
		}
	}
	for _, match := range pythonWalrusRe.FindAllStringSubmatch(own, -1) {
		add(match[1])
	}
	declaredNonLocal := map[string]struct{}{}
	for _, line := range strings.Split(own, "\n") {
		for _, statement := range strings.Split(line, ";") {
			statement = strings.TrimSpace(statement)
			if match := pythonAssignTargetRe.FindStringSubmatch(statement); match != nil {
				addList(match[1])
			}
			if match := pythonAugmentedAssignRe.FindStringSubmatch(statement); match != nil {
				add(match[1])
			}
			for _, match := range pythonForTargetRe.FindAllStringSubmatch(statement, -1) {
				addList(match[1])
			}
			for _, match := range pythonAsTargetRe.FindAllStringSubmatch(statement, -1) {
				add(match[1])
			}
			for _, match := range pythonNestedDefRe.FindAllStringSubmatch(statement, -1) {
				add(match[1])
			}
			if match := pythonNonLocalDeclRe.FindStringSubmatch(statement); match != nil {
				for _, part := range strings.Split(match[1], ",") {
					declaredNonLocal[strings.TrimSpace(part)] = struct{}{}
				}
			}
		}
	}
	// A nested `def` or `class` binds its own name HERE even though everything
	// inside it is a scope of its own.
	for _, nested := range inner {
		add(nested.name)
	}
	// A name an inner scope binds is bound only there, so it may join this
	// scope's set only when no use of it escapes that scope.
	for _, nested := range inner {
		for name := range pythonInnerScopeBindings(nested, depth) {
			if _, already := bindings[name]; already {
				continue
			}
			if pythonNameOccursIn(own, name) {
				continue
			}
			add(name)
		}
	}
	for name := range declaredNonLocal {
		delete(bindings, name)
	}
	return bindings
}

// pythonInnerScope is a region of one scope's text whose bindings belong to a
// scope of its own: a nested `def`/`class` body, a `lambda`, or a
// comprehension.
type pythonInnerScope struct {
	start, end int      // half-open byte range within the enclosing scope's text
	name       string   // what a nested `def`/`class` binds in the ENCLOSING scope
	params     []string // a lambda's parameters
	body       string   // the text whose bindings are the inner scope's own
}

// pythonInnerScopeBindings returns the names bound inside one nested scope.
func pythonInnerScopeBindings(scope pythonInnerScope, depth int) map[string]struct{} {
	bindings := pythonScopeBindingNames(scope.body, depth+1)
	for _, param := range scope.params {
		if param != "" {
			bindings[param] = struct{}{}
		}
	}
	return bindings
}

// pythonInnerScopes locates the scopes nested inside one scope's text. Nested
// `def`/`class` bodies are delimited by indentation; a `lambda` runs to the end
// of the expression that holds it; a comprehension is the bracketed region
// around a `for` that is not a statement (Python 3 scopes comprehension
// variables to the comprehension).
func pythonInnerScopes(scope string) []pythonInnerScope {
	nested := pythonNestedScopes(scope)
	scopes := make([]pythonInnerScope, 0, len(nested))
	scopes = append(scopes, nested...)

	next := 0
	var open []int
	for i := 0; i < len(scope); i++ {
		for next < len(nested) && i >= nested[next].end {
			next++
		}
		if next < len(nested) && i >= nested[next].start {
			// A nested body's brackets balance inside it, so skipping the whole
			// body leaves the bracket stack consistent.
			i = nested[next].end - 1
			next++
			continue
		}
		switch scope[i] {
		case '(', '[', '{':
			open = append(open, i)
			continue
		case ')', ']', '}':
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
			continue
		}
		if pythonWordAt(scope, i, "lambda") {
			if lambda, ok := pythonLambdaScope(scope, i, len(open) > 0); ok {
				scopes = append(scopes, lambda)
				i = lambda.end - 1
				continue
			}
			i += len("lambda") - 1
			continue
		}
		if len(open) > 0 && pythonWordAt(scope, i, "for") {
			start := open[len(open)-1]
			if end, ok := pythonBracketEnd(scope, start); ok {
				scopes = append(scopes, pythonInnerScope{start: start, end: end, body: scope[start+1 : end-1]})
				open = open[:len(open)-1]
				i = end - 1
				continue
			}
			i += len("for") - 1
		}
	}
	return scopes
}

// pythonNestedScopes returns the nested `def` and `class` bodies of one scope's
// text, each running from its header to the first later line indented no deeper
// than that header. The scope's own header -- the first non-blank line -- is not
// one of them: its parameters bind in the scope this call is about.
func pythonNestedScopes(scope string) []pythonInnerScope {
	lines := strings.Split(scope, "\n")
	offsets := make([]int, len(lines))
	for i, offset := 0, 0; i < len(lines); i++ {
		offsets[i] = offset
		offset += len(lines[i]) + 1
	}

	var scopes []pythonInnerScope
	baseIndent := -1
	for i := 0; i < len(lines); i++ {
		body := strings.TrimLeft(lines[i], " \t")
		if body == "" {
			continue
		}
		indent := len(lines[i]) - len(body)
		if baseIndent < 0 {
			baseIndent = indent
			continue
		}
		if indent <= baseIndent {
			continue
		}
		match := pythonNestedScopeHeaderRe.FindStringSubmatch(body)
		if match == nil {
			continue
		}
		end, endLine := len(scope), len(lines)
		for j := i + 1; j < len(lines); j++ {
			rest := strings.TrimLeft(lines[j], " \t")
			if rest == "" {
				continue
			}
			if len(lines[j])-len(rest) <= indent {
				end, endLine = offsets[j], j
				break
			}
		}
		scopes = append(scopes, pythonInnerScope{
			start: offsets[i],
			end:   end,
			name:  match[1],
			body:  scope[offsets[i]:end],
		})
		i = endLine - 1
	}
	return scopes
}

// pythonLambdaScope reads the lambda that starts at the `lambda` keyword in
// source. Its parameters end at the first top-level `:`; its body ends where the
// expression holding it does -- a closing bracket, a comma once the body has
// begun, a comprehension's `for`, or, when the lambda is not inside brackets,
// the end of the line.
func pythonLambdaScope(scope string, start int, insideBrackets bool) (pythonInnerScope, bool) {
	depth, colon, end := 0, -1, len(scope)
scan:
	for i := start + len("lambda"); i < len(scope); i++ {
		switch c := scope[i]; {
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				end = i
				break scan
			}
			depth--
		case c == ':' && depth == 0 && colon < 0:
			colon = i
		case c == ',' && depth == 0 && colon >= 0:
			end = i
			break scan
		case c == '\n' && depth == 0 && !insideBrackets:
			end = i
			break scan
		case depth == 0 && colon >= 0 && pythonWordAt(scope, i, "for"):
			// `[lambda x: x for x in xs]`: the comprehension's `for` ends the
			// lambda body rather than belonging to it.
			end = i
			break scan
		}
	}
	if colon < 0 || colon > end {
		return pythonInnerScope{}, false
	}
	var params []string
	for _, param := range pythonSplitTopLevel(scope[start+len("lambda") : colon]) {
		if name := pythonParameterName(param); name != "" {
			params = append(params, name)
		}
	}
	return pythonInnerScope{start: start, end: end, params: params, body: scope[colon+1 : end]}, true
}

// pythonBracketEnd returns the offset just past the bracket that closes the one
// opened at start.
func pythonBracketEnd(source string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(source); i++ {
		switch source[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// pythonMaskRanges blanks the given ranges, keeping every offset and line break
// so the line-oriented scanners still see the statements around them.
func pythonMaskRanges(scope string, scopes []pythonInnerScope) string {
	if len(scopes) == 0 {
		return scope
	}
	masked := []byte(scope)
	for _, inner := range scopes {
		maskBytes(masked, inner.start, inner.end)
	}
	return string(masked)
}

// pythonNameOccursIn reports whether name appears in source as a whole
// identifier.
func pythonNameOccursIn(source, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i+len(name) <= len(source); {
		next := strings.Index(source[i:], name)
		if next < 0 {
			return false
		}
		at := i + next
		if pythonWordAt(source, at, name) {
			return true
		}
		i = at + 1
	}
	return false
}

// pythonWordAt reports whether word sits at index i of source as a whole
// identifier rather than as part of a longer one.
func pythonWordAt(source string, i int, word string) bool {
	if i < 0 || i+len(word) > len(source) || source[i:i+len(word)] != word {
		return false
	}
	if i > 0 && pythonIdentifierByte(source[i-1]) {
		return false
	}
	if end := i + len(word); end < len(source) && pythonIdentifierByte(source[end]) {
		return false
	}
	return true
}

func pythonIdentifierByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// pythonParameterNames reads the parameter names out of a `def` parameter list
// that starts at the opening parenthesis of source, ignoring nested brackets so
// that annotations and default values (`items: Dict[str, int] = {}`) cannot be
// mistaken for further parameters.
func pythonParameterNames(source string) []string {
	depth := 0
	end := -1
	for i := 0; i < len(source); i++ {
		switch source[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end <= 0 {
		return nil
	}
	var names []string
	for _, param := range pythonSplitTopLevel(source[1:end]) {
		if name := pythonParameterName(param); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// pythonParameterName reduces one parameter to the name it binds, dropping
// `*`/`**` markers, a type annotation, and a default value. A bare `*` or `/`
// separator binds nothing.
func pythonParameterName(param string) string {
	param = strings.TrimSpace(param)
	param = strings.TrimLeft(param, "*")
	if cut := strings.IndexAny(param, ":="); cut >= 0 {
		param = param[:cut]
	}
	param = strings.TrimSpace(param)
	for i := 0; i < len(param); i++ {
		c := param[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return ""
	}
	return param
}

// pythonSplitTopLevel splits on commas that are not inside brackets.
func pythonSplitTopLevel(source string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(source); i++ {
		switch source[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, source[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, source[start:])
}
