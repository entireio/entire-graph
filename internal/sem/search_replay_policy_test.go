package sem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestSearchReplayPolicyFingerprintIsDeterministicAndOrderSensitive(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFile(t, repo, "keep.go", "package sample\n")
	writeFile(t, repo, "ignore-target", "target.go\n")
	writeFile(t, repo, "reinclude-target", "!target.go\n")

	includeTarget := SearchOptions{
		Worktree:    true,
		IgnoreFiles: []string{"ignore-target", "reinclude-target"},
	}
	first, err := ResolveSearchReplayPolicy(t.Context(), repo, includeTarget)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := ResolveSearchReplayPolicy(t.Context(), repo, includeTarget)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() == "" || first.Fingerprint() != repeated.Fingerprint() {
		t.Fatalf("fingerprint is not stable: %q vs %q", first.Fingerprint(), repeated.Fingerprint())
	}

	ignoreTarget := includeTarget
	ignoreTarget.IgnoreFiles = []string{"reinclude-target", "ignore-target"}
	reversed, err := ResolveSearchReplayPolicy(t.Context(), repo, ignoreTarget)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() == reversed.Fingerprint() {
		t.Fatalf("reversing last-rule-wins ignore files left fingerprint unchanged: %q", first.Fingerprint())
	}
}

func TestSearchReplayPolicyFingerprintBindsEveryParsedRuleSemantic(t *testing.T) {
	t.Parallel()

	base := ignoreRule{
		ignore:       true,
		includeFile:  true,
		directory:    true,
		fileOnly:     true,
		basenameOnly: true,
		pattern:      "credentials",
		expression:   regexp.MustCompile(`^credentials$`),
	}
	baseline := searchReplayPolicyFingerprint(searchReplayViewFilesystemWorktree, ignoreMatcher{rules: []ignoreRule{base}})

	mutations := map[string]func(*ignoreRule){
		"ignore":        func(rule *ignoreRule) { rule.ignore = false },
		"include-file":  func(rule *ignoreRule) { rule.includeFile = false },
		"directory":     func(rule *ignoreRule) { rule.directory = false },
		"file-only":     func(rule *ignoreRule) { rule.fileOnly = false },
		"basename-only": func(rule *ignoreRule) { rule.basenameOnly = false },
		"pattern":       func(rule *ignoreRule) { rule.pattern = "secrets" },
		"expression":    func(rule *ignoreRule) { rule.expression = regexp.MustCompile(`(?i)^credentials$`) },
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := base
			mutate(&changed)
			got := searchReplayPolicyFingerprint(searchReplayViewFilesystemWorktree, ignoreMatcher{rules: []ignoreRule{changed}})
			if got == baseline {
				t.Fatalf("changing %s did not change fingerprint %q", name, got)
			}
		})
	}
	if got := searchReplayPolicyFingerprint(searchReplayViewHead, ignoreMatcher{rules: []ignoreRule{base}}); got == baseline {
		t.Fatalf("changing resolved repository view did not change fingerprint %q", got)
	}
	if got := searchReplayPolicyFingerprint(searchReplayViewGitWorktree, ignoreMatcher{rules: []ignoreRule{base}}); got == baseline {
		t.Fatalf("changing worktree eligibility backend did not change fingerprint %q", got)
	}
}

func TestSearchReplayPolicyFingerprintLengthPrefixesRuleFields(t *testing.T) {
	t.Parallel()

	// Delimiter-only serialization makes these two sequences identical:
	// "a\x00b" + NUL + "c" and "a" + NUL + "b\x00c". Length-prefixing each
	// parsed field keeps even unusual rule contents unambiguous.
	left := ignoreMatcher{rules: []ignoreRule{{
		pattern:    "a\x00b",
		expression: regexp.MustCompile("c"),
	}}}
	right := ignoreMatcher{rules: []ignoreRule{{
		pattern:    "a",
		expression: regexp.MustCompile("b\x00c"),
	}}}
	leftFingerprint := searchReplayPolicyFingerprint(searchReplayViewFilesystemWorktree, left)
	rightFingerprint := searchReplayPolicyFingerprint(searchReplayViewFilesystemWorktree, right)
	if leftFingerprint == rightFingerprint {
		t.Fatalf("ambiguous rule fields produced the same fingerprint %q", leftFingerprint)
	}
}

