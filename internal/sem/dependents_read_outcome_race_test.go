package sem

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// readOutcomeRaceCandidates is comfortably above the two concurrent workers the
// race needs, so several candidates reach readLimitedFile's outcome maps from
// different goroutines in one scan.
const readOutcomeRaceCandidates = 6

// importGitlinkCandidates builds a head tree whose leaves are gitlink (submodule)
// entries at line-unsafe paths.
//
// Both properties matter. The embedded newline makes BatchFileReader.IsPathSafe
// reject the path, so the candidate is read through LimitedFileReader --
// readLimitedFile, the function whose outcome writes this test covers -- instead
// of the content batch. The gitlink makes git ls-tree report the entry as
// `commit`, so LimitedFileReader classifies it LimitedFileNonBlob, which is the
// Missing/NonBlob/Unreadable group that readLimitedFile records in
// limitedUnavailable. A blob-backed candidate cannot reach that group at all,
// which is why the pre-existing line-unsafe fixture (an oversized blob) exercises
// only the oversize write.
func importGitlinkCandidates(t *testing.T, repo string, count int) (head string, paths []string) {
	t.Helper()
	// A gitlink names a commit that need not be present, but pointing every
	// entry at a real one keeps the fixture valid for any consistency check Git
	// performs while reading the tree.
	blob := gitObjectInputOutput(t, repo, "def anchor():\n    return Foo\n", "hash-object", "-w", "--stdin")
	anchorTree := gitObjectInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tanchor.py%c", blob, byte(0)), "mktree", "-z")
	target := gitObjectInputOutput(
		t, repo, "",
		"-c", "user.name=Entire Graph Test",
		"-c", "user.email=graph@example.com",
		"commit-tree", anchorTree, "-m", "gitlink target",
	)

	var rootInput strings.Builder
	for i := range count {
		// One directory component per candidate: the leaf name carries the
		// newline, and the containing tree keeps each path distinct.
		leaf := fmt.Sprintf("unsafe\ncaller_%d.py", i)
		dir := fmt.Sprintf("d%d", i)
		tree := gitObjectInputOutput(t, repo, fmt.Sprintf("160000 commit %s\t%s%c", target, leaf, byte(0)), "mktree", "-z")
		fmt.Fprintf(&rootInput, "040000 tree %s\t%s%c", tree, dir, byte(0))
		paths = append(paths, dir+"/"+leaf)
	}
	// One ordinary blob so the scan also has a candidate it can really read.
	fmt.Fprintf(&rootInput, "040000 tree %s\tanchor%c", anchorTree, byte(0))

	root := gitObjectInputOutput(t, repo, rootInput.String(), "mktree", "-z")
	head = gitObjectInputOutput(
		t, repo, "",
		"-c", "user.name=Entire Graph Test",
		"-c", "user.email=graph@example.com",
		"commit-tree", root, "-m", "gitlink candidates",
	)
	git(t, repo, "update-ref", "refs/heads/gitlink-candidates", head)
	return head, paths
}

// TestBuildReferenceIndexGuardsConcurrentReadOutcomeWrites is the regression
// test for the unguarded read-outcome writes in readLimitedFile.
//
// The candidate scan runs on workers, and readLimitedFile records what each read
// found in maps shared by every one of them. Two of those writes -- the
// Unaddressable status and the Missing/NonBlob/Unreadable group -- sat outside
// readOutcomeMu, so a scan with more than one batch-unsafe candidate could abort
// the whole process with `fatal error: concurrent map writes`, which no recover
// can catch. The race was invisible because the suite had exactly one
// line-unsafe candidate anywhere, so two of them never ran at once.
//
// Under -race, removing readOutcomeMu from readLimitedFile fails this test: the
// six candidates below reach the same map from different workers with no
// happens-before edge between them (LimitedFileReader's own lock is released
// before the outcome is recorded, so it orders the reads, not the writes).
func TestBuildReferenceIndexGuardsConcurrentReadOutcomeWrites(t *testing.T) {
	if workers := dependentsScanWorkers(); workers < 2 {
		t.Skipf("the write is only concurrent with a parallel candidate scan; dependentsScanWorkers() = %d", workers)
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	head, unsafePaths := importGitlinkCandidates(t, repo, readOutcomeRaceCandidates)

	// The NUL byte makes the git-grep prefilter fail, so the scan falls back to
	// listing the whole tree -- the only way gitlink entries, which no grep can
	// match, become candidates.
	names := map[string]struct{}{
		"Foo":         {},
		"poison\x00x": {},
	}
	_, warnings, err := buildReferenceIndex(context.Background(), repo, head, names)
	if err != nil {
		t.Fatal(err)
	}

	unavailable := map[string]bool{}
	fallback := 0
	for _, warning := range warnings {
		switch warning.Code {
		case "W_DEPENDENTS_PREFILTER_FAILED":
			fallback++
		case "E_FILE_READ":
			unavailable[warning.FilePath] = true
		default:
			t.Fatalf("unexpected warning %#v", warning)
		}
	}
	if fallback != 1 {
		t.Fatalf("prefilter fallback warnings = %d, want exactly 1: the scan did not take the full-tree path, so the gitlink entries were never candidates", fallback)
	}
	// Every line-unsafe gitlink must be reported, which is also the proof that
	// every one of them reached readLimitedFile's outcome maps on a worker.
	for _, path := range unsafePaths {
		if !unavailable[path] {
			t.Fatalf("no E_FILE_READ warning for %q; got %v", path, unavailable)
		}
	}
	if len(unavailable) != len(unsafePaths) {
		t.Fatalf("E_FILE_READ warnings = %v, want exactly the %d line-unsafe gitlink candidates", unavailable, len(unsafePaths))
	}
}
