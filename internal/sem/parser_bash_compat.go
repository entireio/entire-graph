package sem

import "strings"

// maskBashCompatibilitySyntax applies reviewed, position-preserving parse-view
// shims for narrow gaps in the bundled Bash grammar. Each helper validates its
// own complete syntax shape before changing same-width bytes; the authored
// source remains authoritative for extracted text, ranges, and hashes.
func maskBashCompatibilitySyntax(content string) string {
	masked := maskBashUnsupportedSyntax(content)
	masked = maskBashParameterReplacementPunctuation(masked)
	masked = maskBashCommentBacktickArguments(masked)
	return maskBashAssignmentPrefixedNamespacedCommands(masked)
}

// maskBashAssignmentPrefixedNamespacedCommands blanks only assignment words
// immediately preceding a namespaced command in command position. It retains
// the command spelling and every byte of source length/newline structure, so
// the normal parser and shell call scanner still see the command and preserve
// source coordinates.
func maskBashAssignmentPrefixedNamespacedCommands(content string) string {
	b := []byte(content)
	commandPos := true
	for i := 0; i < len(content); {
		c := content[i]
		switch c {
		case '\'', '"':
			i = skipBashQuoted(content, i)
			commandPos = false
		case '#':
			if commandPos {
				i = skipBashComment(content, i)
				continue
			}
			i++
		case '\n', '\r':
			commandPos = true
			i++
		case ';', '|', '&':
			commandPos = true
			i++
		case '(':
			commandPos = true
			i++
		case '$':
			if i+1 < len(content) && content[i+1] == '(' {
				commandPos = true
				i += 2
				continue
			}
			i++
		default:
			if isBashSpace(c) {
				i++
				continue
			}
			if commandPos {
				end, assignments := scanBashAssignments(content, i)
				if len(assignments) > 0 {
					commandEnd := scanBashWord(content, end)
					if isBashNamespacedCommand(content[end:commandEnd]) {
						if anyBashAssignmentContainsNestedCommand(content, assignments) {
							// Do not erase a command substitution or process substitution
							// nested in an assignment value. Keeping this line partial is
							// safer than losing a real callee from the parse view.
							i = commandEnd
							commandPos = false
							continue
						}
						for _, assignment := range assignments {
							maskBytesPreservingNewlines(b, assignment.start, assignment.end)
						}
						i = commandEnd
						commandPos = false
						continue
					}
					i = end
					commandPos = false
					continue
				}
				end = scanBashWord(content, i)
				if end > i {
					i = end
					commandPos = false
					continue
				}
			}
			i++
		}
	}
	return string(b)
}

type bashAssignmentSpan struct{ start, end int }

func anyBashAssignmentContainsNestedCommand(content string, assignments []bashAssignmentSpan) bool {
	for _, assignment := range assignments {
		value := content[assignment.start:assignment.end]
		if strings.Contains(value, "$(") || strings.ContainsRune(value, '`') || strings.Contains(value, "<(") {
			return true
		}
	}
	return false
}

func scanBashAssignments(content string, start int) (int, []bashAssignmentSpan) {
	i := start
	var assignments []bashAssignmentSpan
	for {
		wordEnd := scanBashWord(content, i)
		eq := findBashAssignmentEquals(content, i, wordEnd)
		if eq < 0 {
			break
		}
		assignments = append(assignments, bashAssignmentSpan{start: i, end: wordEnd})
		i = wordEnd
		for i < len(content) && (content[i] == ' ' || content[i] == '\t') {
			i++
		}
	}
	return i, assignments
}

func findBashAssignmentEquals(content string, start, end int) int {
	if start >= end || !isBashNameStart(content[start]) {
		return -1
	}
	for i := start + 1; i < end; i++ {
		if content[i] == '=' {
			for j := start + 1; j < i; j++ {
				if !isBashNameContinue(content[j]) {
					return -1
				}
			}
			return i
		}
		if !isBashNameContinue(content[i]) {
			return -1
		}
	}
	return -1
}

func scanBashWord(content string, start int) int {
	i := start
	for i < len(content) {
		switch content[i] {
		case '\'', '"':
			i = skipBashQuoted(content, i)
		case '\\':
			if i+1 < len(content) {
				i += 2
			} else {
				i++
			}
		case '$':
			if i+1 < len(content) && content[i+1] == '(' {
				i = skipBashCommandSubstitution(content, i)
			} else {
				i++
			}
		case '<':
			if i+1 < len(content) && content[i+1] == '(' {
				i = skipBashCommandSubstitution(content, i+1)
			} else {
				i++
			}
		case ' ', '\t', '\n', '\r', ';', '|', '&', '(', ')':
			return i
		case '#':
			return i
		default:
			i++
		}
	}
	return i
}

func isBashNamespacedCommand(word string) bool {
	if len(word) < 3 {
		return false
	}
	for i := 0; i+1 < len(word); i++ {
		if word[i] == ':' && word[i+1] == ':' {
			return true
		}
	}
	return false
}

func skipBashQuoted(content string, start int) int {
	quote := content[start]
	for i := start + 1; i < len(content); i++ {
		if content[i] == '\\' && quote == '"' && i+1 < len(content) {
			i++
			continue
		}
		if content[i] == quote {
			return i + 1
		}
	}
	return len(content)
}

func skipBashCommandSubstitution(content string, start int) int {
	depth := 0
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '\'', '"':
			i = skipBashQuoted(content, i) - 1
		case '$':
			if i+1 < len(content) && content[i+1] == '(' {
				depth++
				i++
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth <= 0 {
				return i + 1
			}
		}
	}
	return len(content)
}

func skipBashComment(content string, start int) int {
	if end := indexBashLineEnd(content, start); end >= 0 {
		return end
	}
	return len(content)
}

func indexBashLineEnd(content string, start int) int {
	for i := start; i < len(content); i++ {
		if content[i] == '\n' || content[i] == '\r' {
			return i
		}
	}
	return -1
}

func isBashSpace(c byte) bool { return c == ' ' || c == '\t' }

func isBashNameStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isBashNameContinue(c byte) bool {
	return isBashNameStart(c) || (c >= '0' && c <= '9')
}
