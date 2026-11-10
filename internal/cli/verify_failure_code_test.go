package cli

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestVerifyDeclaredFailureCode(t *testing.T) {
	t.Parallel()
	for _, parser := range []string{"jest/vitest", "rspec"} {
		for _, code := range []int{128, 137, 200, 255} {
			t.Run(parser+"/"+strconv.Itoa(code), func(t *testing.T) {
				t.Parallel()
				input := verifyVerdictInput{
					baseline: verifyBaseline{Parser: parser, Results: verifyResults{"fixed": verifyStatusFail, "broken": verifyStatusFail}},
					current:  verifyResults{"fixed": verifyStatusPass, "broken": verifyStatusFail},
					parser:   parser, parsed: true, exitCode: code, maxBytes: verifyDefaultMaxBytes,
				}
				if got := string(renderVerifyVerdict(input)); !strings.Contains(got, "INCOMPLETE") || !strings.Contains(got, "--test-failure-exit-code") {
					t.Fatalf("undeclared ambiguous status must explain how to disambiguate it: %s", got)
				}
				input.testFailureExitCode = code
				if got := string(renderVerifyVerdict(input)); !strings.Contains(got, "VERDICT: PASS") {
					t.Fatalf("declared normal failure status was refused: %s", got)
				}
				for _, exit := range []int{-1, 1} {
					input.exitCode = exit
					if got := string(renderVerifyVerdict(input)); !strings.Contains(got, "INCOMPLETE") {
						t.Fatalf("exit %d incorrectly matched declared code %d: %s", exit, code, got)
					}
				}
				input.exitCode = code
				input.current["broken"] = verifyStatusPass
				if got := string(renderVerifyVerdict(input)); !strings.Contains(got, "INCOMPLETE") {
					t.Fatalf("declared status explained an exit without any reported failure: %s", got)
				}
				input.current["broken"] = verifyStatusFail
				delete(input.current, "fixed")
				if got := string(renderVerifyVerdict(input)); !strings.Contains(got, "INCOMPLETE") {
					t.Fatalf("declared status hid a missing baseline test: %s", got)
				}
				input.current["fixed"] = verifyStatusPass
				input.unattributed = []string{"unbuilt"}
				if got := string(renderVerifyVerdict(input)); !strings.Contains(got, "INCOMPLETE") {
					t.Fatalf("declared status hid an unbuilt target: %s", got)
				}
			})
		}
	}
}

func TestVerifyDeclaredFailureCodeRoundTrip(t *testing.T) {
	repo := t.TempDir()
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	write(t, repo, "run.sh", "echo 'FAIL example.test.js'\n"+
		"if [ -f fixed ]; then echo '  ✓ fixed'; else echo '  ✕ fixed'; fi\n"+
		"echo '  ✕ broken'\nexit 200\n")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Run(t.Context(), Options{Stdout: &out}, append([]string{
			"verify", "--repo", repo, "--test", "sh run.sh",
		}, args...))
		return out.String(), err
	}
	if _, err := run("--record-baseline", baselinePath, "--test-failure-exit-code", "200"); err != nil {
		t.Fatal(err)
	}
	baseline, err := readVerifyBaseline(baselinePath)
	if err != nil || baseline.TestFailureExitCode != 200 || baseline.ExitCode != 200 || baseline.Parser != "jest/vitest" {
		t.Fatalf("baseline did not preserve configured and observed statuses: %+v, %v", baseline, err)
	}
	write(t, repo, "fixed", "")
	if got, err := run("--pre-edit-baseline", baselinePath, "--test-failure-exit-code", "200"); err != nil || !strings.Contains(got, "VERDICT: PASS") {
		t.Fatalf("configured exit 200 failed end to end: %s, %v", got, err)
	}
	for _, extra := range [][]string{nil, {"--test-failure-exit-code", "201"}} {
		if _, err := run(append([]string{"--pre-edit-baseline", baselinePath}, extra...)...); err == nil || !strings.Contains(err.Error(), "re-record") {
			t.Fatalf("changed declaration was accepted: %v", err)
		}
	}
	// Old baselines remain valid in automatic mode, but must be re-recorded to
	// adopt an explicit failure-code declaration.
	if _, err := run("--record-baseline", baselinePath); err != nil {
		t.Fatal(err)
	}
	if got, err := run("--pre-edit-baseline", baselinePath); err != nil || !strings.Contains(got, "INCOMPLETE") {
		t.Fatalf("automatic baseline no longer works: %s, %v", got, err)
	}
	if _, err := run("--pre-edit-baseline", baselinePath, "--test-failure-exit-code", "200"); err == nil {
		t.Fatal("baseline without declaration accepted an explicit override")
	}
}

func TestParseVerifyDeclaredFailureCode(t *testing.T) {
	t.Parallel()
	base := []string{"--test", "runner", "--record-baseline", "baseline.json", "--test-failure-exit-code"}
	for _, raw := range []string{"0", "-1", "256", "1.5", "unknown", ""} {
		if _, err := parseVerifyFlags(append(append([]string{}, base...), raw)); err == nil {
			t.Fatalf("invalid declared code %q accepted", raw)
		}
	}
	if _, err := parseVerifyFlags(base); err == nil {
		t.Fatal("missing declared code accepted")
	}
}
