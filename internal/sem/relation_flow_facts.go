package sem

import (
	"regexp"
	"strings"
)

// Flow extraction shares the identifier-boundary index built for the other
// relation facts. The old matchers run only at compatible candidate locations;
// captures and non-overlap ordering remain those of the reference regexes.
// In particular, a legacy assignment match may start at the END of the previous
// word and consume whitespace. Keep those boundaries and the preceding byte:
// slicing at a boundary would otherwise change the meaning of \b.
type relationFlowPattern struct {
	start, after *regexp.Regexp
}

var relationFlowPatterns = func() map[*regexp.Regexp]relationFlowPattern {
	out := map[*regexp.Regexp]relationFlowPattern{}
	for _, re := range []*regexp.Regexp{
		returnCallRe, returnVarRe, returnPropertyRe, jsFallbackReturnRe,
		assignCallRe, jsFallbackAssignRe, aliasAssignRe,
		destructuredAssignCallRe, destructuredParamAliasRe,
		parameterPropertyAliasForwardingFlowsAssignmentRe,
		localObjectVarRe, objectLiteralVarRe, objectFieldAssignRe,
		localCollectionVarRe, collectionLiteralVarRe,
		collectionAddRe, collectionSetRe, collectionCallbackRe,
		branchAssignedReturnFlowsBranchRe, flowCallSiteRe,
	} {
		out[re] = relationFlowPattern{
			start: regexp.MustCompile(`\A(?:` + re.String() + `)`),
			after: regexp.MustCompile(`\A(?s:.)(?:` + re.String() + `)`),
		}
	}
	return out
}()

func (facts *relationFacts) collectFlows(text string, boundaries []int) {
	next := map[*regexp.Regexp]int{}
	matchAt := func(re *regexp.Regexp, start int) {
		if start < next[re] {
			return
		}
		pattern := relationFlowPatterns[re]
		offset := start
		var match []int
		if start == 0 {
			match = pattern.start.FindStringSubmatchIndex(text)
		} else {
			offset--
			match = pattern.after.FindStringSubmatchIndex(text[offset:])
			if match != nil {
				match[0] = 1 // Exclude the byte retained only for boundary context.
			}
		}
		if match == nil {
			return
		}
		for i := range match {
			if match[i] >= 0 {
				match[i] += offset
			}
		}
		facts.matches[re] = append(facts.matches[re], match)
		next[re] = match[1]
	}
	for _, start := range boundaries {
		at := relationSkipSpace(text, start)
		if at == len(text) {
			continue
		}
		if text[at] == '[' || text[at] == '(' {
			matchAt(destructuredAssignCallRe, start)
			continue
		}
		if !relationWordStart(text[at]) {
			continue
		}
		end := relationWordEnd(text, at)
		word := text[at:end]
		after := relationSkipSpace(text, end)
		if word == "return" {
			matchAt(returnCallRe, start)
			matchAt(returnVarRe, start)
			matchAt(returnPropertyRe, start)
			matchAt(jsFallbackReturnRe, start)
		}
		if word == "if" {
			matchAt(branchAssignedReturnFlowsBranchRe, start)
		}
		if after == len(text) {
			continue
		}
		// The optional declaration prefix in the reference pattern has no
		// trailing word boundary, so also retain matches such as "varname".
		declaration := strings.HasPrefix(word, "const") || strings.HasPrefix(word, "let") || strings.HasPrefix(word, "var")
		if declaration || text[after] == '=' || text[after] == ':' {
			matchAt(assignCallRe, start)
			matchAt(jsFallbackAssignRe, start)
			matchAt(aliasAssignRe, start)
			matchAt(parameterPropertyAliasForwardingFlowsAssignmentRe, start)
			matchAt(localObjectVarRe, start)
			matchAt(objectLiteralVarRe, start)
			matchAt(localCollectionVarRe, start)
			matchAt(collectionLiteralVarRe, start)
		}
		if declaration || text[after] == ',' {
			matchAt(destructuredAssignCallRe, start)
			matchAt(destructuredParamAliasRe, start)
		}
		if text[after] == '.' {
			matchAt(objectFieldAssignRe, start)
			matchAt(collectionAddRe, start)
			matchAt(collectionSetRe, start)
			matchAt(collectionCallbackRe, start)
		}
		if text[after] == '(' {
			matchAt(flowCallSiteRe, start)
		}
	}
	facts.flowCalls = flowCallsFromMatches(text, facts.matches[flowCallSiteRe])
}

// Argument splitting is shared as well as call matching: eight whole-body forwarding
// extractors previously searched and split the same call sites independently.
type flowCallFact struct {
	name string
	args []string
}

func flowCallsFromMatches(text string, matches [][]int) []flowCallFact {
	var calls []flowCallFact
	for _, match := range matches {
		calls = append(calls, flowCallFact{
			name: strings.TrimPrefix(text[match[2]:match[3]], "$"),
			args: splitSimpleArguments(text[match[4]:match[5]]),
		})
	}
	return calls
}

func (body symbolBody) forwardingCallFacts() []flowCallFact {
	if body.facts != nil {
		return body.facts.flowCalls
	}
	return flowCallsFromMatches(body.stripped, flowCallSiteRe.FindAllStringSubmatchIndex(body.stripped, -1))
}
