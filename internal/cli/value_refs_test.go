package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// valueRefFixture is a Go function reached once by a call and six times as a value. The graph
// models only the call, which is the gap the disclosure names.
const valueRefFixture = `package vref

func target(x int) int { return x + 1 }

var pkgAlias = target

var table = map[string]func(int) int{"t": target}

type holder struct{ fn func(int) int }

func apply(f func(int) int, v int) int { return f(v) }

func passesArg() int { return apply(target, 2) }

func storesStruct() holder { return holder{fn: target} }

func assignsLocal() int {
	g := target
	return g(3)
}

func storesMap() map[string]func(int) int {
	return map[string]func(int) int{"x": target}
}

func directCall() int { return target(4) }

// unused is reached only as a value, so impact on it is degenerate.
func unused() int { return 0 }

var onlyValue = unused
`

func runValueRefCommand(t *testing.T, repo string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, args); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func valueRefWarning(t *testing.T, warnings []sem.ProviderWarning) (sem.ProviderWarning, bool) {
	t.Helper()
	found := 0
	var match sem.ProviderWarning
	for _, warning := range warnings {
		if warning.Code == valueReferenceDisclosureCode {
			found++
			match = warning
		}
	}
	if found > 1 {
		t.Fatalf("disclosure emitted %d times: %#v", found, warnings)
	}
	return match, found == 1
}

const targetSelfCheck = `grep -rnE '(^|[^A-Za-z0-9_])target([^(A-Za-z0-9_]|$)' --include='*.go' .`

func TestNeighborsDisclosesUnmodelledValueReferencesForGoFunction(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "vref.go", valueRefFixture)

	var response neighborResponse
	if err := json.Unmarshal([]byte(runValueRefCommand(t, repo,
		"neighbors", "--repo", repo, "--symbol", "target", "--direction", "in", "--format", "json",
	)), &response); err != nil {
		t.Fatal(err)
	}
	// The premise: six value references, one modelled caller. If the graph ever models the
	// references, this disclosure is stale and must be revisited rather than kept.
	if len(response.Matches) != 1 || len(response.Matches[0].Incoming) != 1 ||
		response.Matches[0].Incoming[0].Endpoint.Name != "directCall" {
		t.Fatalf("premise: target must have exactly the one call edge from directCall, got %#v", response.Matches)
	}
	warning, ok := valueRefWarning(t, response.Warnings)
	if !ok {
		t.Fatalf("JSON warnings carry no %s for a Go function: %#v", valueReferenceDisclosureCode, response.Warnings)
	}
	if !strings.Contains(warning.Detail, targetSelfCheck) || warning.Severity != "info" ||
		!strings.Contains(warning.EffectOnCompleteness, "passed as an argument") {
		t.Fatalf("disclosure lacks the self-check or the effect: %#v", warning)
	}
	for _, scoped := range response.CompletenessScope.InScopeWarnings {
		if scoped.Code == valueReferenceDisclosureCode {
			t.Fatalf("query-scoped disclosure leaked into the snapshot completeness scope: %#v", scoped)
		}
	}

	const budget = 4096
	agent := runValueRefCommand(t, repo,
		"neighbors", "--repo", repo, "--symbol", "target", "--direction", "in",
		"--format", "agent", "--max-context-bytes", "4096",
	)
	if len(agent) > budget {
		t.Fatalf("agent output is %d bytes, over the %d budget:\n%s", len(agent), budget, agent)
	}
	callers := strings.Index(agent, "Callers:\n")
	note := strings.Index(agent, "Value refs unmodelled (var/arg/struct/map): "+targetSelfCheck)
	if callers < 0 || note < callers {
		t.Fatalf("agent output must carry the disclosure after the callers list:\n%s", agent)
	}
	// The completeness banner counts snapshot diagnostics; the disclosure is not one.
	if strings.Contains(agent, valueReferenceDisclosureCode) || strings.Contains(agent, "Completeness:") {
		t.Fatalf("disclosure was reported as a snapshot diagnostic:\n%s", agent)
	}
}

func TestNeighborsValueReferenceDisclosureSurvivesCompaction(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "vref.go", valueRefFixture)
	var response neighborResponse
	if err := json.Unmarshal([]byte(runValueRefCommand(t, repo,
		"neighbors", "--repo", repo, "--symbol", "target", "--direction", "in", "--format", "json",
	)), &response); err != nil {
		t.Fatal(err)
	}
	var full bytes.Buffer
	if err := writeAgentNeighborsFull(&full, response); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		budget int
		want   string
	}{
		{budget: full.Len() - 1, want: "Value refs unmodelled (var/arg/struct/map): " + targetSelfCheck},
		{budget: 200, want: "!value-refs-unmodelled: " + targetSelfCheck},
		{budget: 100, want: "!value-refs-unmodelled\n"},
	} {
		var out bytes.Buffer
		if err := writeAgentNeighborsBounded(&out, response, testCase.budget); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.HasPrefix(got, "!output-truncated") {
			t.Fatalf("budget %d did not take the compact path:\n%s", testCase.budget, got)
		}
		if len(got) > testCase.budget {
			t.Fatalf("budget %d: compact output is %d bytes:\n%s", testCase.budget, len(got), got)
		}
		if !strings.Contains(got, testCase.want) {
			t.Fatalf("budget %d dropped the disclosure (want %q):\n%s", testCase.budget, testCase.want, got)
		}
	}
}

