package sem

// Ruby-specific call-site extraction. The generic scanners miss the dominant
// Ruby call idioms (evidence: on discourse/discourse the focus method
// PostRevisor#revise! got zero inbound/outbound CALLS edges):
//
//   - callLikeIdentifiers requires `name(` and its identifier class has no
//     `!`/`?`, so `revise!(...)`, paren-less `should_revise?`, and paren-less
//     helper calls never register as call sites.
//   - receiverCallRe requires `(` right after the method name, so
//     `self.publish_changes` and `revisor.revise!(...)` are dropped.
//   - Constructor chains are matched as `new Type().m(` / `Type().m(`; Ruby's
//     `Type.new(...).m(...)` matches neither, and `Type.new` itself cannot
//     resolve because Ruby's constructor is named `initialize`.
//
// Everything in this file is gated to Language == "Ruby" by its callers so the
// other languages keep their existing behavior.

import (
	"regexp"
	"strings"
)

var (
	// receiver.method with an optional Ruby `!`/`?` suffix and *without*
	// requiring parentheses: in Ruby a dotted selector is always a method call
	// (there is no public field access through `.`).
	rubyReceiverCallRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*\.\s*([a-z_]\w*[!?]?)`)
	// Klass.new(...).method / Klass.new.method constructor chains.
	rubyCtorChainRe = regexp.MustCompile(`\b([A-Z]\w*)\s*\.\s*new\b(?:\s*\([^)]*\))?\s*\.\s*([a-z_]\w*[!?]?)`)
	// v = Klass.new / v = Mod::Klass.new local constructor assignments.
	rubyCtorAssignRe = regexp.MustCompile(`\b([a-z_]\w*)\s*=\s*(?:[A-Z]\w*::)*([A-Z]\w*)\s*\.\s*new\b`)
	// Bare words that can only be method calls because of their suffix
	// (identifiers ending in `!`/`?` are method names in Ruby).
	rubySuffixedNameRe = regexp.MustCompile(`\b[a-z_]\w*[!?]`)
	// Any bare word that may be a receiver-less call on implicit self.
	rubyBareWordRe = regexp.MustCompile(`\b[a-z_]\w*[!?]?`)
	// Local single/compound assignments (`x = ...`, `x += ...`, `x ||= ...`);
	// `[^=~>]` keeps `==`, `=~`, and `=>` from counting as assignments.
	rubyLocalAssignRe = regexp.MustCompile(`\b([a-z_]\w*)\s*(?:\|\||&&|<<|[+\-*/%])?=(?:[^=~>]|$)`)
	// rescue Foo => e bindings.
	rubyRescueBindingRe = regexp.MustCompile(`=>\s*([a-z_]\w*)`)
	// do |a, b| / { |a, b| block parameter lists.
	rubyBlockParamsRe = regexp.MustCompile(`(?:\bdo|\{)\s*\|([^|\n]*)\|`)
	// Heredoc opener; the delimiter must follow << without a space so `arr <<
	// CONST` shifts are not mistaken for heredocs.
	rubyHeredocStartRe = regexp.MustCompile(`<<[-~]?["']?([A-Z_][A-Za-z0-9_]*)["']?`)
)

// rubyKeywords are bare words that must never be treated as call sites. The
// list also covers ubiquitous Kernel/DSL words whose targets would be noise.
var rubyKeywords = map[string]bool{
	"alias": true, "and": true, "begin": true, "break": true, "case": true,
	"class": true, "def": true, "defined?": true, "do": true, "else": true,
	"elsif": true, "end": true, "ensure": true, "false": true, "for": true,
	"if": true, "in": true, "module": true, "next": true, "nil": true,
	"not": true, "or": true, "redo": true, "rescue": true, "retry": true,
	"return": true, "self": true, "super": true, "then": true, "true": true,
	"undef": true, "unless": true, "until": true, "when": true, "while": true,
	"yield": true, "require": true, "require_relative": true, "raise": true,
	"new": true, "lambda": true, "proc": true, "loop": true, "puts": true,
	"print": true, "attr_accessor": true, "attr_reader": true,
	"attr_writer": true, "include": true, "extend": true, "private": true,
	"public": true, "protected": true, "module_function": true,
}

