package sem

// PHP-specific call-site extraction. The generic scanners miss the dominant
// PHP call idioms (evidence: on composer/composer the focus method
// VersionSelector::findBestCandidate lost half its callgraph):
//
//   - receiverCallRe only knows `->`/`.`, so static calls through the scope
//     resolution operator (`PlatformRepository::isPlatformPackage(...)`,
//     `self::`, `static::`, `parent::`) never register as call sites.
//   - Property receivers (`$this->repositorySet->findPackages(...)`) are
//     matched as a plain local variable `repositorySet`, which no var-type
//     source can type; the property's type is declared at the class level
//     (typed property, `@var` docblock, or constructor assignment).
//   - Constructor chains are matched as `new Type().m(` with a dot; PHP spells
//     them `(new Type(...))->m(...)`.
//   - `$v = $this->createX(); $v->m()` factory receivers cannot type because
//     splitSignatureTypes did not understand PHP's `): Type` return syntax and
//     factoryReturnAssignRe requires a bare callee.
//
// Everything in this file is gated to Language == "PHP" by its callers so the
// other languages keep their existing behavior. The extractors follow the same
// conservative straight-line rules as ruby.go: single-assignment tracking,
// capitalized class names only, conflicting bindings dropped.

import (
	"regexp"
	"strings"
)

var (
	// ClassName::method( / self::method( / \Fully\Qualified::method( static
	// calls. The class part may be namespace-qualified; the terminal segment is
	// what resolves against the workspace symbol index (consistent with how
	// lastTypeSegment treats `\`-qualified PHP type references elsewhere).
	phpStaticCallRe = regexp.MustCompile(`(\\?[A-Za-z_]\w*(?:\\[A-Za-z_]\w*)*)\s*::\s*([A-Za-z_]\w*)\s*\(`)
	// $this->prop->method( property-receiver calls. Only `$this->` receivers:
	// a bare `$var->prop->method(` chain has no cheap type source.
	phpPropertyCallRe = regexp.MustCompile(`\$this\s*->\s*([A-Za-z_]\w*)\s*->\s*([A-Za-z_]\w*)\s*\(`)
	// $v = new Klass( / $v = new \Ns\Klass( local constructor assignments.
	phpNewAssignRe = regexp.MustCompile(`\$([A-Za-z_]\w*)\s*=\s*new\s+\\?((?:[A-Za-z_]\w*\\)*)([A-Z]\w*)\s*[(;]`)
	// new Klass( / new \Ns\Klass( constructor expressions (chain scanning).
	phpNewExprRe = regexp.MustCompile(`\bnew\s+\\?((?:[A-Za-z_]\w*\\)*)([A-Z]\w*)\s*\(`)
	// $v = $this->factory( assignments; the factory's declared return type
	// (same file) types the variable.
	phpThisFactoryAssignRe = regexp.MustCompile(`\$([A-Za-z_]\w*)\s*=\s*\$this\s*->\s*([A-Za-z_]\w*)\s*\(`)
	// Typed property declarations: private ?RepositorySet $repositorySet;
	phpTypedPropDeclRe = regexp.MustCompile(`\b(?:public|protected|private)(?:\s+(?:static|readonly))*\s+\??\\?((?:[A-Za-z_]\w*\\)*)([A-Z]\w*)\s+\$([A-Za-z_]\w*)`)
	// /** @var RepositorySet */ docblock immediately above an untyped property
	// declaration. Runs on raw content (docblocks are comments, so the stripped
	// text has them masked). Complex annotations (generics, unions) do not
	// match the single-identifier + `*/` shape and are skipped.
	phpVarDocPropRe = regexp.MustCompile(`@var\s+\\?((?:[A-Za-z_]\w*\\)*)([A-Z]\w*)\s*\*/\s*(?:(?:public|protected|private|static|readonly)\s+)+\$([A-Za-z_]\w*)`)
	// $this->prop = $param assignments inside the constructor body.
	phpCtorPropAssignRe = regexp.MustCompile(`\$this\s*->\s*([A-Za-z_]\w*)\s*=\s*\$([A-Za-z_]\w*)`)
	// $this->fooFactory->create(...)->bar(...) generated-factory chains.
	phpPropertyFactoryChainStartRe = regexp.MustCompile(`\$this\s*->\s*([A-Za-z_]\w*)\s*->\s*([A-Za-z_]\w*)\s*\(`)
	// Constructor header (params follow at the returned match's end).
	phpCtorHeaderRe = regexp.MustCompile(`\bfunction\s+__construct\s*\(`)
	// One constructor parameter: optional promotion visibility, optional
	// readonly, nullable marker, optionally namespace-qualified capitalized
	// type, by-ref/variadic markers, then the `$name`.
	phpCtorParamRe = regexp.MustCompile(`^(?:(public|protected|private)\s+)?(?:readonly\s+)?\??\\?((?:[A-Za-z_]\w*\\)*)([A-Z]\w*)\s+&?(?:\.\.\.)?\$([A-Za-z_]\w*)`)
	// Heredoc/nowdoc opener: <<<EOT / <<<"EOT" / <<<'EOT' at end of line. The
	// quoted forms are usually already masked as string literals by the
	// generic stripper; the bare form is what this must catch.
	phpHeredocStartRe = regexp.MustCompile(`<<<\s*["']?([A-Za-z_]\w*)["']?\s*$`)
)

