package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func verifySample(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "verify", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestParseVerifyOutputReadsEveryRunner is the parser table. Every sample is real runner output, and
// every expectation names the runner's OWN ids — an id a caller cannot paste back into the runner is
// not an id, it is a label.
func TestParseVerifyOutputReadsEveryRunner(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		sample     string
		wantParser string
		want       verifyResults
	}{
		{
			sample: "pytest.txt", wantParser: "pytest",
			want: verifyResults{
				"tests/test_expand.py::test_is_ignored_file_absolute":       verifyStatusPass,
				"tests/test_expand.py::test_is_ignored_file_relative":       verifyStatusFail,
				"tests/test_self.py::TestRunTC::test_ignore_path_recursive": verifyStatusPass,
				"tests/test_broken.py::test_collect":                        verifyStatusError,
				// SKIPPED is deliberately absent: a skip is not a verdict about the code, and recording
				// it as a pass would make un-skipping look like a regression.
			},
		},
		{
			sample: "jest.txt", wantParser: "jest/vitest",
			want: verifyResults{
				"test/browser/render.test.js > renders a bigint child":  verifyStatusPass,
				"test/browser/render.test.js > renders nested children": verifyStatusPass,
				"test/browser/hooks.test.js > useState with bigint":     verifyStatusFail,
				"test/browser/hooks.test.js > useState with number":     verifyStatusPass,
			},
		},
		{
			sample: "cargo.txt", wantParser: "cargo test",
			want: verifyResults{
				"rules::flake8_simplify::tests::negation_with_equal_op":     verifyStatusPass,
				"rules::flake8_simplify::tests::negation_with_not_equal_op": verifyStatusFail,
				"rules::flake8_simplify::tests::double_negation":            verifyStatusPass,
			},
		},
		{
			sample: "phpunit.txt", wantParser: "phpunit",
			want: verifyResults{
				`PhpOffice\PhpSpreadsheet\Calculation\FunctionsTest::testFlattenSingleValue`: verifyStatusFail,
			},
		},
		{
			sample: "gotest.txt", wantParser: "go test",
			want: verifyResults{
				"TestExpandModules/absolute": verifyStatusPass,
				"TestExpandModules/relative": verifyStatusFail,
				"TestExpandModules":          verifyStatusFail,
				"TestIgnorePaths":            verifyStatusPass,
			},
		},
		{
			sample: "ctest.txt", wantParser: "ctest",
			want: verifyResults{
				"format-test": verifyStatusPass,
				"ranges-test": verifyStatusFail,
				"chrono-test": verifyStatusPass,
			},
		},
	} {
		t.Run(testCase.sample, func(t *testing.T) {
			t.Parallel()
			results, parser, ok := parseVerifyOutput(verifySample(t, testCase.sample))
			if !ok {
				t.Fatal("no parser recognised the sample")
			}
			if parser != testCase.wantParser {
				t.Fatalf("parser = %q, want %q", parser, testCase.wantParser)
			}
			if len(results) != len(testCase.want) {
				t.Fatalf("parsed %d results, want %d: %#v", len(results), len(testCase.want), results)
			}
			for id, want := range testCase.want {
				if got := results[id]; got != want {
					t.Fatalf("%q = %q, want %q (all: %#v)", id, got, want, results)
				}
			}
		})
	}
}

// TestParseVerifyOutputRefusesAnUnknownFormat pins rule 2: a parser that is not sure reports nothing.
// A half-read set would manufacture verdicts out of its own parse failures — a test missing from one
// side of the diff reads as a regression.
func TestParseVerifyOutputRefusesAnUnknownFormat(t *testing.T) {
	t.Parallel()
	if _, parser, ok := parseVerifyOutput(verifySample(t, "unknown.txt")); ok {
		t.Fatalf("an unrecognised format was claimed by %q", parser)
	}
}