// stripRubyCodeText extends the generic literal/comment stripper with the Ruby
// syntaxes it does not know: `#` line comments, heredoc bodies, and
// %w/%i/%W/%I word-array literals. Length and line structure are preserved.
// Without this, comment prose and heredoc content would feed the paren-less
// call scanners below and produce false CALLS edges.
func stripRubyCodeText(content string) string {
	bytes := []byte(stripCodeLiteralsAndComments(content))
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '#':
			j := i
			for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
				j++
			}
			maskBytes(bytes, i, j)
			i = j
		case '%':
			if i+2 >= len(bytes) {
				continue
			}
			letter := bytes[i+1]
			if letter != 'w' && letter != 'W' && letter != 'i' && letter != 'I' {
				continue
			}
			var close byte
			switch bytes[i+2] {
			case '[':
				close = ']'
			case '(':
				close = ')'
			case '{':
				close = '}'
			case '<':
				close = '>'
			default:
				continue
			}
			k := i + 3
			for k < len(bytes) && bytes[k] != close {
				k++
			}
			if k < len(bytes) {
				k++
			}
			maskBytes(bytes, i, k)
			i = k - 1
		}
	}
	return maskRubyHeredocs(string(bytes))
}

// maskRubyHeredocs blanks heredoc bodies line by line: from the line after a
// `<<~DELIM`-style opener up to and including the closing delimiter line.
func maskRubyHeredocs(s string) string {
	lines := strings.Split(s, "\n")
	delim := ""
	for idx, line := range lines {
		if delim != "" {
			trimmed := strings.TrimSpace(line)
			lines[idx] = strings.Repeat(" ", len(line))
			if trimmed == delim {
				delim = ""
			}
			continue
		}
		if m := rubyHeredocStartRe.FindStringSubmatch(line); m != nil {
			delim = m[1]
		}
	}
	return strings.Join(lines, "\n")
}

// rubyReceiverCalls extracts `receiver.method` call sites, keeping `!`/`?`
// method suffixes and accepting paren-less selectors. Attr-writer assignments
// (`x.attr = v`) are skipped: they target `attr=`, not the reader.
func rubyReceiverCalls(block string) []receiverCall {
	stripped := stripRubyCodeText(block)
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range rubyReceiverCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		receiver := stripped[m[2]:m[3]]
		method := stripped[m[4]:m[5]]
		end := m[5]
		// `a.b!= c` is `a.b != c`: the `!` belongs to the comparison.
		if strings.HasSuffix(method, "!") && end < len(stripped) && stripped[end] == '=' {
			method = strings.TrimSuffix(method, "!")
			end--
		}
		if method == "" || rubyWordBefore(stripped, m[2], "def") {
			continue
		}
		rest := strings.TrimLeft(stripped[end:], " \t")
		if strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") {
			continue // attr-writer assignment
		}
		args := ""
		if end < len(stripped) && stripped[end] == '(' {
			if close := matchingParen(stripped, end); close > end {
				args = stripped[end+1 : close]
			}
		}
		key := receiver + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method, Args: args})
	}
	return out
}

// rubyChainedConstructorCalls extracts `Klass.new(...).method` /
// `Klass.new.method` constructor-chained call sites, mirroring the generic
// `new Type().method(` chains for Ruby's `.new` spelling.
func rubyChainedConstructorCalls(block string) []typedMethodCall {
	stripped := stripRubyCodeText(block)
	var out []typedMethodCall
	seen := map[string]bool{}
	for _, m := range rubyCtorChainRe.FindAllStringSubmatch(stripped, -1) {
		typeName, method := m[1], m[2]
		key := typeName + "." + method
		if typeName == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, typedMethodCall{TypeName: typeName, Method: method, Detail: typeName + ".new()." + method})
	}
	return out
}

