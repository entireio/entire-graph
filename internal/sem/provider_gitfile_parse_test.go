package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitDirPointerTargetMatchesGitsOwnGitfileParse pins the `.git` FILE parse
// to git's own read_gitfile_gently(), which checks the prefix `gitdir: ` —
// space included — against BYTE 0 of the file and then strips only trailing
// newline bytes. Every other byte after the prefix, a leading or trailing SPACE
// included, is part of the path.
//
// Trimming the whole buffer before the prefix check disagreed with git in both
// directions, and each direction is a bug of its own: the lenient half accepted
// gitfiles git refuses outright, so an ordinary text file named `.git`
// suppressed an unrelated source tree from the index; the eager half deleted
// spaces git keeps, so a real `--separate-git-dir` whose directory name ends in
// a space was recorded under a name nothing on disk has.
//
// The wants below were taken from git 2.54.0 rather than from its documentation:
//
//	$ git init --separate-git-dir='/tmp/x/trailspace ' wt2
//	$ git -C wt2 rev-parse --git-dir
//	/tmp/x/trailspace          <- the trailing space survives
//	$ printf 'gitdir: /tmp/x/realgit\n'  > wt/.git ; git -C wt rev-parse --git-dir
//	/tmp/x/realgit
//	$ printf ' gitdir: /tmp/x/realgit\n' > wt/.git ; git -C wt rev-parse --git-dir
//	fatal: invalid gitfile format: /tmp/x/wt/.git
//	$ printf 'gitdir:/tmp/x/realgit\n'   > wt/.git ; git -C wt rev-parse --git-dir
//	fatal: invalid gitfile format: /tmp/x/wt/.git
//	$ printf 'gitdir:  /tmp/x/realgit\n' > wt/.git ; git -C wt rev-parse --git-dir
//	fatal: not a git repository                    <- the extra space joined the path
//	$ printf 'gitdir: /tmp/x/realgit \n' > wt/.git ; git -C wt rev-parse --git-dir
//	fatal: not a git repository                    <- ditto, at the other end
//	$ printf 'gitdir: /tmp/x/realgit\r\n' > wt/.git; git -C wt rev-parse --git-dir
//	/tmp/x/realgit                                 <- CR is stripped, like LF
func TestGitDirPointerTargetMatchesGitsOwnGitfileParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"canonical pointer", "gitdir: .repo-git\n", ".repo-git"},
		{"no trailing newline", "gitdir: .repo-git", ".repo-git"},
		{"crlf line ending", "gitdir: .repo-git\r\n", ".repo-git"},
		{"several trailing newlines", "gitdir: .repo-git\n\n", ".repo-git"},
		// Rejected by git: the prefix must start at byte 0.
		{"space before the prefix", " gitdir: .repo-git\n", ""},
		{"tab before the prefix", "\tgitdir: .repo-git\n", ""},
		{"newline before the prefix", "\ngitdir: .repo-git\n", ""},
		// Rejected by git: the space after the colon is part of the prefix.
		{"no space after the colon", "gitdir:.repo-git\n", ""},
		// Accepted by git, but the surplus whitespace is PATH, not padding.
		{"second space belongs to the path", "gitdir:  .repo-git\n", " .repo-git"},
		{"trailing space belongs to the path", "gitdir: .repo-git \n", ".repo-git "},
		{"trailing tab belongs to the path", "gitdir: .repo-git\t\n", ".repo-git\t"},
		// Prefix and nothing else: git resolves it to the directory holding the
		// pointer only to reject it in is_git_directory(). Reported as absent
		// rather than as a hit, which would exclude that whole directory.
		{"prefix and nothing else", "gitdir: \n", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, ".git", testCase.content)
			got, ok, hidden := gitDirPointerTarget(repo, "")
			if ok != (testCase.want != "") || hidden || got != testCase.want {
				t.Errorf("gitDirPointerTarget(%q) = (%q, %v, hidden %v), want (%q, %v, false)",
					testCase.content, got, ok, hidden, testCase.want, testCase.want != "")
			}
		})
	}
}

