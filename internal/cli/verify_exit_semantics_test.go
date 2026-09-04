package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderVerifyVerdictRefusesAnExitCodeTheRunnerNeverUsesForTestFailure is the regression for the
// converse of the unexplained-exit rule.
//
// "A nonzero exit with no failing test is not a PASS" closed one half of the hole and opened the
// other: ONE failing test was then taken to explain ANY nonzero exit. A run that both failed a test
// AND died — pytest exit 2 (interrupted), exit 3 (internal error), a segfault reported by the shell
// as 139 — reports a failure the baseline already knew about, so the delta is a fix plus a
// pre-existing failure and the verdict was "PASS — verification is complete" on a run that never
// finished. A failure the runner reported explains a nonzero exit only when the exit code is one
// that runner USES to report a test failure; pytest says 1, and every other code it emits means the
// run itself came apart.
func TestRenderVerifyVerdictRefusesAnExitCodeTheRunnerNeverUsesForTestFailure(t *testing.T) {
	t.Parallel()
	// The baseline knows both tests failed. The run fixes one and reports the other still failing,
	// so every baseline id is present, there is no regression, and there is a failure on record.
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
		"a::fixed":  verifyStatusFail,
		"a::broken": verifyStatusFail,
	}}
	current := verifyResults{"a::fixed": verifyStatusPass, "a::broken": verifyStatusFail}

	for _, testCase := range []struct {
		name     string
		parser   string
		exitCode int
	}{
		{name: "pytest interrupted", parser: "pytest", exitCode: 2},
		{name: "pytest internal error", parser: "pytest", exitCode: 3},
		{name: "pytest usage error", parser: "pytest", exitCode: 4},
		{name: "killed by SIGSEGV", parser: "pytest", exitCode: 139},
		{name: "killed by SIGKILL (OOM)", parser: "pytest", exitCode: 137},
		{name: "no exit status at all", parser: "pytest", exitCode: -1},
		{name: "go test build failure", parser: "go test", exitCode: 2},
		{name: "cargo test aborted", parser: "cargo test", exitCode: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := string(renderVerifyVerdict(verifyVerdictInput{
				baseline: baseline, current: current, parser: testCase.parser, parsed: true,
				exitCode: testCase.exitCode, maxBytes: verifyDefaultMaxBytes,
			}))
			if strings.Contains(got, "VERDICT: PASS") {
				t.Fatalf("a %s run was adjudicated a PASS because one reported failure was taken to "+
					"explain exit %d:\n%s", testCase.parser, testCase.exitCode, got)
			}
			if !strings.Contains(got, "VERDICT: INCOMPLETE") {
				t.Fatalf("a run that exited %d for a reason its output does not name is not reported "+
					"incomplete:\n%s", testCase.exitCode, got)
			}
		})
	}
}

// TestRenderVerifyVerdictKeepsTheRunnersOwnFailureExitCodes pins the other direction: the ordinary,
// overwhelmingly common case must not become INCOMPLETE. Each of these is the code its runner
// documents for "a test failed", so the reported failure explains it and the verdict stands.
func TestRenderVerifyVerdictKeepsTheRunnersOwnFailureExitCodes(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
		"a::fixed":  verifyStatusFail,
		"a::broken": verifyStatusFail,
	}}
	current := verifyResults{"a::fixed": verifyStatusPass, "a::broken": verifyStatusFail}

	for _, testCase := range []struct {
		parser   string
		exitCode int
	}{
		{parser: "pytest", exitCode: 1},
		{parser: "go test", exitCode: 1},
		{parser: "cargo test", exitCode: 101},
		{parser: "jest/vitest", exitCode: 1},
		{parser: "rspec", exitCode: 1},
		{parser: "minitest", exitCode: 1},
		{parser: "phpunit", exitCode: 1},
		{parser: "surefire", exitCode: 1},
		{parser: "ctest", exitCode: 8},
	} {
		t.Run(testCase.parser, func(t *testing.T) {
			t.Parallel()
			baseline.Parser = testCase.parser
			got := string(renderVerifyVerdict(verifyVerdictInput{
				baseline: baseline, current: current, parser: testCase.parser, parsed: true,
				exitCode: testCase.exitCode, maxBytes: verifyDefaultMaxBytes,
			}))
			if !strings.Contains(got, "VERDICT: PASS") {
				t.Fatalf("%s exit %d is how that runner reports a test failure, and the reported "+
					"failure explains it; the verdict must stand:\n%s",
					testCase.parser, testCase.exitCode, got)
			}
		})
	}
}

// TestVerifyBaselineRepoSurvivesADifferentWorkingDirectory is the regression for the recorded --repo
// path.
//
// resolveRepo returns the caller's argument verbatim, so `--repo .` is stored as ".". Both halves of
// verifySameRepo then resolve that against the CURRENT invocation's working directory, which is not
// where it was recorded:
//
//   - adjudicating the same repository from anywhere else is refused as "a different repository",
//     which is the workflow the finding names; and, worse,
//   - two DIFFERENT repositories recorded and adjudicated with `--repo .` compare equal — "." is ".",
//     whatever directory each ran in — so a baseline from another checkout is accepted and its delta
//     is adjudicated as if it described this one.
//
// The path is canonicalized at RECORD time, where the working directory is still the one the caller
// meant.
func TestVerifyBaselineRepoSurvivesADifferentWorkingDirectory(t *testing.T) {
	record := func(t *testing.T, repo, repoFlag, baselinePath string) {
		t.Helper()
		t.Chdir(repo)
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "0.1.0", Stdout: &out},
			[]string{"verify", "--repo", repoFlag, "--test", "sh run.sh",
				"--record-baseline", baselinePath}); err != nil {
			t.Fatalf("record in %s: %v", repo, err)
		}
	}
	adjudicate := func(t *testing.T, cwd, repoFlag, baselinePath string) (string, error) {
		t.Helper()
		t.Chdir(cwd)
		var out bytes.Buffer
		err := Run(t.Context(), Options{Version: "0.1.0", Stdout: &out},
			[]string{"verify", "--repo", repoFlag, "--test", "sh run.sh",
				"--pre-edit-baseline", baselinePath})
		return out.String(), err
	}
	newRepo := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "run.sh"),
			[]byte("#!/bin/sh\necho \"tests/test_a.py::test_x PASSED\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	t.Run("the same repository named absolutely from elsewhere", func(t *testing.T) {
		repo := newRepo(t)
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		record(t, repo, ".", baselinePath)
		// The caller's own spelling of the repository root, taken from inside it.
		absolute, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		verdict, adjudicateErr := adjudicate(t, t.TempDir(), absolute, baselinePath)
		if adjudicateErr != nil {
			t.Fatalf("a baseline recorded with --repo . in this very repository was refused when "+
				"adjudicated from another directory: %v", adjudicateErr)
		}
		if !strings.Contains(verdict, "VERDICT:") {
			t.Fatalf("no verdict was rendered:\n%s", verdict)
		}
	})

	t.Run("another repository, also named .", func(t *testing.T) {
		recorded := newRepo(t)
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		record(t, recorded, ".", baselinePath)

		elsewhere := newRepo(t)
		_, err := adjudicate(t, elsewhere, ".", baselinePath)
		if err == nil {
			t.Fatal("a baseline recorded in another checkout was accepted because both runs " +
				"spelled the repository \".\": the delta is about neither repository")
		}
		if !strings.Contains(err.Error(), "repository") {
			t.Fatalf("the refusal does not name the mismatched repository: %v", err)
		}
	})
}