// rubyLocalVarTypes infers variable -> type from `v = Klass.new(...)`
// constructor assignments. A variable assigned constructors of two different
// classes is dropped (conservative straight-line tracking, matching the
// single-assignment stance of the generic localVarTypes).
func rubyLocalVarTypes(block string) map[string]string {
	stripped := stripRubyCodeText(block)
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range rubyCtorAssignRe.FindAllStringSubmatch(stripped, -1) {
		name, typeName := m[1], m[2]
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

// rubySuffixedCallIdentifiers returns bare `name!` / `name?` words, which are
// unambiguously method calls in Ruby even without parentheses. Receiver calls
// (dot before), symbols/hash keys (colon), sigiled variables, and definition
// lines are excluded; `a != b` comparisons are not bang names.
func rubySuffixedCallIdentifiers(content string) map[string]struct{} {
	stripped := stripRubyCodeText(content)
	out := map[string]struct{}{}
	for _, loc := range rubySuffixedNameRe.FindAllStringIndex(stripped, -1) {
		name := stripped[loc[0]:loc[1]]
		if strings.HasSuffix(name, "!") && loc[1] < len(stripped) && stripped[loc[1]] == '=' {
			continue // a != b
		}
		if rubyKeywords[name] || rubyBareNameContextExcluded(stripped, loc[0]) {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// rubyBareCallNames returns bare words inside a method body that plausibly are
// receiver-less calls on implicit self: not keywords, not local variables
// (assignments, parameters, block parameters, rescue bindings), not preceded
// by a dot/colon/sigil, not hash keys, and not assignment targets. The caller
// keeps this precise by only emitting an edge when the name resolves to a
// method of the enclosing class (or an ancestor).
func rubyBareCallNames(block, signature string) []string {
	stripped := stripRubyCodeText(block)
	locals := rubyLocalNames(stripped, signature)
	seen := map[string]bool{}
	var out []string
	for _, loc := range rubyBareWordRe.FindAllStringIndex(stripped, -1) {
		name := stripped[loc[0]:loc[1]]
		end := loc[1]
		if strings.HasSuffix(name, "!") && end < len(stripped) && stripped[end] == '=' {
			name = strings.TrimSuffix(name, "!") // a != b
			end--
		}
		if name == "" || rubyKeywords[name] || locals[name] || seen[name] {
			continue
		}
		if rubyBareNameContextExcluded(stripped, loc[0]) {
			continue
		}
		rest := strings.TrimLeft(stripped[end:], " \t")
		// `name:` is a hash key / keyword argument, `name =` an assignment.
		if strings.HasPrefix(rest, ":") && !strings.HasPrefix(rest, "::") {
			continue
		}
		if strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// rubyBareNameContextExcluded reports whether the word starting at start is
// not a receiver-less call site: dotted selectors (handled by the receiver
// path), symbols and hash keys, @ivar/$global sigils, and `def` names.
func rubyBareNameContextExcluded(stripped string, start int) bool {
	if start > 0 {
		switch stripped[start-1] {
		case '@', '$':
			return true
		}
	}
	for i := start - 1; i >= 0; i-- {
		switch stripped[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '.', ':':
			return true
		}
		break
	}
	return rubyWordBefore(stripped, start, "def")
}

// rubyWordBefore reports whether the last word before position start equals
// word (used to skip `def name` definition lines).
func rubyWordBefore(stripped string, start int, word string) bool {
	i := start - 1
	for i >= 0 && (stripped[i] == ' ' || stripped[i] == '\t') {
		i--
	}
	end := i + 1
	for i >= 0 && (stripped[i] == '_' || stripped[i] >= 'a' && stripped[i] <= 'z') {
		i--
	}
	return stripped[i+1:end] == word
}

// rubyLocalNames collects names bound locally in the block: assignments,
// rescue bindings, block parameters, and the method's own parameters from its
// signature. These bare words are variable reads, not implicit-self calls.
func rubyLocalNames(stripped, signature string) map[string]bool {
	out := map[string]bool{}
	for _, m := range rubyLocalAssignRe.FindAllStringSubmatch(stripped, -1) {
		out[m[1]] = true
	}
	for _, m := range rubyRescueBindingRe.FindAllStringSubmatch(stripped, -1) {
		out[m[1]] = true
	}
	addParam := func(param string) {
		param = strings.TrimSpace(param)
		param = strings.TrimLeft(param, "*&")
		if m := regexp.MustCompile(`^[a-z_]\w*`).FindString(param); m != "" {
			out[m] = true
		}
	}
	for _, m := range rubyBlockParamsRe.FindAllStringSubmatch(stripped, -1) {
		for _, param := range strings.Split(m[1], ",") {
			addParam(param)
		}
	}
	params := ""
	if open := strings.Index(signature, "("); open >= 0 {
		if close := strings.LastIndex(signature, ")"); close > open {
			params = signature[open+1 : close]
		}
	} else if m := regexp.MustCompile(`^def\s+\S+\s+(.*)$`).FindStringSubmatch(strings.TrimSpace(signature)); m != nil {
		params = m[1] // paren-less `def name a, b`
	}
	for _, param := range strings.Split(params, ",") {
		addParam(strings.SplitN(param, "=", 2)[0])
	}
	return out
}

// mergeReceiverCalls appends the extra receiver calls not already present
// (keyed by receiver.method, matching the extractors' own dedupe keys).
func mergeReceiverCalls(calls, extra []receiverCall) []receiverCall {
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.Receiver+"."+c.Method] = true
	}
	for _, c := range extra {
		if seen[c.Receiver+"."+c.Method] {
			continue
		}
		seen[c.Receiver+"."+c.Method] = true
		calls = append(calls, c)
	}
	return calls
}
