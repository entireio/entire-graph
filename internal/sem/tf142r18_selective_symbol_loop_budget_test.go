package sem

import (
	"fmt"
	"testing"
)

// Round eighteen. search_cache.go's selective (warm-cache) derivation polled
// the budget once per budgetPollStride FILES while filtering full.Files and
// full.Symbols down to the requested subset, but the inner scan that walks
// every SymbolRecord belonging to a single file had no poll of its own. A
// single pathological file holding far more than budgetPollStride symbols
// (a huge generated or vendored source) let that inner scan run to
// completion -- burning unbounded wall-clock -- before the outer, per-file
// poll ever got another chance to fire. The three downstream grouping loops
// (recordsByFile, and the two loops feeding structuralByFile /
// retainedSymbolsForProfile) had the same gap: each ranged its full input
// with no poll at all.
//
// filterFilesAndSymbolsForBudget is the extracted, directly-testable seam
// for the first of these; this file drives it with a synthetic stop closure
// instead of a real wall-clock deadline, so the inner-loop poll can be
// pinned to fire deterministically mid-file rather than racing a timer.

func tf142r18ManySymbolsFile(path string, n int) []SymbolRecord {
	symbols := make([]SymbolRecord, n)
	for i := range symbols {
		symbols[i] = SymbolRecord{RecordType: "symbol", FilePath: path, ID: fmt.Sprintf("%s#%d", path, i)}
	}
	return symbols
}

// TestFilterFilesAndSymbolsForBudgetStopsMidFile is the finding itself: with
// a single file whose symbol run is much larger than budgetPollStride, a
// stop closure that only trips after enough calls to land inside that run
// (never at a file boundary) must still be observed -- proving the inner
// loop polls independently of the outer, per-file poll.
func TestFilterFilesAndSymbolsForBudgetStopsMidFile(t *testing.T) {
	const symbolsInBigFile = budgetPollStride*3 + 5
	files := []FileRecord{
		{RecordType: "file", Path: "big.go"},
		{RecordType: "file", Path: "small.go"},
	}
	symbols := append(
		tf142r18ManySymbolsFile("big.go", symbolsInBigFile),
		tf142r18ManySymbolsFile("small.go", 2)...,
	)
	allowed := map[string]bool{"big.go": true, "small.go": true}

	// Trips on the second call: the first call is the outer loop's poll for
	// fileIndex 0 (returns false, so the big file's inner scan starts); the
	// second call is the inner loop's first poll at symIndex-runStart == 0
	// would also be at offset 0 (same call as entry), so count from there --
	// the point is this closure is only called budgetPollStride-scale times
	// if the inner loop polls at all. Returning true on call 2 must stop
	// everything before "small.go" -- which the outer loop hasn't reached
	// yet -- is ever appended, and before big.go's scan reaches its end.
	calls := 0
	stop := func() bool {
		calls++
		return calls >= 2
	}

	gotFiles, gotSymbols, complete := filterFilesAndSymbolsForBudget(files, symbols, allowed, stop)

	if complete {
		t.Fatal("a walk stopped inside big.go's symbol run reported a complete selection")
	}

	if len(gotFiles) != 0 {
		t.Fatalf("stopping inside big.go's symbol run must drop big.go whole (no file finished its scan yet), got %d file(s): %#v", len(gotFiles), gotFiles)
	}
	if len(gotSymbols) != 0 {
		t.Fatalf("stopping inside big.go's symbol run must retain zero symbols, got %d", len(gotSymbols))
	}
}

// TestFilterFilesAndSymbolsForBudgetKeepsFileAtomicOnInnerStop pins the
// atomicity guarantee end to end: a stop that fires partway through a large
// file's symbol run must never surface that file with a truncated,
// budget-luck-sized symbol slice -- it is dropped whole, exactly like a stop
// at the outer per-file poll already drops the current file whole.
func TestFilterFilesAndSymbolsForBudgetKeepsFileAtomicOnInnerStop(t *testing.T) {
	const symbolsInBigFile = budgetPollStride*2 + 1
	files := []FileRecord{
		{RecordType: "file", Path: "a.go"},
		{RecordType: "file", Path: "huge.go"},
		{RecordType: "file", Path: "z.go"},
	}
	symbols := append(append(
		tf142r18ManySymbolsFile("a.go", 1),
		tf142r18ManySymbolsFile("huge.go", symbolsInBigFile)...),
		tf142r18ManySymbolsFile("z.go", 1)...,
	)
	allowed := map[string]bool{"a.go": true, "huge.go": true, "z.go": true}

	// Let a.go's file-boundary poll and huge.go's file-boundary poll both
	// pass (calls 1 and 2), then trip on the third call, which can only be
	// reached from inside huge.go's inner symbol scan given
	// symbolsInBigFile far exceeds budgetPollStride.
	calls := 0
	stop := func() bool {
		calls++
		return calls >= 3
	}

	gotFiles, gotSymbols, complete := filterFilesAndSymbolsForBudget(files, symbols, allowed, stop)

	if complete {
		t.Fatal("a walk that never reached z.go reported a complete selection")
	}

	if len(gotFiles) != 1 || gotFiles[0].Path != "a.go" {
		t.Fatalf("want only a.go retained (huge.go dropped whole, z.go never reached), got %#v", gotFiles)
	}
	for _, symbol := range gotSymbols {
		if symbol.FilePath != "a.go" {
			t.Fatalf("want only a.go's symbol retained, got a symbol from %q: huge.go's partial run leaked through", symbol.FilePath)
		}
	}
	if len(gotSymbols) != 1 {
		t.Fatalf("got %d symbols, want exactly a.go's 1", len(gotSymbols))
	}
}

// TestFilterFilesAndSymbolsForBudgetCompletesWithoutStopping is the
// widening direction: a stop that never trips must reproduce the original
// two-independent-loops behavior exactly -- every allowed file retained
// with its complete symbol set, disallowed files dropped.
func TestFilterFilesAndSymbolsForBudgetCompletesWithoutStopping(t *testing.T) {
	files := []FileRecord{
		{RecordType: "file", Path: "keep1.go"},
		{RecordType: "file", Path: "skip.go"},
		{RecordType: "file", Path: "keep2.go"},
	}
	symbols := append(append(
		tf142r18ManySymbolsFile("keep1.go", 3),
		tf142r18ManySymbolsFile("skip.go", 3)...),
		tf142r18ManySymbolsFile("keep2.go", 3)...,
	)
	allowed := map[string]bool{"keep1.go": true, "keep2.go": true}
	stop := func() bool { return false }

	gotFiles, gotSymbols, complete := filterFilesAndSymbolsForBudget(files, symbols, allowed, stop)

	if !complete {
		t.Fatal("a walk that never stopped reported an incomplete selection")
	}

	if len(gotFiles) != 2 || gotFiles[0].Path != "keep1.go" || gotFiles[1].Path != "keep2.go" {
		t.Fatalf("got files %#v, want [keep1.go keep2.go]", gotFiles)
	}
	if len(gotSymbols) != 6 {
		t.Fatalf("got %d symbols, want 6 (3 each for the two kept files)", len(gotSymbols))
	}
	for _, symbol := range gotSymbols {
		if symbol.FilePath == "skip.go" {
			t.Fatalf("skip.go was not in allowedFiles but its symbols were retained")
		}
	}
}
