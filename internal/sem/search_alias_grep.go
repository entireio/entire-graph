package sem

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// One Git scan evaluates all alias routes. Its expressions reject substring-only
// hits before Git's per-file line cap, so the provider need not hydrate noise.
func searchGitAliasPatterns(q searchQuery) []string {
	var patterns []string
	for _, term := range searchGitGrepPreselectionPatterns(q) {
		if !q.inferredAbbreviations[term] {
			patterns = append(patterns, searchFoldedLiteral(term))
		}
	}
	for _, alias := range q.terms {
		if !q.inferredAbbreviations[alias] {
			continue
		}
		forms := make([]string, 0, len(searchAliasForms[alias]))
		for form := range searchAliasForms[alias] {
			forms = append(forms, form)
		}
		sort.Strings(forms)
		var alternatives []string
		for _, form := range forms {
			alternatives = append(alternatives, searchAliasBoundaryPatterns(form)...)
		}
		patterns = append(patterns, "("+strings.Join(alternatives, "|")+")")
	}
	return patterns
}

func searchFoldedLiteral(text string) string {
	var out strings.Builder
	for _, r := range text {
		lower, upper := unicode.ToLower(r), unicode.ToUpper(r)
		if lower != upper {
			out.WriteRune('[')
			out.WriteRune(lower)
			out.WriteRune(upper)
			out.WriteRune(']')
		} else {
			out.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return out.String()
}

// Express the same word/camel-case boundaries as searchTokenVariants in POSIX
// ERE. Boundary characters are consumed; callers request whole lines, so this
// does not hide adjacent aliases on a matching line.
func searchAliasBoundaryPatterns(form string) []string {
	chars := []rune(form)
	var out []string
	for left := 0; left < 3; left++ {
		for right := 0; right < 3; right++ {
			forced := make([]rune, len(chars))
			force := func(index int, r rune) bool {
				if forced[index] != 0 && forced[index] != r {
					return false
				}
				forced[index] = r
				return true
			}
			prefix := "(^|[^[:alnum:]])"
			switch left {
			case 1:
				if !unicode.IsLetter(chars[0]) || !force(0, unicode.ToUpper(chars[0])) {
					continue
				}
				prefix = "[[:lower:][:digit:]]"
			case 2:
				if len(chars) < 2 || !unicode.IsLetter(chars[0]) || !unicode.IsLetter(chars[1]) || !force(0, unicode.ToUpper(chars[0])) || !force(1, unicode.ToLower(chars[1])) {
					continue
				}
				prefix = "[[:upper:]]"
			}
			last := len(chars) - 1
			suffix := "($|[^[:alnum:]])"
			switch right {
			case 1:
				if unicode.IsLetter(chars[last]) && !force(last, unicode.ToLower(chars[last])) {
					continue
				}
				suffix = "[[:upper:]]"
			case 2:
				if !unicode.IsLetter(chars[last]) || !force(last, unicode.ToUpper(chars[last])) {
					continue
				}
				suffix = "[[:upper:]][[:lower:]]"
			}
			var word strings.Builder
			for index, r := range chars {
				if forced[index] != 0 {
					word.WriteString(regexp.QuoteMeta(string(forced[index])))
				} else {
					word.WriteString(searchFoldedLiteral(string(r)))
				}
			}
			out = append(out, prefix+word.String()+suffix)
		}
	}
	return out
}
