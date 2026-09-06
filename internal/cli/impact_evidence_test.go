package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// writeGoDispatchTableRepo is the "incomplete Graph analysis" fixture: two handler functions
// invoked only through a map-of-function-values dispatch table (`registry[name]()`), never
// through a direct, syntactically-resolvable call expression. This is a common, realistic
// dynamic-dispatch shape (command tables, event/route handler registries) that static call
// resolution genuinely cannot follow: at analysis time nothing in the source says WHICH
// function `registry[name]` holds, so neither handler ever gets a CALLS edge, even though both
// are very much reachable. Unlike a Go-interface fixture, plain functions carry no receiver-type
// self-reference to accidentally rescue impactDegenerateReason's structural-zero check, so this
// isolates exactly the "the graph found nothing, and that is not the same as nothing being
// there" case the evidence-state work exists to expose.
func writeGoDispatchTableRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, "go.mod", "module example.com/dispatchtable\n\ngo 1.21\n")
	write(t, repo, "handlers/handlers.go", `package handlers

func handleFoo() error { return nil }

func handleBar() error { return nil }

var registry = map[string]func() error{
	"foo": handleFoo,
	"bar": handleBar,
}

func Dispatch(name string) error {
	return registry[name]()
}
`)
	return repo
}

// TestImpactDynamicDispatchThroughFunctionTableRequiresVerification is the fixture/test for
// incomplete Graph analysis the evidence-state work exists to cover: a handler reachable only
// through a runtime dispatch table gets a genuine "0 callers" answer from the graph (dynamic
// dispatch through a map of function values is not something static call resolution can
// follow), and that answer must come back Degenerate AND EvidenceState requires_verification --
// never a confirmed "nothing calls this" -- with a note naming a concrete way to check by hand.
func TestImpactDynamicDispatchThroughFunctionTableRequiresVerification(t *testing.T) {
	t.Parallel()
	repo := writeGoDispatchTableRepo(t)

	snapshot, err := sem.BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatalf("BuildProviderSnapshot: %v", err)
	}

	response := buildImpactResponseOnDisk(snapshot, impactFlags{
		Symbol: "handleFoo", File: "handlers/handlers.go", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	if response.Focus == nil {
		t.Fatalf("no focus resolved for handlers.go handleFoo: %#v", response)
	}
	if response.Callers.Total != 0 {
		t.Fatalf("handleFoo callers = %d, want 0 (only reachable through the dispatch table): %#v",
			response.Callers.Total, response.Callers.Entries)
	}

	reason := impactDegenerateReason(response)
	if reason == "" {
		t.Fatalf("impactDegenerateReason returned \"\"; want a reason for a 0/0/0 structural answer: %#v", response)
	}
	response.Degenerate, response.DegenerateReason = true, reason
	finalizeImpactEvidence(&response)

	if response.EvidenceState != sem.EvidenceRequiresVerification {
		t.Fatalf("EvidenceState = %q, want %q -- a 0-caller answer must not read as a confirmed absence of callers",
			response.EvidenceState, sem.EvidenceRequiresVerification)
	}
	if response.EvidenceNote == "" {
		t.Fatal("EvidenceNote is empty; a requires_verification answer must name a verification path")
	}
	for _, want := range []string{"dynamic dispatch", "NOT proof of absence", "tests"} {
		if !strings.Contains(response.EvidenceNote, want) {
			t.Fatalf("EvidenceNote = %q, want it to mention %q", response.EvidenceNote, want)
		}
	}

	var out bytes.Buffer
	writeImpactDegenerate(&out, response, response.DegenerateReason)
	text := out.String()
	if !strings.Contains(text, "IMPACT DEGENERATE") {
		t.Fatalf("text does not carry the degenerate marker:\n%s", text)
	}
	if !strings.Contains(text, "Evidence: requires_verification") {
		t.Fatalf("text does not expose the incompleteness to the reader:\n%s", text)
	}
}

// writeGoInterfaceFanoutRepo reproduces the shape appendGoInterfaceImplementationCalls
// documents in internal/sem/provider.go: a Go interface, `implementations` concrete types
// satisfying it, and one caller reaching a method through the interface. Go interface
// satisfaction is implicit (no `implements` clause to read), so past
// goInterfaceImplementationFanoutCap (8, see provider.go) the graph deliberately stops fanning
// the call out to every implementation — "a call through the interface is genuinely
// polymorphic, and the interface node alone is the honest answer" — which means a query aimed
// straight at one of the un-fanned-out implementations finds NO caller at all, even though one
// exists in source. That silence is exactly the shape this test exercises: the graph must not
// present it as a confirmed "nothing calls this".
//
// The interface deliberately declares TWO methods, not one: a single-method interface is
// satisfied by any type that happens to own a same-named method, so the graph separately
// refuses to fan it out at all once more than one candidate type exists (see
// TestGoSingleMethodInterfaceDoesNotFanOutOnAmbiguousName in internal/sem). Two required
// methods keeps this fixture on the fan-out-CAP guard this test means to exercise, not that
// unrelated ambiguous-single-method guard.
func writeGoInterfaceFanoutRepo(t *testing.T, implementations int) string {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, "go.mod", "module example.com/dispatch\n\ngo 1.21\n")
	write(t, repo, "iface/iface.go", `package iface

type Runner interface {
	Run() error
	Close() error
}
`)
	for i := 0; i < implementations; i++ {
		name := fmt.Sprintf("t%d", i)
		write(t, repo, "impls/"+name+"/"+name+".go", `package `+name+`

type Runner struct{}

func (r *Runner) Run() error { return nil }

func (r *Runner) Close() error { return nil }
`)
	}
	write(t, repo, "caller/caller.go", `package caller

import "example.com/dispatch/iface"

func dispatch(r iface.Runner) error {
	defer r.Close()
	return r.Run()
}
`)
	return repo
}

