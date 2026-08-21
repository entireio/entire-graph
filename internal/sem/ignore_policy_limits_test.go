package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
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

func stageUnmergedIgnorePath(t *testing.T, repo, candidate string) {
	t.Helper()
	blobs := []string{
		gitInput(t, repo, "# base\n", "hash-object", "-w", "--stdin"),
		gitInput(t, repo, "# ours\n", "hash-object", "-w", "--stdin"),
		gitInput(t, repo, "# theirs\n", "hash-object", "-w", "--stdin"),
	}
	git(t, repo, "update-index", "--force-remove", "--", candidate)
	var indexInfo strings.Builder
	for index, blob := range blobs {
		fmt.Fprintf(&indexInfo, "100644 %s %d\t%s%c", blob, index+1, candidate, byte(0))
	}
	gitInput(t, repo, indexInfo.String(), "update-index", "-z", "--index-info")
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

func TestCommittedIgnoreReportsUnreadableBlob(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	knownBlob := gitInput(t, repo, "known\n", "hash-object", "-w", "--stdin")
	missingOID := strings.Repeat("1", len(knownBlob))
	tree := gitInput(
		t,
		repo,
		fmt.Sprintf("100644 blob %s\t.gitignore%c", missingOID, byte(0)),
		"mktree",
		"-z",
		"--missing",
	)

	reader := gitutil.NewLimitedFileReader(t.Context(), repo, tree, maxNestedIgnoreFileBytes)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime([]string{".gitignore"}); err != nil {
		t.Fatalf("prime committed ignore reader: %v", err)
	}
	content, present, err := readCommittedIgnoreFile(reader, ".gitignore", ".gitignore", false)
	if content != "" || present || err == nil {
		t.Fatalf("unreadable committed ignore = (%q, %v, %v), want reported refusal", content, present, err)
	}
	if !strings.Contains(err.Error(), "Git blob object is unavailable or unreadable") {
		t.Fatalf("unreadable committed ignore error = %q, want actionable blob error", err)
	}
	if strings.Contains(err.Error(), "unknown read status") {
		t.Fatalf("unreadable committed ignore error = %q, want typed status handling", err)
	}
}

func TestCommittedIgnoreReportsNonBlobTree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	ignoreTree := gitInput(t, repo, "", "mktree", "-z")
	keepBlob := gitInput(t, repo, "package keep\n", "hash-object", "-w", "--stdin")
	tree := gitInput(
		t,
		repo,
		fmt.Sprintf(
			"040000 tree %s\t.gitignore%c100644 blob %s\tkeep.go%c",
			ignoreTree,
			byte(0),
			keepBlob,
			byte(0),
		),
		"mktree",
		"-z",
	)

	opened, err := openSource(t.Context(), repo, tree, sourceOptions{})
	if opened.close != nil {
		defer opened.close()
	}
	if err == nil || !strings.Contains(err.Error(), `committed ignore file ".gitignore" is not a blob`) {
		t.Fatalf("non-blob committed ignore error = %v, want actionable object-type refusal", err)
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

func TestUnmergedNestedIgnoreHasProviderReplayParity(t *testing.T) {
	repo := t.TempDir()
	policy := parsedIgnoreRules(maxIgnoreParsedRules/2) + "*\n!.gitignore\n!mypkg/\n!mypkg/**\n"
	writeFile(t, repo, "vendor/.gitignore", policy)
	writeFile(t, repo, "vendor/mypkg/kept.go", "package kept\n")
	initializeIgnorePolicyRepo(t, repo)
	stageUnmergedIgnorePath(t, repo, "vendor/.gitignore")

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := worktreeSourceFiles(t.Context(), repo, ignores, false)
	if err != nil {
		t.Fatal(err)
	}
	providerAllows := false
	for _, candidate := range paths {
		if candidate == "vendor/mypkg/kept.go" {
			providerAllows = true
			break
		}
	}
	if !providerAllows {
		t.Fatalf("provider paths = %q, want vendored path re-admitted by nested policy", paths)
	}

	replay, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	replayAllows := replay.AllowsReplayPaths([]string{"vendor/mypkg/kept.go"})
	if replayAllows != providerAllows {
		t.Fatalf("replay allows vendored path = %v, provider = %v", replayAllows, providerAllows)
	}
}

func TestCommittedSubdirectoryReadsRootAndNestedIgnoreFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "bench/.gitignore", "vendor/*\n!vendor/rootpkg/\n!vendor/rootpkg/**\n")
	writeFile(t, repo, "bench/vendor/rootpkg/root.go", "package rootpkg\n")
	writeFile(t, repo, "bench/memory/.gitignore", "vendor/*\n!vendor/nestedpkg/\n!vendor/nestedpkg/**\n")
	writeFile(t, repo, "bench/memory/vendor/nestedpkg/nested.go", "package nestedpkg\n")
	revision := initializeIgnorePolicyRepo(t, repo)

	opened, err := openSource(t.Context(), filepath.Join(repo, "bench"), revision, sourceOptions{})
	if opened.close != nil {
		defer opened.close()
	}
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"vendor/rootpkg/root.go":            false,
		"memory/vendor/nestedpkg/nested.go": false,
	}
	for _, candidate := range opened.paths {
		if _, exists := want[candidate]; exists {
			want[candidate] = true
		}
	}
	for candidate, found := range want {
		if !found {
			t.Errorf("committed subdirectory paths = %q, missing %q", opened.paths, candidate)
		}
	}
}
