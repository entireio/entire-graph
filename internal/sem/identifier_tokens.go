package sem

// forEachIdentifierToken calls visit once for each maximal run of
// [A-Za-z0-9_$] in s, in order.
//
// It replaces a regexp.FindAllStringIndex over the same class, which allocated
// a two-int slice for every identifier in every file it looked at, only to
// answer a membership question about each token. On a four-file diff of a
// 1,589-file repository that was 252 MiB, a third of everything the command
// allocated, and 0.8 of its 3.2 seconds.
//
// The class is pure ASCII, so scanning bytes agrees with the regex on every
// input: a multi-byte rune is not in the class, and each of its bytes is >= 0x80
// and so is not in the class either, which ends a token in both readings.
// TestForEachIdentifierTokenMatchesTheRegex holds the two together.
func forEachIdentifierToken(s string, visit func(token string)) {
	start := -1
	for i := 0; i < len(s); i++ {
		if isIdentifierTokenByte(s[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			visit(s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		visit(s[start:])
	}
}

// isIdentifierTokenByte is the class the reference scan tokenizes on. It is not
// isIdentifierByte: that one is Go's own identifier class and excludes `$`,
// which this scan needs so a PHP variable or a JavaScript identifier is one
// token rather than two.
func isIdentifierTokenByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return c == '_' || c == '$'
	}
}
