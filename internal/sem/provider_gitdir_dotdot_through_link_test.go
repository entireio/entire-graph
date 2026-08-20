package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// Git joins a relative path it read out of a `.git` pointer or a `commondir`
// file onto the directory that held the file by CONCATENATION, and hands the
// result to the OS, which resolves every component in order. A `..` that follows
// a SYMLINK component therefore steps out of the LINK'S TARGET.
// `filepath.Join` cleans `..` lexically, before any link is consulted, and names
// a different directory. Verified on git 2.54.0.
//
// The pointer half, with `nested/link -> sub/child`:
//
//	$ printf 'gitdir: link/../.real-git\n' > nested/.git
//	$ cd nested && git rev-parse --absolute-git-dir
//	<repo>/nested/sub/.real-git
//	$ ls -d <repo>/nested/.real-git
//	ls: ...: No such file or directory
//
// The commondir half, with `adm/link -> sub/child`:
//
//	$ printf 'link/../.common\n' > adm/commondir
//	$ git --git-dir=adm rev-parse --git-common-dir
//	<tmp>/adm/sub/.common
//	$ ls -d <tmp>/adm/.common
//	ls: ...: No such file or directory
//
// So both halves resolved a name nothing on disk is called: the pointer recorded
// no target at all — the lexical name has no git structure to pass observe()'s
// test — and the git directory git really names, HEAD-less as a damaged
// `--separate-git-dir` target is, was indexed with its credentialed config.

// TestGitDirPointerTargetJoinsThroughASymlinkedDotDot pins the pointer half.
func TestGitDirPointerTargetJoinsThroughASymlinkedDotDot(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, "nested/sub/.real-git")
	if err := os.MkdirAll(filepath.Join(repo, "nested", "sub", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join("sub", "child"), filepath.Join(repo, "nested", "link"))
	writeFile(t, repo, "nested/.git", "gitdir: link/../.real-git\n")

	got, ok, hidden := gitDirPointerTarget(repo, "nested")
	if !ok || hidden || got != "nested/sub/.real-git" {
		t.Errorf("gitDirPointerTarget = %q, %v, hidden %v; want %q, true, false (git 2.54.0 resolves it there)", got, ok, hidden, "nested/sub/.real-git")
	}
}

// TestGitDirExcluderExcludesATargetReachedThroughASymlinkedDotDot is the same
// shape through the excluder, which is what decides what gets indexed.
func TestGitDirExcluderExcludesATargetReachedThroughASymlinkedDotDot(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, "nested/sub/.real-git")
	if err := os.MkdirAll(filepath.Join(repo, "nested", "sub", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join("sub", "child"), filepath.Join(repo, "nested", "link"))
	writeFile(t, repo, "nested/.git", "gitdir: link/../.real-git\n")
	writeFile(t, repo, "src/app.go", "package src\n")

	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{"src/app.go", "nested/sub/.real-git/config", "nested"}, []string{"nested"})
	if !excluder.excluded("nested/sub/.real-git/config") {
		t.Error("excluded(nested/sub/.real-git/config) = false, want true: git names that directory")
	}
	if excluder.excluded("src/app.go") {
		t.Error("excluded(src/app.go) = true, want false: ordinary source must stay listable")
	}
}

// TestSearchRepositoryNeverIndexesGitDirBehindASymlinkedDotDot is the exploit end
// to end: the credential in the git directory git really names must not come
// back as a ranked search snippet.
func TestSearchRepositoryNeverIndexesGitDirBehindASymlinkedDotDot(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, "nested/sub/.real-git")
	if err := os.MkdirAll(filepath.Join(repo, "nested", "sub", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join("sub", "child"), filepath.Join(repo, "nested", "link"))
	writeFile(t, repo, "nested/.git", "gitdir: link/../.real-git\n")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, "nested/sub/.real-git")
}

// TestGitCommonDirJoinsThroughASymlinkedDotDot pins the sibling half. A linked
// worktree's administrative directory carries HEAD and `commondir` and no local
// objects/ or refs/, so resolving `commondir` to a directory that does not exist
// calls a real git directory ordinary content and leaves its config indexable.
func TestGitCommonDirJoinsThroughASymlinkedDotDot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	adm := filepath.Join(root, "adm")
	if err := os.MkdirAll(filepath.Join(adm, "sub", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join("sub", "child"), filepath.Join(adm, "link"))
	writeGitDirFixture(t, adm, filepath.Join("sub", ".common"))
	if err := os.WriteFile(filepath.Join(adm, "commondir"), []byte("link/../.common\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := gitCommonDir(adm)
	want := filepath.Join(adm, "sub", ".common")
	if !ok {
		t.Fatalf("gitCommonDir(%q) refused; want %q", adm, want)
	}
	gotInfo, gotErr := os.Stat(got)
	wantInfo, wantErr := os.Stat(want)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Errorf("gitCommonDir = %q; want the directory %q (git 2.54.0 resolves it there); stat errors %v/%v", got, want, gotErr, wantErr)
	}
}
