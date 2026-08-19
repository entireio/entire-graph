package sem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalInfoExcludeIsNotDisclosedAsRepoControlled draws the line the
// disclosure depends on. `.git/info/exclude` is machine-local Git metadata: it is
// never part of the tree, so no contributor can push one and no reader of the
// repository is being hidden from by it. Reporting it as something "the
// repository's own ignore rules" removed is a false alarm, and it puts the local
// operator's private exclusion list into a payload.
func TestLocalInfoExcludeIsNotDisclosedAsRepoControlled(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	write(t, repo, "internal/auth/auth.go", "package auth\n\nfunc ValidateToken(token string) bool { return len(token) == 64 }\n")
	write(t, repo, "internal/auth/auth_stub.go", "package auth\n\nfunc ValidateTokenStub(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "initial")
	// The operator's own local exclude, on a TRACKED file so it actually removes
	// something from the corpus.
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("internal/auth/auth.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	response, err := SearchRepository(t.Context(), repo, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RepoIgnored != nil {
		t.Fatalf("a local .git/info/exclude was reported as a repository-controlled exclusion: %+v", *response.RepoIgnored)
	}
	if response.Stats.RepoIgnoredFiles != 0 {
		t.Fatalf("stats counted %d repo-controlled exclusions, want 0", response.Stats.RepoIgnoredFiles)
	}
	response.RepoRoot = ""
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "info/exclude") {
		t.Fatalf("payload leaked the operator's local exclude file:\n%s", payload)
	}
}

// TestWalkFallbackDisclosesGraphIgnoreExclusions covers the listing mode the
// git-backed accounting does not reach: a directory Git cannot enumerate still
// honours `.graphignore`, so the same one-line corpus narrowing works there, and
// it was silent.
//
// `.graphignore` is the channel that must be disclosed here: Git does not know
// the file, so anything it removes is content Git itself would still have shown.
// Ordinary `.gitignore` removals stay silent in this mode by design — see
// walkWorktreeFiles.
func TestWalkFallbackDisclosesGraphIgnoreExclusions(t *testing.T) {
	// No initRepo: with no git directory, ListWorktreeFiles fails and the
	// filesystem walk is the listing.
	repo := t.TempDir()
	write(t, repo, "internal/auth/auth.go", "package auth\n\nfunc ValidateToken(token string) bool { return len(token) == 64 }\n")
	write(t, repo, "internal/auth/auth_stub.go", "package auth\n\nfunc ValidateTokenStub(token string) bool { return token != \"\" }\n")
	write(t, repo, graphIgnoreFileName, "internal/auth/auth.go\n")
	response, err := SearchRepository(t.Context(), repo, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.FilePath == "internal/auth/auth.go" {
			t.Fatalf("fixture is wrong: .graphignore did not hide auth.go in walk mode")
		}
	}
	if response.RepoIgnored == nil {
		t.Fatalf("the filesystem-walk fallback narrowed the corpus and disclosed nothing; stats say %d excluded",
			response.Stats.RepoIgnoredFiles)
	}
	if got := response.RepoIgnored.Files; got != 1 {
		t.Fatalf("disclosed %d exclusions, want 1: %+v", got, *response.RepoIgnored)
	}
	if response.Stats.RepoIgnoredFiles != 1 {
		t.Fatalf("stats counted %d, want 1", response.Stats.RepoIgnoredFiles)
	}
	if response.RepoIgnored.Sample[0].Path != "internal/auth/auth.go" {
		t.Fatalf("wrong path disclosed: %+v", response.RepoIgnored.Sample)
	}
}

// TestTruncatedDisclosurePointsAtWhatJSONActuallyHolds keeps the suggested action
// honest. The text rendering names three paths and counts the rest; it used to
// point at "the full list" in JSON, but the JSON sample is capped too, so for a
// repository with more than ten exclusions that instruction cannot be followed.
func TestTruncatedDisclosurePointsAtWhatJSONActuallyHolds(t *testing.T) {
	sample := make([]RepoExclusion, 0, maxRepoExclusionSample)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		sample = append(sample, RepoExclusion{Path: "vendor/" + name + "/parser.c", Source: ".graphignore", Rule: "parser.c"})
	}
	truncated := RenderRepoIgnoreDisclosure(&RepoIgnoreReport{
		Files:           23,
		Sources:         []RepoIgnoreSource{{File: ".graphignore", Files: 23}},
		Sample:          sample,
		SampleTruncated: true,
	})
	if strings.Contains(string(truncated), "full list") {
		t.Fatalf("the rendering promises a full list the JSON does not hold (sample is capped at %d of 23):\n%s",
			maxRepoExclusionSample, truncated)
	}
	if !strings.Contains(string(truncated), "10") {
		t.Fatalf("the rendering should say how many paths the JSON actually names:\n%s", truncated)
	}
	// An uncapped report may still point at the complete list, because there it is complete.
	whole := RenderRepoIgnoreDisclosure(&RepoIgnoreReport{
		Files:   5,
		Sources: []RepoIgnoreSource{{File: ".graphignore", Files: 5}},
		Sample:  sample[:5],
	})
	if !strings.Contains(string(whole), "full list") {
		t.Fatalf("an uncapped report should still point at the complete list:\n%s", whole)
	}
}

// TestWalkFallbackDoesNotDiscloseWhatGitWouldHideAnyway is the other half of the
// walk fallback's contract. The disclosure's whole claim is "Git would still have
// shown you this file", which is why only `.graphignore` is accounted for there.
// A path that a Git-applied rule ALSO covers fails that claim even when the
// `.graphignore` rule is the one that wins the precedence contest — it is ordinary
// build output, and reporting it both cries wolf and prints paths nobody asked
// about.
func TestWalkFallbackDoesNotDiscloseWhatGitWouldHideAnyway(t *testing.T) {
	// No initRepo: the filesystem walk is the listing.
	repo := t.TempDir()
	write(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	write(t, repo, "bundle.gen.go", "package main\n\nfunc GeneratedBundle() string { return \"bearer token validation\" }\n")
	// Git already hides the generated file; .graphignore covers it too, and its
	// rule is the one that wins (loaded later, matches the path itself).
	write(t, repo, ".gitignore", "bundle.gen.go\n")
	write(t, repo, graphIgnoreFileName, "*.gen.go\n")
	response, err := SearchRepository(t.Context(), repo, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.FilePath == "bundle.gen.go" {
			t.Fatalf("fixture is wrong: the generated file was not excluded")
		}
	}
	if response.RepoIgnored != nil {
		t.Fatalf("ordinary Git-ignored build output was reported as repository-hidden source: %+v",
			*response.RepoIgnored)
	}
	if response.Stats.RepoIgnoredFiles != 0 {
		t.Fatalf("stats counted %d, want 0", response.Stats.RepoIgnoredFiles)
	}
}
