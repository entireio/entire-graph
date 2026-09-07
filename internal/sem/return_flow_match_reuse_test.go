package sem

import (
	"reflect"
	"sort"
	"sync"
	"testing"
)

var forwardingEvidenceKinds = map[string]bool{
	"alias_forward_flow":                    true,
	"argument_forward_flow":                 true,
	"callback_element_forward_flow":         true,
	"collection_element_forward_flow":       true,
	"destructured_alias_forward_flow":       true,
	"literal_argument_forward_flow":         true,
	"object_field_forward_flow":             true,
	"parameter_property_alias_forward_flow": true,
	"parameter_property_forward_flow":       true,
}

// legacyIndependentForwardingFlows deliberately calls the pre-reuse helper
// surfaces independently. It is the differential oracle for the fixtures
// below: each wrapper obtains its own call-site matches, while returnFlowCalls
// passes one invocation-local fact set through the same scanners.
func legacyIndependentForwardingFlows(block string, params map[string]bool) []returnFlowCall {
	var aliases map[string]string
	if len(params) > 0 {
		aliases = parameterAliasMap(block, params)
	}
	groups := [][]returnFlowCall{
		argumentForwardingFlows(block, params),
		parameterPropertyForwardingFlows(block, params),
		parameterPropertyAliasForwardingFlows(block, params),
		aliasForwardingFlows(block, params, aliases),
		destructuredAliasForwardingFlows(block, params),
		objectFieldForwardingFlows(block, params, aliases),
		collectionElementForwardingFlows(block, params, aliases),
		callbackElementForwardingFlows(block, params, aliases),
		directLiteralForwardingFlows(block, params, aliases),
	}
	flows := map[string]returnFlowCall{}
	for _, group := range groups {
		for _, flow := range group {
			flows[flow.Name+"\x00"+flow.EvidenceKind+"\x00"+flow.Detail] = flow
		}
	}
	out := make([]returnFlowCall, 0, len(flows))
	for _, flow := range flows {
		out = append(out, flow)
	}
	sort.Slice(out, func(i, j int) bool { return returnFlowCallLess(out[i], out[j]) })
	return out
}

func forwardingOnly(flows []returnFlowCall) []returnFlowCall {
	out := make([]returnFlowCall, 0, len(flows))
	for _, flow := range flows {
		if forwardingEvidenceKinds[flow.EvidenceKind] {
			out = append(out, flow)
		}
	}
	return out
}

func returnFlowCallLess(a, b returnFlowCall) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.EvidenceKind != b.EvidenceKind {
		return a.EvidenceKind < b.EvidenceKind
	}
	if a.Detail != b.Detail {
		return a.Detail < b.Detail
	}
	if a.Direction != b.Direction {
		return a.Direction < b.Direction
	}
	return a.Reason < b.Reason
}

func TestReturnFlowMatchReuseExactOutputCoversAllForwardingScanners(t *testing.T) {
	block := `function mixed($input, $other) {
  direct($input)
  prop($input.value)
  const valueAlias = $input.value
  propAlias(valueAlias)
  const alias = $input
  aliasCall(alias)
  const { field: destructured } = $input
  destructuredCall(destructured)
  const obj = {}
  obj.field = $input
  objectCall(obj)
  const items = []
  items.push($input)
  collectionCall(items)
  input.map(item => callbackCall(item))
  literalCall({value: $input})
}`
	params := map[string]bool{"input": true, "other": true}
	want := []returnFlowCall{
		{Name: "aliasCall", Reason: "caller parameter alias forwarded into callee argument", EvidenceKind: "alias_forward_flow", Detail: "input -> alias -> aliasCall()", Direction: "caller_to_callee"},
		{Name: "callbackCall", Reason: "caller collection element forwarded into callee argument", EvidenceKind: "callback_element_forward_flow", Detail: "input[] -> item -> callbackCall()", Direction: "caller_to_callee"},
		{Name: "collectionCall", Reason: "caller parameter inserted into collection forwarded to callee argument", EvidenceKind: "collection_element_forward_flow", Detail: "input -> items[] -> collectionCall()", Direction: "caller_to_callee"},
		{Name: "destructuredCall", Reason: "caller parameter destructured alias forwarded into callee argument", EvidenceKind: "destructured_alias_forward_flow", Detail: "input -> destructured -> destructuredCall()", Direction: "caller_to_callee"},
		{Name: "direct", Reason: "caller parameter forwarded into callee argument", EvidenceKind: "argument_forward_flow", Detail: "input -> direct()", Direction: "caller_to_callee"},
		{Name: "literalCall", Reason: "caller parameter forwarded through literal callee argument", EvidenceKind: "literal_argument_forward_flow", Detail: "input -> literal -> literalCall()", Direction: "caller_to_callee"},
		{Name: "mixed", Reason: "caller parameter forwarded into callee argument", EvidenceKind: "argument_forward_flow", Detail: "input -> mixed()", Direction: "caller_to_callee"},
		{Name: "mixed", Reason: "caller parameter forwarded into callee argument", EvidenceKind: "argument_forward_flow", Detail: "other -> mixed()", Direction: "caller_to_callee"},
		{Name: "objectCall", Reason: "caller parameter assigned into object field forwarded to callee argument", EvidenceKind: "object_field_forward_flow", Detail: "input -> obj -> objectCall()", Direction: "caller_to_callee"},
		{Name: "prop", Reason: "caller parameter property forwarded into callee argument", EvidenceKind: "parameter_property_forward_flow", Detail: "input.value -> prop()", Direction: "caller_to_callee"},
		{Name: "propAlias", Reason: "caller parameter property alias forwarded into callee argument", EvidenceKind: "parameter_property_alias_forward_flow", Detail: "input.value -> valueAlias -> propAlias()", Direction: "caller_to_callee"},
		{Name: "push", Reason: "caller parameter forwarded into callee argument", EvidenceKind: "argument_forward_flow", Detail: "input -> push()", Direction: "caller_to_callee"},
	}

	got := returnFlowCalls(newSymbolBody(block), params)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("returnFlowCalls() = %#v, want %#v", got, want)
	}
	if legacy := legacyIndependentForwardingFlows(newSymbolBody(block).stripped, params); !reflect.DeepEqual(got, legacy) {
		t.Fatalf("shared result = %#v, independent scanner oracle %#v", got, legacy)
	}
}

