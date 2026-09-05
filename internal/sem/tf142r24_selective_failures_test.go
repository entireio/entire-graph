package sem

import (
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
