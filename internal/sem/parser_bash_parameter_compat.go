package sem

// maskBashParameterReplacementPunctuation handles three literal punctuation
// shapes inside a complete, double-quoted ${NAME:-word} or ${NAME:+word}
// expansion that the bundled Bash grammar rejects. It keeps the parameter
// expansion, nested expansions, command substitutions, and backticks intact;
// only the parse view's rejected punctuation bytes change.
func maskBashParameterReplacementPunctuation(content string) string {
	masked := []byte(content)
	changed := false
	inSingle, inDouble := false, false
	for index := 0; index < len(content); index++ {
		switch {
		case content[index] == '\n' || content[index] == '\r':
			// The observed class is single-line and intentionally stays so. An
			// unterminated quote is reset rather than influencing another line.
			inSingle, inDouble = false, false
		case inSingle:
			if content[index] == '\'' {
				inSingle = false
			}
		case inDouble:
			if content[index] == '\\' {
				index++
				continue
			}
			if content[index] == '"' {
				inDouble = false
				continue
			}
			if content[index] != '$' || index+1 >= len(content) || content[index+1] != '{' {
				continue
			}
			end, replacements, ok := bashParameterReplacementPunctuation(content, index)
			if !ok {
				if end > index {
					index = end
				} else if end < 0 {
					// A recognized replacement expansion was incomplete. Skip the
					// rest of this quoted segment so a nested fragment cannot be
					// modified independently of its malformed parent.
					for index+1 < len(content) && content[index+1] != '\n' && content[index+1] != '\r' {
						index++
					}
				}
				continue
			}
			for position, replacement := range replacements {
				masked[position] = replacement
				changed = true
			}
			// Continue within the same quoted segment. Nested constructs were
			// validated by the helper and are preserved byte-for-byte.
			index = end
		case content[index] == '#' && bashParameterCommentStart(content, index):
			if end := indexBashLineEnd(content, index); end >= 0 {
				index = end - 1
			} else {
				index = len(content)
			}
		case content[index] == '\'':
			inSingle = true
		case content[index] == '"':
			inDouble = true
		}
	}
	if !changed {
		return content
	}
	return string(masked)
}

func bashParameterReplacementPunctuation(content string, start int) (int, map[int]byte, bool) {
	if start+3 >= len(content) || content[start] != '$' || content[start+1] != '{' {
		return 0, nil, false
	}
	name := start + 2
	if !isBashNameStart(content[name]) {
		return 0, nil, false
	}
	operator := name + 1
	for operator < len(content) && isBashNameContinue(content[operator]) {
		operator++
	}
	if operator+2 > len(content) || content[operator] != ':' || (content[operator+1] != '-' && content[operator+1] != '+') {
		return 0, nil, false
	}
	valueStart := operator + 2
	depth := 1
	sawNested := false
	replacements := map[int]byte{}
	for index := valueStart; index < len(content); index++ {
		switch {
		case content[index] == '\n' || content[index] == '\r' || content[index] == '"':
			return -1, nil, false
		case content[index] == '\\':
			if index+1 >= len(content) {
				return -1, nil, false
			}
			next := content[index+1]
			if depth == 1 && sawNested && next != '$' && next != '`' && next != '"' && next != '\\' && next != '\n' && next != '\r' && next != '}' {
				replacements[index] = '/'
			}
			index++
		case content[index] == '$' && index+1 < len(content) && content[index+1] == '{':
			depth++
			sawNested = true
			index++
		case content[index] == '$' && index+1 < len(content) && content[index+1] == '(':
			next := skipBashCommandSubstitution(content, index)
			if next <= index || next > len(content) || content[next-1] != ')' || bashParameterContainsNewline(content[index:next]) {
				return -1, nil, false
			}
			index = next - 1
		case content[index] == '`':
			next := bashParameterBacktickEnd(content, index)
			if next < 0 {
				return -1, nil, false
			}
			index = next
		case content[index] == ';' && depth == 1 && sawNested:
			replacements[index] = '_'
		case content[index] == '}':
			depth--
			if depth != 0 {
				continue
			}
			if content[valueStart:index] == `{\}` {
				replacements[valueStart] = 'x'
				replacements[valueStart+1] = 'x'
				replacements[valueStart+2] = 'x'
			}
			return index, replacements, len(replacements) > 0
		}
	}
	return -1, nil, false
}

func bashParameterContainsNewline(content string) bool {
	for index := range len(content) {
		if content[index] == '\n' || content[index] == '\r' {
			return true
		}
	}
	return false
}

func bashParameterBacktickEnd(content string, start int) int {
	for index := start + 1; index < len(content); index++ {
		if content[index] == '\\' {
			index++
			continue
		}
		if content[index] == '`' {
			return index
		}
		if content[index] == '\n' || content[index] == '\r' {
			return -1
		}
	}
	return -1
}

func bashParameterCommentStart(content string, index int) bool {
	if index == 0 {
		return true
	}
	switch content[index-1] {
	case ' ', '\t', '\n', '\r', ';', '|', '&', '(', ')':
		return true
	default:
		return false
	}
}
