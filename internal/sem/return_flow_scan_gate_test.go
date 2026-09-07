package sem

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// legacyExpressionAssignedReturnFlows is the retained pre-gate implementation
// used as a differential oracle for the independently authored fixtures below.
// It intentionally extracts assignment events before looking for a returned
// variable.
func legacyExpressionAssignedReturnFlows(block string) []returnFlowCall {
	events := assignmentFlowEvents(block)
	if len(events) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var flows []returnFlowCall
	for _, match := range returnVarRe.FindAllStringSubmatchIndex(block, -1) {
		if len(match) != 4 || followsReturnedVariable(block, match[1]) {
			continue
		}
		returned := strings.TrimPrefix(block[match[2]:match[3]], "$")
		var last assignmentFlowEvent
		for _, event := range events {
			if event.Var != returned || event.Pos > match[0] || event.Pos < last.Pos {
				continue
			}
			if event.Pos == last.Pos && last.EvidenceKind != "" && event.EvidenceKind == "" {
				continue
			}
			last = event
		}
		if last.EvidenceKind == "" || len(last.Calls) == 0 {
			continue
		}
		for _, name := range last.Calls {
			key := name + "\x00" + last.EvidenceKind + "\x00" + returned
			if seen[key] {
				continue
			}
			seen[key] = true
			flows = append(flows, returnFlowCall{
				Name:         name,
				Reason:       last.Reason,
				EvidenceKind: last.EvidenceKind,
				Detail:       name + " -> " + returned,
				Direction:    "callee_to_caller",
			})
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Name != flows[j].Name {
			return flows[i].Name < flows[j].Name
		}
		return flows[i].Detail < flows[j].Detail
	})
	return flows
}

func TestExpressionAssignedReturnFlowsMatchesLegacyOracle(t *testing.T) {
	conditional := func(name string) returnFlowCall {
		return returnFlowCall{
			Name:         name,
			Reason:       "callee return value assigned through conditional expression and returned by caller",
			EvidenceKind: "conditional_assigned_return_flow",
			Detail:       name + " -> value",
			Direction:    "callee_to_caller",
		}
	}
	fallback := func(name string) returnFlowCall {
		return returnFlowCall{
			Name:         name,
			Reason:       "callee return value assigned through fallback expression and returned by caller",
			EvidenceKind: "fallback_assigned_return_flow",
			Detail:       name + " -> value",
			Direction:    "callee_to_caller",
		}
	}

	tests := []struct {
		name  string
		block string
		want  []returnFlowCall
	}{
		{
			name:  "empty body",
			block: "",
		},
		{
			name:  "assignment without return",
			block: "const value = flag ? primary() : fallback()\n",
		},
		{
			name:  "ineligible direct call return",
			block: "const value = flag ? primary() : fallback()\nreturn finish()\n",
		},
		{
			name:  "ineligible property return with Python-looking fallback",
			block: "value = primary() or fallback()\nreturn value.field\n",
		},
		{
			name:  "ineligible indexed return with TypeScript-looking fallback",
			block: "value = primary() ?? fallback()\nreturn value[0]\n",
		},
		{
			name:  "malformed cross-language text without eligible return",
			block: "let value = flag ? primary() : fallback()\nreturn value[\n",
		},
		{
			name:  "conditional assignment",
			block: "const value = flag ? primary() : fallback()\nreturn value\n",
			want:  []returnFlowCall{conditional("fallback"), conditional("primary")},
		},
		{
			name: "mixed and repeated eligible returns",
			block: "const value = flag ? primary() : fallback()\n" +
				"return finish()\n" +
				"return value\n" +
				"return value.field\n" +
				"return value\n",
			want: []returnFlowCall{conditional("fallback"), conditional("primary")},
		},
		{
			name:  "TypeScript fallback assignment",
			block: "const value = primary() ?? fallback()\nreturn value\n",
			want:  []returnFlowCall{fallback("fallback"), fallback("primary")},
		},
		{
			name:  "Python fallback assignment",
			block: "value = primary() or fallback()\nreturn value\n",
			want:  []returnFlowCall{fallback("fallback"), fallback("primary")},
		},
		{
			name:  "destructured assignment",
			block: "const [value, ignored] = loadPair()\nreturn value\n",
			want: []returnFlowCall{{
				Name:         "loadPair",
				Reason:       "callee return value destructured into local and returned by caller",
				EvidenceKind: "destructured_assigned_return_flow",
				Detail:       "loadPair -> value",
				Direction:    "callee_to_caller",
			}},
		},
		{
			name:  "ordinary assignment overwrites fallback",
			block: "let value = primary() || fallback()\nvalue = finalValue()\nreturn value\n",
		},
		{
			name:  "malformed cross-language text with eligible return",
			block: "value = primary() or fallback()\n<? broken ?>\nreturn value\n",
			want:  []returnFlowCall{fallback("fallback"), fallback("primary")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacy := legacyExpressionAssignedReturnFlows(tt.block)
			if !reflect.DeepEqual(legacy, tt.want) {
				t.Fatalf("legacy oracle = %#v, want %#v", legacy, tt.want)
			}
			got := expressionAssignedReturnFlows(tt.block)
			if !reflect.DeepEqual(got, legacy) {
				t.Fatalf("gated result = %#v, legacy oracle %#v", got, legacy)
			}
		})
	}
}

func TestReturnFlowCallsKeepsDirectAndOverwrittenAssignmentFlows(t *testing.T) {
	tests := []struct {
		name  string
		block string
		want  []returnFlowCall
	}{
		{
			name:  "direct return",
			block: "return finish()\n",
			want: []returnFlowCall{{
				Name:         "finish",
				Reason:       "callee return value flows into caller return value",
				EvidenceKind: "return_flow",
				Detail:       "finish",
				Direction:    "callee_to_caller",
			}},
		},
		{
			name:  "ordinary assignment overwrites fallback",
			block: "let value = primary() || fallback()\nvalue = finalValue()\nreturn value\n",
			want: []returnFlowCall{{
				Name:         "finalValue",
				Reason:       "callee return value assigned to local and returned by caller",
				EvidenceKind: "assigned_return_flow",
				Detail:       "finalValue -> value",
				Direction:    "callee_to_caller",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := returnFlowCalls(newSymbolBody(tt.block), nil)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("returnFlowCalls() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
