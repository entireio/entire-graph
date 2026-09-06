package gate

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestReviewOrderRanksByDependentsNotByCoverageProduct(t *testing.T) {
	entities := []ChangedEntity{
		{Anchor: Anchor{Name: "Small"}, Dependents: 1, Coverage: Unchecked},
		{Anchor: Anchor{Name: "Large"}, Dependents: 9, Coverage: Verified},
	}

	// A product of dependents and uncheckedness zeroes every verified entity,
	// which would sort a 9-dependent change below a 1-dependent one. Dependents
	// are the risk; coverage only breaks ties.
	got := ReviewOrder(entities)
	if got[0].Name != "Large" {
		t.Fatalf("first is %q, want Large: dependents must outrank coverage", got[0].Name)
	}
}

func TestReviewOrderBreaksTiesWithUncheckedFirst(t *testing.T) {
	entities := []ChangedEntity{
		{Anchor: Anchor{Name: "Covered", Path: "a.go"}, Dependents: 5, Coverage: Verified},
		{Anchor: Anchor{Name: "Bare", Path: "b.go"}, Dependents: 5, Coverage: Unchecked},
	}

	if got := ReviewOrder(entities); got[0].Name != "Bare" {
		t.Fatalf("first is %q, want Bare: equal dependents should surface the unchecked one", got[0].Name)
	}
}

func TestReviewOrderDoesNotMutateItsInput(t *testing.T) {
	// The report holds one entity slice that several renderers read. Sorting it
	// in place would make the JSON and text outputs disagree depending on which
	// ran first.
	entities := []ChangedEntity{
		{Anchor: Anchor{Name: "First"}, Dependents: 1},
		{Anchor: Anchor{Name: "Second"}, Dependents: 9},
	}
	before := append([]ChangedEntity(nil), entities...)

	ReviewOrder(entities)

	if !reflect.DeepEqual(entities, before) {
		t.Fatal("ReviewOrder reordered the caller's slice")
	}
}

func TestWriteTextLeadsWithTheVerdictAndPrintsTheRule(t *testing.T) {
	report := Report{
		Base: "aaaaaaaaaaaaaaaa", Head: "bbbbbbbbbbbbbbbb",
		Entities: []ChangedEntity{
			{Anchor: Anchor{Name: "VerifyToken", Path: "pkg/auth.go", Line: 88},
				ChangeType: SignatureChanged, Dependents: 14, Coverage: Unchecked},
		},
		Available: allAvailable(),
		Verdict:   Revert,
	}

	var out bytes.Buffer
	WriteText(&out, report, false)
	text := out.String()

	if !strings.HasPrefix(text, "VERDICT  revert") {
		t.Fatalf("report does not lead with the verdict:\n%s", text)
	}
	// A verdict a reader cannot reconstruct is a black box, so the rule that
	// produced it ships with every run.
	if !strings.Contains(text, "RULE") || !strings.Contains(text, "DEGRADATION") {
		t.Fatalf("report omits the rule it applied:\n%s", text)
	}
	if !strings.Contains(text, "pkg/auth.go:88") {
		t.Fatalf("finding is not anchored to source:\n%s", text)
	}
	if !strings.Contains(text, "1 unchecked") {
		t.Fatalf("unchecked count is not reported beside verified:\n%s", text)
	}
}

func TestWriteTextAccountsForEntitiesItDoesNotList(t *testing.T) {
	var entities []ChangedEntity
	for i := 0; i < reviewOrderLimit+3; i++ {
		entities = append(entities, ChangedEntity{
			Anchor: Anchor{Name: "e", Path: "a.go"}, Coverage: Verified,
		})
	}

	var out bytes.Buffer
	WriteText(&out, Report{Entities: entities, Available: allAvailable(), Verdict: Keep}, false)

	// Truncating without saying why is how a reviewer stops trusting a tool.
	// Gate is not hiding the remainder, it is accounting for it.
	if !strings.Contains(out.String(), "The remaining 3 entities") {
		t.Fatalf("elided entities are not accounted for:\n%s", out.String())
	}

	var all bytes.Buffer
	WriteText(&all, Report{Entities: entities, Available: allAvailable(), Verdict: Keep}, true)
	if strings.Contains(all.String(), "The remaining") {
		t.Fatal("--all still elided entities")
	}
}

func TestWriteTextExplainsACappedVerdict(t *testing.T) {
	var out bytes.Buffer
	WriteText(&out, Report{
		Entities:  []ChangedEntity{{Anchor: Anchor{Name: "x"}, Coverage: NoResolver}},
		Available: Availability{Risk: true},
		Verdict:   Continue,
	}, false)

	// Without this line a capped verdict reads as a clean-ish result, and the
	// reader never learns a dimension was missing.
	if !strings.Contains(out.String(), "coverage did not run") {
		t.Fatalf("a capped verdict did not explain itself:\n%s", out.String())
	}
}
