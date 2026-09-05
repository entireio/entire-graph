package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

	// Canonicalizing at record time fixed the SPELLING, and a directory has more than one route:
	// this is the same checkout, named through a symlink to it.
	t.Run("the same repository reached through a symlink", func(t *testing.T) {
		repo := newRepo(t)
		link := filepath.Join(t.TempDir(), "checkout-link")
		if err := os.Symlink(repo, link); err != nil {
			t.Skipf("this filesystem does not support symlinks: %v", err)
		}
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		record(t, repo, ".", baselinePath)

		verdict, adjudicateErr := adjudicate(t, t.TempDir(), link, baselinePath)
		if adjudicateErr != nil {
			t.Fatalf("a baseline recorded in this repository was refused when the same "+
				"repository was named through a symlink to it: %v", adjudicateErr)
		}
		if !strings.Contains(verdict, "VERDICT:") {
			t.Fatalf("no verdict was rendered:\n%s", verdict)
		}
	})

	// The other direction, and the one comparing IDENTITY must not cost: two genuinely different
	// checkouts are two repositories however precisely each is spelled.
	t.Run("another repository named by its own absolute path", func(t *testing.T) {
		recorded := newRepo(t)
		baselinePath := filepath.Join(t.TempDir(), "baseline.json")
		record(t, recorded, ".", baselinePath)

		elsewhere := newRepo(t)
		_, err := adjudicate(t, t.TempDir(), elsewhere, baselinePath)
		if err == nil {
			t.Fatal("a baseline recorded in another checkout was accepted: two distinct " +
				"repositories are not one repository under two names")
		}
		if !strings.Contains(err.Error(), "repository") {
			t.Fatalf("the refusal does not name the mismatched repository: %v", err)
		}
	})
}

