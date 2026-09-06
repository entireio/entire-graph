package sem

import (
	"errors"
	"testing"
)

// A linked worktree's administrative git directory keeps objects/ and refs/
// in the directory named by commondir. If the shared pointer-read ledger is
// exhausted immediately before that file, "commondir exists but was not read"
// is an unknown result, not permission to fall back to the administrative
// directory itself. The latter has no local objects/refs, so that fallback
// leaves its credentialed config in the source listing.
func TestGitDirPointerBudgetExhaustionAtCommondirFailsClosed(t *testing.T) {
	repo := t.TempDir()
	const commonTarget = "../../shared-git\n"
	writeGitDirFixture(t, repo, "shared-git")
	writeFile(t, repo, "state/.wt-git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, repo, "state/.wt-git/commondir", commonTarget)
	writeFile(t, repo, "state/.wt-git/config", gitDirConfigWithCredential)
	writeFile(t, repo, "src/app.go", "package app\n")

	excluder := newGitDirExcluder(t.Context(), repo)
	// Leave exactly the file's observed size in the ledger. Admission also
	// reserves one race-detection byte, so reading commondir must be refused.
	excluder.pointerBytesReserved = maxGitPointerAggregateBytes - int64(len(commonTarget))
	excluder.observeListedPaths([]string{"state/.wt-git/config", "src/app.go"}, nil)

	if !excluder.pointerReadBudgetExceeded {
		t.Fatal("commondir read did not exhaust the aggregate pointer-read budget")
	}
	if !excluder.excluded("state/.wt-git/config") {
		t.Error(`excluded("state/.wt-git/config") = false, want true: an unread commondir must fail closed around the linked-worktree admin directory`)
	}
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false: failing closed must stay scoped to a git-shaped directory`)
	}
}

// sharedindex.<hash> is git-owned only when the whole top-level path is the
// split-index file. A legitimate directory may have exactly that component
// name; source below it must not be discarded merely because its ancestor
// resembles a split-index filename.
func TestGitDirRootSharedIndexNameDoesNotExcludeDirectoryContents(t *testing.T) {
	const hash = "a94a8fe5ccb19ba61c4c0873d391e987982fbbd3"
	repo := t.TempDir()
	writeRootGitDirFixture(t, repo)
	writeFile(t, repo, ".git", "gitdir: .\n")
	writeFile(t, repo, "sharedindex."+hash+"/source.go", "package source\n")

	excluder := newGitDirExcluder(t.Context(), repo)
	path := "sharedindex." + hash + "/source.go"
	if excluder.excluded(path) {
		t.Errorf("excluded(%q) = true, want false: only the whole top-level split-index file is git-owned", path)
	}
}

func TestListedGitDirectoryObservationStopsAtItsFixedBound(t *testing.T) {
	excluder := newGitDirExcluder(t.Context(), t.TempDir())
	excluder.listedDirectoriesObserved = maxListedDirectoryObservations
	excluder.observeListedPaths([]string{"pkg/source.go"}, nil)
	if !errors.Is(excluder.listedObservationError(), errGitDirListedObservationBound) {
		t.Fatalf("listed observation error = %v, want %v", excluder.listedObservationError(), errGitDirListedObservationBound)
	}
	if len(excluder.observedDirs) != 0 {
		t.Fatalf("observed directories after bound = %v, want none", excluder.observedDirs)
	}
}