// TestRenderVerifyVerdictAdjudicatesTheDelta is the output contract: a delta, a verdict, no evidence.
func TestRenderVerifyVerdictAdjudicatesTheDelta(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
		"a::fixed":       verifyStatusFail,
		"a::already_bad": verifyStatusFail,
		"a::green":       verifyStatusPass,
	}}
	for _, testCase := range []struct {
		name    string
		current verifyResults
		want    []string
		absent  []string
	}{
		{
			name: "a fix with no regressions is a complete verification",
			current: verifyResults{
				"a::fixed": verifyStatusPass, "a::already_bad": verifyStatusFail, "a::green": verifyStatusPass,
			},
			want: []string{
				"NEWLY PASSING (1): a::fixed",
				"PRE-EXISTING FAILURES (also failing before your edit; not caused by your change) (1): a::already_bad",
				"VERDICT: PASS — the change fixes the target behavior and introduces no regressions. " +
					"Verification is complete; no further test runs are needed.",
			},
			absent: []string{"NEWLY FAILING"},
		},
		{
			name: "a regression names every id, because the ids are the actionable part",
			current: verifyResults{
				"a::fixed": verifyStatusPass, "a::already_bad": verifyStatusFail, "a::green": verifyStatusFail,
			},
			want:   []string{"NEWLY FAILING (1): a::green", "VERDICT: REGRESSION in 1 test: a::green"},
			absent: []string{"VERDICT: PASS"},
		},
		{
			name: "no change is stated as such rather than as a pass",
			current: verifyResults{
				"a::fixed": verifyStatusFail, "a::already_bad": verifyStatusFail, "a::green": verifyStatusPass,
			},
			want:   []string{"VERDICT: NO EFFECT — the target tests behave exactly as before your edit."},
			absent: []string{"NEWLY PASSING", "NEWLY FAILING"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rendered := string(renderVerifyVerdict(verifyVerdictInput{
				baseline: baseline, current: testCase.current, parser: "pytest", parsed: true,
				maxBytes: verifyDefaultMaxBytes,
			}))
			for _, want := range testCase.want {
				if !strings.Contains(rendered, want) {
					t.Fatalf("missing %q:\n%s", want, rendered)
				}
			}
			for _, absent := range testCase.absent {
				if strings.Contains(rendered, absent) {
					t.Fatalf("unexpected %q:\n%s", absent, rendered)
				}
			}
			if len(rendered) > verifyDefaultMaxBytes {
				t.Fatalf("verdict is %d bytes, over the cap", len(rendered))
			}
		})
	}
}

// TestRenderVerifyVerdictCapsListsAndKeepsTheVerdict pins the two bounds: ids are capped at 20 with a
// count, and the byte cap is enforced from the END so the VERDICT line always survives. A verdict
// without its lists is still a verdict; lists without a verdict are evidence.
func TestRenderVerifyVerdictCapsListsAndKeepsTheVerdict(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{}}
	current := verifyResults{}
	for index := 0; index < 40; index++ {
		id := "suite::test_with_a_deliberately_long_identifier_number_" + string(rune('a'+index%26)) +
			string(rune('a'+index/26))
		baseline.Results[id] = verifyStatusPass
		current[id] = verifyStatusFail
	}
	// ROOMY: the 20-id cap binds and the remainder is COUNTED, not dropped silently.
	roomy := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: "pytest", parsed: true, maxBytes: verifyDefaultMaxBytes,
	}))
	if !strings.Contains(roomy, "and 20 more") {
		t.Fatalf("id list was not capped with a count:\n%s", roomy)
	}
	if !strings.Contains(roomy, "VERDICT: REGRESSION in 40 tests") {
		t.Fatalf("the count is missing from the verdict:\n%s", roomy)
	}
	// TIGHT: the byte cap wins over the id list, and the VERDICT CLAUSE with its count still survives —
	// that clause is the answer, the ids are the bonus.
	tight := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: "pytest", parsed: true, maxBytes: 400,
	}))
	if len(tight) > 400 {
		t.Fatalf("rendered %d bytes over a 400-byte cap:\n%s", len(tight), tight)
	}
	if !strings.Contains(tight, "VERDICT: REGRESSION in 40 tests") {
		t.Fatalf("the verdict clause did not survive the byte cap:\n%s", tight)
	}
}

