package sem

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

var (
	gitBeforeLazyFetchGuard = gitutil.LazyFetchGuardStatus{Version: "2.45.0", Effective: false}
	gitWithLazyFetchGuard   = gitutil.LazyFetchGuardStatus{Version: "2.45.1", Effective: true}
)

// useGitLazyFetchGuard stands in a runtime Git for the metadata guard. It
// swaps a package variable, so callers must not be parallel tests (their
// subtests may be: they finish before the parent's cleanup restores it).
func useGitLazyFetchGuard(t *testing.T, status gitutil.LazyFetchGuardStatus) {
	t.Helper()
	previous := gitLazyFetchGuard
	gitLazyFetchGuard = func(string) gitutil.LazyFetchGuardStatus { return status }
	t.Cleanup(func() { gitLazyFetchGuard = previous })
}

var partialCloneConfigs = map[string]string{
	"partial clone extension":     "[extensions]\npartialClone = origin\n",
	"promisor remote":             "[remote \"origin\"]\npromisor = true\nurl = https://example.invalid/repo.git\n",
	"partial clone filter alone":  "[remote \"origin\"]\npartialCloneFilter = blob:none\n",
	"remote named by its own URL": "[remote \"https://github.com/entireio/cli-checkpoints.git\"]\n\tpromisor = true\n\tpartialclonefilter = blob:none\n",
}

// The version gate is the whole relaxation: the same partial-clone config is
// refused on a Git not proven to honour GIT_NO_LAZY_FETCH and admitted on one that
// honours it. Nothing else about the repository changes between the two runs.
func TestGitMetadataGuardAdmitsPartialCloneOnlyWhenLazyFetchIsDisabled(t *testing.T) {
	for name, config := range partialCloneConfigs {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		useGitLazyFetchGuard(t, gitBeforeLazyFetchGuard)
		if gitMetadataSafeForSubprocess(repo) {
			t.Errorf("%s: admitted on Git %s, which does not honour GIT_NO_LAZY_FETCH for every lazy fetch", name, gitBeforeLazyFetchGuard.Version)
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

	useGitLazyFetchGuard(t, gitBeforeLazyFetchGuard)
	got := describeGitMetadataRefusal(repo)
	for _, want := range []string{
		"declares a promisor remote (a partial clone)",
		"lazy fetching cannot be proven disabled for the Git on PATH (2.45.0)",
		"Git subprocesses are refused for this repository",
		"GIT_NO_LAZY_FETCH stops every lazy fetch in Git 2.45.1 or newer (or, on an older release line, 2.39.4+, 2.40.2+, 2.41.1+, 2.42.2+, 2.43.4+ or 2.44.1+)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal cause %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "falls back") || strings.Contains(got, "filesystem walk") {
		t.Errorf("refusal cause %q promises a fallback that index --head never reaches", got)
	}
	// 2.45.0 is newer than the 2.44.1 backport, so "older"/"predates" would be false.
	if strings.Contains(got, "predates") || strings.Contains(got, "older than") || strings.Contains(got, "too old") {
		t.Errorf("refusal cause %q calls the Git old; it is only unproven", got)
	}

	useGitLazyFetchGuard(t, gitLazyFetchGuardStatusUnknown)
	if got := describeGitMetadataRefusal(repo); !strings.Contains(got, "lazy fetching cannot be proven disabled for the Git on PATH, whose version could not be determined") {
		t.Errorf("unknown-version refusal cause = %q, want it to say the version is unknown and claim nothing else about it", got)
	}

	useGitLazyFetchGuard(t, gitWithLazyFetchGuard)
	if got := describeGitMetadataRefusal(repo); got != "" {
		t.Errorf("blamed a partial clone that Git %s admits: %q", gitWithLazyFetchGuard.Version, got)
	}
}

// Only a partial clone pays for the Git version probe; an ordinary repository
// is decided from its metadata alone, before any Git process starts. The probe
// asks about the Git that runs in the repository, where the provider's own
// Git commands run, not in some other directory.
func TestGitMetadataGuardProbesGitOnlyForPartialClones(t *testing.T) {
	probes := 0
	var probedDirs []string
	previous := gitLazyFetchGuard
	gitLazyFetchGuard = func(dir string) gitutil.LazyFetchGuardStatus {
		probes++
		probedDirs = append(probedDirs, dir)
		return gitWithLazyFetchGuard
	}
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
	physical := func(path string) string {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	for _, dir := range probedDirs {
		if physical(dir) != physical(repo) {
			t.Fatalf("the guard probed Git in %q, want the repository %q", dir, repo)
		}
	}
}

// The refusal and its diagnosis must rest on one probe. A first `git version`
// that fails (a timeout under load) refuses the partial clone; the diagnosis
// must explain that refusal rather than re-probe, succeed, and blame nothing.
func TestPartialCloneRefusalAndDiagnosisShareOneProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Git is a POSIX shell script")
	}
	counter := filepath.Join(t.TempDir(), "calls")
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = version ] || exit 98\necho x >> " + strconv.Quote(counter) +
		"\n[ \"$(wc -l < " + strconv.Quote(counter) + ")\" -gt 1 ] || exit 1\necho 'git version 2.54.0'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := writeRepoConfig(t, partialCloneConfigs["promisor remote"])

	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("a partial clone was admitted although the Git version probe failed")
	}
	if got := describeGitMetadataRefusal(repo); !strings.Contains(got, "whose version could not be determined") {
		t.Fatalf("diagnosis of a refusal caused by a failed probe = %q; it re-probed instead of explaining the refusal", got)
	}
}

// Repository discovery may move metadata checks to the physical path of a
// symlinked --repo, but the provider's Git commands still run at the path as
// given, so the version probe must too (a shim keyed on PWD sees that path).
func TestGitMetadataGuardProbesTheGivenPathOfASymlinkedRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	var probedDirs []string
	previous := gitLazyFetchGuard
	gitLazyFetchGuard = func(dir string) gitutil.LazyFetchGuardStatus {
		probedDirs = append(probedDirs, dir)
		return gitWithLazyFetchGuard
	}
	t.Cleanup(func() { gitLazyFetchGuard = previous })

	real, gitDir := gitConfigPreflightFixture(t)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(partialCloneConfigs["remote named by its own URL"]), 0o644); err != nil {
		t.Fatal(err)
	}
	evil := t.TempDir()
	link := filepath.Join(evil, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(link) {
		t.Fatal("symlinked partial clone was refused although the stand-in Git honours GIT_NO_LAZY_FETCH")
	}
	if len(probedDirs) == 0 {
		t.Fatal("the guard never probed Git for the partial clone")
	}
	for _, dir := range probedDirs {
		if dir != link {
			t.Fatalf("the guard probed Git in %q, want exactly the given path %q, where the commands run", dir, link)
		}
	}
}