// phpStaticCall is a `Class::method(...)` call site. Class is the terminal
// class-name segment (or the self/static/parent keyword); Detail preserves the
// spelled reference for evidence.
type phpStaticCall struct {
	Class  string
	Method string
	Detail string
}

type phpPropertyFactoryChainCall struct {
	Property      string
	FactoryMethod string
	Method        string
	Detail        string
}

// stripPHPCodeText extends the generic literal/comment stripper with the PHP
// syntaxes it does not know: `#` line comments (including `#[...]` attribute
// lines) and heredoc/nowdoc bodies. Length and line structure are preserved.
// Without this, comment prose and heredoc content would feed the static-call
// and receiver scanners below and produce false CALLS edges. Heredocs are
// masked before the generic stripper runs: a nowdoc's quoted `<<<'EOT'`
// opener would otherwise be eaten as a string literal (losing the delimiter),
// and unbalanced quotes inside heredoc bodies would derail string masking for
// the rest of the file.
func stripPHPCodeText(content string) string {
	bytes := []byte(stripCodeLiteralsAndComments(maskPHPHeredocs(content)))
	for i := 0; i < len(bytes); i++ {
		if bytes[i] != '#' {
			continue
		}
		j := i
		for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
			j++
		}
		maskBytes(bytes, i, j)
		i = j
	}
	return string(bytes)
}

// maskPHPHeredocs blanks heredoc/nowdoc bodies line by line: from the line
// after a `<<<DELIM` opener up to and including the closing delimiter line
// (which PHP allows to carry indentation and a trailing `;`/`,`/`)`).
func maskPHPHeredocs(s string) string {
	lines := strings.Split(s, "\n")
	delim := ""
	for idx, line := range lines {
		if delim != "" {
			trimmed := strings.TrimSpace(line)
			rest, isDelim := strings.CutPrefix(trimmed, delim)
			lines[idx] = strings.Repeat(" ", len(line))
			if isDelim && (rest == "" || rest == ";" || rest == "," || rest == ")" || rest == ");") {
				delim = ""
			}
			continue
		}
		if m := phpHeredocStartRe.FindStringSubmatch(line); m != nil {
			delim = m[1]
		}
	}
	return strings.Join(lines, "\n")
}

