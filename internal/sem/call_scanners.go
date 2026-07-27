package sem

import (
	"regexp"
	"strings"
)

var (
	clojureCallHeadRe       = regexp.MustCompile(`\(\s*([A-Za-z0-9_.$!?*+\-<>=/]+)`)
	sqlRoutineCallRe        = regexp.MustCompile(`(?i)\b((?:@?[A-Za-z_][A-Za-z0-9_]*@?|"[^"]+")(?:\s*\.\s*(?:@?[A-Za-z_][A-Za-z0-9_]*@?|"[^"]+"))*)\s*\(`)
	objectiveCMessageSendRe = regexp.MustCompile(`\[\s*([A-Za-z_][A-Za-z0-9_]*)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	fsharpDottedCallRe      = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_']*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_']*)+)\s*\(`)
	fsharpDottedApplyRe     = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_']*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_']*)+)\s+[A-Za-z_({\["'0-9]`)
	juliaCallRe             = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:!)?)\s*(?:\{[^{}\n;()]*\})?\s*\(`)
	luaDottedCallRe         = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:(?:\s*[.:]\s*)[A-Za-z_][A-Za-z0-9_]*)+)\s*\(`)
	zigDottedCallRe         = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_]*)+)\s*\(`)
)

func clojureCallIdentifiers(content string) map[string]struct{} {
	stripped := stripCodeLiteralsAndComments(content)
	out := map[string]struct{}{}
	for _, match := range clojureCallHeadRe.FindAllStringSubmatch(stripped, -1) {
		if len(match) < 2 {
			continue
		}
		name := clojureCallableName(match[1])
		if name == "" || clojureCallNameIgnored(name) {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func clojureCallableName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		name = parts[len(parts)-1]
	}
	return strings.Trim(name, ".")
}

func clojureCallNameIgnored(name string) bool {
	switch name {
	case ".", "..", "->", "->>", "as->", "case", "catch", "comment", "cond", "cond->", "cond->>", "def", "defmacro", "defmethod", "defmulti", "defn", "defn-", "defonce", "defprotocol", "defrecord", "deftype", "do", "doseq", "dotimes", "doto", "extend-protocol", "extend-type", "finally", "fn", "for", "if", "if-let", "if-not", "import", "let", "letfn", "loop", "new", "ns", "quote", "recur", "require", "set!", "throw", "try", "use", "var", "when", "when-let", "when-not", "while", "with-open":
		return true
	default:
		return strings.HasPrefix(name, ":")
	}
}

func sqlCallIdentifiers(content string) map[string]struct{} {
	stripped := stripSQLComments(content)
	out := map[string]struct{}{}
	for _, match := range sqlRoutineCallRe.FindAllStringSubmatch(stripped, -1) {
		if len(match) < 2 {
			continue
		}
		name := normalizeSQLDottedName(match[1])
		if strings.Contains(name, ".") {
			parts := strings.Split(name, ".")
			name = parts[len(parts)-1]
		}
		name = strings.TrimSpace(name)
		if name == "" || sqlCallNameIgnored(name) {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func sqlCallNameIgnored(name string) bool {
	switch strings.ToLower(name) {
	case "array", "cast", "coalesce", "count", "date_part", "exists", "extract", "function", "greatest", "if", "least", "lower", "max", "min", "nullif", "procedure", "sum", "upper", "values":
		return true
	default:
		return false
	}
}

func jsCallableArgumentIdentifiers(content string) map[string]struct{} {
	stripped := stripCodeLiteralsAndComments(content)
	out := map[string]struct{}{}
	for i := 0; i < len(stripped); i++ {
		if stripped[i] != '(' || jsOpenParenStartsDeclarationOrControl(stripped, i) {
			continue
		}
		close := findMatchingStaticDelimiter(stripped, i, '(', ')')
		if close < 0 || jsParenFollowedByArrow(stripped, close) {
			continue
		}
		for _, arg := range splitTopLevelStaticComma(stripped[i+1 : close]) {
			arg = strings.TrimSpace(arg)
			if !isSimpleIdentifier(arg) || jsCallableArgumentIgnored(arg) {
				continue
			}
			out[arg] = struct{}{}
		}
		i = close
	}
	return out
}

func jsOpenParenStartsDeclarationOrControl(content string, open int) bool {
	name, beforeName := identifierBefore(content, open)
	switch name {
	case "", "catch", "for", "function", "if", "switch", "while", "with":
		return true
	}
	previous, _ := identifierBefore(content, beforeName)
	return previous == "function"
}

func jsParenFollowedByArrow(content string, close int) bool {
	for i := close + 1; i < len(content); i++ {
		switch content[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '=':
			return i+1 < len(content) && content[i+1] == '>'
		default:
			return false
		}
	}
	return false
}

func identifierBefore(content string, pos int) (string, int) {
	i := pos - 1
	for i >= 0 && isASCIISpace(content[i]) {
		i--
	}
	end := i + 1
	for i >= 0 && isASCIIIdentifierByte(content[i]) {
		i--
	}
	if end == i+1 {
		return "", i + 1
	}
	return content[i+1 : end], i + 1
}

func isASCIISpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func jsCallableArgumentIgnored(name string) bool {
	switch name {
	case "false", "null", "this", "true", "undefined":
		return true
	default:
		return false
	}
}

func juliaCallIdentifiers(content string) map[string]struct{} {
	stripped := maskJuliaLiteralsAndComments(content)
	out := map[string]struct{}{}
	for _, match := range juliaCallRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(match) < 4 {
			continue
		}
		name := stripped[match[2]:match[3]]
		if juliaCallNameIgnored(stripped, match[2], name) {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func juliaCallNameIgnored(content string, start int, name string) bool {
	for i := start - 1; i >= 0; i-- {
		switch content[i] {
		case ' ', '\t':
			continue
		case '.':
			return true
		default:
			i = -1
		}
	}
	switch strings.TrimSuffix(name, "!") {
	case "begin", "catch", "do", "else", "elseif", "end", "finally", "for", "function", "if", "let", "macro", "quote", "return", "try", "while":
		return true
	default:
		return false
	}
}

func maskJuliaLiteralsAndComments(content string) string {
	bytes := []byte(stripCodeLiteralsAndComments(content))
	for i := 0; i < len(bytes); i++ {
		if bytes[i] != '#' {
			continue
		}
		if i+1 < len(bytes) && bytes[i+1] == '=' {
			j := i + 2
			for j+1 < len(bytes) && !(bytes[j] == '=' && bytes[j+1] == '#') {
				j++
			}
			if j+1 < len(bytes) {
				j += 2
			}
			maskBytes(bytes, i, j)
			i = j - 1
			continue
		}
		j := i + 1
		for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
			j++
		}
		maskBytes(bytes, i, j)
		i = j
	}
	return string(bytes)
}

func fsharpDottedCallIdentifiers(content string) map[string]struct{} {
	stripped := stripCodeLiteralsAndComments(content)
	out := dottedCallIdentifiers(stripped, fsharpDottedCallRe)
	for _, match := range fsharpDottedApplyRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(match) < 4 {
			continue
		}
		if fsharpDottedApplyIgnored(stripped, match[0]) {
			continue
		}
		name := lastDottedCallSegment(stripped[match[2]:match[3]])
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func fsharpDottedApplyIgnored(content string, start int) bool {
	previous, _ := identifierBefore(content, start)
	switch previous {
	case "inherit", "module", "namespace", "new", "open", "type":
		return true
	default:
		return false
	}
}

func luaDottedCallIdentifiers(content string) map[string]struct{} {
	return dottedCallIdentifiers(maskLuaLiteralsAndComments(content), luaDottedCallRe)
}

func zigDottedCallIdentifiers(content string) map[string]struct{} {
	return dottedCallIdentifiers(stripCodeLiteralsAndComments(content), zigDottedCallRe)
}

func dottedCallIdentifiers(content string, pattern *regexp.Regexp) map[string]struct{} {
	out := map[string]struct{}{}
	for _, match := range pattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		name := lastDottedCallSegment(match[1])
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func lastDottedCallSegment(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	fields := strings.FieldsFunc(name, func(r rune) bool {
		return r == '.' || r == ':'
	})
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(fields[len(fields)-1])
}

func maskLuaLiteralsAndComments(content string) string {
	bytes := []byte(content)
	for i := 0; i < len(bytes); i++ {
		switch bytes[i] {
		case '"', '\'':
			quote := bytes[i]
			for j := i + 1; j < len(bytes); j++ {
				if bytes[j] == '\n' || bytes[j] == '\r' {
					i = j
					break
				}
				if bytes[j] == '\\' {
					j++
					continue
				}
				if bytes[j] == quote {
					maskBytes(bytes, i, j+1)
					i = j
					break
				}
			}
		case '-':
			if i+1 < len(bytes) && bytes[i+1] == '-' {
				j := i + 2
				for j < len(bytes) && bytes[j] != '\n' && bytes[j] != '\r' {
					j++
				}
				maskBytes(bytes, i, j)
				i = j
			}
		}
	}
	return string(bytes)
}

func objectiveCMessageReceiverCalls(content string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(content)
	seen := map[string]bool{}
	var out []receiverCall
	for _, match := range objectiveCMessageSendRe.FindAllStringSubmatch(stripped, -1) {
		if len(match) < 3 {
			continue
		}
		receiver := strings.TrimSpace(match[1])
		method := strings.TrimSpace(match[2])
		if receiver == "" || method == "" || objectiveCMessageNameIgnored(method) {
			continue
		}
		key := receiver + "." + method
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method})
	}
	return out
}

func objectiveCMessageNameIgnored(name string) bool {
	switch name {
	case "alloc", "class", "copy", "init", "new", "superclass":
		return true
	default:
		return false
	}
}
