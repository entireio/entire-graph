package sem

import (
	"strings"
	"testing"
)

// A vendored or installed-dependency name says "do not INDEX this tree". It says
// nothing about where a `.git` pointer inside it points: `vendor/dep/.git` names
// a git directory at the repository root, which is not vendored and IS indexed.
// Pruning the tree before reading the pointer therefore leaks the target rather
// than saving anything. Verified on git 2.54.0 with `vendor/dep/` holding only
// the suppressed pointer:
//
//	$ git ls-files --cached --others --exclude-standard
//	.dep-git/config
//	pkg/source.go
//	tracked.go
//	$ git ls-files --cached --others --exclude-standard --directory
//	.dep-git/
//	pkg/
//	vendor/
//	tracked.go
//
// `vendor/dep/` is in neither listing, so no ancestor chain reaches the pointer
// and only the sweep can — and the target `.dep-git` is listed in full, with no
// HEAD for the structural rule to find.
func TestGitDirExcluderSweepsInsideAVendoredTreeForPointers(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"vendor", "node_modules", "third_party"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, "tracked.go", "package tracked\n")
			writeFile(t, repo, "pkg/source.go", "package pkg\n")
			writeFile(t, repo, dir+"/dep/.git", "gitdir: ../../.dep-git\n")
			writeHeadlessGitDirFixture(t, repo, ".dep-git")

			excluder := newGitDirExcluder(t.Context(), repo)
			excluder.unlistedRoots = []string{".dep-git/", "pkg/", dir + "/", "tracked.go"}
			excluder.gitAnsweredRoots = true
			excluder.observeListedPaths([]string{".dep-git/config", "pkg/source.go", "tracked.go"}, nil)

			if !excluder.excluded(".dep-git/config") {
				t.Error(`excluded(".dep-git/config") = false, want true: the pointer inside the vendored tree names it`)
			}
			if excluder.excluded("pkg/source.go") {
				t.Error(`excluded("pkg/source.go") = true, want false: ordinary source must stay listable`)
			}
			if excluder.excluded("tracked.go") {
				t.Error(`excluded("tracked.go") = true, want false`)
			}
		})
	}
}

// The same hole on the filesystem fallback, which prunes the vendored tree
// before it can read anything inside it. Here the pointer's directory need not
// even be pointer-only: the walk never descends `vendor/` at all.
func TestSearchRepositoryNeverIndexesGitDirNamedFromAVendoredTreeOnFallback(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, "vendor/dep/dep.go", "package dep\n")
	writeFile(t, repo, "vendor/dep/.git", "gitdir: ../../.dep-git\n")
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
	var paths []string
	found := false
	for _, result := range response.Results {
		paths = append(paths, result.FilePath)
		if result.FilePath == "src/app.go" {
			found = true
		}
		if strings.HasPrefix(result.FilePath, "vendor/") {
			t.Errorf("search returned vendored content %q, want it still skipped", result.FilePath)
		}
	}
	if !found {
		t.Errorf("search did not return src/app.go; results = %v", paths)
	}
}

// TestSearchRepositoryNeverIndexesGitDirNamedFromAVendoredTreeGitListed is the
// same exploit through git's own listings.
func TestSearchRepositoryNeverIndexesGitDirNamedFromAVendoredTreeGitListed(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "tracked.go", "package tracked\n")
	git(t, repo, "add", "tracked.go")
	git(t, repo, "commit", "-m", "tracked")
	writeFile(t, repo, "pkg/source.go", "package pkg\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, "vendor/dep/.git", "gitdir: ../../.dep-git\n")
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
	var paths []string
	for _, result := range response.Results {
		paths = append(paths, result.FilePath)
		if result.FilePath == "pkg/source.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("search did not return pkg/source.go; results = %v", paths)
	}
}