// TestRenderVerifyVerdictSaysWhenItIsCoarse pins the honest degradation: with no parseable format there
// are no ids, so there is no delta and no claim about regressions — only the exit code, labelled.
func TestRenderVerifyVerdictSaysWhenItIsCoarse(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		exit int
		want string
	}{
		{exit: 0, want: "VERDICT: PASS (exit 0)"},
		{exit: 3, want: "VERDICT: FAIL (exit 3)"},
	} {
		rendered := string(renderVerifyVerdict(verifyVerdictInput{
			baseline: verifyBaseline{}, parsed: false, exitCode: testCase.exit,
			maxBytes: verifyDefaultMaxBytes,
		}))
		if !strings.Contains(rendered, testCase.want) {
			t.Fatalf("missing %q:\n%s", testCase.want, rendered)
		}
		if !strings.Contains(rendered, "not recognised") {
			t.Fatalf("a coarse verdict did not say it was coarse:\n%s", rendered)
		}
	}
}

// TestVerifyEndToEndRecordsThenAdjudicates runs the real verb against a fixture whose "runner" is a
// shell script, so the whole path — record, edit, diff, verdict — is exercised without a toolchain.
func TestVerifyEndToEndRecordsThenAdjudicates(t *testing.T) {
	repo := t.TempDir()
	// The script reports pytest-shaped output and flips one test to PASSED once `fixed` exists.
	write(t, repo, "run.sh", `#!/bin/sh
echo "tests/test_a.py::test_target $( [ -f fixed ] && echo PASSED || echo FAILED )"
echo "tests/test_a.py::test_untouched PASSED"
echo "tests/test_a.py::test_broken FAILED"
[ -f regress ] && echo "tests/test_a.py::test_untouched FAILED"
exit 1
`)
	if err := os.Chmod(filepath.Join(repo, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	run := func(args ...string) string {
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
			append([]string{"verify", "--repo", repo, "--test", "sh run.sh"}, args...)); err != nil {
			t.Fatalf("verify %v: %v", args, err)
		}
		return out.String()
	}

	if got := run("--record-baseline", baselinePath); !strings.Contains(got, "BASELINE RECORDED") ||
		!strings.Contains(got, "pytest") {
		t.Fatalf("record output = %q", got)
	}
	// PASS: the target flips, nothing else moves, and the pre-existing failure is labelled.
	write(t, repo, "fixed", "")
	pass := run("--pre-edit-baseline", baselinePath)
	for _, want := range []string{
		"NEWLY PASSING (1): tests/test_a.py::test_target",
		"PRE-EXISTING FAILURES", "tests/test_a.py::test_broken",
		"VERDICT: PASS —",
	} {
		if !strings.Contains(pass, want) {
			t.Fatalf("PASS verdict missing %q:\n%s", want, pass)
		}
	}
	// REGRESSION wins over the fix: a green test going red is the actionable fact.
	write(t, repo, "regress", "")
	regression := run("--pre-edit-baseline", baselinePath)
	if !strings.Contains(regression, "VERDICT: REGRESSION in 1 test: tests/test_a.py::test_untouched") {
		t.Fatalf("regression verdict wrong:\n%s", regression)
	}
	if strings.Contains(regression, "VERDICT: PASS") {
		t.Fatalf("a regression was reported as a pass:\n%s", regression)
	}
	// No raw runner output ever reaches the caller.
	for _, forbidden := range []string{"exit 1", "run.sh", "echo"} {
		if strings.Contains(regression, forbidden) {
			t.Fatalf("runner output leaked (%q):\n%s", forbidden, regression)
		}
	}
}

// TestParseVerifyFlagsRequiresABaseline pins that the verb cannot be used without the thing that makes
// it a delta. Without a baseline it would be a test runner, which is what does not work.
func TestParseVerifyFlagsRequiresABaseline(t *testing.T) {
	t.Parallel()
	if _, err := parseVerifyFlags([]string{"--test", "pytest"}); err == nil {
		t.Fatal("verify ran without a baseline")
	}
	if _, err := parseVerifyFlags([]string{"--pre-edit-baseline", "b.json"}); err == nil {
		t.Fatal("verify ran without a test command")
	}
	flags, err := parseVerifyFlags([]string{"--test", "pytest -q", "--record-baseline", "b.json"})
	if err != nil || flags.MaxBytes != verifyDefaultMaxBytes {
		t.Fatalf("flags = %#v err = %v", flags, err)
	}
	if !verifyCompiledCommand("cargo test -p x") || verifyCompiledCommand("pytest -q") {
		t.Fatal("timeout ecosystem split is wrong")
	}
}

// TestVerifySetupFailureStopsTheRun pins the exit code of the SETUP command. runVerifyShell reports a
// command that ran and failed as (nonzero, nil error), so a caller that keeps only the error learns
// nothing about a failed install or build. The regression this guards is not cosmetic: the test
// command then runs against a tree whose dependencies were never built and its results are
// adjudicated as if setup had worked, which is a confident verdict about the wrong tree.
func TestVerifySetupFailureStopsTheRun(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "setup.sh", "#!/bin/sh\nexit 3\n")
	// The test command's ONLY observable effect is this file. Asserting on its absence proves the
	// command was never launched, rather than proving something about a message.
	write(t, repo, "run.sh", "#!/bin/sh\n: > ran\necho \"tests/test_a.py::test_x PASSED\"\n")
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
		[]string{"verify", "--repo", repo, "--setup", "sh setup.sh", "--test", "sh run.sh",
			"--record-baseline", baselinePath})
	if err == nil {
		t.Fatalf("a setup command exiting 3 was accepted; output = %q", out.String())
	}
	if !strings.Contains(err.Error(), "setup command exited 3") {
		t.Fatalf("error does not name the setup exit code: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "ran")); statErr == nil {
		t.Fatal("the test command ran after setup failed")
	}
}

