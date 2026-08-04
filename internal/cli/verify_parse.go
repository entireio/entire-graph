package cli

import (
	"regexp"
	"strings"
)

// Test-result parsing for `entire graph verify`
// ============================================
//
// The verb's whole value is that it returns a VERDICT rather than output. That is only possible if the
// runner's own report can be reduced to a set of {test id -> status}: a verdict about a delta needs two
// comparable sets, and raw text is not comparable.
//
// Every parser here obeys the same three rules, and they are what keep the verb honest rather than
// merely terse:
//
//  1. IDS ARE THE RUNNER'S OWN. A parser never invents, prettifies or re-nests an identifier. An id is
//     what the caller would paste back into the runner to re-run that one test, which is what makes a
//     regression list actionable instead of decorative.
//  2. A PARSER THAT IS NOT SURE REPORTS NOTHING. Returning a half-read set is worse than returning
//     none: a missing test reads as "deleted" on one side of a diff and as a regression on the other,
//     so the verb would manufacture verdicts out of its own parse failures. `ok=false` degrades the
//     whole run to the exit-code verdict, which is coarse and says so.
//  3. NO OUTPUT IS EVER FORWARDED. Nothing in this file returns runner text to the caller. The measured
//     failure this verb exists to end is an agent reading test output and deciding to run more tests;
//     handing back a "helpful excerpt" reopens exactly that loop.
type verifyStatus string

const (
	verifyStatusPass verifyStatus = "pass"
	verifyStatusFail verifyStatus = "fail"
	// verifyStatusError is a test that did not run to a verdict — a collection error, a panic before
	// assertions, a build failure attributed to one target. It is kept distinct from `fail` because a
	// pre-existing collection error is a fact about the checkout, and calling it a failure would put it
	// in the regression list on the first run that fixes something else.
	verifyStatusError verifyStatus = "error"
)

// verifyResults is one run's normalized outcome.
type verifyResults map[string]verifyStatus

// verifyParser reads one runner's report. `ok` is false when the format was not recognised WITH
// CONFIDENCE — see rule 2 above.
type verifyParser struct {
	name  string
	parse func(output string) (verifyResults, bool)
}

// verifyParsers is tried in order and the FIRST confident parser wins. Order matters only where two
// formats could plausibly match the same text, so the most distinctive signatures come first.
var verifyParsers = []verifyParser{
	{name: "pytest", parse: parseVerifyPytest},
	{name: "cargo test", parse: parseVerifyCargo},
	{name: "go test", parse: parseVerifyGoTest},
	{name: "phpunit", parse: parseVerifyPHPUnit},
	{name: "jest/vitest", parse: parseVerifyJest},
	{name: "rspec", parse: parseVerifyRSpec},
	{name: "minitest", parse: parseVerifyMinitest},
	{name: "surefire", parse: parseVerifySurefire},
	{name: "ctest", parse: parseVerifyCTest},
}

// parseVerifyOutput reduces a runner's report to a normalized result set, naming the parser that read
// it. An unrecognised format returns ok=false, which is what makes the verb degrade to an exit-code
// verdict rather than guess.
func parseVerifyOutput(output string) (verifyResults, string, bool) {
	for _, parser := range verifyParsers {
		if results, ok := parser.parse(output); ok && len(results) > 0 {
			return results, parser.name, true
		}
	}
	return nil, "", false
}

// verifyLines splits output into trimmed lines once, since every parser scans line-wise.
func verifyLines(output string) []string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	return lines
}

// pytest: `tests/test_x.py::TestC::test_name PASSED` in verbose mode, plus the short-summary block
// (`FAILED tests/test_x.py::test_name - AssertionError`) which is present even without -v. Both are
// read, because a run may have one and not the other and the ids are identical between them.
var (
	verifyPytestVerbose = regexp.MustCompile(`^(\S+::\S+)\s+(PASSED|FAILED|ERROR|XFAIL|XPASS|SKIPPED)\b`)
	verifyPytestSummary = regexp.MustCompile(`^(FAILED|ERROR|PASSED)\s+(\S+::\S+)`)
)

