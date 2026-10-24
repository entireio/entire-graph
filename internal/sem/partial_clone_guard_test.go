package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

var (
	gitBeforeLazyFetchGuard = gitutil.LazyFetchGuardStatus{Version: "2.44.0", Effective: false}
	gitWithLazyFetchGuard   = gitutil.LazyFetchGuardStatus{Version: "2.45.0", Effective: true}
)

// useGitLazyFetchGuard stands in a runtime Git for the metadata guard. It
// swaps a package variable, so callers must not be parallel tests (their
// subtests may be: they finish before the parent's cleanup restores it).
func useGitLazyFetchGuard(t *testing.T, status gitutil.LazyFetchGuardStatus) {
	t.Helper()
	previous := gitLazyFetchGuard
	gitLazyFetchGuard = func() gitutil.LazyFetchGuardStatus { return status }
	t.Cleanup(func() { gitLazyFetchGuard = previous })
}

var partialCloneConfigs = map[string]string{
	"partial clone extension":     "[extensions]\npartialClone = origin\n",
	"promisor remote":             "[remote \"origin\"]\npromisor = true\nurl = https://example.invalid/repo.git\n",
	"partial clone filter alone":  "[remote \"origin\"]\npartialCloneFilter = blob:none\n",
	"remote named by its own URL": "[remote \"https://github.com/entireio/cli-checkpoints.git\"]\n\tpromisor = true\n\tpartialclonefilter = blob:none\n",
}

// The version gate is the whole relaxation: the same partial-clone config is
// refused on a Git that ignores GIT_NO_LAZY_FETCH and admitted on one that
// honours it. Nothing else about the repository changes between the two runs.
func TestGitMetadataGuardAdmitsPartialCloneOnlyWhenLazyFetchIsDisabled(t *testing.T) {
	for name, config := range partialCloneConfigs {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		useGitLazyFetchGuard(t, gitBeforeLazyFetchGuard)
		if gitMetadataSafeForSubprocess(repo) {
			t.Errorf("%s: admitted on Git %s, which ignores GIT_NO_LAZY_FETCH", name, gitBeforeLazyFetchGuard.Version)
		}
		useGitLazyFetchGuard(t, gitLazyFetchGuardStatusUnknown)
		if gitMetadataSafeForSubprocess(repo) {
			t.Errorf("%s: admitted although the runtime Git version is unknown", name)
		}
		useGitLazyFetchGuard(t, gitWithLazyFetchGuard)
		if !gitMetadataSafeForSubprocess(repo) {
			t.Errorf("%s: refused on Git %s, which honours GIT_NO_LAZY_FETCH", name, gitWithLazyFetchGuard.Version)
		}
	}
}

var gitLazyFetchGuardStatusUnknown = gitutil.LazyFetchGuardStatus{}

// Admitting a partial clone must not relax anything else: an include in the
// same config is still refused on a Git that honours GIT_NO_LAZY_FETCH, and
// the diagnosis names the include rather than the partial clone.
func TestGitMetadataGuardStillRefusesIncludesInAnAdmittedPartialClone(t *testing.T) {
	useGitLazyFetchGuard(t, gitWithLazyFetchGuard)
	repo := writeRepoConfig(t, partialCloneConfigs["promisor remote"])
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("control: the partial clone without an include was refused")
	}
	repo = writeRepoConfig(t, partialCloneConfigs["promisor remote"]+"[include]\n\tpath = ../other\n")
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("an include passed metadata preflight because the config also declared a partial clone")
	}
	got := describeGitMetadataRefusal(repo)
	if !strings.Contains(got, "include or includeIf") || strings.Contains(got, "promisor") {
		t.Fatalf("refusal cause = %q, want the include and not the admitted partial clone", got)
	}
}

// The refusal text must be true for every caller. index --head has no
// filesystem fallback and query paths report their own, so the cause names the
// Git version and the remedy and promises no fallback.
func TestPartialCloneRefusalNamesTheGitVersionAndPromisesNoFallback(t *testing.T) {
	repo := writeRepoConfig(t, partialCloneConfigs["remote named by its own URL"])

	useGitLazyFetchGuard(t, gitutil.LazyFetchGuardStatus{Version: "2.39.5", Effective: false})
	got := describeGitMetadataRefusal(repo)
	for _, want := range []string{
		"declares a promisor remote (a partial clone)",
		"the Git on PATH (2.39.5) predates Git 2.45",
		"ignores GIT_NO_LAZY_FETCH",
		"Git subprocesses are refused for this repository",
		"Git 2.45 or newer reads partial clones offline",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal cause %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "falls back") || strings.Contains(got, "filesystem walk") {
		t.Errorf("refusal cause %q promises a fallback that index --head never reaches", got)
	}

	useGitLazyFetchGuard(t, gitLazyFetchGuardStatusUnknown)
	if got := describeGitMetadataRefusal(repo); !strings.Contains(got, "whose version could not be determined") {
		t.Errorf("unknown-version refusal cause = %q, want it to say the version is unknown", got)
	}

	useGitLazyFetchGuard(t, gitWithLazyFetchGuard)
	if got := describeGitMetadataRefusal(repo); got != "" {
		t.Errorf("blamed a partial clone that Git %s admits: %q", gitWithLazyFetchGuard.Version, got)
	}
}

// Only a partial clone pays for the Git version probe; an ordinary repository
// is decided from its metadata alone, before any Git process starts.
func TestGitMetadataGuardProbesGitOnlyForPartialClones(t *testing.T) {
	probes := 0
	previous := gitLazyFetchGuard
	gitLazyFetchGuard = func() gitutil.LazyFetchGuardStatus { probes++; return gitWithLazyFetchGuard }
	t.Cleanup(func() { gitLazyFetchGuard = previous })

	repo, gitDir := gitConfigPreflightFixture(t)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[remote \"origin\"]\nurl = https://example.invalid/repo.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) || probes != 0 {
		t.Fatalf("ordinary repository: safe=%v after %d Git version probes, want safe with none", gitMetadataSafeForSubprocess(repo), probes)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(partialCloneConfigs["promisor remote"]), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) || probes == 0 {
		t.Fatalf("partial clone: %d Git version probes, want the guard consulted", probes)
	}
}
