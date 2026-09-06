package gate

import (
	"reflect"
	"testing"
)

func TestDependentsRespectsHopDepth(t *testing.T) {
	ix := fixtureIndex()

	// One hop is "who calls this directly". Protected reaches VerifyToken only
	// through Require, so it must not appear yet.
	got := names(ix.Dependents([]string{idVerify}, 1))
	want := []string{"TestVerifyToken", "Require"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(hop 1) = %v, want %v", got, want)
	}

	got = names(ix.Dependents([]string{idVerify}, 2))
	want = []string{"TestVerifyToken", "Require", "Protected"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependents(hop 2) = %v, want %v", got, want)
	}
}

func TestDependentsIgnoresStructuralRelations(t *testing.T) {
	// The fixture attaches CONTAINS and DEFINES edges from the enclosing file to
	// VerifyToken. Counting those would make every symbol a dependent of its own
	// file and inflate every risk finding in the report.
	for _, dependent := range fixtureIndex().Dependents([]string{idVerify}, 2) {
		if dependent.Name == "" || dependent.Kind == "file" {
			t.Fatalf("a structural edge was counted as a dependency: %+v", dependent)
		}
	}
	if got := len(fixtureIndex().Dependents([]string{idVerify}, 2)); got != 3 {
		t.Fatalf("Dependents = %d, want 3 (structural edges excluded)", got)
	}
}

func TestDependentsExcludesTheSubjectItself(t *testing.T) {
	for _, dependent := range fixtureIndex().Dependents([]string{idVerify}, 2) {
		if dependent.ID == idVerify {
			t.Fatal("a symbol was reported as its own dependent")
		}
	}
}

func TestDependentsOrderingIsStableAcrossIndexBuilds(t *testing.T) {
	// The real snapshot builds relations in parallel, so the same repository
	// yields the same edges in a different slice order run to run. Gate's whole
	// claim is that two runs of one commit agree, so the walk must not inherit
	// that order.
	forward := fixtureRelations()
	reversed := make([]Relation, len(forward))
	for i, r := range forward {
		reversed[len(forward)-1-i] = r
	}

	a := names(NewIndex(fixtureSymbols(), forward).Dependents([]string{idVerify}, 2))
	b := names(NewIndex(fixtureSymbols(), reversed).Dependents([]string{idVerify}, 2))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("edge order changed the result: %v vs %v", a, b)
	}
}

func TestResolveDisambiguatesByPath(t *testing.T) {
	ix := fixtureIndex()

	// Two symbols are named Load. The semantic diff reports the file it saw the
	// change in, and that file is what separates them.
	got := ix.Resolve("Load", "pkg/config.go")
	if !reflect.DeepEqual(got, []string{idLoad}) {
		t.Fatalf("Resolve with path = %v, want [%s]", got, idLoad)
	}

	// Without a usable path there is nothing to choose on, so both candidates
	// come back rather than an arbitrary one.
	if got := ix.Resolve("Load", ""); len(got) != 2 {
		t.Fatalf("Resolve without path = %v, want both definitions", got)
	}
}

func TestResolveReturnsNothingForUnknownName(t *testing.T) {
	// An entity the graph does not know is not an error: it is an unsupported
	// language or an unparsed file, and callers must be able to tell that apart
	// from "known and has no dependents".
	if got := fixtureIndex().Resolve("NoSuchSymbol", "pkg/auth.go"); len(got) != 0 {
		t.Fatalf("Resolve(unknown) = %v, want empty", got)
	}
}