func TestSearchReplayPolicyCanonicalizesEffectiveFileCap(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "only.go", "package sample\n")
	resolve := func(value string) SearchReplayPolicy {
		t.Helper()
		t.Setenv(maxSourceFilesEnv, value)
		policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
		if err != nil {
			t.Fatalf("resolve policy with %s=%q: %v", maxSourceFilesEnv, value, err)
		}
		return policy
	}

	invalid := resolve("not-an-integer")
	defaulted := resolve("")
	if invalid.Fingerprint() != defaulted.Fingerprint() {
		t.Fatalf("invalid and unset cap values did not share default semantics: %q vs %q",
			invalid.Fingerprint(), defaulted.Fingerprint())
	}

	unlimitedZero := resolve("0")
	for _, value := range []string{"-1", "-999"} {
		if got := resolve(value).Fingerprint(); got != unlimitedZero.Fingerprint() {
			t.Fatalf("unlimited cap %q fingerprint = %q, want %q", value, got, unlimitedZero.Fingerprint())
		}
	}
	if positive := resolve("1"); positive.Fingerprint() == unlimitedZero.Fingerprint() {
		t.Fatal("positive and unlimited cap semantics shared a replay fingerprint")
	}
}

func TestSearchReplayPolicyHeadFingerprintBindsEffectiveFileCap(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "a.go", "package sample\n")
	writeFile(t, repo, "b.go", "package sample\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "head corpus")

	t.Setenv(maxSourceFilesEnv, "2")
	wide, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(maxSourceFilesEnv, "1")
	narrow, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if wide.tree == "" || wide.tree != narrow.tree {
		t.Fatalf("fixture changed tree while only cap changed: %q vs %q", wide.tree, narrow.tree)
	}
	if wide.Fingerprint() == narrow.Fingerprint() {
		t.Fatalf("HEAD cap change retained replay fingerprint %q", wide.Fingerprint())
	}
}

func TestSearchReplayPolicyWorktreeFingerprintBindsCompleteCorpus(t *testing.T) {
	for _, gitRepo := range []bool{false, true} {
		name := "filesystem"
		if gitRepo {
			name = "git"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			if gitRepo {
				initRepo(t, repo)
			}
			t.Setenv(maxSourceFilesEnv, "2")
			writeFile(t, repo, "b.go", "package sample\n")
			before, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, repo, "a.go", "package sample\n")
			after, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
			if err != nil {
				t.Fatal(err)
			}
			if before.Fingerprint() == after.Fingerprint() {
				t.Fatalf("adding an eligible lexical predecessor retained %s worktree fingerprint %q",
					name, before.Fingerprint())
			}
			if !after.AllowsReplayPaths([]string{"b.go"}) {
				t.Fatal("old path stopped being individually eligible; fixture did not isolate corpus identity")
			}
		})
	}
}

