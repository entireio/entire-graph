package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func oversizedNestedIgnoreBody() string {
	return strings.Repeat("# padding\n", maxNestedIgnoreFileBytes/len("# padding\n")+1)
}

func parsedIgnoreRules(count int) string {
	var content strings.Builder
	for index := 0; index < count; index++ {
		fmt.Fprintf(&content, "never-match-%05d\n", index)
	}
	return content.String()
}

func initializeIgnorePolicyRepo(t *testing.T, repo string) string {
	t.Helper()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-m", "ignore policy fixture")
	return rev(t, repo, "HEAD")
}

func wantIgnoreLimitError(t *testing.T, err error, file, limit string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want ignore-policy refusal for %q", file)
	}
	if !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), limit) {
		t.Fatalf("ignore-policy refusal = %q, want file %q and limit %q", err, file, limit)
	}
}

func TestOversizedNestedIgnoreIsReportedAcrossListings(t *testing.T) {
	body := oversizedNestedIgnoreBody()
	if len(body) <= maxNestedIgnoreFileBytes {
		t.Fatalf("fixture is %d bytes, want more than %d", len(body), maxNestedIgnoreFileBytes)
	}

	t.Run("filesystem walk", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "nested/.gitignore", body)
		writeFile(t, repo, "nested/keep.go", "package nested\n")
		_, err := walkWorktreeFiles(repo, ignoreMatcher{}, func(string) bool { return false })
		wantIgnoreLimitError(t, err, "nested/.gitignore", strconv.Itoa(maxNestedIgnoreFileBytes))
	})

	t.Run("Git worktree", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "nested/.gitignore", body)
		writeFile(t, repo, "nested/keep.go", "package nested\n")
		initializeIgnorePolicyRepo(t, repo)
		ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = worktreeSourceFiles(t.Context(), repo, ignores, false)
		wantIgnoreLimitError(t, err, "nested/.gitignore", strconv.Itoa(maxNestedIgnoreFileBytes))
	})

	t.Run("committed tree", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "nested/.gitignore", body)
		writeFile(t, repo, "nested/keep.go", "package nested\n")
		revision := initializeIgnorePolicyRepo(t, repo)
		opened, err := openSource(t.Context(), repo, revision, sourceOptions{})
		if opened.close != nil {
			defer opened.close()
		}
		wantIgnoreLimitError(t, err, "nested/.gitignore", strconv.Itoa(maxNestedIgnoreFileBytes))
	})
}

func TestCommittedRootIgnoreLimitsAreReported(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "bytes", body: oversizedNestedIgnoreBody(), want: strconv.Itoa(maxIgnoreFileBytes)},
		{name: "rules", body: parsedIgnoreRules(maxIgnoreParsedRules + 1), want: strconv.Itoa(maxIgnoreParsedRules) + " parsed rules"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, ".gitignore", test.body)
			writeFile(t, repo, "keep.go", "package keep\n")
			revision := initializeIgnorePolicyRepo(t, repo)
			opened, err := openSource(t.Context(), repo, revision, sourceOptions{})
			if opened.close != nil {
				defer opened.close()
			}
			wantIgnoreLimitError(t, err, ".gitignore", test.want)
		})
	}
}

func TestNestedIgnoreRulesShareOneOperationBudget(t *testing.T) {
	baseRules := parsedIgnoreRules(maxIgnoreParsedRules - 1)
	nestedRules := "!keep/**\nsecond-rule\n"
	limit := strconv.Itoa(maxIgnoreParsedRules) + " parsed rules"

	t.Run("filesystem walk", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".gitignore", baseRules)
		writeFile(t, repo, "vendor/.gitignore", nestedRules)
		writeFile(t, repo, "vendor/keep/keep.go", "package keep\n")
		ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = walkWorktreeFiles(repo, ignores, func(string) bool { return false })
		wantIgnoreLimitError(t, err, "vendor/.gitignore", limit)
	})

	t.Run("Git worktree", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, ".gitignore", baseRules)
		writeFile(t, repo, "vendor/.gitignore", nestedRules)
		writeFile(t, repo, "vendor/keep/keep.go", "package keep\n")
		initializeIgnorePolicyRepo(t, repo)
		ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = worktreeSourceFiles(t.Context(), repo, ignores, false)
		wantIgnoreLimitError(t, err, "vendor/.gitignore", limit)
	})

	t.Run("committed tree shares explicit ledger", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, graphIgnoreFileName, baseRules)
		writeFile(t, repo, ".gitignore", "# committed root\n")
		writeFile(t, repo, "vendor/.gitignore", nestedRules)
		writeFile(t, repo, "vendor/keep/keep.go", "package keep\n")
		revision := initializeIgnorePolicyRepo(t, repo)
		opened, err := openSource(t.Context(), repo, revision, sourceOptions{})
		if opened.close != nil {
			defer opened.close()
		}
		wantIgnoreLimitError(t, err, "vendor/.gitignore", limit)
	})
}