// TestVerifyRefusesAForeignBaseline pins that a baseline is checked against THIS run before it is
// diffed against it. Nothing in the id diff can tell that two id sets came from different
// repositories or different test commands, so an unchecked baseline yields an authoritative-looking
// verdict about nothing.
func TestVerifyRefusesAForeignBaseline(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "run.sh", "#!/bin/sh\necho \"tests/test_a.py::test_x PASSED\"\n")
	baselineDir := t.TempDir()

	record := func(path, testCommand string) {
		t.Helper()
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
			[]string{"verify", "--repo", repo, "--test", testCommand, "--record-baseline", path}); err != nil {
			t.Fatalf("record %s: %v", path, err)
		}
	}
	adjudicate := func(path, testCommand string) error {
		var out bytes.Buffer
		return Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
			[]string{"verify", "--repo", repo, "--test", testCommand, "--pre-edit-baseline", path})
	}

	good := filepath.Join(baselineDir, "good.json")
	record(good, "sh run.sh")
	if err := adjudicate(good, "sh run.sh"); err != nil {
		t.Fatalf("a matching baseline was refused: %v", err)
	}

	// A baseline recorded from a DIFFERENT test command.
	other := filepath.Join(baselineDir, "other.json")
	record(other, "sh ./run.sh")
	if err := adjudicate(other, "sh run.sh"); err == nil {
		t.Fatal("a baseline from another test command was accepted")
	} else if !strings.Contains(err.Error(), "test command") {
		t.Fatalf("error does not name the mismatched test command: %v", err)
	}

	// A baseline recorded in a DIFFERENT repository, and one written by a future format version.
	for _, testCase := range []struct {
		name  string
		patch func(*verifyBaseline)
		want  string
	}{
		{name: "repo", patch: func(b *verifyBaseline) { b.Repo = filepath.Join(t.TempDir(), "elsewhere") },
			want: "repository"},
		{name: "format", patch: func(b *verifyBaseline) { b.FormatVersion = verifyBaselineFormatVersion + 1 },
			want: "format_version"},
		{name: "parser", patch: func(b *verifyBaseline) { b.Parser = "cargo test" }, want: "parsed by"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var baseline verifyBaseline
			content, readErr := os.ReadFile(good)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if unmarshalErr := json.Unmarshal(content, &baseline); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			testCase.patch(&baseline)
			encoded, marshalErr := json.Marshal(baseline)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			path := filepath.Join(baselineDir, testCase.name+".json")
			if writeErr := os.WriteFile(path, encoded, 0o644); writeErr != nil {
				t.Fatal(writeErr)
			}
			err := adjudicate(path, "sh run.sh")
			if err == nil {
				t.Fatalf("a baseline with a mismatched %s was accepted", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error does not name the mismatch (%s): %v", testCase.want, err)
			}
		})
	}
}

