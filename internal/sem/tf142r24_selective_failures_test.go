package sem

import (
	"fmt"
	"testing"
	"time"
)

// Round twenty-four. The selective (warm-cache) derivation filtered the full
// build's per-file partial failures through retainedFileSet(selective.Files) --
// the set of files that produced a FileRecord.
//
// Two of the codes a full build reports are recorded with NO file record at
// all: E_UNSUPPORTED_LANGUAGE ("file omitted because no parser is available")
// and E_FILE_READ ("file listed but content was unavailable"). Both are
// appended with result.file left nil in provider_parallel.go. Filtering by the
// retained records therefore dropped exactly the failures that describe a file
// the snapshot does not otherwise mention, so the same query answered from a
// warm cache came back clean where a cold build reported a machine-readable
// omission -- cache presence changed the reported failure set, which is the one
// thing the selective path is supposed to keep invariant.

func tf142r24Repo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, repo, "legacy.f90", "      PROGRAM LEGACY\n      END PROGRAM LEGACY\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// tf142r24RelationHeavyRepo makes the relation phase, not the file walk, the
// expensive half: the file/symbol filter walks a handful of records while each
// JavaScript file resolves calls the relation phase has to scan for. That is
// the shape a wall-clock budget actually meets in the field, and it is the one
// where the selection is complete but the derivation still truncates.
func tf142r24RelationHeavyRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	for i := range 6 {
		writeFile(t, repo, fmt.Sprintf("pkg%02d.js", i),
			fmt.Sprintf("function helper%d(x) {\n  return fetch(\"https://example.com/v%d/items\") + x;\n}\n\nfunction caller%d(y) {\n  return helper%d(y);\n}\n", i, i, i, i))
	}
	writeFile(t, repo, "legacy.f90", "      PROGRAM LEGACY\n      END PROGRAM LEGACY\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func tf142r24FailureFor(failures []PartialFailure, path, code string) bool {
	for _, failure := range failures {
		if failure.FilePath == path && failure.Code == code {
			return true
		}
	}
	return false
}

// TestTF142R24SelectiveKeepsFailuresForFilesWithNoRecord is the finding: a
// selection consisting only of an unparseable file must report the same
// omission a cold build reports for it.
func TestTF142R24SelectiveKeepsFailuresForFilesWithNoRecord(t *testing.T) {
	repo := tf142r24Repo(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	if !tf142r24FailureFor(full.Header.PartialFailures, "legacy.f90", "E_UNSUPPORTED_LANGUAGE") {
		t.Fatalf("fixture is not exercising the case: the cold build reported %#v", full.Header.PartialFailures)
	}

	selective, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"legacy.f90"},
	}, full)
	if err != nil {
		t.Fatalf("selective derivation: %v", err)
	}
	if !tf142r24FailureFor(selective.Header.PartialFailures, "legacy.f90", "E_UNSUPPORTED_LANGUAGE") {
		t.Fatalf("the warm-cache answer for legacy.f90 reported %#v, want the E_UNSUPPORTED_LANGUAGE the cold build reports; "+
			"a file with no FileRecord is invisible to retainedFileSet", selective.Header.PartialFailures)
	}
}

// TestTF142R24SelectiveDropsFailuresOutsideTheSelection is the widening guard:
// admitting no-record failures must not start reporting failures for files the
// caller did not ask about.
func TestTF142R24SelectiveDropsFailuresOutsideTheSelection(t *testing.T) {
	repo := tf142r24Repo(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	selective, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"main.go"},
	}, full)
	if err != nil {
		t.Fatalf("selective derivation: %v", err)
	}
	if tf142r24FailureFor(selective.Header.PartialFailures, "legacy.f90", "E_UNSUPPORTED_LANGUAGE") {
		t.Fatalf("a selection of main.go reported a failure for legacy.f90: %#v", selective.Header.PartialFailures)
	}
}

