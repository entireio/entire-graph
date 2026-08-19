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
// tool-authored: it carries pathnames and Git's own stderr, and a Git pathname
// may hold any byte but NUL and '/'. An ESC there is the introducer a terminal
// obeys, U+009B is the C1 CSI that introduces the same sequence in one code
// point, and an LF forges a whole extra line of tool output - so without
// escaping, the scanned repository decides what the reader believes was
// reported.
func TestFatalErrorEscapesTerminalControlBytes(t *testing.T) {
	t.Parallel()

	// A hostile pathname: an SGR recolour behind ESC, the same introducer in its
	// two-byte C1 form (\xc2\x9b is U+009B), and a newline that forges a second
	// line of output.
	const hostile = "pay\x1b[31mred\xc2\x9b0m\nAll checks passed"

	// The Go literal forms termsafe escapes those bytes to. The C1 one is
	// spelled through \x5c (a backslash) so no source line here has to carry the
	// characters it is asserting about.
	const (
		escapedESC = `\x1b`
		escapedCSI = "\x5cu009b"
		escapedLF  = `\x0a`
	)

	missingRepo := filepath.Join(t.TempDir(), hostile)
	command := exec.Command(os.Args[0], "diff", "--repo", missingRepo, "--base", "HEAD~1", "--head", "HEAD")
	command.Env = append(os.Environ(), runMainEnv+"=1")
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err == nil {
		t.Fatalf("expected a failing run against a missing repository, got success; stderr=%q", stderr.String())
	}

	got := stderr.String()
	if !strings.Contains(got, "pay") {
		t.Fatalf("the hostile pathname never reached stderr, so this test proves nothing; stderr=%q", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("raw ESC reached the terminal: %q", got)
	}
	if strings.ContainsRune(got, 0x9b) {
		t.Errorf("raw C1 CSI reached the terminal: %q", got)
	}
	if count := strings.Count(got, "\n"); count != 1 {
		t.Errorf("the error report spans %d lines, so a pathname forged output lines: %q", count+1, got)
	}
	if !strings.Contains(got, escapedESC) {
		t.Errorf("ESC was not escaped to its Go literal form: %q", got)
	}
	if !strings.Contains(got, escapedCSI) {
		t.Errorf("C1 CSI was not escaped to its Go literal form: %q", got)
	}
	if !strings.Contains(got, escapedLF) {
		t.Errorf("LF was not escaped to its Go literal form: %q", got)
	}
}