func TestReturnFlowMatchReuseMatchesIndependentScanners(t *testing.T) {
	tests := []struct {
		name   string
		block  string
		params map[string]bool
	}{
		{
			name: "malformed mixed language",
			block: "<?php $alias = $input\nitems := []\nitems.append($input)\nforward(items)\n" +
				"const broken = {value: $input\nreturn unfinished[\n",
			params: map[string]bool{"input": true},
		},
		{
			name:   "UTF-8 surrounds ASCII captures",
			block:  "func résumé(input string) {\n label := \"λ\"\n alias := input\n emit(alias)\n}\n",
			params: map[string]bool{"input": true},
		},
		{
			name:   "repeated callees retain total order",
			block:  "zeta($first)\nalpha($second)\nzeta($second)\nalpha($first)\n",
			params: map[string]bool{"first": true, "second": true},
		},
		{
			name:   "no parameters keeps scanners gated",
			block:  "forward(value)\nreturn finish()\n",
			params: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := newSymbolBody(tt.block)
			want := legacyIndependentForwardingFlows(body.stripped, tt.params)
			got := forwardingOnly(returnFlowCalls(body, tt.params))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("shared result = %#v, independent scanner oracle %#v", got, want)
			}
		})
	}
}

func TestReturnFlowMatchFactsStayLazyAndDoNotMutateReturnMatches(t *testing.T) {
	noEligible := newReturnFlowMatchFacts("value = choose()\nreturn finish()\n")
	if got := expressionAssignedReturnFlowsFromFacts(noEligible); got != nil {
		t.Fatalf("ineligible return produced flows: %#v", got)
	}
	if !noEligible.returnVarsLoaded || noEligible.assignCallsLoaded || noEligible.flowCallSitesLoaded {
		t.Fatalf("no-eligible gate loaded facts: %+v", noEligible)
	}

	block := "return before()\nconst value = flag ? primary() : fallback()\n" +
		"return value\nreturn between()\nreturn value\nreturn value.field\n"
	facts := newReturnFlowMatchFacts(block)
	original := cloneMatchIndexes(facts.returnVarMatches())
	if len(original) != 5 {
		t.Fatalf("raw return matches=%d, want 5", len(original))
	}
	got := expressionAssignedReturnFlowsFromFacts(facts)
	if len(got) != 2 || !facts.assignCallsLoaded {
		t.Fatalf("eligible result=%#v facts=%+v", got, facts)
	}
	if after := facts.returnVarMatches(); !reflect.DeepEqual(after, original) {
		t.Fatalf("shared return matches mutated: got %#v want %#v", after, original)
	}

	callFacts := newReturnFlowMatchFacts("first(input)\nsecond(input)\n")
	if callFacts.flowCallSitesLoaded {
		t.Fatal("call-site facts loaded eagerly")
	}
	first := callFacts.flowCallSites()
	second := callFacts.flowCallSites()
	if !callFacts.flowCallSitesLoaded || len(first) != 2 || len(second) != 2 || &first[0] != &second[0] {
		t.Fatalf("call-site facts were not reused: first=%#v second=%#v", first, second)
	}
}

func cloneMatchIndexes(matches [][]int) [][]int {
	out := make([][]int, len(matches))
	for i, match := range matches {
		out[i] = append([]int(nil), match...)
	}
	return out
}

func TestReturnFlowMatchReuseConcurrentIndependentBodies(t *testing.T) {
	blocks := []struct {
		body   symbolBody
		params map[string]bool
		want   []returnFlowCall
	}{
		{body: newSymbolBody("alias := input\nsend(alias)\n"), params: map[string]bool{"input": true}},
		{body: newSymbolBody("items := []\nitems.append(other)\nflush(items)\n"), params: map[string]bool{"other": true}},
	}
	for i := range blocks {
		blocks[i].want = returnFlowCalls(blocks[i].body, blocks[i].params)
	}

	var wg sync.WaitGroup
	errors := make(chan int, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fixture := blocks[i%len(blocks)]
			if got := returnFlowCalls(fixture.body, fixture.params); !reflect.DeepEqual(got, fixture.want) {
				errors <- i
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for i := range errors {
		t.Errorf("concurrent fixture %d differed from serial output", i)
	}
}
