package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLooksLikeGitDirAcceptsSymlinkedCommondir pins the `commondir` read to what
// git actually accepts. git's get_common_dir() reads the file through the
// ordinary stdio path, so a `commondir` that is a SYMLINK to a regular file is
// followed and the directory is a git directory. Verified on git 2.54.0:
//
//	$ ln -s ../realcommondir adm/commondir
//	$ git --git-dir=adm rev-parse --git-dir
//	adm
//	$ git --git-dir=adm rev-parse --git-common-dir
//	.../main/.git
//
// Refusing it (os.Lstat plus a regular-file requirement) calls a real linked
// worktree's administrative git directory ordinary content, and its config and
// hooks stay indexable.
func TestLooksLikeGitDirAcceptsSymlinkedCommondir(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege ordinary Windows accounts lack")
	}
	dir := t.TempDir()
	writeGitDirFixture(t, dir, "common")
	writeFile(t, dir, "d/HEAD", "ref: refs/heads/main\n")
	writeFile(t, dir, "realcommondir", "../common\n")
	if err := os.Symlink(filepath.Join("..", "realcommondir"), filepath.Join(dir, "d", "commondir")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if !looksLikeGitDir(filepath.Join(dir, "d")) {
		t.Error("looksLikeGitDir = false for a git directory whose commondir is a symlink, want true")
	}
}

// TestSearchRepositoryNeverIndexesLinkedWorktreeGitDirWithSymlinkedCommondir is
// the same shape end to end on the filesystem fallback, where the structural
// rule is the only one in reach: the administrative directory is not named
// `.git` and no pointer names it, so refusing its symlinked `commondir` puts its
// credentialed config straight into a ranked snippet.
func TestSearchRepositoryNeverIndexesLinkedWorktreeGitDirWithSymlinkedCommondir(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege ordinary Windows accounts lack")
	}
	repo := t.TempDir()
	writeGitDirFixture(t, repo, "shared")
	writeFile(t, repo, "state/.wt-git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, repo, "state/.wt-git/config", gitDirConfigWithCredential)
	writeFile(t, repo, "state/.wt-git/hooks/post-commit.go", gitDirHookSource)
	writeFile(t, repo, "state/realcommondir", "../../shared\n")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	if err := os.Symlink(filepath.Join("..", "realcommondir"), filepath.Join(repo, "state", ".wt-git", "commondir")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	assertNoGitDirLeak(t, repo, "state/.wt-git", "shared")
}

// TestGitDirExcluderKeepsSourceNamedByAGitfileGitRejects is the over-exclusion
// side of the pointer rule. A `.git` FILE whose syntax git accepts still names
// nothing unless git accepts the TARGET as a repository — is_git_directory().
// Verified on git 2.54.0, where `src` is an ordinary Go package:
//
//	$ printf 'gitdir: ../src\n' > nested/.git
//	$ git -C nested rev-parse --git-dir
//	fatal: not a git repository: (null)
//
// Excluding on the pointer alone lets any file named `.git` — a fixture, a
// stray note, attacker-authored repository content — delete an arbitrary source
// tree from the index.
func TestGitDirExcluderKeepsSourceNamedByAGitfileGitRejects(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "nested/.git", "gitdir: ../src\n")
	writeFile(t, repo, "src/app.go", "package src\n")
	writeFile(t, repo, "src/lib.go", "package src\n")
	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{"src/app.go", "src/lib.go"}, nil)
	for _, rel := range []string{"src/app.go", "src/lib.go"} {
		if excluder.excluded(rel) {
			t.Errorf("excluded(%q) = true, want false: git rejects `gitdir: ../src`", rel)
		}
	}
}

// TestSearchRepositoryStillIndexesSourceNamedByAGitfileGitRejects is the same
// over-exclusion end to end: the planted pointer must not make the repository's
// only source file unfindable.
func TestSearchRepositoryStillIndexesSourceNamedByAGitfileGitRejects(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "nested/.git", "gitdir: ../src\n")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	response, err := SearchRepository(t.Context(), repo, "test", "origin remote credential loader", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	var got []string
	for _, result := range response.Results {
		got = append(got, result.FilePath)
		if result.FilePath == "src/app.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("search did not return src/app.go; results = %v", got)
	}
}

// TestGitDirExcluderStillExcludesAGitDirNamedByAPointer is the other direction
// of the same narrowing: a target git DOES treat as a repository — and a real
// `--separate-git-dir` target whose HEAD has been damaged, which is precisely
// the state that makes git refuse the worktree and the filesystem fallback run
// — must stay excluded, credentials and all.
func TestGitDirExcluderStillExcludesAGitDirNamedByAPointer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "nested/.git", "gitdir: ../.repo-git\n")
	writeGitDirFixture(t, repo, ".repo-git")
	writeFile(t, repo, "headless/.git", "gitdir: ../.dead-git\n")
	writeGitDirFixture(t, repo, ".dead-git")
	if err := os.Remove(filepath.Join(repo, ".dead-git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "src/app.go", "package src\n")
	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{".repo-git/config", ".dead-git/config", "src/app.go"}, nil)
	for _, rel := range []string{".repo-git/config", ".dead-git/config"} {
		if !excluder.excluded(rel) {
			t.Errorf("excluded(%q) = false, want true", rel)
		}
	}
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false`)
	}
}

// TestGitDirExcluderSweepReadsOnlyTheDirectoriesGitNames is the cost bound on
// the unlisted-directory sweep. Given git's own answer for the directories it
// lists nothing under, the sweep reads those and their descendants — not every
// ancestor of every listed path, and not the ignored build and cache trees that
// git already excluded.
//
// Deriving them instead made the successful git-listing path traverse the whole
// tree a second time: on a 5,946-directory fixture whose 501 listed source files
// sit beside ignored build/ and .cache/ trees, one cold `search` went from
// 0.12 s to 0.43 s.
func TestGitDirExcluderSweepReadsOnlyTheDirectoriesGitNames(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var listed []string
	for pkg := range 8 {
		rel := fmt.Sprintf("src/pkg%d/app.go", pkg)
		writeFile(t, repo, rel, "package src\n")
		listed = append(listed, rel)
	}
	// A fully ignored, non-vendored build tree: git's listing never mentions it.
	for object := range 32 {
		writeFile(t, repo, fmt.Sprintf("build/o%d/x/y/a.o", object), "")
	}
	writeFile(t, repo, "nested/.git", "gitdir: ../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")
	excluder := newGitDirExcluder(repo)
	// Exactly what `git ls-files --cached --others --exclude-standard
	// --directory` reports for this tree: `nested/` as an entry of its own, and
	// nothing at all about the ignored build tree.
	excluder.unlistedRoots = []string{"nested/"}
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths(listed, nil)
	if !excluder.excluded(".dep-git/config") {
		t.Error(`excluded(".dep-git/config") = false, want true: the suppressed pointer must still be read`)
	}
	if excluder.excluded("src/pkg0/app.go") {
		t.Error(`excluded("src/pkg0/app.go") = true, want false`)
	}
	// `nested` itself, and nothing else: no ancestor of a listed path, no
	// directory of the ignored build tree.
	if excluder.directoriesRead != 1 {
		t.Errorf("directoriesRead = %d, want 1 (only the directory git named)", excluder.directoriesRead)
	}
}
