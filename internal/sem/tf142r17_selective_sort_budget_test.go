package sem

import (
	"fmt"
	"sort"
	"testing"
)

// Round seventeen. Three review findings on the same finalization pattern,
// present in both search_cache.go's warm-cache selective path AND
// provider.go's streaming build:
//
//  1. Each sorted every accumulated external (and, in search_cache.go,
//     relation) record during finalization with no further budget check, so
//     an expensive O(n log n) sort over a large accumulated prefix could
//     itself blow the wall-clock ceiling the rest of the derivation had just
//     finished enforcing.
//  2. The fix for (1) skipped the sort by falling back to externalsByID's
//     map iteration order, which regressed the provider's determinism
//     contract: two derivations of the identical truncated tree could emit
//     Externals in a different order.
//
// orderedExternalIDs (provider.go) is the shared fix both call sites use:
// alphabetical (sorted) when budgetHit is false, or externalOrder -- the
// deterministic first-seen scan order, tracked by each caller's emitRelation
// alongside its externalsByID map -- when budgetHit is true. TestOrderedExternalIDs*
// below test it directly; TestFinalizeSelectiveOrdering* below test
// search_cache.go's caller of it end to end.

// tf142r17UnsortedFixture builds externals and relations whose natural
// insertion order is the reverse of sorted order, so a skipped sort is
// observable: the descending IDs never accidentally land in ascending order.
// It also returns externalOrder in that same descending discovery order, the
// way the real caller's emitRelation would have built it.
func tf142r17UnsortedFixture(n int) (map[string]ExternalRecord, []string, []RelationRecord) {
	externalsByID := make(map[string]ExternalRecord, n)
	externalOrder := make([]string, 0, n)
	relations := make([]RelationRecord, 0, n)
	for i := n; i > 0; i-- {
		id := fmt.Sprintf("external:pkg%03d", i)
		externalsByID[id] = ExternalRecord{RecordType: "external", ID: id}
		externalOrder = append(externalOrder, id)
		relations = append(relations, RelationRecord{
			RecordType: "relation",
			Type:       "IMPORTS",
			FromID:     fmt.Sprintf("repo:file%03d.go", i),
			ToID:       id,
		})
	}
	return externalsByID, externalOrder, relations
}

// TestOrderedExternalIDsSortsOnTheCompletePath is the widening direction for
// orderedExternalIDs, shared by provider.go's streaming build and
// search_cache.go's selective derivation: budgetHit == false must return
// every ID, alphabetically, regardless of externalOrder's contents.
func TestOrderedExternalIDsSortsOnTheCompletePath(t *testing.T) {
	externalsByID, externalOrder, _ := tf142r17UnsortedFixture(20)

	got := orderedExternalIDs(externalsByID, externalOrder, false)

	if len(got) != len(externalsByID) {
		t.Fatalf("got %d ids, want %d", len(got), len(externalsByID))
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("orderedExternalIDs(budgetHit=false) = %v, want sorted", got)
	}
}

// TestOrderedExternalIDsUsesScanOrderOnceTheBudgetIsHit is the narrowing
// direction: budgetHit == true must return externalOrder verbatim -- no
// sort, and no fallback to ranging externalsByID (which would be
// nondeterministic across calls with identical input).
func TestOrderedExternalIDsUsesScanOrderOnceTheBudgetIsHit(t *testing.T) {
	externalsByID, externalOrder, _ := tf142r17UnsortedFixture(20)

	got := orderedExternalIDs(externalsByID, externalOrder, true)

	if len(got) != len(externalOrder) {
		t.Fatalf("got %d ids, want %d", len(got), len(externalOrder))
	}
	for i := range got {
		if got[i] != externalOrder[i] {
			t.Fatalf("orderedExternalIDs(budgetHit=true)[%d] = %q, want externalOrder[%d] = %q verbatim", i, got[i], i, externalOrder[i])
		}
	}
	if sort.StringsAreSorted(got) {
		t.Fatalf("orderedExternalIDs(budgetHit=true) came out sorted despite a deliberately descending externalOrder: %v", got)
	}
}

// TestFinalizeSelectiveOrderingSortsOnTheCompletePath is the widening
// direction: budgetHit == false must reproduce the exact prior
// behavior -- externals and relations both come out fully sorted.
func TestFinalizeSelectiveOrderingSortsOnTheCompletePath(t *testing.T) {
	externalsByID, externalOrder, relations := tf142r17UnsortedFixture(20)
	selective := ProviderSnapshot{Relations: append([]RelationRecord(nil), relations...)}

	finalizeSelectiveOrdering(&selective, externalsByID, externalOrder, false)

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
// spend an unbounded sort on whatever was accumulated -- but must still be
// deterministic (see the next test), which is what externalOrder is for.
//
// selective.Relations is built as a plain append-ordered slice (not sourced
// from a map), so its insertion order is exactly the fixture's descending
// order; a skipped sort must reproduce that order verbatim.
func TestFinalizeSelectiveOrderingSkipsTheSortOnceTheBudgetIsHit(t *testing.T) {
	const n = 20
	externalsByID, externalOrder, relations := tf142r17UnsortedFixture(n)
	selective := ProviderSnapshot{Relations: append([]RelationRecord(nil), relations...)}

	finalizeSelectiveOrdering(&selective, externalsByID, externalOrder, true)

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
	if selective.Externals[0].ID != "external:pkg020" || selective.Externals[n-1].ID != "external:pkg001" {
		t.Fatalf("externals did not follow externalOrder verbatim: got first=%q last=%q, want first=pkg020 last=pkg001",
			selective.Externals[0].ID, selective.Externals[n-1].ID)
	}
	if selective.Relations[0].ToID != "external:pkg020" || selective.Relations[n-1].ToID != "external:pkg001" {
		t.Fatalf("relations were reordered despite budgetHit=true: got first=%q last=%q, want the untouched descending insertion order (first=pkg020, last=pkg001)",
			selective.Relations[0].ToID, selective.Relations[n-1].ToID)
	}
}

// TestFinalizeSelectiveOrderingIsDeterministicOnceTheBudgetIsHit is the
// review finding on the round-seventeen fix itself: skipping the sort by
// ranging externalsByID directly (a map) would make a truncated snapshot's
// Externals order vary run to run despite the underlying scan being
// deterministic, regressing the provider's determinism contract independent
// of caching. Running finalizeSelectiveOrdering many times over freshly
// built (but identically ordered) inputs and requiring byte-identical
// Externals order every time is what a map-iteration regression would break
// with overwhelming probability on the first repeat.
func TestFinalizeSelectiveOrderingIsDeterministicOnceTheBudgetIsHit(t *testing.T) {
	const n = 30
	var firstIDs []string
	for attempt := 0; attempt < 25; attempt++ {
		externalsByID, externalOrder, relations := tf142r17UnsortedFixture(n)
		selective := ProviderSnapshot{Relations: append([]RelationRecord(nil), relations...)}
		finalizeSelectiveOrdering(&selective, externalsByID, externalOrder, true)

		ids := make([]string, len(selective.Externals))
		for i, external := range selective.Externals {
			ids[i] = external.ID
		}
		if attempt == 0 {
			firstIDs = ids
			continue
		}
		if len(ids) != len(firstIDs) {
			t.Fatalf("attempt %d: got %d externals, want %d", attempt, len(ids), len(firstIDs))
		}
		for i := range ids {
			if ids[i] != firstIDs[i] {
				t.Fatalf("attempt %d: Externals order is not deterministic: position %d was %q on attempt 0, %q here (map iteration order leaking through)",
					attempt, i, firstIDs[i], ids[i])
			}
		}
	}
}
