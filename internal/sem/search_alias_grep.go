package sem

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Bound the generated expression payload and Git regex compilation work.
// Oversized expansions use the existing content-scanning fallback.
const maxSearchGitAliasPatternBytes = 128 * 1024

// Expressions reject substring-only hits before the provider reads content.
func searchGitAliasPatterns(q searchQuery) []string {
	return searchGitAliasPatternsForTerms(q, searchGitAliasTerms(q))
}

func searchGitAliasTerms(q searchQuery) []string {
	terms := searchGitGrepPreselectionPatterns(q)
	seen := map[string]bool{}
	for _, term := range terms {
		seen[term] = true
	}
	for _, term := range q.terms {
		if q.inferredAbbreviations[term] && !seen[term] {
			terms = append(terms, term)
			seen[term] = true
		}
	}
	return terms
}

func searchGitAliasPatternsForTerms(q searchQuery, terms []string) []string {
	if !searchTermsSafeForGitGrep(terms) {
		return nil
	}
	var patterns []string
	for _, term := range terms {
		if !q.inferredAbbreviations[term] {
			patterns = append(patterns, searchFoldedLiteral(term))
		}
	}
	for _, alias := range terms {
		if !q.inferredAbbreviations[alias] {
			continue
		}
		forms := make([]string, 0, len(searchAliasForms[alias]))
		for form, routes := range q.aliasTokens {
			for _, route := range routes {
				if route == alias {
					forms = append(forms, form)
					break
				}
			}
		}
		if len(forms) == 0 {
			continue
		}
		sort.Strings(forms)
		var alternatives []string
		for _, form := range forms {
			alternatives = append(alternatives, searchAliasBoundaryPatterns(form)...)
		}
		patterns = append(patterns, "("+strings.Join(alternatives, "|")+")")
	}
	bytes := 0
	for _, pattern := range patterns {
		if len(pattern) >= 128*1024 {
			return nil
		}
		bytes += len(pattern) + 4 // include the -e argument and terminators
		if bytes > maxSearchGitAliasPatternBytes {
			return nil
		}
	}
	return patterns
}

func searchFoldedLiteral(text string) string {
	initSearchASCIILowerAlternatives()
	var out strings.Builder
	for _, r := range text {
		lower, upper := unicode.ToLower(r), unicode.ToUpper(r)
		literal := regexp.QuoteMeta(string(r))
		if lower != upper {
			literal = "[" + string(lower) + string(upper) + "]"
		}
		if lower < 128 && len(searchASCIILowerAlternatives[lower]) > 0 {
			alternatives := []string{literal}
			for _, alt := range searchASCIILowerAlternatives[lower] {
				alternatives = append(alternatives, regexp.QuoteMeta(string(alt)))
			}
			literal = "(" + strings.Join(alternatives, "|") + ")"
		}
		out.WriteString(literal)
	}
	return out.String()
}

func searchForcedCaseLiteral(r rune) string {
	initSearchASCIILowerAlternatives()
	alternatives := []string{regexp.QuoteMeta(string(r))}
	lower := unicode.ToLower(r)
	if lower < 128 {
		for _, alt := range searchASCIILowerAlternatives[lower] {
			if unicode.IsUpper(r) == unicode.IsUpper(alt) {
				alternatives = append(alternatives, regexp.QuoteMeta(string(alt)))
			}
		}
	}
	if len(alternatives) == 1 {
		return alternatives[0]
	}
	return "(" + strings.Join(alternatives, "|") + ")"
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
					word.WriteString(searchForcedCaseLiteral(forced[index]))
				} else {
					word.WriteString(searchFoldedLiteral(string(r)))
				}
			}
			out = append(out, prefix+word.String()+suffix)
		}
	}
	return out
}
