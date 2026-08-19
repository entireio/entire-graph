package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// symlinkOrSkip makes one symlink for these fixtures, skipping only where the
// machine actually refuses to create one. The attempt itself is the probe: a
// runtime.GOOS test would skip every Windows run, including the ones where the
// account holds SeCreateSymbolicLinkPrivilege and the fixture is representable.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}

// TestGitDirPointerTargetRecordsSymlinkResolvedTarget pins the pointer rule to
// the path git itself resolves. git's read_gitfile_gently() ends in
// real_pathdup(), so a `gitdir:` naming an in-tree SYMLINK names the directory
// the link points at, not the link. Verified on git 2.54.0:
//
//	$ git init --separate-git-dir=.real-git .
//	$ ln -s .real-git admin-link && printf 'gitdir: admin-link\n' > .git
//	$ git rev-parse --git-dir
//	<worktree>/.real-git
//
// Recording only the link's own spelling leaves the real git directory outside
// every target, and the structural rule cannot rescue it: a `--separate-git-dir`
// target whose HEAD is missing or corrupt is `not a git repository` to git,
// which is the state that makes git refuse the worktree in the first place.
func TestGitDirPointerTargetRecordsSymlinkResolvedTarget(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, ".real-git")
	symlinkOrSkip(t, ".real-git", filepath.Join(repo, "admin-link"))
	writeFile(t, repo, ".git", "gitdir: admin-link\n")

	excluder := newGitDirExcluder(repo)
	if !excluder.excluded(".real-git/config") {
		t.Error("excluded(.real-git/config) = false for the directory the gitfile's symlinked target resolves to, want true")
	}
	if !excluder.excluded(".real-git/hooks/post-commit.go") {
		t.Error("excluded(.real-git/hooks/post-commit.go) = false, want true")
	}
}

// TestGitDirExcluderResolvesNestedSymlinkedPointerTarget is the same shape one
// level down, where no listing ever mentions the link: a vendored dependency's
// `.git` pointer names a sibling symlink and the real git directory carries an
// ordinary name elsewhere in the tree.
func TestGitDirExcluderResolvesNestedSymlinkedPointerTarget(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, "state/.dep-git")
	writeFile(t, repo, "libs/dep/.git", "gitdir: ../admin-link\n")
	writeFile(t, repo, "src/app.go", "package src\n")
	symlinkOrSkip(t, filepath.Join("..", "state", ".dep-git"), filepath.Join(repo, "libs", "admin-link"))

	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{"src/app.go", "state/.dep-git/config", "libs/dep"}, []string{"libs/dep"})
	if !excluder.excluded("state/.dep-git/config") {
		t.Error("excluded(state/.dep-git/config) = false for the directory the nested gitfile's symlinked target resolves to, want true")
	}
	if excluder.excluded("src/app.go") {
		t.Error("excluded(src/app.go) = true, want false: ordinary source must stay listable")
	}
}

// TestSearchRepositoryNeverIndexesSymlinkedSeparateGitDir is the end-to-end
// exploit: the credential in the real git directory's config must not come back
// as a ranked search snippet when the `.git` pointer reaches it through a link.
func TestSearchRepositoryNeverIndexesSymlinkedSeparateGitDir(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, ".real-git")
	symlinkOrSkip(t, ".real-git", filepath.Join(repo, "admin-link"))
	writeFile(t, repo, ".git", "gitdir: admin-link\n")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, ".real-git")
}

// TestGitDirExcluderKeepsSymlinkTargetOutsideRepoUnexcluded is the widening
// direction of the same rule. A link that leaves the repository names nothing
// this listing contains, so resolving it must add no target: the ordinary
// linked-worktree shape, whose git directory lives in the main checkout.
//
// filepath.Join cleans `..` LEXICALLY, before any link resolves, so the
// containment test has to run on the RESOLVED path or a link pointing out of
// the tree is recorded as an in-tree directory and deletes it from the index.
func TestGitDirExcluderKeepsSymlinkTargetOutsideRepoUnexcluded(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	repo := filepath.Join(base, "wt")
	writeFile(t, repo, "src/app.go", "package src\n")
	// A real git directory OUTSIDE the repository, and an in-repo link to it.
	writeHeadlessGitDirFixture(t, base, "outside-git")
	symlinkOrSkip(t, filepath.Join("..", "outside-git"), filepath.Join(repo, "admin-link"))
	writeFile(t, repo, ".git", "gitdir: admin-link\n")

	excluder := newGitDirExcluder(repo)
	for _, rel := range []string{"src", "src/app.go", ".."} {
		if excluder.excluded(rel) {
			t.Errorf("excluded(%q) = true, want false: a target outside the repository excludes nothing inside it", rel)
		}
	}
}

// TestGitDirExcluderKeepsOrdinaryDirBehindSymlinkListable is the other widening
// direction: a `.git` pointer may name a link to an ordinary package, and git
// calls that `fatal: not a git repository`. The structure test rejects it before
// any spelling is recorded, so neither the link nor the package is excluded.
func TestGitDirExcluderKeepsOrdinaryDirBehindSymlinkListable(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/app.go", "package src\n")
	writeFile(t, repo, "libs/dep/.git", "gitdir: ../../src-link\n")
	symlinkOrSkip(t, "src", filepath.Join(repo, "src-link"))

	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{"src/app.go", "libs/dep"}, []string{"libs/dep"})
	for _, rel := range []string{"src", "src/app.go", "src-link"} {
		if excluder.excluded(rel) {
			t.Errorf("excluded(%q) = true, want false: a link to an ordinary package is not a git directory", rel)
		}
	}
}
