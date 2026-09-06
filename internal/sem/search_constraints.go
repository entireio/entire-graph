package sem

import (
	"strings"
	"unicode"
)

const (
	maxSearchEntities       = 8
	maxSearchPhrases        = 16
	maxSearchAgreementBonus = 4.0
)

type searchEntityConstraint struct {
	Raw        string
	Normalized string
}

type searchPhraseConstraint struct {
	Words    []string
	Explicit bool
}

type searchConstraints struct {
	Entities  []searchEntityConstraint
	Phrases   []searchPhraseConstraint
	Truncated bool
}

type searchConstraintSurface struct {
	words       []string
	wholeTokens map[string]bool
}

var searchConstraintInterrogatives = map[string]bool{
	"what": true, "when": true, "where": true, "who": true, "why": true, "how": true,
}

// parseSearchConstraints retains only a bounded, generic description of the
// caller's explicit entities and ordered content. It never decides admission:
// agreement is an additive score only after ordinary retrieval has found a
// candidate.
func parseSearchConstraints(raw string) searchConstraints {
	explicit, remaining := searchExplicitSearchPhrases(raw)
	constraints := searchConstraints{}
	shouted := searchQueryIsShouted(searchWordPattern.FindAllString(raw, -1))
	seenPhrases := map[string]bool{}
	for _, phrase := range explicit {
		searchAppendPhraseConstraint(&constraints, seenPhrases, phrase)
	}

	content := make([]string, 0, 16)
	seenEntities := map[string]bool{}
	firstInSentence := true
	last := 0
	for _, match := range searchWordPattern.FindAllStringIndex(remaining, -1) {
		if strings.ContainsAny(remaining[last:match[0]], ".!?") {
			firstInSentence = true
		}
		rawToken := remaining[match[0]:match[1]]
		normalized := strings.ToLower(rawToken)
		if normalized != "" {
			if !shouted && !firstInSentence && !searchStopWords[normalized] && !searchConstraintInterrogatives[normalized] &&
				(identifierShapedToken(rawToken) || searchConstraintTitleOrAllCaps(rawToken)) && !seenEntities[normalized] {
				if len(constraints.Entities) < maxSearchEntities {
					constraints.Entities = append(constraints.Entities, searchEntityConstraint{Raw: rawToken, Normalized: normalized})
					seenEntities[normalized] = true
				} else {
					constraints.Truncated = true
				}
			}
			for _, word := range searchQueryWordSequence(normalized) {
				if !searchStopWords[word] {
					content = append(content, word)
				}
			}
		}
		firstInSentence = false
		last = match[1]
	}

	for width := 3; width >= 2; width-- {
		for start := 0; start+width <= len(content); start++ {
			words := append([]string(nil), content[start:start+width]...)
			searchAppendPhraseConstraint(&constraints, seenPhrases, searchPhraseConstraint{Words: words})
		}
	}
	return constraints
}

func searchExplicitSearchPhrases(raw string) ([]searchPhraseConstraint, string) {
	var phrases []searchPhraseConstraint
	masked := []rune(raw)
	start := -1
	escaped := false
	for index, r := range masked {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r != '"' {
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		words := searchQueryWordSequence(strings.ToLower(string(masked[start+1 : index])))
		if len(words) > 0 {
			phrases = append(phrases, searchPhraseConstraint{Words: words, Explicit: true})
		}
		for clear := start; clear <= index; clear++ {
			masked[clear] = ' '
		}
		start = -1
	}
	return phrases, string(masked)
}

func searchAppendPhraseConstraint(constraints *searchConstraints, seen map[string]bool, phrase searchPhraseConstraint) {
	if len(phrase.Words) == 0 {
		return
	}
	key := strings.Join(phrase.Words, "\x00")
	if phrase.Explicit {
		key = "quoted\x00" + key
	}
	if seen[key] {
		return
	}
	seen[key] = true
	if len(constraints.Phrases) >= maxSearchPhrases {
		constraints.Truncated = true
		return
	}
	constraints.Phrases = append(constraints.Phrases, phrase)
}

func searchConstraintTitleOrAllCaps(raw string) bool {
	letters := 0
	hasUpper := false
	for _, r := range raw {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			hasUpper = true
		}
	}
	if letters == 0 || !hasUpper {
		return false
	}
	first, _ := utf8FirstRune(raw)
	return unicode.IsUpper(first)
}