// TestVerifySameRepoComparesIdentityNotSpelling pins the predicate itself, including the branch the
// end-to-end subtests cannot reach: what happens when a path cannot be stat'd at all.
//
// os.SameFile is the filesystem's own answer about two directories — device plus inode, file index
// on Windows — so a symlink, a bind mount, `/tmp` against `/private/tmp` and a case-variant spelling
// all resolve to one repository, while two checkouts stay two. When either side cannot be stat'd
// there is no identity to compare and the cleaned-string comparison stands alone: that can refuse a
// repository that is really the same one under another spelling, which is loud and costs a
// re-record, but it cannot ACCEPT two different ones, and the accept direction is the one that
// renders a confident verdict about a repository the baseline never described.
func TestVerifySameRepoComparesIdentityNotSpelling(t *testing.T) {
	t.Parallel()
	t.Run("a symlinked route is the same repository", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(repo, link); err != nil {
			t.Skipf("this filesystem does not support symlinks: %v", err)
		}
		if !verifySameRepo(repo, link) {
			t.Fatalf("the same repository was refused: recorded %q, current %q", repo, link)
		}
	})
	t.Run("two checkouts are two repositories", func(t *testing.T) {
		t.Parallel()
		recorded, current := t.TempDir(), t.TempDir()
		if verifySameRepo(recorded, current) {
			t.Fatalf("two different checkouts compared equal: %q and %q", recorded, current)
		}
	})
	t.Run("an unstattable recorded path falls back without accepting", func(t *testing.T) {
		t.Parallel()
		// The recorded checkout has been removed since the baseline was written. There is no
		// identity left to compare, and the fallback must not answer "same".
		gone := filepath.Join(t.TempDir(), "moved-away")
		if verifySameRepo(gone, t.TempDir()) {
			t.Fatalf("a baseline recorded in %q was accepted for a different, existing "+
				"repository", gone)
		}
	})
	t.Run("an unstattable path is still the same repository as its own spelling", func(t *testing.T) {
		t.Parallel()
		gone := filepath.Join(t.TempDir(), "moved-away")
		if !verifySameRepo(gone, gone) {
			t.Fatalf("a repository was refused against its own recorded path: %q", gone)
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

// TestVerifyRefusesALegacyBaselineRecordedWithARelativeRepo is the regression for the half of the
// repository check that record-time canonicalization could not reach.
//
// Canonicalizing at record time fixed every baseline THIS build writes. A baseline written by an
// older build still carries the caller's spelling verbatim, and `verifySameRepo` accepted it on
// string equality before any of the identity work below ran: two different checkouts that both said
// `--repo .` compared equal, exactly as they did before the fix, and a verdict was rendered about a
// repository the baseline never described.
//
// Resolving both sides — the remedy that suggests itself — does not close it. Both "."s resolve
// against the CURRENT invocation's working directory, so they still agree; the recording directory
// was never written down. The choice made here is to REFUSE such a baseline and say so, because the
// only alternative readings are "unknown" and "guess".
func TestVerifyRefusesALegacyBaselineRecordedWithARelativeRepo(t *testing.T) {
	newRepo := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "run.sh"),
			[]byte("#!/bin/sh\necho \"tests/test_a.py::test_x PASSED\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	// The predicate itself. This is the pair the early string equality accepted.
	if verifySameRepo(".", ".") {
		t.Fatal("a baseline that recorded its repository as \".\" was accepted for a run that also " +
			"spelled it \".\": the two \".\"s are different checkouts and the delta is about neither")
	}
	if verifySameRepo("../sibling", "../sibling") {
		t.Fatal("any relative recorded spelling is unanchored, not just \".\"")
	}

	// End to end: a baseline this build wrote, downgraded to the shape an older build wrote by
	// putting the caller's spelling back, and then adjudicated in a DIFFERENT checkout.
	recorded := newRepo(t)
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	t.Chdir(recorded)
	var recordOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Stdout: &recordOut},
		[]string{"verify", "--repo", ".", "--test", "sh run.sh",
			"--record-baseline", baselinePath}); err != nil {
		t.Fatalf("record: %v", err)
	}
	raw, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["repo"] == "." {
		t.Fatal("this build already stores the raw spelling, so the downgrade below proves nothing")
	}
	stored["repo"] = "."
	downgraded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, downgraded, 0o644); err != nil {
		t.Fatal(err)
	}

	elsewhere := newRepo(t)
	t.Chdir(elsewhere)
	var out bytes.Buffer
	err = Run(t.Context(), Options{Version: "0.1.0", Stdout: &out},
		[]string{"verify", "--repo", ".", "--test", "sh run.sh", "--pre-edit-baseline", baselinePath})
	if err == nil {
		t.Fatalf("a legacy baseline recorded in another checkout as \".\" was adjudicated here:\n%s",
			out.String())
	}
	if !strings.Contains(err.Error(), "repository") || !strings.Contains(err.Error(), "Re-record it") {
		t.Fatalf("the refusal does not name the repository or say what to do about it: %v", err)
	}
}

// TestRenderVerifyVerdictAcceptsRSpecsConfiguredFailureExitCode is the regression for pinning a code
// to a runner that does not own it.
//
// RSpec's failure exit code is a PROJECT setting, not an RSpec constant: `--failure-exit-code 2` on
// the command line and `config.failure_exit_code = 2` in spec_helper.rb both change it, and CI
// suites set it precisely to separate "a test failed" from "the run died". Neither source is visible
// to this verb — the second is Ruby it never reads, and the first can arrive through `.rspec` or
// `SPEC_OPTS` rather than the recorded command — so {1} adjudicated every such suite INCOMPLETE
// however complete it was. RSpec is unlisted for that reason, which restores the permissive rule an
// unknown runner already gets.
func TestRenderVerifyVerdictAcceptsRSpecsConfiguredFailureExitCode(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "rspec", Results: verifyResults{
		"rspec ./spec/a_spec.rb:1": verifyStatusFail,
		"rspec ./spec/b_spec.rb:2": verifyStatusFail,
	}}
	current := verifyResults{
		"rspec ./spec/a_spec.rb:1": verifyStatusPass,
		"rspec ./spec/b_spec.rb:2": verifyStatusFail,
	}
	for _, exitCode := range []int{1, 2, 3} {
		got := string(renderVerifyVerdict(verifyVerdictInput{
			baseline: baseline, current: current, parser: "rspec", parsed: true,
			exitCode: exitCode, maxBytes: verifyDefaultMaxBytes,
		}))
		if strings.Contains(got, "VERDICT: INCOMPLETE") {
			t.Fatalf("an RSpec run configured with --failure-exit-code %d was called incomplete "+
				"although its own reported failure explains the exit:\n%s", exitCode, got)
		}
		if !strings.Contains(got, "VERDICT: PASS") {
			t.Fatalf("rspec exit %d: want PASS, got:\n%s", exitCode, got)
		}
	}
}