func TestSearchReplayPolicyWorktreeCorpusBoundsFailClosed(t *testing.T) {
	t.Run("bounded provider cap truncation", func(t *testing.T) {
		repo := t.TempDir()
		t.Setenv(maxSourceFilesEnv, "1")
		writeFile(t, repo, "b.go", "package sample\n")
		before, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, repo, "a.go", "package sample\n")
		after, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
		if err != nil {
			t.Fatalf("bounded pre-cap corpus was not fingerprinted: %v", err)
		}
		if before.Fingerprint() == after.Fingerprint() {
			t.Fatal("same-cap lexical displacement retained the old corpus fingerprint")
		}
	})

	t.Run("path count", func(t *testing.T) {
		collector := newSearchReplayCorpusCollector()
		for index := 0; index < SearchReplayMaxPathCount; index++ {
			if !collector.add(fmt.Sprintf("p-%04d.go", index)) {
				t.Fatalf("collector rejected path %d at the boundary", index)
			}
		}
		if collector.add("overflow.go") {
			t.Fatal("collector accepted one path beyond replay bound")
		}
		if _, err := collector.identity(); err == nil {
			t.Fatal("overflowed corpus produced an identity")
		}
	})

	t.Run("aggregate bytes", func(t *testing.T) {
		collector := newSearchReplayCorpusCollector()
		for index := 0; !collector.exceeded; index++ {
			collector.add(fmt.Sprintf("%04d-%s.go", index, strings.Repeat("x", 180)))
		}
		if _, err := collector.identity(); err == nil {
			t.Fatal("aggregate-overflow corpus produced an identity")
		}
	})
}

func TestResolveSearchReplayPolicySubdirectoryGitProbeFailureFailsClosed(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "scope/safe.go", "package safe\n")
	scope := filepath.Join(repo, "scope")

	baseline, err := ResolveSearchReplayPolicy(t.Context(), scope, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.gitWorktree || !baseline.AllowsReplayPaths([]string{"safe.go"}) {
		t.Fatal("fixture did not resolve the no-HEAD subdirectory as a Git worktree")
	}
	writeFile(t, repo, ".git/info/exclude", "scope/safe.go\n")

	// Simulate a transient Git execution failure without removing the ancestor
	// metadata that says filesystem fallback would have weaker semantics.
	t.Setenv("PATH", t.TempDir())
	policy, err := ResolveSearchReplayPolicy(t.Context(), scope, SearchOptions{})
	if err == nil {
		t.Fatalf("Git probe failure under ancestor metadata returned policy %q", policy.Fingerprint())
	}
	if policy.Fingerprint() != "" || policy.AllowsReplayPaths([]string{"safe.go"}) {
		t.Fatal("failed Git probe returned a replayable filesystem policy")
	}
}

func TestResolveSearchReplayPolicyFallsBackToWorktreeWithoutHEAD(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFile(t, repo, ".gitignore", "blocked.go\n")
	writeFile(t, repo, "blocked.go", "package blocked\n")
	writeFile(t, repo, "keep.go", "package keep\n")

	fallback, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	explicitWorktree, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Fingerprint() != explicitWorktree.Fingerprint() {
		t.Fatalf("head fallback fingerprint = %q, explicit worktree = %q",
			fallback.Fingerprint(), explicitWorktree.Fingerprint())
	}
	if fallback.AllowsReplayPaths([]string{"blocked.go"}) {
		t.Fatal("head fallback ignored the worktree .gitignore")
	}
	if !fallback.AllowsReplayPaths([]string{"keep.go"}) {
		t.Fatal("head fallback rejected an admitted regular worktree file")
	}
}

func TestSearchReplayPolicyMatchesExactResolvedTree(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "first.go", "package first\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first")
	firstTree := rev(t, repo, "HEAD^{tree}")

	head, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !head.MatchesTree(firstTree) {
		t.Fatal("committed policy did not match the exact tree resolved with it")
	}

	writeFile(t, repo, "second.go", "package second\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "second")
	secondTree := rev(t, repo, "HEAD^{tree}")
	if firstTree == secondTree {
		t.Fatal("fixture did not move HEAD to a different tree")
	}
	if head.MatchesTree(secondTree) {
		t.Fatal("committed policy matched a tree resolved after HEAD moved")
	}

	worktree, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !worktree.MatchesTree("") || !worktree.MatchesTree(secondTree) {
		t.Fatal("effective worktree policy was incorrectly bound to a Git tree")
	}
	if (SearchReplayPolicy{}).MatchesTree("") {
		t.Fatal("zero policy matched a tree")
	}
}

