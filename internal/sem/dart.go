package sem

import (
	"regexp"
	"strings"
)

var dartSetterAssignmentRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_$]*)\s*\.\s*([A-Za-z_][A-Za-z0-9_$]*)\s*=`)

func dartSetterAssignmentCalls(block string) []receiverCall {
	stripped := stripCodeLiteralsAndComments(maskDartMultilineStrings(block))
	var out []receiverCall
	seen := map[string]bool{}
	for _, m := range dartSetterAssignmentRe.FindAllStringSubmatchIndex(stripped, -1) {
		if len(m) < 6 {
			continue
		}
		if m[1] < len(stripped) {
			switch stripped[m[1]] {
			case '=', '>':
				continue
			}
		}
		receiver := strings.TrimSpace(stripped[m[2]:m[3]])
		method := strings.TrimSpace(stripped[m[4]:m[5]])
		key := receiver + "." + method
		if receiver == "" || method == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, receiverCall{Receiver: receiver, Method: method})
	}
	return out
}

func maskDartMultilineStrings(content string) string {
	bytes := []byte(content)
	for i := 0; i+2 < len(bytes); i++ {
		start := i
		quotePos := i
		if (bytes[i] == 'r' || bytes[i] == 'R') && i+3 < len(bytes) && isDartTripleQuote(bytes[i+1:]) {
			quotePos = i + 1
		} else if !isDartTripleQuote(bytes[i:]) {
			continue
		}
		quote := bytes[quotePos]
		j := quotePos + 3
		for j+2 < len(bytes) && !(bytes[j] == quote && bytes[j+1] == quote && bytes[j+2] == quote) {
			j++
		}
		if j+2 < len(bytes) {
			j += 3
		}
		maskBytes(bytes, start, j)
		i = j - 1
	}
	return string(bytes)
}

func isDartTripleQuote(bytes []byte) bool {
	if len(bytes) < 3 {
		return false
	}
	return (bytes[0] == '\'' || bytes[0] == '"') && bytes[1] == bytes[0] && bytes[2] == bytes[0]
}
