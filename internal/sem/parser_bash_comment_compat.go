package sem

// maskBashCommentBacktickArguments masks only standalone backtick words whose
// complete content is a single-line shell comment. Bash accepts these as
// argument annotations, while the bundled grammar treats them as malformed
// command substitutions. Real backtick commands, quoted text, comments,
// multiline forms, and escaped forms remain unchanged.
func maskBashCommentBacktickArguments(content string) string {
	masked := []byte(content)
	inSingle, inDouble := false, false
	for i := 0; i < len(content); {
		switch content[i] {
		case '\\':
			if !inSingle && i+1 < len(content) {
				i += 2
			} else {
				i++
			}
		case '\'', '"':
			if content[i] == '\'' && !inDouble {
				inSingle = !inSingle
				i++
				continue
			}
			if content[i] == '"' && !inSingle {
				inDouble = !inDouble
				i++
				continue
			}
			i++
		case '#':
			if !inSingle && !inDouble && bashCommentBacktickBoundaryBefore(content, i) {
				i = skipBashComment(content, i)
				continue
			}
			i++
		case '$':
			if !inSingle && !inDouble && i+1 < len(content) && content[i+1] == '(' {
				i = skipBashCommandSubstitution(content, i)
				continue
			}
			i++
		case '<':
			if !inSingle && !inDouble && i+1 < len(content) && content[i+1] == '(' {
				i = skipBashCommandSubstitution(content, i+1)
				continue
			}
			i++
		case '`':
			if !inSingle && !inDouble {
				if end := bashCommentBacktickEnd(content, i); end >= 0 &&
					bashCommentBacktickWord(content, i, end) &&
					bashCommentBacktickBoundaryBefore(content, i) &&
					bashCommentBacktickBoundaryAfter(content, end+1) {
					maskBytesPreservingNewlines(masked, i, end+1)
					i = end + 1
					continue
				}
			}
			i++
		default:
			i++
		}
	}
	return string(masked)
}

func bashCommentBacktickEnd(content string, start int) int {
	for i := start + 1; i < len(content); i++ {
		switch content[i] {
		case '\n', '\r':
			return -1
		case '\\':
			// Escaped delimiters have shell semantics that this narrow mask does
			// not interpret; leave the complete word unchanged.
			return -1
		case '`':
			return i
		}
	}
	return -1
}

func bashCommentBacktickWord(content string, start, end int) bool {
	inner := content[start+1 : end]
	for i := 0; i < len(inner) && (inner[i] == ' ' || inner[i] == '\t'); i++ {
		if inner[i] == '#' {
			return true
		}
	}
	trimmed := inner
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	return len(trimmed) > 0 && trimmed[0] == '#'
}

func bashCommentBacktickBoundaryBefore(content string, index int) bool {
	if index <= 0 {
		return true
	}
	switch content[index-1] {
	case ' ', '\t', '\n', '\r', ';', '|', '&', '(', ')', '<', '>':
		return true
	default:
		return false
	}
}

func bashCommentBacktickBoundaryAfter(content string, index int) bool {
	if index >= len(content) {
		return true
	}
	switch content[index] {
	case ' ', '\t', '\n', '\r', ';', '|', '&', '(', ')', '<', '>':
		return true
	default:
		return false
	}
}