// goTestBuildFailureOutput is real `go test -v ./...` output, captured verbatim from Go 1.26.5 on a
// module with three packages: one whose test fails, one that does not compile, one that passes.
//
// The exit code that accompanied it was 1 — the SAME code the toolchain uses for an ordinary failing
// test. Measured on Go 1.26.5, so is a setup failure (`FAIL ... [setup failed]`), a vet failure, an
// unknown flag and a missing package. That is the whole difficulty: no exit code separates "a test
// failed" from "a package never compiled", so the classification table cannot, and the output has to.
const goTestBuildFailureOutput = `# gt/compilepkg [gt/compilepkg.test]
compilepkg/a_test.go:3:46: cannot use "notanint" (untyped string constant) as int value in variable declaration
FAIL	gt/compilepkg [build failed]
=== RUN   TestFail
    a_test.go:3: boom
--- FAIL: TestFail (0.00s)
FAIL
FAIL	gt/failpkg	0.337s
=== RUN   TestOK
--- PASS: TestOK (0.00s)
PASS
ok  	gt/okpkg	0.468s
FAIL
`

// TestRenderVerifyVerdictRefusesAGoRunWithAnUnbuiltPackage is the regression for the half of the
// exit-code rule that listing `go test: {1}` cannot reach.
//
// `verifyExitCodeMeansTestFailure` asks whether the exit code is one the runner uses for a test
// failure. For go test the answer is yes for 1 — and 1 is also what it returns for a package that
// never compiled. So a single reported failure (a PRE-EXISTING one is enough; the baseline already
// knew about it) made exit 1 look accounted for, the unbuilt package produced no `--- FAIL:` line for
// the parser to see, and a run that verified none of that package was adjudicated
// "PASS — verification is complete; no further test runs are needed."
func TestRenderVerifyVerdictRefusesAGoRunWithAnUnbuiltPackage(t *testing.T) {
	t.Parallel()
	current, parser, ok := parseVerifyOutput(goTestBuildFailureOutput)
	if !ok || parser != "go test" {
		t.Fatalf("the go parser did not read its own output (parser=%q ok=%v): the premise is gone",
			parser, ok)
	}
	if _, seen := current["TestOK"]; !seen {
		t.Fatalf("the go parser did not report TestOK: %v", current)
	}
	// The parser structurally cannot see the unbuilt package: `FAIL ... [build failed]` names no test.
	for id := range current {
		if strings.Contains(id, "compilepkg") {
			t.Fatalf("the parser now attributes the build failure to id %q, so this test no longer "+
				"exercises the unattributed class", id)
		}
	}

	unattributed := verifyUnattributedFailures(parser, goTestBuildFailureOutput)
	if len(unattributed) != 1 || unattributed[0] != "gt/compilepkg" {
		t.Fatalf("the unbuilt package was not recovered from the output: %v", unattributed)
	}

	// The baseline knew both tests failed. The change fixes one; the other still fails and explains
	// exit 1 under the classification table. Every baseline id reported, so nothing is NOT RUN.
	baseline := verifyBaseline{Parser: "go test", Results: verifyResults{
		"TestOK":   verifyStatusFail,
		"TestFail": verifyStatusFail,
	}}
	got := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: parser, parsed: true,
		exitCode: 1, maxBytes: verifyDefaultMaxBytes, unattributed: unattributed,
	}))
	if strings.Contains(got, "VERDICT: PASS") {
		t.Fatalf("a go run in which a package never compiled was adjudicated a PASS, because a "+
			"pre-existing test failure made its exit 1 look explained:\n%s", got)
	}
	if !strings.Contains(got, "VERDICT: INCOMPLETE") {
		t.Fatalf("the unbuilt package did not make the run incomplete:\n%s", got)
	}
	if !strings.Contains(got, "gt/compilepkg") {
		t.Fatalf("the verdict does not name the target that never built:\n%s", got)
	}

	// A go run with NO build failure keeps the verdict it always had: the fix must not turn every
	// ordinary go suite into an INCOMPLETE.
	clean := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: parser, parsed: true,
		exitCode: 1, maxBytes: verifyDefaultMaxBytes,
	}))
	if !strings.Contains(clean, "VERDICT: PASS") {
		t.Fatalf("an ordinary go run whose exit 1 is explained by a still-failing test lost its "+
			"PASS:\n%s", clean)
	}
}

