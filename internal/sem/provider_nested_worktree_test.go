package sem

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A marker alone is not a repository boundary. Pruning on its name would hide
// ordinary source next to bogus pointers and incomplete .git directories.
func TestWorktreeFallbackKeepsSourceBesideInvalidGitMarkers(t *testing.T) {
	for _, marker := range []string{"missing-target", "plain-target", "invalid-file", "plain-directory"} {
		t.Run(marker, func(t *testing.T) {
			repo := t.TempDir()
			initRepo(t, repo)
			writeFile(t, repo, "outer.go", "package outer\n")
			git(t, repo, "add", "outer.go")
			writeFile(t, repo, "nested/source.go", "package nested\n")
			switch marker {
			case "missing-target":
				writeFile(t, repo, "nested/.git", "gitdir: ../nowhere\n")
			case "plain-target":
				writeFile(t, repo, "plain/source.go", "package plain\n")
				writeFile(t, repo, "nested/.git", "gitdir: ../plain\n")
			case "invalid-file":
				writeFile(t, repo, "nested/.git", "not a gitfile\n")
			case "plain-directory":
				if err := os.Mkdir(filepath.Join(repo, "nested/.git"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			paths, _, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"outer.go", "nested/source.go"} {
				if !slices.Contains(paths, want) {
					t.Errorf("source %q absent beside invalid marker: %v", want, paths)
				}
			}
			if marker == "plain-target" && !slices.Contains(paths, "plain/source.go") {
				t.Errorf("structureless pointer target source absent: %v", paths)
			}
		})
	}
}

func TestWorktreeFallbackKeepsNestedProjectInNonGitWorkspace(t *testing.T) {
	workspace := t.TempDir()
	writeFile(t, workspace, "project/source.go", "package project\nfunc NestedWorkspaceSource() {}\n")
	// Retaining the project must not disable existing untracked vendor policy.
	writeFile(t, workspace, "project/build/generated.go", "package generated\n")
	initRepo(t, filepath.Join(workspace, "project"))
	if repositoryHasGitMetadata(workspace) {
		t.Fatal("fixture unexpectedly has outer Git metadata")
	}
	paths, warnings, err := worktreeSourceFiles(t.Context(), workspace, ignoreMatcher{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"project/source.go"}) {
		t.Errorf("non-Git workspace lost nested project source: paths=%v warnings=%v", paths, warnings)
	}
}

// Both real Git boundary shapes must leave the same corpus and search result
// as the outer checkout alone, even when preflight safely refuses enumeration.
func TestWorktreeFallbackPrunesNestedRepositories(t *testing.T) {
	for _, kind := range []string{"worktree", "clone"} {
		t.Run(kind, func(t *testing.T) {
			repo := nestedBoundaryRepo(t)
			clean := searchNestedBoundaryRepo(t, repo)
			if kind == "worktree" {
				git(t, repo, "worktree", "add", "--detach", "nested", "HEAD")
			} else {
				git(t, repo, "clone", "--no-hardlinks", repo, "nested")
			}
			paths, warnings, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(paths, []string{"internal/source.go"}) {
				t.Errorf("nested %s changed outer corpus: %v", kind, paths)
			}
			if !slices.ContainsFunc(warnings, func(w ProviderWarning) bool { return w.Code == "W_GIT_WORKTREE_FALLBACK" }) {
				t.Errorf("nested marker must still refuse Git enumeration: %v", warnings)
			}
			nested := searchNestedBoundaryRepo(t, repo)
			t.Logf("clean scanned=%d indexed=%d results=%d; nested scanned=%d indexed=%d results=%d", clean.Stats.FilesScanned, clean.Stats.FilesIndexed, len(clean.Results), nested.Stats.FilesScanned, nested.Stats.FilesIndexed, len(nested.Results))
			if nested.Stats.FilesScanned != clean.Stats.FilesScanned || nested.Stats.FilesIndexed != clean.Stats.FilesIndexed {
				t.Error("nested checkout enlarged scanned/parsed corpus")
			}
			if len(nested.Results) != 1 || nested.Results[0].FilePath != "internal/source.go" {
				t.Errorf("want only outer search result; got %+v", nested.Results)
			}
		})
	}
}

func TestWorktreeFallbackKeepsParentTrackedSourceBelowNestedBoundary(t *testing.T) {
	repo := nestedBoundaryRepo(t)
	writeFile(t, repo, "nested/tracked.go", "package tracked\n")
	git(t, repo, "add", "nested/tracked.go")
	initRepo(t, filepath.Join(repo, "nested"))
	paths, _, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(paths, "nested/tracked.go") {
		t.Errorf("outer-index source below nested repository omitted: %v", paths)
	}
}

func TestWorktreeFallbackKeepsNestedSourceWhenIndexUnavailable(t *testing.T) {
	for _, damaged := range []string{"config", "index"} {
		t.Run(damaged, func(t *testing.T) {
			repo := nestedBoundaryRepo(t)
			git(t, repo, "worktree", "add", "--detach", "nested", "HEAD")
			// Both metadata refusal and failed index enumeration must retain
			// potentially parent-tracked source rather than guessing ownership.
			writeFile(t, repo, ".git/"+damaged, "[invalid\n")
			paths, _, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(paths, "nested/internal/source.go") {
				t.Errorf("unknown index must conservatively retain nested source: %v", paths)
			}
		})
	}
}

func TestWorktreeFallbackSweepsPrunedNestedBoundary(t *testing.T) {
	for _, budget := range []string{"0", "1"} {
		t.Run("budget="+budget, func(t *testing.T) {
			t.Setenv(sweepDirBudgetEnv, budget)
			repo := nestedBoundaryRepo(t)
			git(t, repo, "worktree", "add", "--detach", "nested", "HEAD")
			writeHeadlessGitDirFixture(t, repo, "a-private")
			writeFile(t, repo, "nested/deeper/.git", "gitdir: ../../a-private\n")
			paths, warnings, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if strings.HasPrefix(path, "a-private/") || strings.HasPrefix(path, "nested/") {
					t.Errorf("pruned boundary or its hidden pointer target leaked: %q", path)
				}
			}
			if !slices.Contains(paths, "internal/source.go") {
				t.Errorf("ordinary outer source omitted: %v", paths)
			}
			exhausted := slices.ContainsFunc(warnings, func(w ProviderWarning) bool { return w.Code == "W_GITDIR_SWEEP_BUDGET" })
			if exhausted != (budget == "1") {
				t.Errorf("budget exhaustion disclosure = %v, budget %s: %v", exhausted, budget, warnings)
			}
		})
	}
}

func nestedBoundaryRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "internal/source.go", "package source\n\n// UniqueBoundaryLoader loads the outer boundary.\nfunc UniqueBoundaryLoader() {}\n")
	git(t, repo, "add", "internal/source.go")
	git(t, repo, "commit", "-m", "outer source")
	return repo
}

func searchNestedBoundaryRepo(t *testing.T, repo string) SearchResponse {
	t.Helper()
	response, err := SearchRepository(t.Context(), repo, "test", "UniqueBoundaryLoader", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}