func parseVerifyPytest(output string) (verifyResults, bool) {
	if !strings.Contains(output, "::") {
		return nil, false
	}
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		if match := verifyPytestVerbose.FindStringSubmatch(line); match != nil {
			// SKIPPED and the xfail family are deliberately not recorded. A skip is not a verdict about
			// the code, and recording it as a pass would make un-skipping look like a regression.
			if status, keep := verifyPytestStatus(match[2]); keep {
				results[match[1]] = status
			}
			continue
		}
		if match := verifyPytestSummary.FindStringSubmatch(line); match != nil {
			if status, keep := verifyPytestStatus(match[1]); keep {
				results[match[2]] = status
			}
		}
	}
	return results, len(results) > 0
}

func verifyPytestStatus(word string) (verifyStatus, bool) {
	switch word {
	case "PASSED", "XPASS":
		return verifyStatusPass, true
	case "FAILED":
		return verifyStatusFail, true
	case "ERROR":
		return verifyStatusError, true
	}
	return "", false
}

// cargo test: `test module::path::name ... ok` / `... FAILED`. The id is the module path, which is what
// `cargo test <path>` re-runs.
var verifyCargoLine = regexp.MustCompile(`^test\s+(\S+)\s+\.\.\.\s+(ok|FAILED|ignored)\b`)

func parseVerifyCargo(output string) (verifyResults, bool) {
	if !strings.Contains(output, "... ok") && !strings.Contains(output, "... FAILED") {
		return nil, false
	}
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		match := verifyCargoLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		switch match[2] {
		case "ok":
			results[match[1]] = verifyStatusPass
		case "FAILED":
			results[match[1]] = verifyStatusFail
		}
	}
	return results, len(results) > 0
}

// go test -v: `--- PASS: TestName/sub (0.00s)`. The leading dashes and indentation carry subtest depth,
// which is part of the id `go test -run` accepts.
var verifyGoLine = regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+)`)

func parseVerifyGoTest(output string) (verifyResults, bool) {
	if !strings.Contains(output, "--- PASS") && !strings.Contains(output, "--- FAIL") {
		return nil, false
	}
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		match := verifyGoLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		switch match[1] {
		case "PASS":
			results[match[2]] = verifyStatusPass
		case "FAIL":
			results[match[2]] = verifyStatusFail
		}
	}
	return results, len(results) > 0
}

// PHPUnit --testdox / default: the machine-readable form is the `--teamcity` or JUnit XML, but the
// default text report names failures in a numbered block ("1) ClassTest::testName"). Passes are only
// countable, not nameable, without --testdox, so the parser reads BOTH: testdox lines when present, and
// the failure block always.
var (
	verifyPHPUnitFailure = regexp.MustCompile(`^\d+\)\s+([A-Za-z_][\w\\]*::\w+)`)
	verifyPHPUnitTestdox = regexp.MustCompile(`^\s*([✔✘])\s+(.+?)\s*$`)
)

func parseVerifyPHPUnit(output string) (verifyResults, bool) {
	if !strings.Contains(output, "PHPUnit") && !verifyPHPUnitFailure.MatchString(output) {
		return nil, false
	}
	results := verifyResults{}
	suite := ""
	for _, line := range verifyLines(output) {
		if match := verifyPHPUnitFailure.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			results[match[1]] = verifyStatusFail
			continue
		}
		// testdox prints a class header followed by ticked/crossed test descriptions.
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ")") == false &&
			!strings.ContainsAny(trimmed, "✔✘") && strings.Contains(trimmed, "\\") {
			suite = trimmed
			continue
		}
		if match := verifyPHPUnitTestdox.FindStringSubmatch(line); match != nil {
			id := match[2]
			if suite != "" {
				id = suite + "::" + id
			}
			if match[1] == "✔" {
				results[id] = verifyStatusPass
			} else {
				results[id] = verifyStatusFail
			}
		}
	}
	return results, len(results) > 0
}

// jest / vitest: `✓ suite > name (3 ms)` / `✕ suite > name`, and the `PASS|FAIL <file>` header lines.
// The tick characters are the reliable per-test signal in both runners' default reporters.
var verifyJestLine = regexp.MustCompile(`^\s*(✓|✔|×|✕|✗)\s+(.+?)(?:\s+\(\d+\s*m?s\))?\s*$`)

func parseVerifyJest(output string) (verifyResults, bool) {
	if !strings.ContainsAny(output, "✓✔×✕✗") {
		return nil, false
	}
	results := verifyResults{}
	file := ""
	for _, line := range verifyLines(output) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "PASS ") || strings.HasPrefix(trimmed, "FAIL ") {
			if fields := strings.Fields(trimmed); len(fields) > 1 {
				file = fields[1]
			}
			continue
		}
		match := verifyJestLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		id := strings.TrimSpace(match[2])
		if file != "" {
			id = file + " > " + id
		}
		if match[1] == "✓" || match[1] == "✔" {
			results[id] = verifyStatusPass
		} else {
			results[id] = verifyStatusFail
		}
	}
	return results, len(results) > 0
}

// rspec --format progress prints no ids; the documentation format and the failure block do. The rerun
// block ("rspec ./spec/x_spec.rb:12") is the most useful id there is — it is literally the re-run
// command — so it is preferred when present.
var verifyRSpecRerun = regexp.MustCompile(`^rspec\s+(\./\S+:\d+)`)

func parseVerifyRSpec(output string) (verifyResults, bool) {
	if !strings.Contains(output, "examples,") {
		return nil, false
	}
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		if match := verifyRSpecRerun.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			results[match[1]] = verifyStatusFail
		}
	}
	// A green rspec run names nothing, and that is a real answer: zero failures. The summary line is the
	// evidence, and one synthetic id would be a lie about what was read.
	if len(results) == 0 && strings.Contains(output, "0 failures") {
		return verifyResults{"rspec: all examples": verifyStatusPass}, true
	}
	return results, len(results) > 0
}

// minitest: `TestClass#test_name = 0.01 s = .` / `= F` / `= E`.
var verifyMinitestLine = regexp.MustCompile(`^(\S+#\S+)\s*=\s*[\d.]+\s*s\s*=\s*([.FES])`)

