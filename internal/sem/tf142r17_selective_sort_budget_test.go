package sem

import (
	"fmt"
	"sort"
	"testing"
)

// Round seventeen. One review finding: the warm-cache selective path
// (search_cache.go) sorted every accumulated external and relation record
// during finalization with no further budget check, so an expensive
// O(n log n) sort over a large accumulated prefix could itself blow the
// wall-clock ceiling the rest of the derivation had just finished enforcing.

// tf142r17UnsortedFixture builds externals and relations whose natural
// insertion order is the reverse of sorted order, so a skipped sort is
// observable: the descending IDs never accidentally land in ascending order.
func tf142r17UnsortedFixture(n int) (map[string]ExternalRecord, []RelationRecord) {
	externalsByID := make(map[string]ExternalRecord, n)
	relations := make([]RelationRecord, 0, n)
	for i := n; i > 0; i-- {
		id := fmt.Sprintf("external:pkg%03d", i)
		externalsByID[id] = ExternalRecord{RecordType: "external", ID: id}
		relations = append(relations, RelationRecord{
			RecordType: "relation",
			Type:       "IMPORTS",
			FromID:     fmt.Sprintf("repo:file%03d.go", i),
			ToID:       id,
		})
	}
	return externalsByID, relations
}

// TestFinalizeSelectiveOrderingSortsOnTheCompletePath is the widening
// direction: budgetHit == false must reproduce the exact prior
// behavior -- externals and relations both come out fully sorted.
func TestFinalizeSelectiveOrderingSortsOnTheCompletePath(t *testing.T) {
	externalsByID, relations := tf142r17UnsortedFixture(20)
	selective := ProviderSnapshot{Relations: append([]RelationRecord(nil), relations...)}

	finalizeSelectiveOrdering(&selective, externalsByID, false)

	if len(selective.Externals) != len(externalsByID) {
		t.Fatalf("got %d externals, want %d", len(selective.Externals), len(externalsByID))
	}
	if !sort.SliceIsSorted(selective.Externals, func(i, j int) bool {
		return selective.Externals[i].ID < selective.Externals[j].ID
	}) {
		t.Fatalf("externals not sorted on the complete path: %#v", selective.Externals)
	}
	if !sort.SliceIsSorted(selective.Relations, func(i, j int) bool {
		left := selective.Relations[i].Type + selective.Relations[i].FromID + selective.Relations[i].ToID
		right := selective.Relations[j].Type + selective.Relations[j].FromID + selective.Relations[j].ToID
		return left < right
	}) {
		t.Fatalf("relations not sorted on the complete path: %#v", selective.Relations)
	}
}

// TestFinalizeSelectiveOrderingSkipsTheSortOnceTheBudgetIsHit is the
// narrowing direction of the search_cache.go:783 finding: once the caller
// has classified the stop as a budget truncation, finalization must not
// spend an unbounded sort on whatever was accumulated.
//
// selective.Relations is built as a plain append-ordered slice (not sourced
// from a map), so its insertion order is exactly the fixture's descending
// order; a skipped sort must reproduce that order verbatim, which is what
// the first assertion checks directly. externalsByID is a map, so Go gives
// no order guarantee over it even when the sort is skipped -- the second
// assertion instead checks that the output is NOT the fully-sorted order a
// real sort.Strings would have produced, which a 20-element random map
// iteration lands on with probability 1/20! and a correct skip never does.
func TestFinalizeSelectiveOrderingSkipsTheSortOnceTheBudgetIsHit(t *testing.T) {
	const n = 20
	externalsByID, relations := tf142r17UnsortedFixture(n)
	selective := ProviderSnapshot{Relations: append([]RelationRecord(nil), relations...)}

	finalizeSelectiveOrdering(&selective, externalsByID, true)

	if len(selective.Externals) != len(externalsByID) {
		t.Fatalf("budgetHit dropped externals entirely: got %d, want %d", len(selective.Externals), len(externalsByID))
	}
	if len(selective.Relations) != n {
		t.Fatalf("budgetHit dropped relations entirely: got %d, want %d", len(selective.Relations), n)
	}
	if sort.SliceIsSorted(selective.Externals, func(i, j int) bool {
		return selective.Externals[i].ID < selective.Externals[j].ID
	}) {
		t.Fatalf("externals came out fully sorted despite budgetHit=true: %#v", selective.Externals)
	}
	if selective.Relations[0].ToID != "external:pkg020" || selective.Relations[n-1].ToID != "external:pkg001" {
		t.Fatalf("relations were reordered despite budgetHit=true: got first=%q last=%q, want the untouched descending insertion order (first=pkg020, last=pkg001)",
			selective.Relations[0].ToID, selective.Relations[n-1].ToID)
	}
}