// TestGitInfoExcludePathIgnoresAGitfileGitRejects covers the SECOND reader of
// the same bytes. gitInfoExcludePath parsed the pointer with its own, equally
// lenient rule, so a `.git` text file git refuses to parse still steered the
// worktree's exclude rules at a directory of the file's choosing — git applies
// no info/exclude at all there. Both readers now share one parser.
func TestGitInfoExcludePathIgnoresAGitfileGitRejects(t *testing.T) {
	t.Parallel()
	for _, content := range []string{" gitdir: elsewhere\n", "\tgitdir: elsewhere\n", "gitdir:elsewhere\n"} {
		t.Run(strings.TrimSpace(content), func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, ".git", content)
			if got := gitInfoExcludePath(repo); got != "" {
				t.Errorf("gitInfoExcludePath = %q, want \"\" for a gitfile git rejects", got)
			}
		})
	}
}

// TestSearchRepositoryStillIndexesSourceBesideAGitfileGitRejects is the
// over-suppression half, end to end through the real verb. A regular file named
// `.git` that git refuses to parse is ordinary repository content — a fixture, a
// stray note — and it must not delete a source tree from the index. Each
// spelling below made `internal/app.go`, the fixture's ONLY source file,
// unfindable: the search returned nothing at all.
func TestSearchRepositoryStillIndexesSourceBesideAGitfileGitRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		dir     string
		content string
	}{
		{"space before the prefix", "", " gitdir: internal\n"},
		{"no space after the colon", "", "gitdir:internal\n"},
		{"second space joins the path", "", "gitdir:  internal\n"},
		{"nested fixture climbing into source", "testdata/gitfiles", "gitdir:../../internal\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, filepath.ToSlash(filepath.Join(testCase.dir, ".git")), testCase.content)
			writeFile(t, repo, "internal/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

			response, err := SearchRepository(t.Context(), repo, "test", "origin remote credential loader", SearchOptions{
				Worktree: true,
				Profile:  ProfileSyntaxOnly,
				TopK:     10,
			})
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, result := range response.Results {
				if result.FilePath == "internal/app.go" {
					found = true
				}
			}
			if !found {
				var got []string
				for _, result := range response.Results {
					got = append(got, result.FilePath)
				}
				t.Errorf("search did not return internal/app.go; results = %v", got)
			}
		})
	}
}

// TestSearchRepositoryNeverIndexesGitDirWhosePointerEndsInASpace is the
// under-detection half, end to end. `git init --separate-git-dir='.repo-git '`
// is accepted by git and its pointer keeps the trailing space; trimming it
// recorded `.repo-git`, which nothing on disk is called, and the real directory
// was walked. The fixture carries no HEAD, so the structural rule is blind and
// the POINTER is the only evidence — exactly the complementarity the guard
// claims.
func TestSearchRepositoryNeverIndexesGitDirWhosePointerEndsInASpace(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if !trailingSpaceNameTestFS(t, repo) {
		t.Skip("filesystem does not store a trailing space in a name (Win32 strips it), so this git directory is unrepresentable here")
	}
	writeFile(t, repo, ".git", "gitdir: .repo-git \n")
	writeHeadlessGitDirFixture(t, repo, ".repo-git ")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, ".repo-git ")
}

// trailingSpaceNameTestFS reports whether dir's filesystem stores a directory
// name ending in a space under exactly that name. This is a property of the
// filesystem rather than of the OS — Unix filesystems keep the space, Win32
// strips trailing spaces from a path component — so it is probed, the way
// caseFoldingTestFS probes folding, rather than guessed from runtime.GOOS.
func trailingSpaceNameTestFS(t *testing.T, dir string) bool {
	t.Helper()
	const name = "tf132-space-probe "
	probe := filepath.Join(dir, name)
	if err := os.Mkdir(probe, 0o755); err != nil {
		return false
	}
	defer func() {
		_ = os.RemoveAll(probe)
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}
