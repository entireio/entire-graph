package sem

import (
	"regexp"
	"sort"
	"strings"
)

// relationFacts holds immutable matches for one symbol. One lexical pass over
// the already-masked body indexes identifiers, word boundaries and closing
// parentheses. Receiver/field/type and return-flow scanners share the resulting
// matches instead of each searching the whole body. Forwarding extractors also
// share parsed arguments. Resolution results, evidence and ordering stay intact.
//
// An AST-only candidate collector missed legacy matches across implicit Go
// semicolons. Keeping this index lexical preserves those matches deliberately;
// correcting their meaning is a separate semantic change. Each indexed pattern
// must preserve captures, byte offsets and independent non-overlap ordering;
// differential tests compare these against the original regular expressions.
type relationFacts struct {
	matches   map[*regexp.Regexp][][]int
	flowCalls []flowCallFact
	indexed   bool // All relationIndexedPatterns were scanned, including misses.
}

type relationWord struct{ start, end int }

var relationAnchoredPatterns = func() map[*regexp.Regexp]*regexp.Regexp {
	out := map[*regexp.Regexp]*regexp.Regexp{}
	for _, re := range []*regexp.Regexp{
		newCtorMethodCallRe, ctorMethodCallRe, returnedMethodCallRe,
		newCtorMethodChainCallRe, ctorMethodChainCallRe, returnedMethodChainCallRe,
		newCtorMethodDeepChainCallRe, ctorMethodDeepChainCallRe, returnedMethodDeepChainRe,
		newAssignRe, ctorAssignRe, factoryReturnAssignRe, outParamDeclRe, nullableLocalDeclRe,
	} {
		out[re] = regexp.MustCompile("^(?:" + re.String() + ")")
	}
	return out
}()

// Share the negative-match registry too, instead of allocating an entry for
// every indexed pattern in every body. Dollar bodies cache only their explicit
// matches-map entries (including nil); their other patterns still scan normally.
var relationIndexedPatterns = func() map[*regexp.Regexp]bool {
	patterns := map[*regexp.Regexp]bool{receiverCallRe: true, fieldAccessRe: true}
	for re := range relationAnchoredPatterns {
		patterns[re] = true
	}
	for re := range relationFlowPatterns {
		patterns[re] = true
	}
	return patterns
}()

func relationWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func relationWordByte(c byte) bool {
	return relationWordStart(c) || c >= '0' && c <= '9'
}

func relationSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func relationSkipSpace(text string, pos int) int {
	for pos < len(text) && relationSpace(text[pos]) {
		pos++
	}
	return pos
}

func relationWordEnd(text string, pos int) int {
	for pos < len(text) && relationWordByte(text[pos]) {
		pos++
	}
	return pos
}