func utf8FirstRune(value string) (rune, bool) {
	for _, r := range value {
		return r, true
	}
	return 0, false
}

// orderedSearchPhraseMatch is the strict predicate used for quoted spans and
// direct callers: every normalized word must be adjacent and in written order.
func orderedSearchPhraseMatch(text string, phrase searchPhraseConstraint) bool {
	return searchOrderedPhraseMatch(searchQueryWordSequence(strings.ToLower(text)), phrase.Words, 0, false)
}

func searchOrderedPhraseMatch(words, phrase []string, maxInterveningContent int, looseMorphology bool) bool {
	if len(phrase) == 0 {
		return false
	}
	equal := func(left, right string) bool { return left == right }
	if looseMorphology {
		equal = searchConstraintWordsLooselyEqual
	}
	for start := range words {
		if !equal(words[start], phrase[0]) {
			continue
		}
		at := start + 1
		matched := true
		for want := 1; want < len(phrase); want++ {
			intervening := 0
			found := false
			for at < len(words) {
				if equal(words[at], phrase[want]) {
					found = true
					at++
					break
				}
				if !searchStopWords[words[at]] {
					intervening++
				}
				if intervening > maxInterveningContent {
					break
				}
				at++
			}
			if !found {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func searchConstraintWordsLooselyEqual(got, want string) bool {
	if got == want {
		return true
	}
	for _, suffix := range []string{"ed", "ing", "s"} {
		if strings.HasSuffix(got, suffix) && len(got) > len(suffix)+2 && strings.TrimSuffix(got, suffix) == want ||
			strings.HasSuffix(want, suffix) && len(want) > len(suffix)+2 && strings.TrimSuffix(want, suffix) == got {
			return true
		}
	}
	return false
}

func searchConstraintsRequireAgreement(constraints searchConstraints) bool {
	if len(constraints.Entities) > 0 {
		return true
	}
	for _, phrase := range constraints.Phrases {
		if phrase.Explicit {
			return true
		}
	}
	return false
}

func buildSearchConstraintSurface(parts ...string) searchConstraintSurface {
	surface := searchConstraintSurface{
		wholeTokens: map[string]bool{},
	}
	word := make([]rune, 0, 16)
	raw := make([]byte, 0, 16)
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		token := string(word)
		surface.words = append(surface.words, token)
		surface.wholeTokens[token] = true
		word = word[:0]
	}
	flushRaw := func() {
		if len(raw) == 0 {
			return
		}
		surface.wholeTokens[string(raw)] = true
		raw = raw[:0]
	}
	for _, part := range parts {
		for _, character := range part {
			if unicode.IsLetter(character) || unicode.IsDigit(character) {
				word = append(word, unicode.ToLower(character))
			} else {
				flushWord()
			}

			// Go regexp POSIX character classes are ASCII. This is the exact
			// character set of searchWordPattern, accumulated during the same
			// scan as the Unicode word sequence above.
			if character >= 'A' && character <= 'Z' {
				raw = append(raw, byte(character-'A'+'a'))
			} else if character >= 'a' && character <= 'z' ||
				character >= '0' && character <= '9' ||
				strings.ContainsRune("_./:+#-", character) {
				raw = append(raw, byte(character))
			} else {
				flushRaw()
			}
		}
		// The old implementation joined parts with a newline, which terminates
		// both token classes between metadata fields.
		flushWord()
		flushRaw()
	}
	return surface
}

func searchCandidateAgreementSurface(surface searchConstraintSurface, constraints searchConstraints) (float64, []string) {
	if !searchConstraintsRequireAgreement(constraints) {
		return 0, nil
	}
	matchedEntities := 0
	for _, entity := range constraints.Entities {
		if surface.wholeTokens[entity.Normalized] {
			matchedEntities++
		}
	}
	matchedPhrases := 0
	eligiblePhrases := 0
	for _, phrase := range constraints.Phrases {
		if len(constraints.Entities) == 0 && !phrase.Explicit {
			continue
		}
		eligiblePhrases++
		maxInterveningContent := 2
		looseMorphology := true
		if phrase.Explicit {
			maxInterveningContent = 0
			looseMorphology = false
		}
		if searchOrderedPhraseMatch(surface.words, phrase.Words, maxInterveningContent, looseMorphology) {
			matchedPhrases++
		}
	}
	if matchedEntities == 0 && matchedPhrases == 0 {
		return 0, nil
	}
	bonus := 0.0
	signals := make([]string, 0, 3)
	if matchedEntities > 0 {
		bonus += 1.5 * float64(matchedEntities) / float64(len(constraints.Entities))
		signals = append(signals, "constraint:entity")
	}
	if matchedPhrases > 0 {
		bonus += 1.5 * float64(matchedPhrases) / float64(eligiblePhrases)
		signals = append(signals, "constraint:phrase")
	}
	if matchedEntities > 0 && matchedPhrases > 0 {
		bonus += 1.0
		signals = append(signals, "constraint:relationship")
	}
	return minFloat64(maxSearchAgreementBonus, bonus), signals
}

func buildSearchCandidateConstraintSurface(candidate searchCandidate) searchConstraintSurface {
	return buildSearchConstraintSurface(
		candidate.result.SymbolName,
		candidate.result.QualifiedName,
		candidate.result.Signature,
		candidate.result.Snippet,
		strings.Join(candidate.aliases, "\n"),
	)
}

func searchCandidateConstraintMayMatch(candidate searchCandidate, constraints searchConstraints) bool {
	text := strings.ToLower(strings.Join([]string{
		candidate.result.SymbolName,
		candidate.result.QualifiedName,
		candidate.result.Signature,
		candidate.result.Snippet,
		strings.Join(candidate.aliases, "\n"),
	}, "\n"))
	for _, entity := range constraints.Entities {
		if strings.Contains(text, entity.Normalized) {
			return true
		}
	}
	for _, phrase := range constraints.Phrases {
		if len(phrase.Words) == 0 || len(constraints.Entities) == 0 && !phrase.Explicit {
			continue
		}
		first := phrase.Words[0]
		if strings.Contains(text, first) {
			return true
		}
		if phrase.Explicit {
			continue
		}
		for _, suffix := range []string{"ed", "ing", "s"} {
			if strings.HasSuffix(first, suffix) && len(first) > len(suffix)+2 &&
				strings.Contains(text, strings.TrimSuffix(first, suffix)) {
				return true
			}
		}
	}
	return false
}

func searchCandidateAgreementCached(candidate *searchCandidate, constraints searchConstraints) (float64, []string) {
	if !searchConstraintsRequireAgreement(constraints) {
		return 0, nil
	}
	if candidate.constraintSurface == nil {
		if !searchCandidateConstraintMayMatch(*candidate, constraints) {
			return 0, nil
		}
		surface := buildSearchCandidateConstraintSurface(*candidate)
		candidate.constraintSurface = &surface
	}
	return searchCandidateAgreementSurface(*candidate.constraintSurface, constraints)
}

func searchCandidateAgreement(candidate searchCandidate, constraints searchConstraints) (float64, []string) {
	return searchCandidateAgreementCached(&candidate, constraints)
}

func searchConstraintWholeTokens(text string, words []string) map[string]bool {
	tokens := make(map[string]bool, len(words))
	for _, word := range words {
		tokens[word] = true
	}
	for _, raw := range searchWordPattern.FindAllString(text, -1) {
		tokens[strings.ToLower(raw)] = true
	}
	return tokens
}

func applySearchCandidateAgreement(candidates []searchCandidate, constraints searchConstraints) {
	for index := range candidates {
		bonus, signals := searchCandidateAgreementCached(&candidates[index], constraints)
		if bonus == 0 {
			continue
		}
		candidates[index].score += bonus
		candidates[index].result.Signals = appendUnique(candidates[index].result.Signals, signals...)
	}
}