// TestRenderVerifyVerdictRefusesAnUnexplainedNonzeroExit pins that parsed mode still reads the process
// exit code. A collection error, a configuration error or a crash after the first test can leave the
// per-test output entirely green while the runner exits nonzero; adjudicating that as "PASS —
// verification is complete" is exactly the false PASS this verb exists to prevent.
func TestRenderVerifyVerdictRefusesAnUnexplainedNonzeroExit(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
		"a::fixed": verifyStatusFail,
		"a::green": verifyStatusPass,
	}}
	current := verifyResults{"a::fixed": verifyStatusPass, "a::green": verifyStatusPass}

	crashed := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: "pytest", parsed: true,
		exitCode: 2, maxBytes: verifyDefaultMaxBytes,
	}))
	if strings.Contains(crashed, "VERDICT: PASS") {
		t.Fatalf("a nonzero exit with no failing test was adjudicated a PASS:\n%s", crashed)
	}
	if !strings.Contains(crashed, "VERDICT: INCOMPLETE") || !strings.Contains(crashed, "exited 2") {
		t.Fatalf("the unexplained exit is not reported:\n%s", crashed)
	}

	// EXPLAINED: the same nonzero exit accompanied by a failing test is the runner's normal way of
	// reporting that failure, and must stay a REGRESSION rather than becoming INCOMPLETE.
	regressed := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: verifyResults{"a::fixed": verifyStatusPass, "a::green": verifyStatusFail},
		parser: "pytest", parsed: true, exitCode: 1, maxBytes: verifyDefaultMaxBytes,
	}))
	if !strings.Contains(regressed, "VERDICT: REGRESSION in 1 test: a::green") {
		t.Fatalf("an explained nonzero exit lost its regression verdict:\n%s", regressed)
	}

	// A clean exit with a fix is still the complete verification it always was.
	clean := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: current, parser: "pytest", parsed: true,
		exitCode: 0, maxBytes: verifyDefaultMaxBytes,
	}))
	if !strings.Contains(clean, "VERDICT: PASS") {
		t.Fatalf("a clean run stopped being a PASS:\n%s", clean)
	}
}