func collectRelationFacts(text string) *relationFacts {
	// Dollar identifiers occur in JS, PHP, Perl and shell, and in malformed
	// snippets from any language. Their regex word boundaries differ from their
	// identifier boundaries. Retain the original matcher for these bodies, but
	// share repeated matches and argument splitting rather than disabling reuse.
	if strings.Contains(text, "$") {
		facts := &relationFacts{matches: map[*regexp.Regexp][][]int{}}
		for _, re := range []*regexp.Regexp{
			receiverCallRe, fieldAccessRe, newAssignRe, ctorAssignRe,
			factoryReturnAssignRe, outParamDeclRe, nullableLocalDeclRe,
			returnVarRe, assignCallRe, flowCallSiteRe,
		} {
			facts.matches[re] = re.FindAllStringSubmatchIndex(text, -1)
		}
		facts.flowCalls = flowCallsFromMatches(text, facts.matches[flowCallSiteRe])
		return facts
	}
	var words []relationWord
	var closes []int
	var boundaries []int
	for i := 0; i < len(text); {
		if relationWordByte(text[i]) {
			end := relationWordEnd(text, i)
			boundaries = append(boundaries, i, end)
			// Relation patterns without a word boundary can start after digits.
			letter := i
			for letter < end && !relationWordStart(text[letter]) {
				letter++
			}
			if letter < end {
				words = append(words, relationWord{letter, end})
			}
			i = end
			continue
		}
		if text[i] == ')' {
			closes = append(closes, i)
		}
		i++
	}
	facts := &relationFacts{matches: map[*regexp.Regexp][][]int{}, indexed: true}
	next := map[*regexp.Regexp]int{}
	add := func(re *regexp.Regexp, match []int) {
		facts.matches[re] = append(facts.matches[re], match)
		next[re] = match[1]
	}
	matchAt := func(re *regexp.Regexp, start int) {
		if start < next[re] {
			return
		}
		if strings.HasPrefix(re.String(), `\b`) && start > 0 && relationWordByte(text[start-1]) {
			return
		}
		match := relationAnchoredPatterns[re].FindStringSubmatchIndex(text[start:])
		if match == nil {
			return
		}
		for i := range match {
			if match[i] >= 0 {
				match[i] += start
			}
		}
		add(re, match)
	}
	for _, word := range words {
		after := relationSkipSpace(text, word.end)
		if after == len(text) {
			continue
		}
		// One member-expression recognition supplies both call and field
		// extraction. Each retains its own non-overlap cursor, as the original
		// regular expressions do (a.b.c() yields different matches in each).
		member := after
		if text[member] == '.' {
			member++
		} else if strings.HasPrefix(text[member:], "->") {
			member += 2
		} else {
			member = -1
		}
		if member >= 0 {
			member = relationSkipSpace(text, member)
			if member < len(text) && relationWordStart(text[member]) {
				end := relationWordEnd(text, member)
				if word.start >= next[fieldAccessRe] {
					start := word.start
					for start > next[fieldAccessRe] && relationSpace(text[start-1]) {
						start--
					}
					ampEnd := start
					if start > next[fieldAccessRe] && text[start-1] == '&' {
						start--
					} else {
						ampEnd = start
					}
					add(fieldAccessRe, []int{start, end, start, ampEnd, word.start, word.end, member, end})
				}
				open := relationSkipSpace(text, end)
				if open < len(text) && text[open] == '<' {
					// The generic matcher accepts one flat type-argument span,
					// including its existing comparison-expression false positives.
					angle := open + 1
					for angle < len(text) && !strings.ContainsRune(">\n;{}()", rune(text[angle])) {
						angle++
					}
					if angle < len(text) && text[angle] == '>' {
						open = angle + 1
					}
				}
				if open < len(text) && text[open] == '(' && word.start >= next[receiverCallRe] {
					add(receiverCallRe, []int{word.start, open + 1, word.start, word.end, member, end})
				}
			}
		}
		if text[after] == '=' || text[after] == ':' {
			matchAt(newAssignRe, word.start)
			matchAt(ctorAssignRe, word.start)
			matchAt(factoryReturnAssignRe, word.start)
		}
		if text[word.start:word.end] == "out" {
			matchAt(outParamDeclRe, word.start)
		}
		if text[word.start] >= 'A' && text[word.start] <= 'Z' && (text[after] == '?' || text[after] == '<') {
			matchAt(nullableLocalDeclRe, word.start)
		}
		open := after
		isNew := text[word.start:word.end] == "new"
		if isNew && relationWordStart(text[open]) {
			open = relationSkipSpace(text, relationWordEnd(text, open))
		}
		if open >= len(text) || text[open] != '(' {
			continue
		}
		ci := sort.SearchInts(closes, open)
		if ci == len(closes) {
			continue
		}
		tail := relationSkipSpace(text, closes[ci]+1)
		if tail >= len(text) || text[tail] != '.' && !strings.HasPrefix(text[tail:], "->") {
			continue
		}
		if isNew {
			matchAt(newCtorMethodCallRe, word.start)
			matchAt(newCtorMethodChainCallRe, word.start)
			matchAt(newCtorMethodDeepChainCallRe, word.start)
		}
		matchAt(ctorMethodCallRe, word.start)
		matchAt(ctorMethodChainCallRe, word.start)
		matchAt(ctorMethodDeepChainCallRe, word.start)
		matchAt(returnedMethodCallRe, word.start)
		matchAt(returnedMethodChainCallRe, word.start)
		matchAt(returnedMethodDeepChainRe, word.start)
	}
	facts.collectFlows(text, boundaries)
	return facts
}

func (body symbolBody) relationMatchIndices(re *regexp.Regexp) [][]int {
	if body.facts != nil {
		if matches, ok := body.facts.matches[re]; ok || body.facts.indexed && relationIndexedPatterns[re] {
			return matches
		}
	}
	return re.FindAllStringSubmatchIndex(body.stripped, -1)
}

func (body symbolBody) relationMatches(re *regexp.Regexp) [][]string {
	if body.facts == nil {
		return re.FindAllStringSubmatch(body.stripped, -1)
	}
	matches, ok := body.facts.matches[re]
	if !ok && !(body.facts.indexed && relationIndexedPatterns[re]) {
		return re.FindAllStringSubmatch(body.stripped, -1)
	}
	var out [][]string
	for _, match := range matches {
		values := make([]string, len(match)/2)
		for i := range values {
			if match[2*i] >= 0 {
				values[i] = body.stripped[match[2*i]:match[2*i+1]]
			}
		}
		out = append(out, values)
	}
	return out
}
