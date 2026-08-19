package sem

import "testing"

// Git reads a `.git` pointer into a NUL-terminated buffer and hands the target
// on as a C string, so the FIRST NUL byte ends the path — everything after it is
// not part of the name and not an error either. Verified on git 2.54.0 with
// `printf 'gitdir: ../.repo-git\0junkjunk\n' > .git`:
//
//	$ git rev-parse --git-dir
//	<parent>/.repo-git
//	$ git status --short
//	(exit 0)
//
// and, with the target inside the worktree (`gitdir: .repo-git\0junk`):
//
//	$ git ls-files --cached --others --exclude-standard --directory
//	.repo-git/
//	tracked.go
//
// So the pointer is accepted, the target is resolved without the suffix, and its
// whole content — config with the credentialed remote URL, hooks — is listed as
// ordinary untracked source. Keeping the NUL and the suffix produced a name
// nothing on disk is called: the structure test could not find it, no target was
// recorded, and the git directory was indexed.
func TestParseGitDirPointerStopsAtNUL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    string
		wantOK  bool
	}{
		{"NUL ends the target", "gitdir: .repo-git\x00junk\n", ".repo-git", true},
		{"NUL before the newline trim", "gitdir: .repo-git\x00junk", ".repo-git", true},
		{"NUL only", "gitdir: \x00.repo-git\n", "", false},
		{"trailing space survives ahead of a NUL", "gitdir: .repo-git \x00junk\n", ".repo-git ", true},
		{"no NUL is unchanged", "gitdir: .repo-git\n", ".repo-git", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseGitDirPointer([]byte(testCase.content))
			if ok != testCase.wantOK || got != testCase.want {
				t.Errorf("parseGitDirPointer(%q) = %q, %v; want %q, %v", testCase.content, got, ok, testCase.want, testCase.wantOK)
			}
		})
	}
}

// The same exploit end to end, on the filesystem fallback: the target's HEAD is
// damaged, which is what makes git refuse the worktree and this path run, and
// which leaves the pointer as the only rule that can reach the directory.
func TestSearchRepositoryNeverIndexesGitDirNamedByANULTerminatedPointer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, ".git", "gitdir: .repo-git\x00junkjunk\n")
	writeHeadlessGitDirFixture(t, repo, ".repo-git")

	assertNoGitDirLeak(t, repo, ".repo-git")
}

// And on the excluder directly, with git's own listing of the exploit above.
func TestGitDirExcluderResolvesANULTerminatedPointer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "tracked.go", "package tracked\n")
	writeFile(t, repo, ".git", "gitdir: .repo-git\x00junkjunk\n")
	writeHeadlessGitDirFixture(t, repo, ".repo-git")

	excluder := newGitDirExcluder(repo)
	excluder.unlistedRoots = []string{".repo-git/", "tracked.go"}
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths([]string{".repo-git/config", "tracked.go"}, nil)

	if !excluder.excluded(".repo-git/config") {
		t.Error(`excluded(".repo-git/config") = false, want true: the NUL-terminated pointer names it`)
	}
	if excluder.excluded("tracked.go") {
		t.Error(`excluded("tracked.go") = true, want false`)
	}
}
