package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// doctorAssert runs one preflight the way a harness does before a batch.
func doctorAssert(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "0.9.9",
		Env:     EntireEnv{RepoRoot: t.TempDir()},
		Stdout:  &out,
		Stderr:  &out,
	}, append([]string{"doctor"}, args...))
	return out.String(), err
}

// TestPreflightAcceptsFlagsThisBinaryHas pins the passing direction: a command line built for this
// build reports success and the report still renders.
func TestPreflightAcceptsFlagsThisBinaryHas(t *testing.T) {
	t.Parallel()
	out, err := doctorAssert(t, "--assert", "search --profile full --top-k 10 --format text")
	if err != nil {
		t.Fatalf("preflight rejected a command line this binary accepts: %v\n%s", err, out)
	}
	if !strings.Contains(out, "assert_ok") {
		t.Fatalf("preflight did not report success:\n%s", out)
	}
}

// TestPreflightCatchesVersionSkew is the reason this verb exists.
//
// A harness whose flag set was built for a newer binary gets exit 1 and an empty payload on the
// agent's FIRST mandated action, in every session, for the whole run — and what a reviewer sees
// afterwards is a graph arm whose numbers look like the baseline arm. One preflight call turns that
// into a startup failure, before any instance runs.
func TestPreflightCatchesVersionSkew(t *testing.T) {
	t.Parallel()
	out, err := doctorAssert(t, "--assert", "search --flag-from-a-newer-build")
	if err == nil {
		t.Fatalf("preflight accepted a flag this binary does not have:\n%s", out)
	}
	for _, want := range []string{"--flag-from-a-newer-build", "0.9.9", "older"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("preflight error does not mention %q: %v", want, err)
		}
	}
	// The report must not print above the failure: a caller that asked one question wants that
	// answer, not an otherwise-healthy environment dump with the refusal buried under it.
	if strings.Contains(out, "no_egress") {
		t.Fatalf("preflight printed the report despite failing:\n%s", out)
	}
}

// TestPreflightChecksEveryAssertion pins that --assert is repeatable and that a later failure is
// not masked by an earlier success — a harness usually drives more than one verb, and learning
// about the second only after fixing the first costs another whole run.
func TestPreflightChecksEveryAssertion(t *testing.T) {
	t.Parallel()
	_, err := doctorAssert(t,
		"--assert", "search --profile full",
		"--assert", "impact --symbol Foo --flag-from-a-newer-build",
	)
	if err == nil {
		t.Fatal("a failing second assertion was masked by a passing first one")
	}
	if !strings.Contains(err.Error(), "impact") {
		t.Fatalf("error does not name the assertion that failed: %v", err)
	}
}

// TestPreflightRunsNothing is what makes --assert safe against a production command line: it parses
// and returns. The repo it names does not exist, so anything that touched a repository would fail.
func TestPreflightRunsNothing(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "0.9.9", Env: EntireEnv{RepoRoot: t.TempDir()}, Stdout: &out, Stderr: &out},
		[]string{"doctor", "--json", "--assert", "search --repo /nonexistent/repo --query anything --profile full"})
	if err != nil {
		t.Fatalf("preflight touched the repository it was asked about: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("--json did not emit JSON: %v\n%s", err, out.String())
	}
	asserted, ok := report["asserted_command_lines"].([]any)
	if !ok || len(asserted) != 1 {
		t.Fatalf("JSON report does not record the assertions: %v", report["asserted_command_lines"])
	}
}

// TestPreflightRejectsUncheckableCommand pins that an unknown command word is an error naming what
// CAN be checked, rather than a silent pass — a preflight that quietly approves everything is worse
// than none, because it is trusted.
func TestPreflightRejectsUncheckableCommand(t *testing.T) {
	t.Parallel()
	_, err := doctorAssert(t, "--assert", "snapshot --format ndjson")
	if err == nil {
		t.Fatal("preflight silently approved a command it cannot check")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Fatalf("error does not list the checkable commands: %v", err)
	}
}
