package sem

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// Round twenty-two. The selective (warm-cache) derivation classified its stop,
// appended E_ANALYSIS_BUDGET_EXCEEDED, and then handed the SAME already-tripped
// predicate to populateSelectiveHeader. That predicate is level-triggered
// against the clock, so it answers true on the first poll, and every loop in
// the header population broke at index 0.
//
// The result is a snapshot whose header contradicts its own body: Stats.Files
// counts the retained files unconditionally, while Languages, LanguageTiers,
// Completeness.Languages and ParsedFiles were all left describing zero of them.
// A selection that survived the budget whole was reported to every consumer of
// the coverage figures as "0 languages / 0 files", and the per-language tiers a
// SCIP consumer decides per-language trust from disappeared for languages that
// were in fact fully indexed.

// tf142r22LaggingClock reads the real clock for its first `tolerate` polls --
// long enough for the deadline to be derived and the file/symbol filter to run
// under a live budget -- and reports a time past the deadline on every poll
// after that. Deterministic: no assertion below depends on machine speed.
func tf142r22LaggingClock(budget time.Duration, tolerate int64) func() time.Time {
	var polls atomic.Int64
	base := time.Now()
	return func() time.Time {
		if polls.Add(1) <= tolerate {
			return base
		}
		return base.Add(budget + 2*time.Hour)
	}
}

func tf142r22Repo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	for i := range 12 {
		writeFile(t, repo, fmt.Sprintf("pkg%02d.js", i),
			fmt.Sprintf("function helper%d(x) {\n  return x + %d;\n}\n\nfunction caller%d(y) {\n  return helper%d(y);\n}\n", i, i, i, i))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// TestTF142R22SelectiveHeaderDescribesTheFilesItReturns is the finding, driven
// through the real derivation rather than the seam. The budget is expired at a
// sweep of points; wherever the derivation still came back holding files, the
// header must name their languages and count them as parsed. A header that
// disagrees with the Stats.Files sitting beside it is not a truncation report,
// it is a wrong one.
func TestTF142R22SelectiveHeaderDescribesTheFilesItReturns(t *testing.T) {
	repo := tf142r22Repo(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("unbudgeted build: %v", err)
	}

	const budget = time.Hour
	reached := 0
	for tolerate := int64(1); tolerate <= 30; tolerate++ {
		selective, deriveErr := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile:     ProfileFull,
			OnlyFiles:   []string{"pkg00.js", "pkg01.js", "pkg02.js"},
			MaxDuration: budget,
			nowFn:       tf142r22LaggingClock(budget, tolerate),
		}, full)
		if deriveErr != nil {
			t.Fatalf("tolerate=%d: an opt-in budget must truncate, not fail: %v", tolerate, deriveErr)
		}
		if len(selective.Files) == 0 {
			continue // Stopped before retaining anything; an empty header is honest.
		}
		reached++
		if !partialFailuresTruncated(selective.Header.PartialFailures) {
			t.Fatalf("tolerate=%d: a derivation stopped by the clock must stay self-describing: %#v",
				tolerate, selective.Header.PartialFailures)
		}
		if len(selective.Header.Languages) == 0 {
			t.Fatalf("tolerate=%d: the header reports %d file(s) and no language at all: stats=%#v",
				tolerate, selective.Header.Stats.Files, selective.Header.Stats)
		}
		if selective.Header.Stats.ParsedFiles != selective.Header.Stats.Files {
			t.Fatalf("tolerate=%d: %d retained file(s) counted as %d parsed; nothing here failed to parse",
				tolerate, selective.Header.Stats.Files, selective.Header.Stats.ParsedFiles)
		}
		if len(selective.Header.LanguageTiers) == 0 {
			t.Fatalf("tolerate=%d: language tiers are empty for %d retained file(s); a SCIP consumer decides "+
				"per-language trust from this field", tolerate, selective.Header.Stats.Files)
		}
		files := 0
		for _, completeness := range selective.Header.Completeness.Languages {
			files += completeness.Files
		}
		if files != selective.Header.Stats.Files {
			t.Fatalf("tolerate=%d: completeness accounts for %d file(s), stats report %d",
				tolerate, files, selective.Header.Stats.Files)
		}
	}
	if reached == 0 {
		t.Fatal("fixture never produced a truncated derivation that still held files, so this test asserted nothing")
	}
}

// TestTF142R22SelectiveHeaderSeamIsIndependentOfTheGate pins the same property
// at the seam, with no clock in play: given a snapshot holding records, the
// header must describe them. The end-to-end sweep above can only reach the
// states the derivation happens to produce; this one cannot be skipped past.
func TestTF142R22SelectiveHeaderSeamIsIndependentOfTheGate(t *testing.T) {
	t.Parallel()
	selective := &ProviderSnapshot{
		Files: []FileRecord{
			{RecordType: "file", Path: "a.go", Language: "Go"},
			{RecordType: "file", Path: "b.py", Language: "Python"},
		},
		Symbols: []SymbolRecord{{RecordType: "symbol", FilePath: "a.go", Language: "Go"}},
	}
	populateSelectiveHeader(selective, nil, []PartialFailure{analysisBudgetFailure(time.Second)}, nil)

	if got := len(selective.Header.Languages); got != 2 {
		t.Fatalf("Languages = %v, want both retained languages", selective.Header.Languages)
	}
	if selective.Header.Stats.ParsedFiles != 2 {
		t.Fatalf("ParsedFiles = %d, want 2 — neither file failed to parse", selective.Header.Stats.ParsedFiles)
	}
	if got := selective.Header.Completeness.Languages["Go"]; got.Files != 1 || got.Symbols != 1 {
		t.Fatalf("Go completeness = %#v, want 1 file and 1 symbol", got)
	}
	if len(selective.Header.LanguageTiers) != 2 {
		t.Fatalf("LanguageTiers = %#v, want one entry per retained language", selective.Header.LanguageTiers)
	}
	// The truncation marker is the caller's, and must survive untouched: the
	// header describing what was kept is not a claim that nothing was lost.
	if !partialFailuresTruncated(selective.Header.PartialFailures) {
		t.Fatalf("the caller's truncation marker was dropped: %#v", selective.Header.PartialFailures)
	}
	if selective.Header.Stats.CompletenessLevel != "unsafe" {
		t.Fatalf("completeness_level = %q, want unsafe for a truncated snapshot", selective.Header.Stats.CompletenessLevel)
	}
}

// TestTF142R22UnbudgetedSelectiveHeaderIsUnchanged is the widening guard: a
// derivation with no budget at all must produce exactly the header it did
// before, or the fix above would be a behaviour change dressed as a bug fix.
func TestTF142R22UnbudgetedSelectiveHeaderIsUnchanged(t *testing.T) {
	repo := tf142r22Repo(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("unbudgeted build: %v", err)
	}
	selective, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"pkg00.js", "pkg01.js", "pkg02.js"},
	}, full)
	if err != nil {
		t.Fatalf("unbudgeted derivation: %v", err)
	}
	if partialFailuresTruncated(selective.Header.PartialFailures) {
		t.Fatalf("an unbudgeted derivation reported truncation: %#v", selective.Header.PartialFailures)
	}
	if selective.Header.Stats.Files != 3 || selective.Header.Stats.ParsedFiles != 3 {
		t.Fatalf("unbudgeted stats = %#v, want 3 files all parsed", selective.Header.Stats)
	}
	if len(selective.Header.Languages) != 1 || selective.Header.Languages[0] != "JavaScript" {
		t.Fatalf("unbudgeted languages = %v, want JavaScript", selective.Header.Languages)
	}
}