func parseVerifyMinitest(output string) (verifyResults, bool) {
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		match := verifyMinitestLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		switch match[2] {
		case ".":
			results[match[1]] = verifyStatusPass
		case "F":
			results[match[1]] = verifyStatusFail
		case "E":
			results[match[1]] = verifyStatusError
		}
	}
	return results, len(results) > 0
}

// maven/gradle surefire: `[ERROR] ClassTest.testName:42 expected ...` for failures and
// `[INFO] Tests run: 5, Failures: 0` for counts. Only named entries become ids.
var verifySurefireFailure = regexp.MustCompile(`^\[ERROR\]\s+(\w[\w.$]*\.\w+)(?::\d+)?`)

func parseVerifySurefire(output string) (verifyResults, bool) {
	if !strings.Contains(output, "Tests run:") {
		return nil, false
	}
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		if match := verifySurefireFailure.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			results[match[1]] = verifyStatusFail
		}
	}
	if len(results) == 0 && strings.Contains(output, "Failures: 0") && strings.Contains(output, "Errors: 0") {
		return verifyResults{"surefire: all tests": verifyStatusPass}, true
	}
	return results, len(results) > 0
}

// ctest: `    1/12 Test  #1: name .......   Passed    0.01 sec` / `***Failed`.
var verifyCTestLine = regexp.MustCompile(`^\s*\d+/\d+\s+Test\s+#\d+:\s+(\S+)\s+\.*\s*(Passed|\*\*\*Failed|\*\*\*Timeout|\*\*\*Exception)`)

func parseVerifyCTest(output string) (verifyResults, bool) {
	if !strings.Contains(output, "Test #") {
		return nil, false
	}
	results := verifyResults{}
	for _, line := range verifyLines(output) {
		match := verifyCTestLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if match[2] == "Passed" {
			results[match[1]] = verifyStatusPass
		} else {
			results[match[1]] = verifyStatusFail
		}
	}
	return results, len(results) > 0
}
