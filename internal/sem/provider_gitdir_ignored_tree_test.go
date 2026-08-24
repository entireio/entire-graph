package sem

import (
	"strings"
	"testing"
)

// An ignored tree is in NEITHER of git's `--exclude-standard` listings, so no
// ancestor chain and no collapsed root reaches a `.git` pointer inside it —
// while the git directory that pointer names is somewhere else and IS listed.
// Verified on git 2.54.0 with `build/` in a committed `.gitignore`, holding
// `build/dep/.git` -> `gitdir: ../../.dep-git`:
//
//	$ git ls-files --cached --others --exclude-standard
//	.dep-git/config
//	pkg/source.go
//	.gitignore
//	tracked.go
//	$ git ls-files --cached --others --exclude-standard --directory
//	.dep-git/
//	pkg/
//	.gitignore
//	tracked.go
//	$ git ls-files --others --ignored --exclude-standard --directory
//	build/
//
// `build/` is absent from both listings the sweep used to read, and the target
// is listed in full with no HEAD for the structural rule to find. The third
// listing is the one that names it.
//
// This is why the sweep may not prune by the project's ignore rules:
// `.gitignore` is committed content in the repository being scanned, so pruning
// by it hands that repository the choice of which pointers this rule may read —
// the same reason no gitignore negation may cancel either half of the rule.
func TestSearchRepositoryNeverIndexesGitDirNamedFromAnIgnoredTreeGitListed(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, ".gitignore", "build/\n")
	writeFile(t, repo, "tracked.go", "package tracked\n")
	git(t, repo, "add", ".gitignore", "tracked.go")
	git(t, repo, "commit", "-m", "tracked")
	writeFile(t, repo, "pkg/source.go", "package pkg\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, "build/dep/.git", "gitdir: ../../.dep-git\n")
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

// The same exploit on the filesystem fallback, which the sweep's own comment
// claimed was unaffected. It was not: the walk pruned the ignored tree exactly
// the same way, before reading anything inside it.
func TestSearchRepositoryNeverIndexesGitDirNamedFromAnIgnoredTreeOnFallback(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "build/\n")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, "build/dep/.git", "gitdir: ../../.dep-git\n")
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
		if result.FilePath == "src/app.go" {
			found = true
		}
		if strings.HasPrefix(result.FilePath, "build/") {
			t.Errorf("search returned ignored content %q, want it still skipped", result.FilePath)
		}
	}
	if !found {
		t.Error("search did not return src/app.go")
	}
}