// TestRenderVerifyVerdictReportsIncompletenessAlongsideARegression is the regression for the verdict
// switch's precedence.
//
// `case len(newlyFailing) > 0` came first, so a run that regressed a test AND came apart was reported
// only as a REGRESSION. For the NOT RUN class that merely demoted evidence — the list is printed above
// the verdict, and verifyTruncateOutput drops the lists before it drops the verdict, so a tight byte
// cap removed it entirely. For an unexplained exit it lost the fact outright: that condition has no
// list of its own, and the verdict line was its only carrier. A caller keying on the verdict was told
// "one test regressed" about a run that was killed before it finished.
func TestRenderVerifyVerdictReportsIncompletenessAlongsideARegression(t *testing.T) {
	t.Parallel()

	t.Run("killed run that also regressed a test", func(t *testing.T) {
		t.Parallel()
		baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
			"a::fixed": verifyStatusFail,
			"a::green": verifyStatusPass,
		}}
		// Every baseline id reported, so NOT RUN is empty and the verdict line is the only place the
		// kill can be reported. 137 is SIGKILL — an OOM kill, not a pytest verdict.
		got := string(renderVerifyVerdict(verifyVerdictInput{
			baseline: baseline,
			current:  verifyResults{"a::fixed": verifyStatusPass, "a::green": verifyStatusFail},
			parser:   "pytest", parsed: true, exitCode: 137, maxBytes: verifyDefaultMaxBytes,
		}))
		if !strings.Contains(got, "VERDICT: REGRESSION in 1 test: a::green") {
			t.Fatalf("the actionable regression ids were dropped:\n%s", got)
		}
		if !strings.Contains(got, "INCOMPLETE") || !strings.Contains(got, "exited 137") {
			t.Fatalf("a run killed at exit 137 was reported as a plain REGRESSION, so nothing in the "+
				"output says verification never finished:\n%s", got)
		}
		if !strings.Contains(got, "Verification is NOT complete.") {
			t.Fatalf("the verdict does not state that verification is incomplete:\n%s", got)
		}
	})

	t.Run("truncated run that also regressed a test", func(t *testing.T) {
		t.Parallel()
		baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
			"a::fixed":     verifyStatusFail,
			"a::green":     verifyStatusPass,
			"a::never_ran": verifyStatusPass,
		}}
		input := verifyVerdictInput{
			baseline: baseline,
			current:  verifyResults{"a::fixed": verifyStatusPass, "a::green": verifyStatusFail},
			parser:   "pytest", parsed: true, exitCode: 1, maxBytes: verifyDefaultMaxBytes,
		}
		got := string(renderVerifyVerdict(input))
		if !strings.Contains(got, "VERDICT: REGRESSION in 1 test: a::green") {
			t.Fatalf("the actionable regression ids were dropped:\n%s", got)
		}
		if !strings.Contains(got, "INCOMPLETE") {
			t.Fatalf("a run that lost a baseline test was reported as a plain REGRESSION:\n%s", got)
		}
		// The byte cap keeps the VERDICT line and drops the lists, which is exactly when carrying the
		// incompleteness on the verdict line stops being cosmetic.
		verdictOnly := verifyLastLine(string(renderVerifyVerdict(input)))
		if !strings.Contains(verdictOnly, "INCOMPLETE") {
			t.Fatalf("the incompleteness is not on the VERDICT line, so verifyTruncateOutput would "+
				"discard it with the lists:\n%s", verdictOnly)
		}
	})

	t.Run("complete run that regressed a test keeps the plain verdict", func(t *testing.T) {
		t.Parallel()
		baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
			"a::fixed": verifyStatusFail,
			"a::green": verifyStatusPass,
		}}
		got := string(renderVerifyVerdict(verifyVerdictInput{
			baseline: baseline,
			current:  verifyResults{"a::fixed": verifyStatusPass, "a::green": verifyStatusFail},
			parser:   "pytest", parsed: true, exitCode: 1, maxBytes: verifyDefaultMaxBytes,
		}))
		if !strings.Contains(got, "VERDICT: REGRESSION in 1 test: a::green\n") {
			t.Fatalf("a complete run's regression verdict changed shape:\n%s", got)
		}
		if strings.Contains(got, "INCOMPLETE") {
			t.Fatalf("a complete run was labelled incomplete:\n%s", got)
		}
	})
}

