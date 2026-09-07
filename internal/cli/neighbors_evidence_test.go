package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// neighborsEvidenceFixture builds a two-symbol snapshot with one caller reaching the focus.
// resolution/warningCodes/language are the only knobs, so a test can hold the shape constant
// and vary exactly the evidence-relevant fields the graph already computes.
func neighborsEvidenceFixture(resolution string, warningCodes []string, language string) sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "focus", Name: "Target", QualifiedName: "Target", FilePath: "target.go", StartLine: 1, Language: language},
			{ID: "caller", Name: "Caller", QualifiedName: "Caller", FilePath: "caller.go", StartLine: 1, Language: language},
		},
		Relations: []sem.RelationRecord{
			{FromID: "caller", ToID: "focus", Type: "CALLS", Resolution: resolution, WarningCodes: warningCodes, Confidence: 0.8},
		},
	}
}

// TestNeighborsWeakMatchRequiresVerificationAndNamesAVerificationPath is the "incomplete Graph
// analysis" fixture at the relationship-query layer: a name-only match (the shape reflection, a
// dynamically-built call, or a generated binding leaves behind -- see sem.RelationEvidenceState)
// must come back requires_verification, both per-edge and answer-wide, with a note naming a
// concrete way to check it, and the flag must be visible in the default text rendering.
func TestNeighborsWeakMatchRequiresVerificationAndNamesAVerificationPath(t *testing.T) {
	t.Parallel()
	snapshot := neighborsEvidenceFixture("name_only", nil, "Go")
	response := buildNeighborResponseOnDisk(snapshot, neighborFlags{
		Symbol: "Target", Relation: "CALLS", Direction: "in", Depth: 1, Limit: defaultNeighborLimit,
	})
	if len(response.Matches) != 1 || len(response.Matches[0].Incoming) != 1 {
		t.Fatalf("want one focus with one incoming edge: %#v", response.Matches)
	}
	edge := response.Matches[0].Incoming[0]
	if edge.EvidenceState != sem.EvidenceRequiresVerification {
		t.Fatalf("edge EvidenceState = %q, want %q", edge.EvidenceState, sem.EvidenceRequiresVerification)
	}
	if response.EvidenceState != sem.EvidenceRequiresVerification {
		t.Fatalf("response EvidenceState = %q, want %q", response.EvidenceState, sem.EvidenceRequiresVerification)
	}
	if response.EvidenceNote == "" {
		t.Fatal("EvidenceNote is empty; a requires_verification answer must name a verification path")
	}
	for _, want := range []string{"NOT proof of absence", "tests"} {
		if !strings.Contains(response.EvidenceNote, want) {
			t.Fatalf("EvidenceNote = %q, want it to mention %q", response.EvidenceNote, want)
		}
	}

	var out bytes.Buffer
	if err := writeAgentNeighbors(&out, response); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Evidence: requires_verification") {
		t.Fatalf("text does not expose the incompleteness to the reader:\n%s", text)
	}
	if !strings.Contains(text, "[name_only, requires_verification]") {
		t.Fatalf("edge annotation missing the requires_verification flag:\n%s", text)
	}
}

// TestNeighborsInventoryOnlyLanguageRequiresVerification covers the OTHER shape of "the graph
// cannot resolve relationships": a language it only inventories has no relation extraction at
// all, so even a resolved-looking edge (there isn't one here -- zero relations) must not read as
// a confirmed "nothing calls this".
func TestNeighborsInventoryOnlyLanguageRequiresVerification(t *testing.T) {
	t.Parallel()
	snapshot := sem.ProviderSnapshot{
		Symbols: []sem.SymbolRecord{
			{ID: "focus", Name: "Target", QualifiedName: "Target", FilePath: "target.forth", StartLine: 1, Language: "Forth"},
		},
	}
	response := buildNeighborResponseOnDisk(snapshot, neighborFlags{
		Symbol: "Target", Relation: "CALLS", Direction: "both", Depth: 1, Limit: defaultNeighborLimit,
	})
	if response.EvidenceState != sem.EvidenceRequiresVerification {
		t.Fatalf("response EvidenceState = %q, want %q for an inventory-only-language focus",
			response.EvidenceState, sem.EvidenceRequiresVerification)
	}
	if response.EvidenceNote == "" {
		t.Fatal("EvidenceNote is empty for an inventory-only-language focus")
	}
}

// TestNeighborsFullyResolvedAnswerIsConfirmedAndTextIsUnchanged is the proof that existing,
// fully-resolved behavior is unchanged: an "exact" single-target match must classify Confirmed,
// both per-edge and answer-wide, and the default text rendering must carry none of the new
// evidence vocabulary.
func TestNeighborsFullyResolvedAnswerIsConfirmedAndTextIsUnchanged(t *testing.T) {
	t.Parallel()
	snapshot := neighborsEvidenceFixture("exact", nil, "Go")
	response := buildNeighborResponseOnDisk(snapshot, neighborFlags{
		Symbol: "Target", Relation: "CALLS", Direction: "in", Depth: 1, Limit: defaultNeighborLimit,
	})
	if len(response.Matches) != 1 || len(response.Matches[0].Incoming) != 1 {
		t.Fatalf("want one focus with one incoming edge: %#v", response.Matches)
	}
	if got := response.Matches[0].Incoming[0].EvidenceState; got != sem.EvidenceConfirmed {
		t.Fatalf("edge EvidenceState = %q, want %q", got, sem.EvidenceConfirmed)
	}
	if response.EvidenceState != sem.EvidenceConfirmed {
		t.Fatalf("response EvidenceState = %q, want %q", response.EvidenceState, sem.EvidenceConfirmed)
	}
	if response.EvidenceNote != "" {
		t.Fatalf("EvidenceNote = %q, want empty for a Confirmed answer", response.EvidenceNote)
	}

	var out bytes.Buffer
	if err := writeAgentNeighbors(&out, response); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, mustNotAppear := range []string{"requires_verification", "Evidence:"} {
		if strings.Contains(text, mustNotAppear) {
			t.Fatalf("a fully-resolved answer's text must carry no new content (%q found):\n%s", mustNotAppear, text)
		}
	}
}