func TestSearchReplayPolicyAllowsReplayPathsRejectsUnsafeWorktreePaths(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFile(t, repo, "keep.go", "package keep\n")
	writeFile(t, repo, ".env", "SECRET=placeholder\n")
	if err := os.Mkdir(filepath.Join(repo, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}

	policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsReplayPaths(nil) {
		t.Fatal("a valid policy rejected a payload with no repository paths")
	}
	if !policy.AllowsReplayPaths([]string{"keep.go"}) {
		t.Fatal("regular admitted worktree file was rejected")
	}

	for _, invalid := range []string{
		".env",
		"missing.go",
		"directory",
		"../outside.go",
		"nested/../../outside.go",
		`..\outside.go`,
		filepath.Join(repo, "keep.go"),
		"C:/outside.go",
	} {
		if policy.AllowsReplayPaths([]string{invalid}) {
			t.Errorf("unsafe replay path %q was admitted", invalid)
		}
	}

	symlink := filepath.Join(repo, "link.go")
	requireReplayTestSymlink(t, "keep.go", symlink)
	if policy.AllowsReplayPaths([]string{"link.go"}) {
		t.Error("worktree symlink was admitted for replay")
	}

	outside := t.TempDir()
	writeFile(t, outside, "secret.go", "package secret\n")
	requireReplayTestSymlink(t, outside, filepath.Join(repo, "linked"))
	if policy.AllowsReplayPaths([]string{"linked/secret.go"}) {
		t.Error("worktree path with a symlink ancestor was admitted for replay")
	}
}

func requireReplayTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		const windowsErrorPrivilegeNotHeld = syscall.Errno(1314)
		if errors.Is(err, os.ErrPermission) ||
			(runtime.GOOS == "windows" && errors.Is(err, windowsErrorPrivilegeNotHeld)) {
			t.Skipf("symlink creation requires unavailable privileges: %v", err)
		}
		t.Fatalf("create symlink %q -> %q: %v", link, target, err)
	}
}

func TestWorktreeNestedIgnoreReadIsBounded(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "nested/.gitignore", "*.generated\n")
	writeFile(t, repo, "oversized/.gitignore", strings.Repeat("x", maxNestedIgnoreFileBytes+1))
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	content, ok, err := readWorktreeNestedIgnore(root, repo, "nested/.gitignore")
	if err != nil || !ok || content != "*.generated\n" {
		t.Fatalf("bounded nested ignore read = (%q, %v, %v)", content, ok, err)
	}
	if content, ok, err := readWorktreeNestedIgnore(root, repo, "oversized/.gitignore"); err == nil || ok || content != "" {
		t.Fatalf("oversized nested ignore read = (%q, %v, %v), want reported refusal", content, ok, err)
	}
}

func TestSearchReplayPolicyWorktreeUsesGitEffectiveExcludes(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "nested/.gitignore", "*.generated\n")
	writeFile(t, repo, "global-excludes", "*.private\n")
	git(t, repo, "config", "core.excludesFile", filepath.Join(repo, "global-excludes"))
	writeFile(t, repo, ".git/info/exclude", "*.local-only\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "ignore policy")
	writeFile(t, repo, "nested/drop.generated", "generated\n")
	writeFile(t, repo, "nested/drop.private", "private\n")
	writeFile(t, repo, "nested/drop.local-only", "local\n")
	writeFile(t, repo, "nested/keep.go", "package keep\n")

	policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, ignored := range []string{
		"nested/drop.generated",
		"nested/drop.private",
		"nested/drop.local-only",
	} {
		if policy.AllowsReplayPaths([]string{ignored}) {
			t.Errorf("Git-ignored path %q was admitted for replay", ignored)
		}
	}
	if policy.AllowsReplayPaths([]string{".git/config"}) {
		t.Fatal("Git-internal path was admitted for replay")
	}
	if !policy.AllowsReplayPaths([]string{"nested/keep.go"}) {
		t.Fatal("Git-eligible worktree path was rejected")
	}
}