// TestTF142R24TruncatedSelectiveReportsOnlyReachedFiles pins the other half:
// once the budget has tripped, the derivation has stopped deciding files, so it
// must not start describing per-file outcomes for a selection it never walked.
// The single E_ANALYSIS_BUDGET_EXCEEDED marker is what says why they are gone.
func TestTF142R24TruncatedSelectiveReportsOnlyReachedFiles(t *testing.T) {
	repo := tf142r24Repo(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	selective, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:     ProfileFull,
		OnlyFiles:   []string{"legacy.f90", "main.go"},
		MaxDuration: time.Second,
		// One live poll (deriving the deadline), then a clock past it, so the
		// gate is already tripped when the file filter starts.
		nowFn: tf142r22LaggingClock(time.Second, 1),
	}, full)
	if err != nil {
		t.Fatalf("an opt-in budget must truncate, not fail: %v", err)
	}
	if !partialFailuresTruncated(selective.Header.PartialFailures) {
		t.Fatalf("a derivation stopped by the clock must say so: %#v", selective.Header.PartialFailures)
	}
	if len(selective.Files) != 0 {
		t.Skipf("budget did not truncate before the first file: %d retained", len(selective.Files))
	}
	if tf142r24FailureFor(selective.Header.PartialFailures, "legacy.f90", "E_UNSUPPORTED_LANGUAGE") {
		t.Fatalf("a derivation that retained nothing still reported a per-file outcome: %#v", selective.Header.PartialFailures)
	}
}

// TestTF142R24TruncationAfterTheWalkStillReportsRecordlessFailures is the
// distinction that makes the guard above exact rather than merely safe.
//
// "The budget was hit" and "the selection is incomplete" are not the same
// question. The file/symbol walk is cheap; the relation phase is what burns a
// wall-clock ceiling. When the gate trips downstream of the walk, the retained
// selection is exactly the one an unbudgeted derivation would have produced,
// and the record-less failures are exact too -- so keying the filter on
// budgetHit threw away accurate information on the most common truncation
// there is. It is keyed on whether the walk itself stopped mid-selection.
func TestTF142R24TruncationAfterTheWalkStillReportsRecordlessFailures(t *testing.T) {
	repo := tf142r24RelationHeavyRepo(t)
	full, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	if !tf142r24FailureFor(full.Header.PartialFailures, "legacy.f90", "E_UNSUPPORTED_LANGUAGE") {
		t.Fatalf("fixture is not exercising the case: the cold build reported %#v", full.Header.PartialFailures)
	}
	selection := []string{"legacy.f90", "pkg00.js", "pkg01.js", "pkg02.js", "pkg03.js", "pkg04.js", "pkg05.js"}
	wantRecords := len(selection) - 1 // legacy.f90 produces none

	budget := time.Second
	reached := 0
	for tolerate := int64(1); tolerate <= 400; tolerate++ {
		selective, deriveErr := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile:     ProfileFull,
			OnlyFiles:   selection,
			MaxDuration: budget,
			nowFn:       tf142r22LaggingClock(budget, tolerate),
		}, full)
		if deriveErr != nil {
			t.Fatalf("tolerate=%d: an opt-in budget must truncate, not fail: %v", tolerate, deriveErr)
		}
		if !partialFailuresTruncated(selective.Header.PartialFailures) {
			continue // Ran to completion at this clock; the other tests cover that.
		}
		if len(selective.Files) != wantRecords {
			continue // The walk itself stopped mid-selection; nothing exact to report.
		}
		reached++
		if !tf142r24FailureFor(selective.Header.PartialFailures, "legacy.f90", "E_UNSUPPORTED_LANGUAGE") {
			t.Fatalf("tolerate=%d: the walk retained all %d record-bearing files, so the selection is exact, "+
				"yet legacy.f90's E_UNSUPPORTED_LANGUAGE was dropped: %#v",
				tolerate, wantRecords, selective.Header.PartialFailures)
		}
	}
	if reached == 0 {
		t.Fatal("fixture never truncated downstream of a complete file walk, so this test asserted nothing")
	}
}