// phpStaticCalls extracts `Class::method(...)` static call sites. The class
// reference keeps only its terminal segment (matching how PHP class references
// resolve by short name elsewhere); self/static/parent pass through for the
// caller to resolve against the enclosing class or its superclass. Class names
// must be capitalized (PSR convention) — anything else is skipped rather than
// guessed.
func phpStaticCalls(block string) []phpStaticCall {
	stripped := stripPHPCodeText(block)
	var out []phpStaticCall
	seen := map[string]bool{}
	for _, m := range phpStaticCallRe.FindAllStringSubmatch(stripped, -1) {
		ref, method := m[1], m[2]
		class := ref[strings.LastIndexByte(ref, '\\')+1:]
		switch class {
		case "self", "static", "parent":
		default:
			if !isCapitalized(class) {
				continue
			}
		}
		key := class + "::" + method
		if class == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, phpStaticCall{Class: class, Method: method, Detail: ref + "::" + method})
	}
	return out
}

// phpPropertyReceiverCalls extracts `$this->prop->method(...)` call sites. The
// receiver is the property name; the caller types it through phpPropertyTypes.
func phpPropertyReceiverCalls(block string) []receiverCall {
	stripped := stripPHPCodeText(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range phpPropertyCallRe.FindAllStringSubmatch(stripped, -1) {
		prop, method := m[1], m[2]
		key := prop + "." + method
		if prop == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: prop, Method: method})
	}
	return out
}

// phpChainedConstructorCalls extracts `(new Klass(...))->method(...)`
// constructor-chained call sites, mirroring the generic `new Type().method(`
// chains for PHP's parenthesized `->` spelling. The constructor's argument
// list is skipped with a real paren matcher so nested `new`/call expressions
// inside the arguments do not break the chain detection.
func phpChainedConstructorCalls(block string) []typedMethodCall {
	stripped := stripPHPCodeText(block)
	var out []typedMethodCall
	seen := map[string]bool{}
	for _, m := range phpNewExprRe.FindAllStringSubmatchIndex(stripped, -1) {
		typeName := stripped[m[4]:m[5]]
		open := m[1] - 1 // the regex ends at `(`
		close := matchingParen(stripped, open)
		if close < 0 {
			continue
		}
		i := skipPHPSpace(stripped, close+1)
		if i < len(stripped) && stripped[i] == ')' {
			i = skipPHPSpace(stripped, i+1)
		}
		if !strings.HasPrefix(stripped[i:], "->") {
			continue
		}
		i = skipPHPSpace(stripped, i+2)
		j := i
		for j < len(stripped) && isPHPWordByte(stripped[j]) {
			j++
		}
		method := stripped[i:j]
		if method == "" || skipPHPSpace(stripped, j) >= len(stripped) || stripped[skipPHPSpace(stripped, j)] != '(' {
			continue
		}
		key := typeName + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, typedMethodCall{TypeName: typeName, Method: method, Detail: "new " + typeName + "()->" + method})
	}
	return out
}

func phpPropertyFactoryChainCalls(block string) []phpPropertyFactoryChainCall {
	stripped := stripPHPCodeText(block)
	var out []phpPropertyFactoryChainCall
	seen := map[string]bool{}
	for _, m := range phpPropertyFactoryChainStartRe.FindAllStringSubmatchIndex(stripped, -1) {
		prop := stripped[m[2]:m[3]]
		factory := stripped[m[4]:m[5]]
		open := m[1] - 1
		close := matchingParen(stripped, open)
		if close < 0 {
			continue
		}
		i := skipPHPSpace(stripped, close+1)
		if !strings.HasPrefix(stripped[i:], "->") {
			continue
		}
		i = skipPHPSpace(stripped, i+2)
		j := i
		for j < len(stripped) && isPHPWordByte(stripped[j]) {
			j++
		}
		method := stripped[i:j]
		if method == "" || skipPHPSpace(stripped, j) >= len(stripped) || stripped[skipPHPSpace(stripped, j)] != '(' {
			continue
		}
		key := prop + "." + factory + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, phpPropertyFactoryChainCall{
			Property:      prop,
			FactoryMethod: factory,
			Method:        method,
			Detail:        "this->" + prop + "->" + factory + "()->" + method,
		})
	}
	return out
}

