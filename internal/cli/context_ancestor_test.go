package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// contextAncestorRepo is a committed fixture with 85 indexed Go files, so the
// 5% gate admits exactly four changed paths.
func contextAncestorRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "1")
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Context Ancestor Test")
	git(t, repo, "config", "user.email", "context-ancestor@example.invalid")
	for index := range 80 {
		write(t, repo, fmt.Sprintf("pkg/filler_%02d.go", index), fmt.Sprintf("package pkg\n\nfunc Filler%02d() int { return %d }\n", index, index))
	}
	write(t, repo, "widget.go", "package sample\n\ntype Widget struct{ Name string }\n\nfunc NewWidget(name string) *Widget { return &Widget{Name: name} }\n\nfunc RenderWidget(w *Widget) string { return w.Name }\n")
	write(t, repo, "widget_test.go", "package sample\n\nimport \"testing\"\n\nfunc TestRenderWidget(t *testing.T) {\n\tif RenderWidget(NewWidget(\"x\")) != \"x\" {\n\t\tt.Fatal(\"render\")\n\t}\n}\n")
	write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return RenderWidget(NewWidget(\"gadget\")) }\n")
	write(t, repo, "old_name.go", "package sample\n\nfunc RenderLegacy() string { return RenderWidget(NewWidget(\"legacy\")) }\n")
	write(t, repo, "doomed.go", "package sample\n\nfunc RenderDoomed() string { return \"doomed\" }\n")
	contextAncestorCommit(t, repo, "base")
	return repo
}

func contextAncestorCommit(t *testing.T, repo, message string) {
	t.Helper()
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", message)
}

