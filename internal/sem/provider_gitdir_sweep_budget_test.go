package sem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIgnoredDirTree creates count directories under root, all of them empty,
// as a stand-in for the tree whose size nobody controls: `node_modules`, a pnpm
// store, a build cache.
func writeIgnoredDirTree(t *testing.T, repo, root string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if err := os.MkdirAll(filepath.Join(repo, filepath.FromSlash(root), fmt.Sprintf("d%04d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// The sweep descends every root git collapsed, ignored ones included, and it
// prunes on nothing — for a reason that is still right, since every prune tried
// here was something the scanned repository could arrange. What that left was a
// traversal whose size is set by content git OMITS. Measured on 3c1353dd with 2
// indexed files and 110,120 directories under an ignored `node_modules/`, one
// working-tree listing took 2.31-2.39 s against 9-11 ms on main, linear at
// ~21 us per directory and paid again on every query.
//
// The ledger bounds it. This pins the bound itself: a tree far larger than the
// budget costs the budget and not the tree.
func TestSweepStopsAtItsDirectoryBudget(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "64")
	writeFile(t, repo, "src/app.go", "package app\n")
	writeIgnoredDirTree(t, repo, "node_modules", 400)

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.unlistedRoots = []string{"node_modules/"}
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths([]string{"src/app.go"}, nil)

	if excluder.sweepStop != sweepStoppedOnBudget {
		t.Fatalf("sweepStop = %d, want sweepStoppedOnBudget: a 400-directory tree must exhaust a 64-directory budget", excluder.sweepStop)
	}
	if excluder.sweepDirectories > 64 {
		t.Errorf("sweepDirectories = %d, want <= 64", excluder.sweepDirectories)
	}
	if excluder.directoriesRead > 64 {
		t.Errorf("directoriesRead = %d, want <= 64: the ledger bounds the reads as well as the observations", excluder.directoriesRead)
	}
}

// The ledger is ONE ledger. Splitting a huge ignored tree into many ignored
// trees is free for the repository being scanned, so a per-root allowance would
// be no allowance at all.
func TestSweepBudgetIsSharedAcrossRoots(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "64")
	writeFile(t, repo, "src/app.go", "package app\n")
	roots := make([]string, 0, 9)
	for i := 0; i < 8; i++ {
		root := fmt.Sprintf("cache%d", i)
		writeIgnoredDirTree(t, repo, root, 100)
		roots = append(roots, root+"/")
	}

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.unlistedRoots = roots
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths([]string{"src/app.go"}, nil)

	if excluder.sweepDirectories > 64 {
		t.Errorf("sweepDirectories = %d over 8 roots, want <= 64: eight roots must not buy eight allowances", excluder.sweepDirectories)
	}
}

// Exhaustion is not silence, and that is the whole reason the bound may exist at
// all. A prune says "there is nothing to find in here"; a spent ledger says
// "I did not look". It records hidden evidence, so promoteUnverifiedGitDirs
// excludes every observed directory carrying a git directory's structure — a
// SUPERSET of what the unread pointer could have named. Running the ledger out
// therefore buys the scanned repository a wider exclusion, not a hiding place.
func TestSweepBudgetExhaustionStillExcludesTheHiddenPointersTarget(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "8")
	writeFile(t, repo, "src/app.go", "package app\n")
	writeIgnoredDirTree(t, repo, "node_modules", 200)
	// The only pointer that names the HEAD-damaged target is buried past the
	// budget, in the tree the ledger will run out inside.
	writeFile(t, repo, "node_modules/d0199/dep/.git", "gitdir: ../../../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.unlistedRoots = []string{"node_modules/", ".dep-git/"}
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths([]string{".dep-git/config", "src/app.go"}, nil)

	if excluder.sweepStop != sweepStoppedOnBudget {
		t.Fatalf("sweepStop = %d, want sweepStoppedOnBudget", excluder.sweepStop)
	}
	if !excluder.excluded(".dep-git/config") {
		t.Error(`excluded(".dep-git/config") = false, want true: a sweep that ran out of budget must fail closed, not fall silent`)
	}
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false: failing closed narrows one subtree, not the search`)
	}
}

// The same, end to end through the real `git ls-files` listings and the real
// `search` verb, with a budget too small to reach the pointer: the planted
// credential must not reach a snippet and the source beside the tree must still
// be findable.
func TestSearchNeverLeaksAGitDirWhenTheSweepRunsOutOfBudget(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "8")
	initRepo(t, repo)
	writeFile(t, repo, "src/app.go", "package app\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, ".gitignore", "node_modules/\n")
	git(t, repo, "add", "src/app.go", ".gitignore")
	git(t, repo, "commit", "-m", "src")
	writeIgnoredDirTree(t, repo, "node_modules", 200)
	writeFile(t, repo, "node_modules/d0199/dep/.git", "gitdir: ../../../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	assertNoGitDirLeak(t, repo, ".dep-git")
	assertSearchFinds(t, repo, "src/app.go")
}

// And on the filesystem fallback, where the ledger is spent by
// observePrunedSubtree instead. The repository is deliberately NOT a git
// repository, so `git ls-files` fails and walkWorktreeFiles runs.
func TestSearchNeverLeaksAGitDirWhenTheSweepRunsOutOfBudgetOnTheFallback(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "8")
	writeFile(t, repo, "src/app.go", "package app\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, ".gitignore", "node_modules/\n")
	writeIgnoredDirTree(t, repo, "node_modules", 200)
	writeFile(t, repo, "node_modules/d0199/dep/.git", "gitdir: ../../../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	assertNoGitDirLeak(t, repo, ".dep-git")
	assertSearchFinds(t, repo, "src/app.go")
}

// An incomplete sweep changes what the listing MEANS — the exclusions under it
// are wider than a complete sweep's, and the directory count is a lower bound on
// the tree — so it is disclosed rather than inferred from a short listing.
func TestExhaustedSweepIsDisclosedAsAWarning(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "8")
	writeFile(t, repo, "src/app.go", "package app\n")
	writeFile(t, repo, ".gitignore", "node_modules/\n")
	writeIgnoredDirTree(t, repo, "node_modules", 200)

	_, warnings, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var found *ProviderWarning
	for i := range warnings {
		if warnings[i].Code == "W_GITDIR_SWEEP_BUDGET" {
			found = &warnings[i]
		}
	}
	if found == nil {
		t.Fatalf("no W_GITDIR_SWEEP_BUDGET warning; warnings = %v", warnings)
	}
	if !strings.Contains(found.Detail, sweepDirBudgetEnv) {
		t.Errorf("warning detail does not name the override %q: %q", sweepDirBudgetEnv, found.Detail)
	}
}

// A repository of ordinary size spends nothing and is told nothing: the whole
// point of the budget is that no real checkout meets it.
func TestCompleteSweepDisclosesNothing(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/app.go", "package app\n")
	writeIgnoredDirTree(t, repo, "node_modules", 50)

	_, warnings, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range warnings {
		if strings.HasPrefix(warning.Code, "W_GITDIR_SWEEP") {
			t.Errorf("a sweep that finished reported %q", warning.Code)
		}
	}
}

// The sweep observes the caller's context, and a cancelled one stops it the same
// fail-closed way the ledger does. Without this, a caller who has given up still
// waits out the whole traversal.
func TestCancelledSweepStopsAndFailsClosed(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/app.go", "package app\n")
	writeIgnoredDirTree(t, repo, "node_modules", 200)
	writeFile(t, repo, "node_modules/d0199/dep/.git", "gitdir: ../../../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	excluder := newGitDirExcluder(ctx, repo)
	excluder.unlistedRoots = []string{"node_modules/", ".dep-git/"}
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths([]string{".dep-git/config", "src/app.go"}, nil)

	if excluder.sweepStop != sweepStoppedOnCancel {
		t.Fatalf("sweepStop = %d, want sweepStoppedOnCancel", excluder.sweepStop)
	}
	if excluder.sweepDirectories > sweepCancelCheckInterval {
		t.Errorf("sweepDirectories = %d, want <= %d: a cancelled sweep must stop within one check interval", excluder.sweepDirectories, sweepCancelCheckInterval)
	}
	if !excluder.excluded(".dep-git/config") {
		t.Error(`excluded(".dep-git/config") = false, want true: a cancelled sweep must fail closed too`)
	}
	if warnings := excluder.sweepWarnings(); len(warnings) != 1 || warnings[0].Code != "W_GITDIR_SWEEP_CANCELLED" {
		t.Errorf("sweepWarnings() = %v, want one W_GITDIR_SWEEP_CANCELLED", warnings)
	}
}

// WHICH directories the sweep got to is part of the answer once a ledger is in
// play, so it must not differ between two runs over the same tree. The derived
// fallback used to range a map, which made it differ.
func TestExhaustedSweepIsDeterministic(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "37")
	writeFile(t, repo, "src/app.go", "package app\n")
	writeIgnoredDirTree(t, repo, "node_modules", 200)

	observed := func() string {
		excluder := newGitDirExcluder(t.Context(), repo)
		excluder.unlistedRoots = []string{"node_modules/"}
		excluder.gitAnsweredRoots = true
		excluder.observeListedPaths([]string{"src/app.go"}, nil)
		return strings.Join(excluder.observedDirs, "\n")
	}
	if first, second := observed(), observed(); first != second {
		t.Errorf("two sweeps over the same tree observed different directories:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// The derived-roots fallback — git could not answer, so the roots are the listed
// directories themselves — spends the same ledger. It is the branch that reads
// the whole tree, so leaving it uncharged would leave the finding open on every
// repository without a usable git binary.
func TestDerivedSweepRootsSpendTheSameLedger(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(sweepDirBudgetEnv, "16")
	writeFile(t, repo, "src/app.go", "package app\n")
	writeIgnoredDirTree(t, repo, "node_modules", 200)

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.gitAnsweredRoots = false
	excluder.observeListedPaths([]string{"src/app.go"}, nil)

	if excluder.sweepDirectories > 16 {
		t.Errorf("sweepDirectories = %d, want <= 16 on the derived-roots branch", excluder.sweepDirectories)
	}
	if excluder.sweepStop != sweepStoppedOnBudget {
		t.Errorf("sweepStop = %d, want sweepStoppedOnBudget", excluder.sweepStop)
	}
}

// The override is read from the environment, and anything unparseable falls back
// to the default rather than silently unbounding the sweep.
func TestResolveSweepDirectoryBudget(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		set   bool
		value string
		want  int
	}{
		{"unset", false, "", defaultSweepDirectoryBudget},
		{"override", true, "512", 512},
		{"padded", true, "  512  ", 512},
		{"unbounded", true, "0", 0},
		{"garbage", true, "many", defaultSweepDirectoryBudget},
		{"empty", true, "", defaultSweepDirectoryBudget},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// t.Setenv first in both branches: it is what registers the
			// restore, so the unset case cannot leak a cleared variable into
			// the tests that follow.
			t.Setenv(sweepDirBudgetEnv, testCase.value)
			if !testCase.set {
				if err := os.Unsetenv(sweepDirBudgetEnv); err != nil {
					t.Fatal(err)
				}
			}
			if got := resolveSweepDirectoryBudget(); got != testCase.want {
				t.Errorf("resolveSweepDirectoryBudget() = %d, want %d", got, testCase.want)
			}
		})
	}
}