func TestSearchReplayPolicyFailsClosedWhenGitEligibilityCheckFails(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "keep.go", "package keep\n")
	ctx, cancel := context.WithCancel(t.Context())
	policy, err := ResolveSearchReplayPolicy(ctx, repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if policy.AllowsReplayPaths([]string{"keep.go"}) {
		t.Fatal("canceled Git eligibility validation admitted replay")
	}
}

func TestSearchReplayPolicyExplicitIncludeReadmitsGitIgnoredPath(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "global-excludes", "*.private\n")
	writeFile(t, repo, "replay.include", "*.private\n")
	git(t, repo, "config", "core.excludesFile", filepath.Join(repo, "global-excludes"))
	writeFile(t, repo, "nested/readmitted.private", "not a credential\n")

	withoutInclude, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	withInclude, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{
		Worktree:     true,
		IncludeFiles: []string{"replay.include"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if withoutInclude.AllowsReplayPaths([]string{"nested/readmitted.private"}) {
		t.Fatal("global Git exclude was ignored without an explicit include")
	}
	if !withInclude.AllowsReplayPaths([]string{"nested/readmitted.private"}) {
		t.Fatal("explicit include did not re-admit a Git-ignored regular file")
	}
}

func TestSearchReplayPolicyHonorsNestedVendoredReinclude(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "vendor/.gitignore", "*\n!mypkg/\n!mypkg/**\n")
	writeFile(t, repo, "vendor/mypkg/kept.go", "package kept\n")
	git(t, repo, "add", "-f", "vendor/.gitignore", "vendor/mypkg/kept.go")
	git(t, repo, "commit", "-m", "vendor policy")

	for _, worktree := range []bool{false, true} {
		policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: worktree})
		if err != nil {
			t.Fatal(err)
		}
		if !policy.AllowsReplayPaths([]string{"vendor/mypkg/kept.go"}) {
			t.Fatalf("worktree=%v: nested .gitignore re-inclusion did not override the vendored-directory heuristic", worktree)
		}
	}
}

func TestSearchReplayPolicyReportsGlobalNestedIgnoreCap(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "vendor/.gitignore", "*\n!mypkg/\n!mypkg/**\n")
	writeFile(t, repo, "vendor/mypkg/kept.go", "package kept\n")
	git(t, repo, "add", "-f", "vendor/.gitignore", "vendor/mypkg/kept.go")
	git(t, repo, "commit", "-m", "vendor policy")

	before, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !before.AllowsReplayPaths([]string{"vendor/mypkg/kept.go"}) {
		t.Fatal("fixture did not begin with the nested vendor rule inside the provider cap")
	}
	for index := 0; index < maxNestedIgnoreFiles; index++ {
		writeFile(t, repo, fmt.Sprintf("a%03d/.gitignore", index), "")
	}

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeSourceFiles(t.Context(), repo, ignores, false); err == nil ||
		!strings.Contains(err.Error(), "more than 512 nested ignore files") {
		t.Fatalf("provider nested-ignore cap error = %v", err)
	}

	if _, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true}); err == nil ||
		!strings.Contains(err.Error(), "nested-ignore candidates exceed") {
		t.Fatalf("replay nested-ignore cap error = %v", err)
	}
}

func TestSearchReplayPolicyNonGitFallbackUsesNestedIgnoreRules(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFile(t, repo, "nested/.gitignore", "*.generated\n")
	writeFile(t, repo, "nested/drop.generated", "generated\n")
	writeFile(t, repo, "nested/keep.go", "package keep\n")

	policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if policy.AllowsReplayPaths([]string{"nested/drop.generated"}) {
		t.Fatal("non-Git replay fallback ignored a nested .gitignore")
	}
	if !policy.AllowsReplayPaths([]string{"nested/keep.go"}) {
		t.Fatal("non-Git replay fallback rejected an eligible regular file")
	}
}