// verifyLastLine returns the VERDICT line — the one line verifyTruncateOutput guarantees survives.
func verifyLastLine(rendered string) string {
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestRenderVerifyVerdictHonoursMaxBytesWithAMultiClauseVerdict is the regression for the byte cap
// against a verdict line that carries more than one clause.
//
// verifyTruncateOutput split the verdict at its LAST ": " and kept everything before it. That was safe
// while the only colon after "VERDICT" was the one introducing the id list — the head was then the
// short count clause. Reporting a regression together with the run's incompleteness introduces a
// SECOND colon, and the retained head became the whole regression clause, every listed id, and the
// start of the incompleteness clause: bounded by nothing. Measured before the fix, a 40-id regression
// on an incomplete run returned 1254 bytes under a 400-byte cap and the same 1254 under a 100-byte
// one. `--max-bytes` is an explicit contract and this broke it by more than 12x.
func TestRenderVerifyVerdictHonoursMaxBytesWithAMultiClauseVerdict(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{"a::never_ran": verifyStatusPass}}
	current := verifyResults{}
	for index := 0; index < 40; index++ {
		id := "suite::test_with_a_deliberately_long_identifier_number_" + string(rune('a'+index%26)) +
			string(rune('a'+index/26))
		baseline.Results[id] = verifyStatusPass
		current[id] = verifyStatusFail
	}
	for _, maxBytes := range []int{verifyDefaultMaxBytes, 800, 400, 300, 200, 100, 40, 12, 4, 1} {
		got := renderVerifyVerdict(verifyVerdictInput{
			baseline: baseline, current: current, parser: "pytest", parsed: true,
			exitCode: 1, maxBytes: maxBytes,
		})
		if len(got) > maxBytes {
			t.Fatalf("--max-bytes %d returned %d bytes:\n%s", maxBytes, len(got), got)
		}
		if !utf8.Valid(got) {
			t.Fatalf("--max-bytes %d cut a rune in half: %q", maxBytes, got)
		}
		// While there is room for it, the clause that carries the ANSWER must still survive: the count
		// is the information and the ids are the bonus.
		if maxBytes >= 100 && !strings.Contains(string(got), "VERDICT: REGRESSION in 40 tests") {
			t.Fatalf("--max-bytes %d dropped the verdict clause itself:\n%s", maxBytes, got)
		}
	}

	// The single-clause verdict keeps the behaviour it always had: at 400 bytes the ids yield one at a
	// time and the count clause survives with an ellipsis, rather than the whole line being cut.
	complete := verifyBaseline{Parser: "pytest", Results: verifyResults{}}
	for id := range current {
		complete.Results[id] = verifyStatusPass
	}
	tight := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: complete, current: current, parser: "pytest", parsed: true,
		exitCode: 1, maxBytes: 400,
	}))
	if len(tight) > 400 {
		t.Fatalf("a single-clause verdict overran a 400-byte cap at %d bytes:\n%s", len(tight), tight)
	}
	if !strings.Contains(tight, "VERDICT: REGRESSION in 40 tests: suite::") || !strings.HasSuffix(tight, "…\n") {
		t.Fatalf("the single-clause verdict stopped yielding ids one at a time:\n%s", tight)
	}
}

