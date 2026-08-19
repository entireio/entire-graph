package sem

import (
	"path/filepath"
	"testing"
)

// Git reads a `.git` gitfile with stat(), not lstat(): read_gitfile_gently()
// stats the path and asks S_ISREG of the result, so a `.git` SYMLINK pointing at
// a regular gitfile IS a gitfile to git. Verified on git 2.54.0, with the
// gitfile `git init --separate-git-dir` wrote moved aside and `.git` made a link
// to it:
//
//	$ ls -l .git realpointer
//	lrwxr-xr-x  .git -> realpointer
//	-rw-r--r--  realpointer
//	$ cat realpointer
//	gitdir: /…/wt/.real-git
//	$ git rev-parse --git-dir
//	/…/wt/.real-git
//	$ git status --porcelain
//	?? .real-git/
//	?? realpointer
//
// So git resolves the separate git directory through the link and then lists
// that directory as ordinary untracked content — while `gitDirPointerTarget`
// lstat'd the pointer and refused a symlink, and `gitDirLinkTarget` only accepts
// a link to a DIRECTORY. Neither rule fired, and with the target's HEAD moved
// aside (`fatal: not a git repository`, which is what makes the filesystem
// fallback run) the structural rule cannot fire either: the config and its
// credential were indexable.
func TestGitDirPointerTargetReadsAGitfileThroughASymlink(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, ".real-git")
	writeFile(t, repo, "realpointer", "gitdir: .real-git\n")
	symlinkOrSkip(t, "realpointer", filepath.Join(repo, ".git"))

	excluder := newGitDirExcluder(repo)
	if !excluder.excluded(".real-git/config") {
		t.Error(`excluded(".real-git/config") = false, want true: git reads a symlinked gitfile and resolves this directory`)
	}
	if !excluder.excluded(".real-git/hooks/post-commit.go") {
		t.Error(`excluded(".real-git/hooks/post-commit.go") = false, want true`)
	}
}

// The same shape one level down, where the linked gitfile belongs to a nested
// checkout and the git directory carries an ordinary name elsewhere in the tree.
func TestGitDirExcluderReadsANestedGitfileThroughASymlink(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, "state/.dep-git")
	writeFile(t, repo, "libs/dep/pointer", "gitdir: ../../state/.dep-git\n")
	writeFile(t, repo, "src/app.go", "package src\n")
	symlinkOrSkip(t, "pointer", filepath.Join(repo, "libs", "dep", ".git"))

	excluder := newGitDirExcluder(repo)
	excluder.observeListedPaths([]string{"src/app.go", "state/.dep-git/config", "libs/dep/pointer"}, nil)
	if !excluder.excluded("state/.dep-git/config") {
		t.Error(`excluded("state/.dep-git/config") = false, want true: the nested gitfile is reached through a symlink`)
	}
	if excluder.excluded("src/app.go") {
		t.Error(`excluded("src/app.go") = true, want false: ordinary source must stay listable`)
	}
}

// The other direction, and the reason the swap is stat() rather than "treat a
// symlink as a pointer": git stats the path, so a DANGLING `.git` link is
// `fatal: not a git repository` and names nothing, and a link to a file that is
// not a gitfile names nothing either. Neither may delete anything from the index.
func TestGitDirExcluderIgnoresASymlinkedGitPathGitRefuses(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		setup func(t *testing.T, repo string)
	}{
		{"dangling link", func(t *testing.T, repo string) {
			symlinkOrSkip(t, "missing-pointer", filepath.Join(repo, ".git"))
		}},
		{"link to a file that is not a gitfile", func(t *testing.T, repo string) {
			writeFile(t, repo, "notes.txt", "gitdir is not what this says\n")
			symlinkOrSkip(t, "notes.txt", filepath.Join(repo, ".git"))
		}},
		{"link to a gitfile naming ordinary source", func(t *testing.T, repo string) {
			writeFile(t, repo, "realpointer", "gitdir: src\n")
			symlinkOrSkip(t, "realpointer", filepath.Join(repo, ".git"))
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, "src/app.go", "package src\n")
			testCase.setup(t, repo)

			excluder := newGitDirExcluder(repo)
			if excluder.excluded("src/app.go") {
				t.Error(`excluded("src/app.go") = true, want false: git names no directory here`)
			}
		})
	}
}

// TestSearchRepositoryNeverIndexesGitDirBehindASymlinkedGitfile is the exploit
// end to end: the credential in the separate git directory must not come back as
// a ranked snippet, and the source beside the link must still be findable.
func TestSearchRepositoryNeverIndexesGitDirBehindASymlinkedGitfile(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeHeadlessGitDirFixture(t, repo, ".real-git")
	writeFile(t, repo, "realpointer", "gitdir: .real-git\n")
	symlinkOrSkip(t, "realpointer", filepath.Join(repo, ".git"))
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, ".real-git")

	response, err := SearchRepository(t.Context(), repo, "test", "origin remote credential loader", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range response.Results {
		if result.FilePath == "src/app.go" {
			found = true
		}
	}
	if !found {
		var paths []string
		for _, result := range response.Results {
			paths = append(paths, result.FilePath)
		}
		t.Errorf("search did not return src/app.go; results = %v", paths)
	}
}