func TestValidateSearchReplayPathsBoundsHostileProvenance(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, SearchReplayMaxPathCount+1)
	for index := range tooMany {
		tooMany[index] = "same.go"
	}
	if err := ValidateSearchReplayPaths(tooMany); err == nil {
		t.Fatal("path-count limit was not enforced")
	}
	if err := ValidateSearchReplayPaths([]string{strings.Repeat("a", SearchReplayMaxPathBytes+1)}); err == nil {
		t.Fatal("per-path byte limit was not enforced")
	}
	aggregate := make([]string, SearchReplayMaxAggregatePathBytes/SearchReplayMaxPathBytes+1)
	for index := range aggregate {
		aggregate[index] = strings.Repeat("a", SearchReplayMaxPathBytes)
	}
	if err := ValidateSearchReplayPaths(aggregate); err == nil {
		t.Fatal("aggregate path-byte limit was not enforced")
	}
	if err := ValidateSearchReplayPaths([]string{"valid/path.go"}); err != nil {
		t.Fatalf("valid bounded provenance was rejected: %v", err)
	}

	policy := SearchReplayPolicy{repo: t.TempDir(), worktree: true, fingerprint: "valid"}
	if policy.AllowsReplayPaths(tooMany) {
		t.Fatal("AllowsReplayPaths bypassed provenance bounds")
	}
}

func TestSearchReplayPolicyHeadDoesNotRequireWorktreeFile(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "committed.go", "package committed\n")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-m", "initial")

	policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "committed.go")); err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsReplayPaths([]string{"committed.go"}) {
		t.Fatal("committed-tree replay incorrectly required the file to remain in the worktree")
	}
	if policy.AllowsReplayPaths([]string{".env"}) {
		t.Fatal("committed-tree replay admitted a built-in credential-store path")
	}
}

func TestSearchReplayPolicyHeadRequiresProviderCorpusMembership(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "app.go", "package app\n")
	writeFile(t, repo, "node_modules/pkg/dependency.go", "package dependency\n")
	writeFile(t, repo, "package-lock.json", "{}\n")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-m", "tracked corpus")

	policy, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsReplayPaths([]string{"app.go"}) {
		t.Fatal("ordinary HEAD member was rejected")
	}
	for _, excluded := range []string{
		"missing.go",
		"node_modules/pkg/dependency.go",
		"package-lock.json",
	} {
		if policy.AllowsReplayPaths([]string{excluded}) {
			t.Errorf("HEAD path outside the provider corpus %q was admitted", excluded)
		}
	}
}

func TestSearchReplayPolicyHeadUsesRepoSubdirectoryPathBasis(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "outside.go", "package outside\n")
	writeFile(t, repo, "scope/[literal].go", "package literal\n")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-m", "scoped tree")

	policy, err := ResolveSearchReplayPolicy(t.Context(), filepath.Join(repo, "scope"), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsReplayPaths([]string{"[literal].go"}) {
		t.Fatal("HEAD replay rejected a literal member relative to the --repo subdirectory")
	}
	if policy.AllowsReplayPaths([]string{"outside.go"}) {
		t.Fatal("HEAD replay admitted a root path outside the --repo subdirectory")
	}
}