// phpLocalVarTypes infers variable -> type from `$v = new Klass(...)`
// constructor assignments, including namespace-qualified class references
// (`new \Foo\Bar(...)` types $v as Bar, where the generic scanner would stop
// at the first segment). A variable assigned constructors of two different
// classes is dropped (conservative straight-line tracking, matching
// rubyLocalVarTypes).
func phpLocalVarTypes(block string) map[string]string {
	stripped := stripPHPCodeText(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range phpNewAssignRe.FindAllStringSubmatch(stripped, -1) {
		name, typeName := m[1], m[3]
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			continue
		}
		out[name] = typeName
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// phpThisFactoryVarTypes infers variable -> type from `$v = $this->factory(...)`
// assignments where the factory method's declared return type is known in this
// file. This is the PHP spelling of factoryReturnVarTypes (whose regex requires
// a bare callee). Conflicting assignments are dropped.
func phpThisFactoryVarTypes(block, filePath string, returnTypesBySymbolNameAndFile map[string]map[string][]string) map[string]string {
	stripped := stripPHPCodeText(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range phpThisFactoryAssignRe.FindAllStringSubmatch(stripped, -1) {
		name, factory := m[1], m[2]
		types := returnTypesBySymbolNameAndFile[factory][filePath]
		if len(types) == 0 {
			continue
		}
		if existing, ok := out[name]; ok && existing != types[0] {
			conflicted[name] = true
			continue
		}
		out[name] = types[0]
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// phpPropertyTypes infers property -> type for a PHP file from its cheap
// declared sources: typed property declarations, `@var` docblocks above
// untyped properties, and constructor assignments `$this->p = $param` where
// the constructor parameter is typed (including promoted `private Type $p`
// constructor properties). PSR-4 keeps one class per file, so a flat map is
// sufficient; a property name bound to two different types is dropped.
func phpPropertyTypes(content string) map[string]string {
	stripped := stripPHPCodeText(content)
	out := map[string]string{}
	conflicted := map[string]bool{}
	record := func(name, typeName string) {
		if name == "" || typeName == "" {
			return
		}
		if existing, ok := out[name]; ok && existing != typeName {
			conflicted[name] = true
			return
		}
		out[name] = typeName
	}
	for _, m := range phpTypedPropDeclRe.FindAllStringSubmatch(stripped, -1) {
		record(m[3], m[2])
	}
	// @var docblocks are comments: scan the raw content.
	for _, m := range phpVarDocPropRe.FindAllStringSubmatch(content, -1) {
		record(m[3], m[2])
	}
	if loc := phpCtorHeaderRe.FindStringIndex(stripped); loc != nil {
		open := loc[1] - 1
		close := matchingParen(stripped, open)
		if close > open {
			paramTypes := map[string]string{}
			for _, param := range splitTopLevelCommas(stripped[open+1 : close]) {
				m := phpCtorParamRe.FindStringSubmatch(strings.TrimSpace(param))
				if m == nil {
					continue
				}
				visibility, typeName, name := m[1], m[3], m[4]
				paramTypes[name] = typeName
				if visibility != "" {
					record(name, typeName) // promoted constructor property
				}
			}
			for _, m := range phpCtorPropAssignRe.FindAllStringSubmatch(phpBlockAfter(stripped, close), -1) {
				if typeName, ok := paramTypes[m[2]]; ok {
					record(m[1], typeName)
				}
			}
		}
	}
	for name := range conflicted {
		delete(out, name)
	}
	return out
}

// phpBlockAfter returns the first balanced `{...}` block starting at or after
// position i (the constructor body, when called with the position just past
// the parameter list).
func phpBlockAfter(s string, i int) string {
	for ; i < len(s); i++ {
		if s[i] == '{' {
			break
		}
		// Only whitespace, `:` return-type syntax, and type names may appear
		// between the params and the body; a `;` means an abstract/interface
		// constructor with no body.
		if s[i] == ';' {
			return ""
		}
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i : j+1]
			}
		}
	}
	return s[i:]
}

// splitTopLevelCommas splits on commas that are not nested inside (), [] or {}
// (constructor parameter defaults may contain calls or array literals).
func splitTopLevelCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func skipPHPSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isPHPWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