// TestImpactCallerWithinFanoutCapIsPartialNotConfirmed complements the dispatch-table test
// above with the OTHER shade of incompleteness: a caller reached WITHIN the interface fan-out
// cap is real, graph-recorded evidence (not a fabrication, and not silently dropped), but it is
// still produced by inference (Go interface implementation fan-out, resolution "type_inferred"
// -- see appendGoInterfaceImplementationCalls in internal/sem/provider.go), so it must classify
// Partial, not Confirmed, and the answer as a whole must not be Degenerate.
func TestImpactCallerWithinFanoutCapIsPartialNotConfirmed(t *testing.T) {
	t.Parallel()
	repo := writeGoInterfaceFanoutRepo(t, 2)

	snapshot, err := sem.BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatalf("BuildProviderSnapshot: %v", err)
	}

	response := buildImpactResponseOnDisk(snapshot, impactFlags{
		Symbol: "Run", File: "impls/t0/t0.go", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	if response.Callers.Total == 0 {
		t.Fatalf("t0.Run callers = 0, want at least the fanned-out interface call: %#v", response)
	}
	if response.Callers.EvidenceState != sem.EvidencePartial {
		t.Fatalf("callers section evidence = %q, want %q (interface fan-out is inferred, not resolved)",
			response.Callers.EvidenceState, sem.EvidencePartial)
	}
	for _, entry := range response.Callers.Entries {
		if entry.EvidenceState != sem.EvidencePartial {
			t.Fatalf("caller entry evidence = %q, want %q: %#v", entry.EvidenceState, sem.EvidencePartial, entry)
		}
	}

	if reason := impactDegenerateReason(response); reason != "" {
		response.Degenerate, response.DegenerateReason = true, reason
	}
	finalizeImpactEvidence(&response)
	if response.Degenerate {
		t.Fatalf("a real, within-cap caller must not be Degenerate: %#v", response)
	}
	// Partial does not, on its own, demand a human check: it is disclosed per-entry/per-section
	// in JSON, but the answer-wide verdict only escalates on requires_verification.
	if response.EvidenceState == sem.EvidenceRequiresVerification {
		t.Fatalf("EvidenceState = %q, want anything but requires_verification for an all-Partial answer", response.EvidenceState)
	}

	var out bytes.Buffer
	writeImpactText(&out, response)
	if strings.Contains(out.String(), "requires_verification") {
		t.Fatalf("Partial-only answer must stay silent about verification:\n%s", out.String())
	}
}

// TestImpactFullyResolvedAnswerIsConfirmedAndTextIsUnchanged is the other half of the
// contract this package's evidence-state work exists to keep: reuses impactFixtureSnapshot
// (impact_test.go), the existing "every section populated" fixture, entirely unmodified --
// its relations carry no Resolution/WarningCodes, which RelationEvidenceState reads as
// Confirmed by design (an unrecognized/absent resolution must not manufacture new doubt; see
// evidence.go). A fully-resolved answer must gain only the new, additive JSON field: no
// existing behavior -- and specifically no new visible text -- may change.
func TestImpactFullyResolvedAnswerIsConfirmedAndTextIsUnchanged(t *testing.T) {
	t.Parallel()
	response := buildImpactResponseOnDisk(impactFixtureSnapshot(), impactFlags{
		Symbol: "Orders", Depth: 2, Limit: defaultImpactSectionLimit,
	})
	if reason := impactDegenerateReason(response); reason != "" {
		response.Degenerate, response.DegenerateReason = true, reason
	}
	finalizeImpactEvidence(&response)

	if response.Degenerate {
		t.Fatalf("impactFixtureSnapshot's Orders focus has real callers/callees; must not be Degenerate: %#v", response)
	}
	if response.EvidenceState != sem.EvidenceConfirmed {
		t.Fatalf("EvidenceState = %q, want %q for a fully-resolved answer", response.EvidenceState, sem.EvidenceConfirmed)
	}
	if response.EvidenceNote != "" {
		t.Fatalf("EvidenceNote = %q, want empty for a Confirmed answer", response.EvidenceNote)
	}
	for _, section := range []impactSection{
		response.Callers, response.Callees, response.TypeConsumers,
		response.DataFlows, response.CoChanges, response.Siblings,
	} {
		if section.EvidenceState != sem.EvidenceConfirmed {
			t.Fatalf("section EvidenceState = %q, want %q: %#v", section.EvidenceState, sem.EvidenceConfirmed, section)
		}
		for _, entry := range section.Entries {
			if entry.EvidenceState != sem.EvidenceConfirmed {
				t.Fatalf("entry EvidenceState = %q, want %q: %#v", entry.EvidenceState, sem.EvidenceConfirmed, entry)
			}
		}
	}

	var out bytes.Buffer
	writeImpactText(&out, response)
	text := out.String()
	for _, mustNotAppear := range []string{"requires_verification", "Evidence:"} {
		if strings.Contains(text, mustNotAppear) {
			t.Fatalf("a fully-resolved answer's text must carry no new content (%q found):\n%s", mustNotAppear, text)
		}
	}
}