func TestResolveSearchReplayPolicyUsesTheHeadMatcher(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, ".gitignore", "worktree-only.go\n")
	writeFile(t, repo, graphIgnoreFileName, "both-views.go\n")
	writeFile(t, repo, "worktree-only.go", "package worktreeonly\n")
	writeFile(t, repo, "both-views.go", "package bothviews\n")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-m", "initial")

	head, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := ResolveSearchReplayPolicy(t.Context(), repo, SearchOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !head.AllowsReplayPaths([]string{"worktree-only.go"}) {
		t.Fatal("head policy incorrectly loaded the repository .gitignore")
	}
	if head.AllowsReplayPaths([]string{"both-views.go"}) {
		t.Fatal("head policy did not load .graphignore")
	}
	if worktree.AllowsReplayPaths([]string{"worktree-only.go"}) {
		t.Fatal("worktree policy did not load .gitignore")
	}
	if head.Fingerprint() == worktree.Fingerprint() {
		t.Fatal("head and worktree policies produced the same fingerprint")
	}
}

func TestSearchResponsePathsCoversEveryPathBearingBlock(t *testing.T) {
	t.Parallel()

	response := SearchResponse{
		replayProvenancePaths: []string{"ranking-hidden.go", "duplicate.go"},
		Results:               []SearchResult{{FilePath: "result.go"}, {FilePath: "duplicate.go"}},
		SignatureTypes: []SearchSignatureType{{
			FilePath:        "signature.go",
			provenancePaths: []string{"signature-extension.go", "duplicate.go"},
		}},
		TypeCard:     []TypeCardEntry{{FilePath: "typecard.go"}},
		ContainerMap: &SearchContainerMap{FilePath: "container.go"},
		LiteralCluster: &SearchLiteralCluster{
			Hits: []SearchLiteralHit{
				{FilePath: "literal.go"},
				{FilePath: "duplicate.go"},
			},
			provenancePaths: []string{"literal-hidden.go", "duplicate.go"},
		},
		FileOutlines: []SearchFileOutline{{FilePath: "outline.go"}},
		ClosedSet: &SearchClosedSet{
			Sites:           []SearchClosedSetSite{{FilePath: "closed.go"}},
			provenancePaths: []string{"closed-declaration.go", "closed-hidden.go"},
		},
		CoverageNote: &SearchCoverageNote{
			FilePath:        "coverage.go",
			provenancePaths: []string{"coverage-shown.go", "coverage-hidden.go"},
		},
		VerifyCommand: &SearchVerifyCommand{provenancePaths: []string{
			"build-manifest.toml",
			"duplicate.go",
		}},
		Warnings: []ProviderWarning{
			{FilePath: "warning.go"},
			{},
		},
		PartialFailures: []PartialFailure{
			{FilePath: "failure.go"},
			{FilePath: "duplicate.go"},
		},
	}

	want := []string{
		"build-manifest.toml",
		"closed-declaration.go",
		"closed-hidden.go",
		"closed.go",
		"container.go",
		"coverage-hidden.go",
		"coverage-shown.go",
		"coverage.go",
		"duplicate.go",
		"failure.go",
		"literal-hidden.go",
		"literal.go",
		"outline.go",
		"ranking-hidden.go",
		"result.go",
		"signature-extension.go",
		"signature.go",
		"typecard.go",
		"warning.go",
	}
	if got := SearchResponsePaths(response); !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchResponsePaths() = %v, want %v", got, want)
	}
}

func TestBoundedWorktreeSearchReplayProvenanceIsDeterministicAndRejectsOverflow(t *testing.T) {
	t.Parallel()

	forward := make([]string, 0, SearchReplayMaxPathCount+100)
	for index := 0; index < SearchReplayMaxPathCount+100; index++ {
		forward = append(forward, fmt.Sprintf("path-%04d.go", index))
	}
	reversedWithDuplicates := make([]string, 0, len(forward)*2)
	for index := len(forward) - 1; index >= 0; index-- {
		reversedWithDuplicates = append(reversedWithDuplicates, forward[index], forward[index])
	}

	want := forward[:SearchReplayMaxPathCount+1]
	got := boundedWorktreeSearchReplayProvenance(reversedWithDuplicates)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded replay provenance = %v, want %v", got, want)
	}
	if repeated := boundedWorktreeSearchReplayProvenance(forward); !reflect.DeepEqual(repeated, got) {
		t.Fatalf("bounded replay provenance depends on input order: %v vs %v", got, repeated)
	}
	if err := ValidateSearchReplayPaths(got); err == nil {
		t.Fatal("overflow sentinel did not disable replay")
	}
}

