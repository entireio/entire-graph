package sem

import (
	"errors"
	"testing"
)

// The sweep descends from the roots GIT names, and observeUnlistedDirs skips the
// whole-tree fallback whenever gitAnsweredRoots says git answered. So "answered"
// must mean BOTH listings answered.
//
// It used to mean only the first. `git ls-files --cached --others
// --exclude-standard --directory` is the listing that nearly always works, and
// on its success alone the flag went true — after which a failure of `git
// ls-files --others --ignored --exclude-standard --directory` cost the sweep
// every ignored tree AND the fallback that would have covered them. A `.git`
// pointer under an ignored `build/` then went unread while the git directory it
// names, HEAD-less and listed by git in full, was indexed with its credentialed
// config: exactly the leak the ignored listing was added to close, restored by
// one silent error.
//
// The two commands differ by one flag, so whatever can fail the second — a spawn
// refused under fd or memory pressure, a killed child, a git that does not know
// the flag — fails it after the first has already succeeded.
func TestGitSweepRootsRefusesToClaimGitAnsweredUnlessBothListingsDid(t *testing.T) {
	t.Parallel()
	boom := errors.New("git ls-files: signal: killed")
	cases := []struct {
		name         string
		dirErr       error
		ignoredErr   error
		wantAnswered bool
		wantRoots    int
	}{
		{"both answered", nil, nil, true, 3},
		{"ignored listing failed", nil, boom, false, 0},
		{"plain listing failed", boom, nil, false, 0},
		{"both failed", boom, boom, false, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			roots, answered := gitSweepRoots(
				[]string{"pkg/", "tracked.go"}, testCase.dirErr,
				[]string{"build/"}, testCase.ignoredErr,
			)
			if answered != testCase.wantAnswered {
				t.Errorf("gitSweepRoots answered = %v, want %v", answered, testCase.wantAnswered)
			}
			if len(roots) != testCase.wantRoots {
				t.Errorf("gitSweepRoots roots = %v (%d), want %d", roots, len(roots), testCase.wantRoots)
			}
		})
	}
}

// And the consequence the flag controls: with git NOT having answered, the sweep
// must still reach a `.git` pointer buried in an ignored tree. This is the
// fallback the false "answered" skipped, so it has to actually work.
func TestGitDirExcluderSweepsAnIgnoredTreeWhenGitDidNotAnswer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, ".dep-git")
	writeFile(t, repo, "build/dep/.git", "gitdir: ../../.dep-git\n")
	writeFile(t, repo, "pkg/source.go", "package pkg\n")

	excluder := newGitDirExcluder(t.Context(), repo)
	// gitAnsweredRoots deliberately left false: this is the state gitSweepRoots
	// now reports when either listing failed.
	excluder.observeListedPaths([]string{"pkg/source.go", ".dep-git/config", ".gitignore"}, nil)
	if !excluder.excluded(".dep-git/config") {
		t.Error("excluded(.dep-git/config) = false, want true: the whole-tree fallback must reach build/dep/.git")
	}
	if excluder.excluded("pkg/source.go") {
		t.Error("excluded(pkg/source.go) = true, want false")
	}
}
