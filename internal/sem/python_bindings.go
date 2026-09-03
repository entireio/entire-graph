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
	// `for a, b in ...`, statement and comprehension alike.
	pythonForTargetRe = regexp.MustCompile(`\bfor\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)\s+in\b`)
	// `with ... as name`, `except ... as name`, `import ... as name`.
	pythonAsTargetRe = regexp.MustCompile(`\bas\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// A nested definition binds its own name in the enclosing body.
	pythonNestedDefRe = regexp.MustCompile(`\b(?:def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// The header of any `def` in the block; the parameter list is read by
	// balancing parentheses from the captured `(`.
	pythonDefHeaderRe = regexp.MustCompile(`\bdef\s+[A-Za-z_][A-Za-z0-9_]*\s*\(`)
	pythonLambdaRe    = regexp.MustCompile(`\blambda\b`)
)

// pythonLocalBindingNames returns the names Python's syntax binds inside one
// callable's body: parameters, assignment targets, loop and context-manager
// variables, exception aliases, walrus targets, and nested definitions.
//
// A name bound here is that binding at every call site in the body -- Python
// makes a function-local name local for the whole body -- so it is NOT the
// same-named file-level or imported callable, and a call to it must not be
// resolved to one. Only a block that is itself a callable body may be passed:
// at module scope a `def` binds the very symbol a call is meant to reach.
//
// Deliberately conservative in the direction of suppression: the parameters of
// nested `def`s and `lambda`s are included even though they bind only in their
// own scope, because a name used as a parameter anywhere inside one callable is
// ambiguous evidence at best.
func pythonLocalBindingNames(block string) map[string]struct{} {
	stripped := stripPythonLiteralsAndComments(block)
	bindings := map[string]struct{}{}
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

	for _, header := range pythonDefHeaderRe.FindAllStringIndex(stripped, -1) {
		for _, param := range pythonParameterNames(stripped[header[1]-1:]) {
			add(param)
		}
	}
	for _, keyword := range pythonLambdaRe.FindAllStringIndex(stripped, -1) {
		rest := stripped[keyword[1]:]
		if colon := strings.IndexByte(rest, ':'); colon >= 0 {
			for _, param := range pythonSplitTopLevel(rest[:colon]) {
				add(pythonParameterName(param))
			}
		}
	}
	for _, match := range pythonWalrusRe.FindAllStringSubmatch(stripped, -1) {
		add(match[1])
	}
	for _, line := range strings.Split(stripped, "\n") {
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
		}
	}
	if len(bindings) == 0 {
		return nil
	}
	return bindings
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
