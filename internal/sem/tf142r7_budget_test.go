package sem

import (
	"testing"
)

// Round seven. Two open review findings, both about work that is done BEFORE
// the guard that is supposed to bound it.
//
// Neither test uses a stopwatch. One uses an already-expired budget, the other
// flips the predicate directly, so neither can flake on a loaded shared runner.

// tf142r7TypeRecords builds a single-file symbol table in which every function
// signature references a declaration that lives in the SAME file. That is the
// shape resolveTypeReference is quadratic on: firstTypeLikeNamed rescans the
// whole file slice once per reference.
func tf142r7TypeRecords(functions int) (map[string][]SymbolRecord, map[string][]SymbolRecord, map[string][]SymbolRecord) {
	const path = "a.ts"
	records := make([]SymbolRecord, 0, functions+1)
	for index := 0; index < functions; index++ {
		records = append(records, SymbolRecord{
			RecordType: "symbol",
			ID:         "repo:TypeScript:a.ts:function:fn" + string(rune('a'+index%26)) + itoaTF142R7(index),
			Kind:       "function",
			Name:       "fn" + itoaTF142R7(index),
			FilePath:   path,
			Language:   "TypeScript",
			Signature:  "function fn" + itoaTF142R7(index) + "(a: Zzz): Zzz",
		})
	}
	target := SymbolRecord{
		RecordType: "symbol",
		ID:         "repo:TypeScript:a.ts:interface:Zzz",
		Kind:       "interface",
		Name:       "Zzz",
		FilePath:   path,
		Language:   "TypeScript",
	}
	records = append(records, target)

	recordsByFile := map[string][]SymbolRecord{path: records}
	symbolsByFile := map[string][]SymbolRecord{path: records}
	symbolsByShortName := map[string][]SymbolRecord{"Zzz": {target}}
	return recordsByFile, symbolsByFile, symbolsByShortName
}

func itoaTF142R7(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestTF142R7TypeProducersStopWhenTheBudgetIsGone is the narrowing direction of
// the provider.go:3998 finding.
//
// forEachRelation guards these producers before the stage, between stages and
// per emitted record, but never DURING the producer, and both of these are
// quadratic in symbols-per-file rather than the single linear pass the rest of
// the disclosed set is: resolveTypeReference calls firstTypeLikeNamed, which
// rescans the whole same-file slice for every identifier in every signature.
// Measured with the real verb on one 16k-symbol TypeScript file, that charged
// 5.2 s of work to a budget that had already expired (`snapshot --max-seconds 12`
// finished in 17.23 s, having emitted exactly one USES_TYPE record: the slice was
// built in full, then the per-record guard fired on the first record).
//
// The invariant: with the predicate already true, neither producer does any work.
func TestTF142R7TypeProducersStopWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()
	recordsByFile, symbolsByFile, symbolsByShortName := tf142r7TypeRecords(64)
	imports := map[string]map[string][]string{}
	expired := func() bool { return true }

	if got := usesTypeRelations(expired, recordsByFile, symbolsByFile, symbolsByShortName, imports); len(got) != 0 {
		t.Fatalf("usesTypeRelations ran to completion on an expired budget: built %d relation(s)", len(got))
	}
	spec := resolveProfile(ProfileFull)
	if got := signatureTypeRelations(expired, recordsByFile, symbolsByFile, symbolsByShortName, imports, spec); len(got) != 0 {
		t.Fatalf("signatureTypeRelations ran to completion on an expired budget: built %d relation(s)", len(got))
	}
}

// TestTF142R7TypeProducersUnbudgetedAreUnchanged is the widening direction: the
// predicate must not eat legitimate input. A nil predicate (the unbudgeted path,
// which is what forEachRelation passes when no MaxDuration was set) and a
// predicate that never fires must both produce the complete relation set.
func TestTF142R7TypeProducersUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	const functions = 64
	recordsByFile, symbolsByFile, symbolsByShortName := tf142r7TypeRecords(functions)
	imports := map[string]map[string][]string{}
	spec := resolveProfile(ProfileFull)

	nilStop := usesTypeRelations(nil, recordsByFile, symbolsByFile, symbolsByShortName, imports)
	if len(nilStop) != functions {
		t.Fatalf("a nil predicate changed usesTypeRelations output: want %d relation(s), got %d", functions, len(nilStop))
	}
	neverStop := usesTypeRelations(func() bool { return false }, recordsByFile, symbolsByFile, symbolsByShortName, imports)
	if len(neverStop) != len(nilStop) {
		t.Fatalf("a live-but-unexpired predicate changed usesTypeRelations output: %d vs %d", len(neverStop), len(nilStop))
	}
	for index := range nilStop {
		if nilStop[index].FromID != neverStop[index].FromID ||
			nilStop[index].ToID != neverStop[index].ToID ||
			nilStop[index].Type != neverStop[index].Type ||
			nilStop[index].Resolution != neverStop[index].Resolution {
			t.Fatalf("relation %d differs between the nil and never-firing predicates", index)
		}
	}

	nilSig := signatureTypeRelations(nil, recordsByFile, symbolsByFile, symbolsByShortName, imports, spec)
	neverSig := signatureTypeRelations(func() bool { return false }, recordsByFile, symbolsByFile, symbolsByShortName, imports, spec)
	if len(nilSig) == 0 {
		t.Fatal("the fixture produced no PARAM_TYPE/RETURNS_TYPE relations, so this test proves nothing")
	}
	if len(neverSig) != len(nilSig) {
		t.Fatalf("a live-but-unexpired predicate changed signatureTypeRelations output: %d vs %d", len(neverSig), len(nilSig))
	}
}

// TestTF142R7SelectiveDerivationStopsBeforeThePreprocessing is the narrowing
// direction of the search_cache.go:585 finding.
//
// Round five gave the warm-cache derivation a work context, but only the
// RELATION phase observed it: the function still filtered the complete cached
// file and symbol inventory (linear in the FULL snapshot, not in the selection)
// and ran importsFor over every selected fast-profile file -- a content scan per
// file -- before shouldStop existed. An already-expired budget therefore still
// paid for the whole preprocessing pass.
//
// The invariant, with no stopwatch: a budget that expired before the call must
// leave the derivation with nothing filtered in. Before the fix the same call
// returns the complete selected symbol set.
func TestTF142R7SelectiveDerivationStopsBeforeThePreprocessing(t *testing.T) {
	repo := tf142r5SelectiveRepo(t)
	cacheDir := t.TempDir()

	if _, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir, false); err != nil {
		t.Fatalf("warm the complete snapshot: %v", err)
	}

	selective := ProviderSnapshotOptions{
		Profile:     ProfileFull,
		OnlyFiles:   []string{"deep.js"},
		MaxDuration: tf142r5ExpiredBudget,
	}
	snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", selective, cacheDir, false)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("the derivation did not report truncation: %#v", snapshot.Header.PartialFailures)
	}
	if len(snapshot.Symbols) != 0 || len(snapshot.Files) != 0 {
		t.Fatalf("the derivation filtered the complete cached inventory on an expired budget: %d file(s), %d symbol(s) retained",
			len(snapshot.Files), len(snapshot.Symbols))
	}
	if snapshot.Header.Stats.CompletenessLevel != "unsafe" {
		t.Fatalf("a truncated derivation must be unsafe, got %q", snapshot.Header.Stats.CompletenessLevel)
	}
}
