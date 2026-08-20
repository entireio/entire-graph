package sem

import (
	"fmt"
	"testing"
)

// The `--directory` listing collapses an untracked directory to ONE entry, and
// says nothing about anything under it — including the pointer-only directories
// the sweep exists to find. The ordinary listing, meanwhile, does mention the
// collapsed directory's files, so the directory is already `seen` and the sweep
// skipped it outright. Verified on git 2.54.0, with `pkg/` untracked and
// holding both an ordinary source file and a pointer-only subdirectory:
//
//	$ git ls-files --cached --others --exclude-standard
//	.dep-git/config
//	pkg/source.go
//	tracked.go
//	$ git ls-files --cached --others --exclude-standard --directory
//	.dep-git/
//	pkg/
//	tracked.go
//
// `pkg/nested/` appears in neither, so nothing but a descent from `pkg` can
// reach the pointer it holds.
func TestGitDirExcluderSweepsInsideACollapsedDirectoryGitNames(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		pointer string
		gitdir  string
		source  string
		listed  []string
	}{
		{
			name:    "one level under the collapsed root",
			pointer: "pkg/nested/.git",
			gitdir:  "gitdir: ../../.dep-git\n",
			source:  "pkg/source.go",
			listed:  []string{".dep-git/config", "pkg/source.go", "tracked.go"},
		},
		{
			// Git collapses at the TOP-most untracked directory, so a deeper
			// pointer-only directory is invisible even though its parent holds
			// listed content and is therefore already `seen`.
			name:    "two levels under the collapsed root",
			pointer: "pkg/a/nested/.git",
			gitdir:  "gitdir: ../../../.dep-git\n",
			source:  "pkg/a/source.go",
			listed:  []string{".dep-git/config", "pkg/a/source.go", "tracked.go"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, "tracked.go", "package tracked\n")
			writeFile(t, repo, testCase.source, "package pkg\n")
			writeFile(t, repo, testCase.pointer, testCase.gitdir)
			// No HEAD: git calls it "not a git repository", so the structural
			// rule cannot reach it and only the suppressed pointer can.
			writeHeadlessGitDirFixture(t, repo, ".dep-git")

			excluder := newGitDirExcluder(t.Context(), repo)
			excluder.unlistedRoots = []string{".dep-git/", "pkg/", "tracked.go"}
			excluder.gitAnsweredRoots = true
			excluder.observeListedPaths(testCase.listed, nil)

			if !excluder.excluded(".dep-git/config") {
				t.Error(`excluded(".dep-git/config") = false, want true: the pointer inside the collapsed directory must still be read`)
			}
			if excluder.excluded(testCase.source) {
				t.Errorf("excluded(%q) = true, want false: ordinary source must stay listable", testCase.source)
			}
			if excluder.excluded("tracked.go") {
				t.Error(`excluded("tracked.go") = true, want false`)
			}
		})
	}
}

// TestSearchRepositoryNeverIndexesGitDirNamedFromACollapsedDirectory is the same
// shape end to end, through the real `git ls-files` listings rather than a
// hand-written answer: the credential must not reach a snippet, and the source
// beside the pointer must still be findable.
func TestSearchRepositoryNeverIndexesGitDirNamedFromACollapsedDirectory(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "tracked.go", "package tracked\n")
	git(t, repo, "add", "tracked.go")
	git(t, repo, "commit", "-m", "tracked")
	// pkg/ is untracked, so git collapses it to `pkg/` in the --directory
	// listing while the ordinary listing names pkg/source.go.
	writeFile(t, repo, "pkg/source.go", "package pkg\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, "pkg/nested/.git", "gitdir: ../../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	assertNoGitDirLeak(t, repo, ".dep-git")

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
		if result.FilePath == "pkg/source.go" {
			found = true
		}
	}
	if !found {
		var paths []string
		for _, result := range response.Results {
			paths = append(paths, result.FilePath)
		}
		t.Errorf("search did not return pkg/source.go; results = %v", paths)
	}
}

// TestGitDirExcluderSweepReadsNoListedFileEntry is the cost bound on the widened
// sweep. `--directory` reports every tracked and untracked FILE by name too, and
// only a directory it collapsed carries the trailing slash — so a file entry
// must not become a swept root, or the sweep pays one failed ReadDir per file in
// the repository. Gitlink entries are also spelled without a slash, and they are
// observed from the listing itself, not from this sweep.
func TestGitDirExcluderSweepReadsNoListedFileEntry(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var listed []string
	var roots []string
	for pkg := range 8 {
		rel := fmt.Sprintf("src/pkg%d/app.go", pkg)
		writeFile(t, repo, rel, "package src\n")
		listed = append(listed, rel)
		roots = append(roots, rel)
	}
	writeFile(t, repo, "nested/.git", "gitdir: ../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.unlistedRoots = append(roots, "nested/")
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths(listed, nil)

	if !excluder.excluded(".dep-git/config") {
		t.Error(`excluded(".dep-git/config") = false, want true`)
	}
	if excluder.directoriesRead != 1 {
		t.Errorf("directoriesRead = %d, want 1 (only the directory git collapsed)", excluder.directoriesRead)
	}
}

// TestGitDirExcluderSweepsIgnoredCollapsedTrees reverses what an earlier round
// pinned here. That round stopped the descent at a tree the project's own ignore
// rules cover and called the give-up narrow; it was a leak. `.gitignore` is
// committed content in the repository being scanned, so pruning by it hands the
// scanned repository the choice of which pointers this rule may read — the same
// reason no gitignore negation may cancel either half of the rule. An ignored
// `pkg/build/` holding `pkg/build/dep/.git` -> `gitdir: ../../../.dep-git` named
// a root-level, HEAD-damaged git directory that git lists in full and no other
// rule can reach.
//
// The price is stated rather than hidden: the descent now reads the directories
// of an ignored tree under a collapsed root — directories only, never a file,
// and nothing in that tree becomes indexable.
func TestGitDirExcluderSweepsIgnoredCollapsedTrees(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "pkg/source.go", "package pkg\n")
	for object := range 32 {
		writeFile(t, repo, fmt.Sprintf("pkg/build/o%d/x/y/a.o", object), "")
	}
	writeFile(t, repo, "pkg/build/dep/.git", "gitdir: ../../../.dep-git\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")

	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.unlistedRoots = []string{"pkg/"}
	excluder.gitAnsweredRoots = true
	excluder.observeListedPaths([]string{"pkg/source.go"}, nil)

	if !excluder.excluded(".dep-git/config") {
		t.Error(`excluded(".dep-git/config") = false, want true: the pointer under the ignored tree names it`)
	}
	if excluder.excluded("pkg/source.go") {
		t.Error(`excluded("pkg/source.go") = true, want false: ordinary source must stay listable`)
	}
}