// TestRenderVerifyVerdictReportsDisappearedBaselineTests pins the class the delta loop structurally
// cannot see: the loop iterates ids in the CURRENT output, so a baseline test the run never reached
// is silently dropped. A run that prints one newly passing test and then aborts would otherwise be
// adjudicated "PASS — introduces no regressions" on the strength of a single line.
func TestRenderVerifyVerdictReportsDisappearedBaselineTests(t *testing.T) {
	t.Parallel()
	baseline := verifyBaseline{Parser: "pytest", Results: verifyResults{
		"a::fixed":     verifyStatusFail,
		"a::never_ran": verifyStatusPass,
		"a::also_gone": verifyStatusPass,
	}}
	// The run reported ONE test and then stopped. Exit 0 on purpose: the exit code must not be the
	// only thing standing between this and a PASS.
	aborted := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline, current: verifyResults{"a::fixed": verifyStatusPass},
		parser: "pytest", parsed: true, exitCode: 0, maxBytes: verifyDefaultMaxBytes,
	}))
	if strings.Contains(aborted, "VERDICT: PASS") {
		t.Fatalf("a partial run was adjudicated a PASS:\n%s", aborted)
	}
	if !strings.Contains(aborted, "VERDICT: INCOMPLETE") {
		t.Fatalf("a partial run did not report itself incomplete:\n%s", aborted)
	}
	for _, id := range []string{"a::never_ran", "a::also_gone"} {
		if !strings.Contains(aborted, id) {
			t.Fatalf("disappeared test %q is not named:\n%s", id, aborted)
		}
	}
	if !strings.Contains(aborted, "NOT RUN") {
		t.Fatalf("the disappeared class is unlabelled:\n%s", aborted)
	}
	// A complete run keeps its old verdict: nothing here fires when every baseline id reported.
	complete := string(renderVerifyVerdict(verifyVerdictInput{
		baseline: baseline,
		current: verifyResults{
			"a::fixed": verifyStatusPass, "a::never_ran": verifyStatusPass, "a::also_gone": verifyStatusPass,
		},
		parser: "pytest", parsed: true, exitCode: 0, maxBytes: verifyDefaultMaxBytes,
	}))
	if !strings.Contains(complete, "VERDICT: PASS") {
		t.Fatalf("a complete run lost its PASS:\n%s", complete)
	}
	if strings.Contains(complete, "NOT RUN") {
		t.Fatalf("a complete run reported a disappeared test:\n%s", complete)
	}
}

// TestVerifyShellForSelectsAPlatformShell pins the shell selection.
//
// entire-graph builds, ships and tests a Windows binary, but a normal Windows installation has no
// executable named `sh`, so `exec.CommandContext(ctx, "sh", ...)` could never launch there — the
// verb was reachable on a platform where every verification failed before running. Git for Windows
// installs sh.exe and bash.exe, so the shell usually exists and only has to be looked up; when it
// genuinely is not there the verb must say so, because cmd.exe would run the POSIX commands this
// verb emits incorrectly and turn a launch failure into a wrong verdict.
func TestVerifyShellForSelectsAPlatformShell(t *testing.T) {
	t.Parallel()
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	found := func(name string) (string, error) {
		if name == "sh" {
			return `C:\Program Files\Git\usr\bin\sh.exe`, nil
		}
		return "", exec.ErrNotFound
	}
	bashOnly := func(name string) (string, error) {
		if name == "bash" {
			return `C:\Program Files\Git\bin\bash.exe`, nil
		}
		return "", exec.ErrNotFound
	}

	if shell, err := verifyShellFor("linux", missing); err != nil || shell != "sh" {
		t.Fatalf("verifyShellFor(linux) = %q, %v; want sh and no error on every POSIX platform", shell, err)
	}
	if shell, err := verifyShellFor("darwin", missing); err != nil || shell != "sh" {
		t.Fatalf("verifyShellFor(darwin) = %q, %v; want sh and no error", shell, err)
	}

	shell, err := verifyShellFor("windows", found)
	if err != nil {
		t.Fatalf("verifyShellFor(windows) with sh on PATH returned %v, want the resolved shell", err)
	}
	if shell != `C:\Program Files\Git\usr\bin\sh.exe` {
		t.Fatalf("verifyShellFor(windows) = %q, want the sh.exe PATH lookup resolved", shell)
	}
	if shell, err = verifyShellFor("windows", bashOnly); err != nil ||
		shell != `C:\Program Files\Git\bin\bash.exe` {
		t.Fatalf("verifyShellFor(windows) = %q, %v; want bash when only bash is installed", shell, err)
	}

	shell, err = verifyShellFor("windows", missing)
	if err == nil {
		t.Fatalf("verifyShellFor(windows) with no POSIX shell returned %q and no error: "+
			"launching a shell that does not exist reports a failure the caller cannot act on", shell)
	}
	if !strings.Contains(err.Error(), "POSIX shell") {
		t.Fatalf("error %q does not say a POSIX shell is what is missing", err)
	}
}