func TestFilesystemWalkReleasesDepartedIgnoreRuleLevels(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "first/.gitignore", "first-rule\n")
	writeFile(t, repo, "first/keep.go", "package first\n")
	writeFile(t, repo, "second/.gitignore", "second-rule\n")
	writeFile(t, repo, "second/keep.go", "package second\n")
	base := ignoreMatcher{parsedRuleCount: maxIgnoreParsedRules - 1}
	if _, err := walkWorktreeFiles(repo, base, func(string) bool { return false }); err != nil {
		t.Fatalf("sibling ignore levels were retained after departure: %v", err)
	}

	deep := t.TempDir()
	writeFile(t, deep, "first/.gitignore", "first-rule\n")
	writeFile(t, deep, "first/second/.gitignore", "second-rule\n")
	writeFile(t, deep, "first/second/keep.go", "package second\n")
	_, err := walkWorktreeFiles(deep, base, func(string) bool { return false })
	wantIgnoreLimitError(t, err, "first/second/.gitignore", strconv.Itoa(maxIgnoreParsedRules)+" parsed rules")
}

func TestNestedIgnoreFileCountIsReportedAcrossListings(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index <= maxNestedIgnoreFiles; index++ {
		writeFile(t, repo, fmt.Sprintf("d%03d/.gitignore", index), "# empty\n")
	}
	revision := initializeIgnorePolicyRepo(t, repo)
	want := fmt.Sprintf("more than %d nested ignore files", maxNestedIgnoreFiles)

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeSourceFiles(t.Context(), repo, ignores, false); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Git worktree nested-file limit error = %v, want %q", err, want)
	}
	opened, err := openSource(t.Context(), repo, revision, sourceOptions{})
	if opened.close != nil {
		defer opened.close()
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("committed nested-file limit error = %v, want %q", err, want)
	}
	if _, err := walkWorktreeFiles(repo, ignores, func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("filesystem nested-file limit error = %v, want %q", err, want)
	}
	if _, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true}); err == nil ||
		!strings.Contains(err.Error(), "nested-ignore candidates exceed") {
		t.Fatalf("replay-policy nested-file limit error = %v", err)
	}
}

func TestWorktreeNestedIgnoreDoesNotFollowEscapingDirectorySymlink(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "vendor/.gitignore", "# indexed placeholder\n")
	writeFile(t, repo, "vendor/keep/keep.go", "package keep\n")
	initializeIgnorePolicyRepo(t, repo)

	external := t.TempDir()
	writeFile(t, external, ".gitignore", "!keep/**\n")
	writeFile(t, external, "keep/keep.go", "package escaped\n")
	if err := os.RemoveAll(filepath.Join(repo, "vendor")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repo, "vendor")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeSourceFiles(t.Context(), repo, ignores, false); err == nil ||
		!strings.Contains(err.Error(), "vendor/.gitignore") {
		t.Fatalf("escaping nested-ignore symlink error = %v", err)
	}
}

func TestReusableNestedIgnoreBudgetStartsFresh(t *testing.T) {
	base := ignoreMatcher{parsedRuleCount: maxIgnoreParsedRules - 1}
	for attempt := 0; attempt < 2; attempt++ {
		rules := newNestedIgnoreRules(base)
		if err := rules.addFile("vendor/.gitignore", "!keep/**\n"); err != nil {
			t.Fatalf("attempt %d reused a spent ignore budget: %v", attempt+1, err)
		}
	}
}

func TestSearchReplayPolicyNestedBudgetIsFreshPerEvaluation(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", parsedIgnoreRules(maxIgnoreParsedRules-1))
	writeFile(t, repo, "vendor/.gitignore", "!keep/**\n")
	writeFile(t, repo, "vendor/keep/keep.go", "package keep\n")
	initializeIgnorePolicyRepo(t, repo)

	policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !policy.AllowsReplayPaths([]string{"vendor/keep/keep.go"}) {
			t.Fatalf("replay attempt %d reused a spent nested-ignore budget", attempt+1)
		}
	}
}
