package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLooksLikeGitDirResolvesObjectsAndRefsThroughCommondir pins looksLikeGitDir
// to git's own is_git_directory(), which resolves `objects` and `refs` through
// get_common_dir() — the `commondir` file — rather than inside the candidate.
// Both directions matter: a linked worktree's administrative git directory holds
// HEAD and commondir but no local objects/refs and IS a git directory to git,
// while a source fixture carrying HEAD plus local objects/refs and a stale
// commondir is NOT one and must stay indexable.
func TestLooksLikeGitDirResolvesObjectsAndRefsThroughCommondir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  bool
	}{
		{"linked worktree gitdir: HEAD plus commondir, no local objects or refs", func(t *testing.T, dir string) {
			writeGitDirFixture(t, dir, "common")
			writeFile(t, dir, "d/HEAD", "ref: refs/heads/main\n")
			writeFile(t, dir, "d/commondir", "../common\n")
		}, true},
		{"stale commondir: HEAD and local objects/refs, commondir names nothing", func(t *testing.T, dir string) {
			writeGitDirFixture(t, dir, "d")
			writeFile(t, dir, "d/commondir", "../absent\n")
		}, false},
		// A `commondir` git READS and finds empty is not one git fails to read:
		// git only dies when it got nothing out of the file at all, so a bare
		// newline leaves the common directory at the git directory itself and
		// git accepts it. Verified on git 2.54.0, with objects/ and refs/ beside
		// it: `printf '\n' > admG/commondir; git --git-dir=admG rev-parse
		// --git-common-dir` answers `<tmp>/admG`, and `--git-dir` answers
		// `admG`.
		{"newline-only commondir resolves to the git directory itself", func(t *testing.T, dir string) {
			writeGitDirFixture(t, dir, "d")
			writeFile(t, dir, "d/commondir", "\n")
		}, true},
		// A NUL as the first byte is the same case by the byte rule: the path
		// git hands on is empty. `printf '\0junkjunk\n' > admE/commondir` and
		// `git --git-dir=admE rev-parse --git-dir` answers `admE`.
		{"commondir whose path is empty at the first NUL", func(t *testing.T, dir string) {
			writeGitDirFixture(t, dir, "d")
			writeFile(t, dir, "d/commondir", "\x00junkjunk\n")
		}, true},
		// The zero-byte file is the read git gets nothing out of, and it dies:
		// `fatal: failed to read admF/commondir: No such file or directory`.
		{"zero-byte commondir is one git refuses to read", func(t *testing.T, dir string) {
			writeGitDirFixture(t, dir, "d")
			writeFile(t, dir, "d/commondir", "")
		}, false},
		{"absolute commondir", func(t *testing.T, dir string) {
			writeGitDirFixture(t, dir, "common")
			writeFile(t, dir, "d/HEAD", "ref: refs/heads/main\n")
			writeFile(t, dir, "d/commondir", filepath.Join(dir, "common")+"\n")
		}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			testCase.setup(t, dir)
			if got := looksLikeGitDir(filepath.Join(dir, "d")); got != testCase.want {
				t.Errorf("looksLikeGitDir = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestGitDirExcluderExcludesCaseFoldedGitDirName is the alias exploit: on a
// case-folding filesystem (macOS APFS/HFS+, Windows) a directory physically
// named `.GIT` IS the repository's git directory, and reading `.GIT/config`
// reads `.git/config`. The component test is case-sensitive and the structural
// test needs a HEAD, so an incomplete or corrupt `.GIT` slipped through both.
func TestGitDirExcluderExcludesCaseFoldedGitDirName(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, ".GIT/config", gitDirConfigWithCredential)
	writeFile(t, repo, ".GIT/hooks/post-commit.go", "package hooks\n")
	writeFile(t, repo, "src/app.go", "package src\n")
	if !foldsCase(repo, ".GIT") {
		t.Skip("filesystem is case-sensitive: `.GIT` is a distinct directory, not an alias of `.git`")
	}
	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{".GIT/config", ".GIT/hooks/post-commit.go", "src/app.go"}, nil)
	for _, rel := range []string{".GIT/config", ".GIT/hooks/post-commit.go"} {
		if !excluder.excluded(rel) {
			t.Errorf("excluded(%q) = false, want true", rel)
		}
	}
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false`)
	}
}

// TestGitDirExcluderObservesDirectoriesGitListsNothingUnder is the suppressed-
// pointer exploit. Git's listing omits every `.git` entry, so a directory whose
// entire content is a `.git` pointer file produces NO listed path: observing
// only listed paths and their ancestors never reaches it, and the in-repository
// target it names — which git does list in full — stayed indexable whenever the
// target lacks the structural HEAD/objects/refs signature.
func TestGitDirExcluderObservesDirectoriesGitListsNothingUnder(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// `nested/` holds nothing but the pointer, so git reports nothing for it.
	writeFile(t, repo, "nested/.git", "gitdir: ../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")
	writeFile(t, repo, "src/app.go", "package src\n")
	// Exactly what `git ls-files --cached --others --exclude-standard` reports:
	// no entry mentions `nested` at all.
	listed := []string{".dep-git/config", ".dep-git/hooks/post-commit.go", "src/app.go"}
	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths(listed, nil)
	for _, rel := range []string{".dep-git/config", ".dep-git/hooks/post-commit.go"} {
		if !excluder.excluded(rel) {
			t.Errorf("excluded(%q) = false, want true", rel)
		}
	}
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false`)
	}
	if excluder.excluded("nested/lib.go") {
		t.Error(`excluded("nested/lib.go") = true, want false`)
	}
}

// TestGitDirExcluderSweepSkipsVendoredTrees keeps the unlisted-directory sweep
// off trees the scanner does not index anyway, so a fully ignored dependency
// tree is not enumerated for pointers it can never leak through.
func TestGitDirExcluderSweepSkipsVendoredTrees(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "node_modules/dep/lib.js", "module.exports = {}\n")
	writeFile(t, repo, "src/app.go", "package src\n")
	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{"src/app.go"}, nil)
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false`)
	}
	if _, err := os.Stat(filepath.Join(repo, "node_modules")); err != nil {
		t.Fatal(err)
	}
}
