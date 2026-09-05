package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// Help detection has to read the args the way the command's own parser will. A flat scan for the two
// spellings cannot tell a request for help from DATA that is spelled like one.
func TestWantsHelpRespectsFlagValuesAndTheSeparator(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		command string
		args    []string
		want    bool
	}{
		{"a bare help flag", "search", []string{"--help"}, true},
		{"help after a complete flag", "search", []string{"--query", "x", "--help"}, true},
		{"the short spelling", "search", []string{"-h"}, true},
		{"a search FOR the text --help", "search", []string{"--query", "--help"}, false},
		{"a path named --help after the separator", "diff", []string{"--", "--help"}, false},
		{"a revision named -h", "diff", []string{"--base", "-h"}, false},
		{"nothing at all", "search", nil, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			doc, ok := findCommandDoc(testCase.command)
			if !ok {
				t.Fatalf("no command doc for %q", testCase.command)
			}
			if got := wantsHelp(doc, testCase.args); got != testCase.want {
				t.Fatalf("wantsHelp(%q, %v) = %t, want %t", testCase.command, testCase.args, got, testCase.want)
			}
		})
	}
}

// End to end, because the harm is that the command never runs: `search --query --help` printed the
// search help and exited 0 instead of searching for the literal text.
func TestSearchForTheLiteralHelpTextIsNotAHelpRequest(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	// It is expected to FAIL (no repository here); what matters is that it is not answered with help.
	_ = Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: t.TempDir()},
		Stdout: &out, Stderr: &bytes.Buffer{}}, []string{"search", "--query", "--help", "--repo", t.TempDir()})
	if strings.Contains(out.String(), "Usage:") {
		t.Fatalf("a search for the text --help was answered with help:\n%s", out.String())
	}
}

// `--` is the separator only when it is not the VALUE of the flag in front of it. `diff --repo --`
// addresses a repository directory literally named `--`, and there is no `--repo=--` spelling to
// fall back on because these parsers accept no `=` form.
func TestParseDiffFlagsTreatsASeparatorShapedValueAsAValue(t *testing.T) {
	t.Parallel()
	parsed, unknown, err := parseDiffFlags([]string{"--repo", "--"})
	if err != nil {
		t.Fatalf("diff --repo -- : %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v", unknown)
	}
	if parsed.common.Repo != "--" {
		t.Fatalf("Repo = %q, want %q", parsed.common.Repo, "--")
	}
	if len(parsed.paths) != 0 {
		t.Fatalf("paths = %v, want none", parsed.paths)
	}
}

// The separator still separates when it is not a flag's value.
func TestParseDiffFlagsStillHonoursTheRealSeparator(t *testing.T) {
	t.Parallel()
	parsed, unknown, err := parseDiffFlags([]string{"--repo", "/r", "--", "--base"})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("err=%v unknown=%v", err, unknown)
	}
	if parsed.common.Repo != "/r" {
		t.Fatalf("Repo = %q", parsed.common.Repo)
	}
	if len(parsed.paths) != 1 || parsed.paths[0] != "--base" {
		t.Fatalf("paths = %v, want [--base] read as a literal path", parsed.paths)
	}
	if parsed.base != "HEAD~1" {
		t.Fatalf("base = %q: a path after the separator must not be read as a flag", parsed.base)
	}
}

// A partial failure is a FILE the graph could not parse. The terminal summary counted warnings and
// dropped these entirely, so a run full of E_PARSE_ERROR printed a clean report and the only place
// the loss survived was the machine format the user had just been steered away from.
func TestIndexTextNamesTheFilesItCouldNotParse(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	response := indexResponse{RepoRoot: "/r", Profile: "full", PartialFailures: []sem.PartialFailure{
		{Code: "E_PARSE_ERROR", Severity: "error", FilePath: "pkg/broken.go"},
		{Code: "E_UNSUPPORTED_LANGUAGE", Severity: "warning", FilePath: "vendor/x.zig"},
	}}
	if err := writeIndexText(&out, response, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pkg/broken.go", "E_PARSE_ERROR", "vendor/x.zig", "E_UNSUPPORTED_LANGUAGE"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary does not report %q:\n%s", want, out.String())
		}
	}
	// The other direction: a clean run must not grow a section about failures it did not have.
	var clean bytes.Buffer
	if err := writeIndexText(&clean, indexResponse{RepoRoot: "/r", Profile: "full"}, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean.String(), "not fully analyzed") {
		t.Fatalf("clean run reported a failure section:\n%s", clean.String())
	}
}

// A Git pathname is repository-controlled and may hold any byte but NUL and '/'. Written raw into a
// Markdown cell, a '|' in one splits the row and corrupts every row below it.
func TestReportTableCellsSurviveASeparatorInAFilename(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	writeCountTable(&out, "Top files", "File", map[string]int{"a|b.go": 3}, 0)
	row := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "b.go") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("no row rendered:\n%s", out.String())
	}
	// Three structural separators is what a two-column row has: leading, between, trailing. A raw
	// '|' inside the filename makes a fourth and turns one row into three columns.
	if got := strings.Count(row, "|") - strings.Count(row, `\|`); got != 3 {
		t.Fatalf("row %q has %d structural separators, want 3", row, got)
	}
	if !strings.Contains(row, `a\|b.go`) {
		t.Fatalf("row %q does not carry the escaped filename", row)
	}
}

// The control bytes the same cell already defended against must stay escaped.
func TestReportTableCellsStillEscapeControlBytes(t *testing.T) {
	t.Parallel()
	if got := markdownTableCell("a\nb.go"); strings.Contains(got, "\n") {
		t.Fatalf("markdownTableCell kept a newline: %q", got)
	}
}

// The guide's own drift check has to see a flag wherever a multiline invocation puts it. Read line by
// line, a continuation is a line that names no command, so every flag on it was silently skipped and
// an unsupported flag could ship inside a documented invocation with this check still green.
func TestGuideClaimsCoverContinuationLinesOfAnInvocation(t *testing.T) {
	t.Parallel()
	guide := strings.Join([]string{
		"### search — find code",
		"",
		"    entire graph search --repo . --query \\",
		`      "some text" --made-up-continuation x`,
		"",
		"Prose that mentions --not-a-claim in passing.",
	}, "\n")
	_, flags, _, _ := parseGuideClaims(guide)
	claimed := map[string]string{}
	for _, claim := range flags {
		claimed[claim.flag] = claim.command
	}
	if claimed["--made-up-continuation"] != "search" {
		t.Fatalf("continuation flag not claimed against search: %v", claimed)
	}
	if _, ok := claimed["--not-a-claim"]; ok {
		t.Fatalf("prose was read as an invocation: %v", claimed)
	}
}

// The other multiline spelling: an indented block whose wrapped line carries only flags.
func TestGuideClaimsCoverFlagOnlyContinuationLinesInAnIndentedBlock(t *testing.T) {
	t.Parallel()
	guide := strings.Join([]string{
		"### search — find code",
		"",
		"    entire graph search --repo .",
		"      --made-up-blockline x",
		"",
		"    --orphan-flag-with-no-invocation",
	}, "\n")
	_, flags, _, _ := parseGuideClaims(guide)
	claimed := map[string]string{}
	for _, claim := range flags {
		claimed[claim.flag] = claim.command
	}
	if claimed["--made-up-blockline"] != "search" {
		t.Fatalf("block continuation flag not claimed against search: %v", claimed)
	}
	if _, ok := claimed["--orphan-flag-with-no-invocation"]; ok {
		t.Fatalf("a flag in a block that names no command was claimed anyway: %v", claimed)
	}
}
