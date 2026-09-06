package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runMainEnv makes the test binary re-execute its own main() instead of running
// the test suite.
//
// The fatal-error sink lives inside main(), which ends in os.Exit, so it cannot
// be called in-process without taking the test binary down with it. Re-execing
// the test binary exercises the real entry point - the same Fprintln, the same
// os.Stderr - and needs no separate build step, which matters because the
// tree-sitter grammars make building this command expensive.
const runMainEnv = "ENTIRE_GRAPH_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// TestFatalErrorEscapesTerminalControlBytes pins the last unescaped sink.
//
// Every human-readable renderer writes through termsafe, but the error that ends
// a failed run is printed by main() straight to os.Stderr. Error text is not
// tool-authored: gitutil's run() folds both git's own stderr and the argv it
// invoked into the message verbatim (internal/gitutil/git.go, run), and neither
// is a byte string this tool chose. An ESC there is the introducer a terminal
// obeys, U+009B is the C1 CSI that introduces the same sequence in one code
// point, and an LF forges a whole extra line of tool output - so without
// escaping, the scanned repository decides what the reader believes was
// reported.
//
// WHAT THIS ASSERTS, AND WHY IT IS NOT THE ESCAPED SPELLING. The assertions
// below are the security property itself - no introducer survives, and the
// report occupies the one line it claims to - not the particular literal
// termsafe escapes to. Two things make the spelling the wrong thing to pin.
// First, it does not discriminate: the message below reaches stderr carrying
// BOTH a %q-quoted copy of the revision (from the "resolve diff head %q"
// wrapper) and a raw copy (from run's argv echo), so a `\x1b` is present in the
// output whether or not this sink escapes anything. Second, it is brittle
// against exactly the change that should leave this test alone: when an upstream
// guard starts quoting a value with %q, LF arrives already spelled `\n` and an
// assertion demanding termsafe's `\x0a` fails while the property it meant to
// check still holds.
func TestFatalErrorEscapesTerminalControlBytes(t *testing.T) {
	t.Parallel()

	// A hostile revision: an SGR recolour behind ESC, the same introducer in its
	// two-byte C1 form (\xc2\x9b is U+009B), and a newline that forges a second
	// line of output. The "zzz" prefix is what guarantees the revision does not
	// resolve, so the run fails and reaches the fatal-error sink.
	const hostile = "zzzpay\x1b[31mred\xc2\x9b0m\nAll checks passed"

	repo := repoWithTwoCommits(t)
	command := exec.Command(os.Args[0], "diff", "--repo", repo, "--base", "HEAD~1", "--head", hostile)
	command.Env = append(os.Environ(), runMainEnv+"=1")
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err == nil {
		t.Fatalf("expected a failing run against an unresolvable revision, got success; stderr=%q", stderr.String())
	}

	got := stderr.String()
	// Without this the rest would pass vacuously on any error that never
	// carried the hostile bytes at all.
	if !strings.Contains(got, "zzzpay") {
		t.Fatalf("the hostile revision never reached stderr, so this test proves nothing; stderr=%q", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("raw ESC reached the terminal: %q", got)
	}
	// Both spellings of the C1 CSI: the two-byte UTF-8 form a Go string holds it
	// in, and a stray 0x9b byte, which a terminal in an 8-bit locale reads as
	// CSI on its own.
	if strings.ContainsRune(got, 0x9b) {
		t.Errorf("raw C1 CSI (U+009B) reached the terminal: %q", got)
	}
	if bytes.IndexByte(stderr.Bytes(), 0x9b) >= 0 {
		t.Errorf("raw C1 CSI byte (0x9b) reached the terminal: %q", got)
	}
	if count := strings.Count(got, "\n"); count != 1 {
		t.Errorf("the error report spans %d lines, so a revision forged output lines: %q", count+1, got)
	}
}

// repoWithTwoCommits makes a temp repository with two commits, so that a
// `--base HEAD~1` resolves and the command fails on the hostile `--head`
// revision rather than earlier, on the repository itself.
func repoWithTwoCommits(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	// A deterministic identity, and no signing: a developer's global
	// commit.gpgsign would otherwise fail these commits.
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	for _, content := range []string{"package a\n", "package a\n\nfunc A() {}\n"} {
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", "-A")
		git(t, repo, "commit", "-m", "c")
	}
	return repo
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