// contextAncestorRefactor modifies gadget.go, renames old_name.go and deletes
// doomed.go.
func contextAncestorRefactor(t *testing.T, repo string) {
	t.Helper()
	write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return RenderWidget(NewWidget(\"gadget v2\")) }\n")
	git(t, repo, "mv", "old_name.go", "new_name.go")
	git(t, repo, "rm", "-q", "doomed.go")
	contextAncestorCommit(t, repo, "refactor")
}

func contextAncestorIndex(t *testing.T, repo, cacheDir, version string) {
	t.Helper()
	var out bytes.Buffer
	opts := Options{Stdout: &out, Stderr: &out, Version: version}
	if err := runIndex(context.Background(), opts, []string{"--repo", repo, "--cache-dir", cacheDir, "--format", "json"}); err != nil {
		t.Fatalf("index: %v\n%s", err, out.String())
	}
}

var contextLatencyField = regexp.MustCompile(`"latency_ms":[0-9]+`)

// contextAncestorRun returns the exact bytes `graph context` emits, with the
// one wall-clock field zeroed so two runs can be compared byte for byte.
func contextAncestorRun(t *testing.T, repo, cacheDir, version string, extra ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts := Options{Stdout: &stdout, Stderr: &stderr, Version: version}
	args := append([]string{"--repo", repo, "--cache-dir", cacheDir, "--task", "render"}, extra...)
	if err := runContext(context.Background(), opts, args); err != nil {
		t.Fatalf("context %q: %v\n%s", extra, err, stderr.String())
	}
	if !contextLatencyField.Match(stdout.Bytes()) {
		t.Fatalf("output has no latency_ms field to normalize: %s", stdout.String())
	}
	return contextLatencyField.ReplaceAllString(stdout.String(), `"latency_ms":0`)
}

// The stale block must reach the JSON `graph context` actually writes, not
// only the internal result: a field that lands on an internal struct and
// never on the emitted response is invisible to every consumer. The block
// carries exactly the five contracted names, and nothing in a changed file
// is emitted.
func TestContextAllowAncestorEmitsTheStaleBlock(t *testing.T) {
	repo := contextAncestorRepo(t)
	cacheDir := t.TempDir()
	contextAncestorIndex(t, repo, cacheDir, "context-ancestor")
	ancestorCommit := rev(t, repo, "HEAD")
	ancestorTree := rev(t, repo, "HEAD^{tree}")
	contextAncestorRefactor(t, repo)

	output := contextAncestorRun(t, repo, cacheDir, "context-ancestor", "--allow-ancestor", "3", "--limit", "100", "--tests-limit", "100")
	var response map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if _, unavailable := response["store_unavailable"]; unavailable {
		t.Fatalf("ancestor answer still reports store_unavailable: %s", output)
	}
	raw, ok := response["stale"]
	if !ok {
		t.Fatalf("emitted JSON has no stale block: %s", output)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	if want := []string{"ancestor_commit", "ancestor_tree", "changed_files", "commits_behind", "excluded_records"}; !slices.Equal(names, want) {
		t.Fatalf("stale block fields = %q, want exactly %q", names, want)
	}
	var stale struct {
		AncestorCommit  string   `json:"ancestor_commit"`
		AncestorTree    string   `json:"ancestor_tree"`
		CommitsBehind   int      `json:"commits_behind"`
		ChangedFiles    []string `json:"changed_files"`
		ExcludedRecords int      `json:"excluded_records"`
	}
	if err := json.Unmarshal(raw, &stale); err != nil {
		t.Fatal(err)
	}
	if stale.AncestorCommit != ancestorCommit || stale.AncestorTree != ancestorTree || stale.CommitsBehind != 1 {
		t.Fatalf("stale identity = %+v, want %s / %s / 1", stale, ancestorCommit, ancestorTree)
	}
	wantChanged := []string{"doomed.go", "gadget.go", "new_name.go", "old_name.go"}
	if !slices.Equal(stale.ChangedFiles, wantChanged) {
		t.Fatalf("changed_files = %q, want %q", stale.ChangedFiles, wantChanged)
	}
	if stale.ExcludedRecords <= 0 {
		t.Fatalf("excluded_records = %d, but RenderGadget, RenderLegacy and RenderDoomed all match", stale.ExcludedRecords)
	}
	var lists struct {
		Symbols []struct {
			Name     string `json:"name"`
			FilePath string `json:"file_path"`
		} `json:"symbols"`
		Tests []struct {
			FilePath string `json:"file_path"`
		} `json:"tests"`
	}
	if err := json.Unmarshal([]byte(output), &lists); err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, symbol := range lists.Symbols {
		served[symbol.Name] = true
		if slices.Contains(wantChanged, symbol.FilePath) {
			t.Fatalf("emitted a symbol from changed file %s: %s", symbol.FilePath, output)
		}
	}
	for _, test := range lists.Tests {
		if slices.Contains(wantChanged, test.FilePath) {
			t.Fatalf("emitted a test from changed file %s: %s", test.FilePath, output)
		}
	}
	if !served["RenderWidget"] {
		t.Fatalf("the unchanged file's symbol was not emitted: %s", output)
	}
	for _, path := range wantChanged {
		if strings.Contains(output, `"file_path":"`+path+`"`) {
			t.Fatalf("emitted JSON quotes changed file %s: %s", path, output)
		}
	}

	// Without the flag the same state is today's exact miss.
	if plain := contextAncestorRun(t, repo, cacheDir, "context-ancestor"); !strings.Contains(plain, `"store_unavailable":true`) || strings.Contains(plain, `"stale"`) {
		t.Fatalf("no-flag output changed: %s", plain)
	}
}

// An exact hit under --allow-ancestor is byte-identical to the no-flag
// output: no stale block, nothing else different.
func TestContextAllowAncestorExactHitIsByteIdentical(t *testing.T) {
	repo := contextAncestorRepo(t)
	cacheDir := t.TempDir()
	contextAncestorIndex(t, repo, cacheDir, "context-ancestor")
	contextAncestorRefactor(t, repo)
	contextAncestorIndex(t, repo, cacheDir, "context-ancestor")

	plain := contextAncestorRun(t, repo, cacheDir, "context-ancestor")
	if strings.Contains(plain, `"store_unavailable"`) {
		t.Fatalf("fixture store missing at HEAD: %s", plain)
	}
	allowed := contextAncestorRun(t, repo, cacheDir, "context-ancestor", "--allow-ancestor", "5")
	if allowed != plain {
		t.Fatalf("exact hit changed under --allow-ancestor:\n got %s\nwant %s", allowed, plain)
	}
	if strings.Contains(allowed, `"stale"`) {
		t.Fatalf("exact hit carries a stale block: %s", allowed)
	}
}

// A refused ancestor is byte-identical to the no-flag miss: no stale block at
// all, never one with empty or zero fields. Each case has a control showing
// the same fixture serves when the refused condition is lifted.
func TestContextAllowAncestorRefusalIsByteIdenticalToExactMiss(t *testing.T) {
	assertMiss := func(t *testing.T, repo, cacheDir, version, allow string) {
		t.Helper()
		plain := contextAncestorRun(t, repo, cacheDir, version)
		if !strings.Contains(plain, `"store_unavailable":true`) {
			t.Fatalf("fixture is not an exact miss: %s", plain)
		}
		refused := contextAncestorRun(t, repo, cacheDir, version, "--allow-ancestor", allow)
		if refused != plain {
			t.Fatalf("refused ancestor output differs from the exact miss:\n got %s\nwant %s", refused, plain)
		}
		if strings.Contains(refused, `"stale"`) {
			t.Fatalf("refusal emitted a stale block: %s", refused)
		}
	}
	assertServes := func(t *testing.T, repo, cacheDir, version, allow string) {
		t.Helper()
		if served := contextAncestorRun(t, repo, cacheDir, version, "--allow-ancestor", allow); !strings.Contains(served, `"stale":{`) {
			t.Fatalf("control did not serve an ancestor: %s", served)
		}
	}

	t.Run("beyond N commits", func(t *testing.T) {
		repo := contextAncestorRepo(t)
		cacheDir := t.TempDir()
		contextAncestorIndex(t, repo, cacheDir, "context-ancestor")
		write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return \"one\" }\n")
		contextAncestorCommit(t, repo, "one")
		write(t, repo, "gadget.go", "package sample\n\nfunc RenderGadget() string { return \"two\" }\n")
		contextAncestorCommit(t, repo, "two")
		assertMiss(t, repo, cacheDir, "context-ancestor", "1")
		assertServes(t, repo, cacheDir, "context-ancestor", "2")
	})

	t.Run("over five percent changed", func(t *testing.T) {
		repo := contextAncestorRepo(t)
		cacheDir := t.TempDir()
		contextAncestorIndex(t, repo, cacheDir, "context-ancestor")
		contextAncestorRefactor(t, repo)
		assertServes(t, repo, cacheDir, "context-ancestor", "5")
		write(t, repo, "pkg/filler_00.go", "package pkg\n\nfunc Filler00() int { return -1 }\n")
		contextAncestorCommit(t, repo, "fifth path")
		assertMiss(t, repo, cacheDir, "context-ancestor", "5")
	})

	t.Run("provider version mismatch", func(t *testing.T) {
		repo := contextAncestorRepo(t)
		cacheDir := t.TempDir()
		contextAncestorIndex(t, repo, cacheDir, "context-ancestor-old")
		contextAncestorRefactor(t, repo)
		assertServes(t, repo, cacheDir, "context-ancestor-old", "1")
		assertMiss(t, repo, cacheDir, "context-ancestor-new", "1")
	})
}

func TestContextAllowAncestorFlagIsBounded(t *testing.T) {
	for _, value := range []string{"-1", "6"} {
		if _, err := parseContextFlags([]string{"--task", "render", "--allow-ancestor", value}); err == nil {
			t.Fatalf("--allow-ancestor %s was accepted", value)
		}
	}
	for value, want := range map[string]int{"0": 0, "5": 5} {
		flags, err := parseContextFlags([]string{"--task", "render", "--allow-ancestor", value})
		if err != nil || flags.AllowAncestor != want {
			t.Fatalf("--allow-ancestor %s = %d, %v", value, flags.AllowAncestor, err)
		}
	}
	if flags, err := parseContextFlags([]string{"--task", "render"}); err != nil || flags.AllowAncestor != 0 {
		t.Fatalf("default --allow-ancestor = %d, %v; the fallback must be opt-in", flags.AllowAncestor, err)
	}
}

// Consumers feature-detect the fallback from `capabilities --json` before
// passing --allow-ancestor: a flat boolean in optional_local_only_features,
// whose absence (any older provider) means unsupported.
func TestCapabilitiesAdvertiseContextAllowAncestor(t *testing.T) {
	var out bytes.Buffer
	if err := runCapabilities(Options{Stdout: &out}, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Optional map[string]json.RawMessage `json:"optional_local_only_features"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if got := string(report.Optional["context_allow_ancestor"]); got != "true" {
		t.Fatalf("optional_local_only_features.context_allow_ancestor = %q, want the flat boolean true: %s", got, out.String())
	}
}