func TestValueReferenceDisclosureOnlyWhereItCanMatter(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "vref.go", valueRefFixture)
	write(t, repo, "py/target.py", "def pytarget(x):\n    return x\n\nalias = pytarget\n\ndef caller():\n    return pytarget(1)\n")
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "direction out has no callers", args: []string{"--symbol", "target", "--direction", "out"}},
		{name: "Go struct type is not a function", args: []string{"--symbol", "holder", "--direction", "in"}},
		{name: "Python function", args: []string{"--symbol", "pytarget", "--direction", "in"}},
		{name: "relation other than CALLS", args: []string{"--symbol", "target", "--direction", "in", "--relation", "USES_TYPE"}},
	} {
		args := append([]string{"neighbors", "--repo", repo, "--format", "json"}, testCase.args...)
		var response neighborResponse
		if err := json.Unmarshal([]byte(runValueRefCommand(t, repo, args...)), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Matches) != 1 {
			t.Fatalf("%s: premise: want one focus, got %#v", testCase.name, response.Matches)
		}
		if warning, ok := valueRefWarning(t, response.Warnings); ok {
			t.Fatalf("%s: disclosure emitted where it cannot apply: %#v", testCase.name, warning)
		}
		agentArgs := append([]string{"neighbors", "--repo", repo, "--format", "agent"}, testCase.args...)
		if agent := runValueRefCommand(t, repo, agentArgs...); strings.Contains(agent, "Value refs unmodelled") {
			t.Fatalf("%s: agent disclosure emitted where it cannot apply:\n%s", testCase.name, agent)
		}
	}
}

func TestImpactDisclosesUnmodelledValueReferences(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "vref.go", valueRefFixture)

	var response impactResponse
	if err := json.Unmarshal([]byte(runValueRefCommand(t, repo,
		"impact", "--repo", repo, "--symbol", "target", "--format", "json",
	)), &response); err != nil {
		t.Fatal(err)
	}
	if warning, ok := valueRefWarning(t, response.Warnings); !ok || !strings.Contains(warning.Detail, targetSelfCheck) {
		t.Fatalf("impact JSON lacks the disclosure: %#v", response.Warnings)
	}

	text := runValueRefCommand(t, repo, "impact", "--repo", repo, "--symbol", "target", "--max-context-bytes", "4096")
	if len(text) > 4096 {
		t.Fatalf("impact text is %d bytes, over budget:\n%s", len(text), text)
	}
	summary := strings.Index(text, "Summary:\n")
	note := strings.Index(text, "- Value refs unmodelled (var/arg/struct/map): "+targetSelfCheck)
	sections := strings.Index(text, "Callers (")
	if summary < 0 || note < summary || sections < note {
		t.Fatalf("impact text must carry the disclosure in the summary, above the sections:\n%s", text)
	}
	if strings.Contains(text, "Diagnostics: "+valueReferenceDisclosureCode) {
		t.Fatalf("disclosure was reported as a snapshot diagnostic:\n%s", text)
	}

	// A function reached only as a value is the degenerate "no callers" case: the one place the
	// verdict is most wrong, so the disclosure must follow the marker line.
	degenerate := runValueRefCommand(t, repo, "impact", "--repo", repo, "--symbol", "unused")
	lines := strings.Split(strings.TrimLeft(degenerate, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], impactDegenerateMarker+": unused has ") ||
		!strings.HasPrefix(lines[1], "Value refs unmodelled (var/arg/struct/map): grep -rnE '(^|[^A-Za-z0-9_])unused(") {
		t.Fatalf("degenerate impact must keep its marker line and follow it with the disclosure:\n%s", degenerate)
	}
}

// The self-check has to find what the graph misses. A reference at the end of a line is the common
// shape (`var x = f`), and a pattern that requires a character after the name never matches it.
func TestValueReferenceSelfCheckFindsEveryValueReferenceAndNoCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the self-check is a POSIX grep command line")
	}
	t.Parallel()
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("grep not on PATH")
	}
	repo := t.TempDir()
	write(t, repo, "vref.go", valueRefFixture)
	// Run exactly the line a reader is told to run, through a shell, from the repository root.
	command := valueReferenceGrep("target")
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		_, rest, _ := strings.Cut(line, "vref.go:")
		number, _, _ := strings.Cut(rest, ":")
		lines = append(lines, number)
	}
	sort.Strings(lines)
	want := []string{"13", "15", "18", "23", "5", "7"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("self-check matched lines %v, want the six value references %v (and not the call on 26):\n%s", lines, want, out)
	}
}

func TestCompletenessBannerDoesNotCountTheDisclosure(t *testing.T) {
	t.Parallel()
	snapshotWarning := sem.ProviderWarning{Code: "W_SNAPSHOT", Severity: "warning"}
	disclosure, ok := valueReferenceDisclosure(neighborEndpoint{Name: "target", Kind: "function", Language: "Go"})
	if !ok {
		t.Fatal("premise: a Go function gets the disclosure")
	}
	response := neighborResponse{
		Warnings:        []sem.ProviderWarning{snapshotWarning, disclosure},
		PartialFailures: []sem.PartialFailure{},
		Stats:           sem.ProviderStats{CompletenessLevel: "ok"},
		CompletenessScope: completenessScope{
			Language: "Go", LanguageFiles: 1, Level: "ok",
			InScopeWarnings: []sem.ProviderWarning{snapshotWarning},
		},
	}
	var out bytes.Buffer
	writeAgentNeighborCompleteness(&out, response)
	if !strings.Contains(out.String(), "; 1 total diagnostic in this snapshot)") {
		t.Fatalf("banner must count only the snapshot's own diagnostic:\n%s", out.String())
	}
}
