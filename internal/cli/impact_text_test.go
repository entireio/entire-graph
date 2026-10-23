package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestImpactTextGroupsBeforeApplyingLimit(t *testing.T) {
	snapshot := impactFixtureSnapshot()
	snapshot.Symbols = append(snapshot.Symbols,
		sem.SymbolRecord{ID: "test1", Name: "TestOrders", FilePath: "app_test.go", StartLine: 2},
		sem.SymbolRecord{ID: "test2", Name: "TestRoute", FilePath: "app_test.go", StartLine: 8},
	)
	snapshot.Relations = append(snapshot.Relations,
		sem.RelationRecord{FromID: "test1", ToID: "orders", Type: "CALLS"},
		sem.RelationRecord{FromID: "test2", ToID: "caller1", Type: "CALLS"},
		sem.RelationRecord{FromID: "orders", ToID: "limitType", Type: "USES_TYPE"},
		sem.RelationRecord{FromID: "orders", ToID: "callee", Type: "DATA_FLOWS"},
	)
	response := buildImpactResponseOnDisk(snapshot, impactFlags{Format: "text", Symbol: "Orders", Depth: 2, Limit: 1})
	var out bytes.Buffer
	writeImpactText(&out, response)
	text := out.String()
	for _, want := range []string{
		"\nService.Orders — method in Service, app.go:10\n\nSummary:\n",
		"- 2 production callers\n- 2 tests\n- 1 internal callee\n- 2 types\n",
		"Tests (2)", "TestOrders (app_test.go:2)",
		"- ... +1 more", "Data flows (1 additional)", "Sink (orders.go:30)",
		"Limit (app.go:1) [-> parameter; -> body use]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "TestRoute (") || strings.Contains(text, "Data flows (2") || !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("limit, flow deduplication or surrounding whitespace incorrect:\n%s", text)
	}
	for _, prefix := range []string{"", "No search results.\n"} {
		if paths := graphOutputPaths(prefix + text); len(paths) == 0 || paths[0] != "app.go" {
			t.Fatalf("stats missed the focus location in the new heading: %v", paths)
		}
	}
	// JSON's capped relation-level representation stays intact.
	jsonResponse := buildImpactResponseOnDisk(snapshot, impactFlags{Format: "json", Symbol: "Orders", Depth: 2, Limit: 1})
	if jsonResponse.Callers.Total != 4 || len(jsonResponse.Callers.Entries) != 1 ||
		jsonResponse.TypeConsumers.Total != 3 || jsonResponse.DataFlows.Total != 2 {
		t.Fatalf("JSON sections changed: %#v", jsonResponse)
	}
}

func TestImpactTextDiagnosticPlacementAndEmptySections(t *testing.T) {
	response := buildImpactResponseOnDisk(impactFixtureSnapshot(), impactFlags{Format: "text", Symbol: "Orders", Depth: 2, Limit: 15})
	response.CoChanges, response.Siblings = impactSection{}, impactSection{}
	response.CompletenessScope = completenessScope{Language: "Go", LanguageFiles: 4,
		InScopeWarnings: []sem.ProviderWarning{{Code: "W_DATA_FLOW_EVIDENCE_UNMERGED"}}}
	var out bytes.Buffer
	writeImpactText(&out, response)
	text := out.String()
	if strings.Contains(text, "Co-change coupling (") || strings.Contains(text, "Same-container siblings (") ||
		!strings.Contains(text, "- No co-change coupling found\n- No sibling coupling found") ||
		strings.Index(text, "Diagnostics:") < strings.Index(text, "Summary:") ||
		!strings.Contains(text, "W_DATA_FLOW_EVIDENCE_UNMERGED") {
		t.Fatalf("unexpected empty sections or diagnostic position:\n%s", text)
	}
	response.CompletenessScope.LanguageFailed = 1
	response.CompletenessScope.InScopeFailures = []sem.PartialFailure{{Code: "E_PARSE", FilePath: "broken.go"}}
	response.PartialFailures = response.CompletenessScope.InScopeFailures
	out.Reset()
	writeImpactText(&out, response)
	text = out.String()
	if strings.Index(text, "E_PARSE") < 0 || strings.Index(text, "E_PARSE") > strings.Index(text, "Summary:") {
		t.Fatalf("relevant parse failure must precede the answer:\n%s", text)
	}
}
