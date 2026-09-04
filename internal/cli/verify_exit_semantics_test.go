package cli

import (
	"bytes"
	"fmt"
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

// verifyPHPUnitErroredRunOutput is a real PHPUnit --testdox report, captured verbatim from PHPUnit
// 13.3.2 on PHP 8.5.9, for a run of two tests where one passes and one raises. That process exited
// 2, not 1.
const verifyPHPUnitErroredRunOutput = `PHPUnit 13.3.2 by Sebastian Bergmann and contributors.

Runtime:       PHP 8.5.9
Configuration: /tmp/probe/phpunit.xml

.E                                                                  2 / 2 (100%)

Time: 00:00.001, Memory: 20.00 MB

Delta
 ✔ Fixed
 ✘ Still errors
   │
   │ RuntimeException: boom
   │

ERRORS!
Tests: 2, Assertions: 1, Errors: 1.
`

// TestRenderVerifyVerdictAcceptsPHPUnitsErrorExitCode is the regression for the one runner in
// verifyTestFailureExitCodes that grades its own outcomes with more than one code.
//
// PHPUnit splits its nonzero exits by KIND of non-pass, not by whether the run finished: a test that
// fails an assertion exits 1 (FAILURE_EXIT), a test that raises exits 2 (EXCEPTION_EXIT), and 2 wins
// whenever a run contains both. An errored test is a per-test verdict — the report names it in the
// same numbered block as a failure and parseVerifyPHPUnit records it as a non-pass — so it explains
// the exit exactly as a failure does. With {1} alone, every PHPUnit run that contains a raising test
// was adjudicated INCOMPLETE however complete it was, which is the false-negative direction: a
// correct PASS/NO EFFECT turned into "verification is NOT complete".
//
// Measured, not inferred: PHPUnit 9.6.36, 10.5.64, 11.5.56, 12.5.34 and 13.3.2 all exit 1 for a bare
// assertion failure and 2 for a test that raises.
func TestRenderVerifyVerdictAcceptsPHPUnitsErrorExitCode(t *testing.T) {
	t.Parallel()

	// The parser's own reading of the real report is what makes exit 2 explicable: the errored test
	// is recorded, and it is recorded as a non-pass.
	current, parsed := parseVerifyPHPUnit(verifyPHPUnitErroredRunOutput)
	if !parsed {
		t.Fatalf("the real PHPUnit report was not recognised at all:\n%s", verifyPHPUnitErroredRunOutput)
	}
	errored, seen := current["Still errors"]
	if !seen {
		t.Fatalf("the errored test is absent from the parsed results %v", current)
	}
	if errored != verifyStatusFail {
		t.Fatalf("PHPUnit's errored test is recorded as %v, so exit 2 would have nothing to explain "+
			"it; the premise of this test is gone", errored)
	}

	// The baseline knew both tests were broken. The change fixes one; the other still raises. No
	// regression, no disappeared test, and the run finished — the report accounts for its own exit.
	baseline := verifyBaseline{Parser: "phpunit", Results: verifyResults{
		"Fixed":        verifyStatusFail,
		"Still errors": verifyStatusFail,
	}}
	got := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: "phpunit", parsed: true,
		exitCode: 2, maxBytes: verifyDefaultMaxBytes,
	}))
	if strings.Contains(got, "VERDICT: INCOMPLETE") {
		t.Fatalf("a complete PHPUnit run was called incomplete because 2 — the code PHPUnit uses for "+
			"a test that raised — was treated as a way for the run to come apart:\n%s", got)
	}
	if !strings.Contains(got, "VERDICT: PASS") {
		t.Fatalf("a fix with no regressions is not reported as a PASS:\n%s", got)
	}
}

// TestRenderVerifyVerdictStillRefusesPHPUnitCodesItDoesNotGradeWith pins the other edge of that
// widening: 1 and 2 are the whole of PHPUnit's non-pass vocabulary, and admitting 2 must not admit
// the codes that mean the run itself came apart.
func TestRenderVerifyVerdictStillRefusesPHPUnitCodesItDoesNotGradeWith(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "phpunit", Results: verifyResults{
		"Fixed":        verifyStatusFail,
		"Still errors": verifyStatusFail,
	}}
	current := verifyResults{"Fixed": verifyStatusPass, "Still errors": verifyStatusFail}

	for _, exitCode := range []int{3, 9, 127, 139, 255} {
		t.Run(fmt.Sprintf("exit %d", exitCode), func(t *testing.T) {
			t.Parallel()
			got := string(renderVerifyVerdict(verifyVerdictInput{
				baseline: baseline, current: current, parser: "phpunit", parsed: true,
				exitCode: exitCode, maxBytes: verifyDefaultMaxBytes,
			}))
			if !strings.Contains(got, "VERDICT: INCOMPLETE") {
				t.Fatalf("PHPUnit does not report a test outcome with exit %d, so a reported failure "+
					"cannot explain it:\n%s", exitCode, got)
			}
		})
	}
}