func TestSearchResponsePathsIncludesUnreturnedWorktreeRankingCorpus(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFile(t, repo, "winner.go", "package sample\nfunc alphaBetaTarget() { println(\"alpha beta target\") }\n")
	writeFile(t, repo, "other/influencer.go", "package sample\nfunc alphaNoise() { println(\"alpha\") }\n")
	options := SearchOptions{
		Worktree:     true,
		Profile:      ProfileSyntaxOnly,
		TopK:         1,
		DisableCache: true,
	}
	withInfluencer, err := SearchRepository(t.Context(), repo, "test", "alpha beta target", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(withInfluencer.Results) != 1 || withInfluencer.Results[0].FilePath != "winner.go" {
		t.Fatalf("unexpected ranking fixture result: %#v", withInfluencer.Results)
	}
	paths := SearchResponsePaths(withInfluencer)
	if !containsString(paths, "other/influencer.go") {
		t.Fatalf("unreturned ranking influencer absent from replay provenance: %v", paths)
	}

	writeFile(t, repo, "other/influencer.go", "package sample\n")
	withoutInfluence, err := SearchRepository(t.Context(), repo, "test", "alpha beta target", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutInfluence.Results) != 1 ||
		withInfluencer.Results[0].Score == withoutInfluence.Results[0].Score {
		t.Fatalf("fixture file did not influence the visible score: before=%#v after=%#v",
			withInfluencer.Results, withoutInfluence.Results)
	}
}

func TestCommittedSearchResponseOmitsMutableCorpusProvenance(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "winner.go", "package sample\nfunc alphaBetaTarget() {}\n")
	writeFile(t, repo, "other/influencer.go", "package sample\nfunc alphaNoise() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "corpus")
	response, err := SearchRepository(t.Context(), repo, "test", "alpha beta target", SearchOptions{
		Profile:      ProfileSyntaxOnly,
		TopK:         1,
		DisableCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.replayProvenancePaths) != 0 {
		t.Fatalf("tree-bound HEAD response retained mutable corpus provenance: %v", response.replayProvenancePaths)
	}
}

func TestNoHitHeadFallbackRetainsWorktreeRankingCorpus(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFile(t, repo, "a.go", "package sample\n")
	writeFile(t, repo, "b.go", "package sample\n")
	response, err := SearchRepository(t.Context(), repo, "test", "missing concept", SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            1,
		MaxIndexedFiles: 1,
		DisableCache:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("no-hit fixture returned results: %#v", response.Results)
	}
	want := []string{"a.go", "b.go"}
	if !reflect.DeepEqual(response.replayProvenancePaths, want) {
		t.Fatalf("no-hit fallback provenance = %v, want %v", response.replayProvenancePaths, want)
	}
}

func TestSearchReplayProvenanceIsNotPublicJSON(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(SearchResponse{replayProvenancePaths: []string{"hidden-ranking.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hidden-ranking.go") {
		t.Fatalf("hidden replay provenance changed the public schema: %s", encoded)
	}
}

func TestResolveSearchReplayPolicyRejectsMissingExplicitRuleFile(t *testing.T) {
	t.Parallel()

	policy, err := ResolveSearchReplayPolicy(t.Context(), t.TempDir(), SearchOptions{
		Worktree:    true,
		IgnoreFiles: []string{"missing.ignore"},
	})
	if err == nil {
		t.Fatal("missing explicit ignore file did not fail policy resolution")
	}
	if policy.Fingerprint() != "" || policy.AllowsReplayPaths(nil) {
		t.Fatal("failed policy resolution returned a replayable policy")
	}
}