// TestRenderVerifyVerdictAcceptsJestsConfiguredFailureExitCode is the RSpec regression again, for the
// runner whose version of the problem is documented rather than idiomatic.
//
// `testFailureExitCode` is a first-class Jest setting. Measured on Jest 29.7.0 against a suite with one
// failing test:
//
//	jest a.test.js                              -> exit 1   (the default)
//	jest --testFailureExitCode=2 a.test.js      -> exit 2
//	jest.config.js { testFailureExitCode: 7 }   -> exit 7
//	package.json  { "jest": { ...: 5 } }        -> exit 5
//
// The last two are files this verb never opens, and package.json is where most Jest projects keep
// their configuration, so pinning {1} adjudicated every configured suite INCOMPLETE however complete
// it was. That is the same false negative the RSpec entry was removed for.
func TestRenderVerifyVerdictAcceptsJestsConfiguredFailureExitCode(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "jest/vitest", Results: verifyResults{
		"a > fixed":  verifyStatusFail,
		"a > broken": verifyStatusFail,
	}}
	current := verifyResults{"a > fixed": verifyStatusPass, "a > broken": verifyStatusFail}

	// Every code measured above, plus the default.
	for _, exitCode := range []int{1, 2, 5, 7} {
		got := string(renderVerifyVerdict(verifyVerdictInput{
			baseline: baseline, current: current, parser: "jest/vitest", parsed: true,
			exitCode: exitCode, maxBytes: verifyDefaultMaxBytes,
		}))
		if strings.Contains(got, "VERDICT: INCOMPLETE") {
			t.Fatalf("a jest run configured with testFailureExitCode %d was called incomplete although "+
				"its own reported failure explains the exit:\n%s", exitCode, got)
		}
		if !strings.Contains(got, "VERDICT: PASS") {
			t.Fatalf("jest exit %d: want PASS, got:\n%s", exitCode, got)
		}
	}

	// Unlisting gives up only the ordinary codes. A run that was KILLED is still refused, because
	// verifyExitCodeMeansTestFailure rejects >= 128 for listed and unlisted runners alike — otherwise
	// this change would have traded one false negative for the false PASS the verb exists to prevent.
	for _, exitCode := range []int{137, 139, -1} {
		got := string(renderVerifyVerdict(verifyVerdictInput{
			baseline: baseline, current: current, parser: "jest/vitest", parsed: true,
			exitCode: exitCode, maxBytes: verifyDefaultMaxBytes,
		}))
		if !strings.Contains(got, "VERDICT: INCOMPLETE") {
			t.Fatalf("a jest run that was killed (%d) was adjudicated complete:\n%s", exitCode, got)
		}
	}
}
