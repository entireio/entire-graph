package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This is the guard the per-renderer unit tests cannot be: it drives the REAL
// verbs against a REAL repository whose filename and source both carry a live
// terminal sequence, and fails if any human-readable output contains a raw ESC.
//
// It exists because the unit tests missed one. Each of them wraps the renderer
// it knows about, so `def` passed its own test while still leaking — its body
// section is printed by writeSymbolMatchBodies, which runDef calls separately
// from writeDefText and which nothing had wrapped. Only running the verb end to
// end found that. A renderer added later, or a verb that grows a second output
// path, is caught here without anyone remembering to add a case.

// escapeSequence is a real erase-line followed by a colour change: if it were
// interpreted, it would blank the line the reader had just been shown and leave
// the terminal recoloured afterwards.
const escapeSequence = "\x1b[2K\x1b[31m"

// hostileRepo builds a repository that attacks its reader through both channels
// a scanned repo controls: the name of a file, and the bytes inside one.
func hostileRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()

	// Git pathnames may hold any byte but NUL and '/', but the FILESYSTEM has the
	// final say. Probe before committing to the test rather than reporting a
	// platform limit as a security failure.
	hostileName := "evil" + escapeSequence + "file.go"
	if err := os.WriteFile(filepath.Join(repo, hostileName), []byte("package hostile\n"), 0o644); err != nil {
		t.Skipf("filesystem rejects control characters in filenames: %v", err)
	}

	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, hostileName, "package hostile\n\nfunc Helper() int { return Merge() }\n")
	// The sequence goes INSIDE the body, not above it. A comment on the line
	// before a declaration is not part of the source any body-printing renderer
	// emits, so a fixture that put it there would leave those sinks untested.
	write(t, repo, "main.go", "package hostile\n\nfunc Merge() int {\n\t// "+escapeSequence+"SPOOFED\n\treturn 1\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	// A second commit so `diff` has a range to report, and so the entity names it
	// prints come from the hostile file.
	write(t, repo, hostileName, "package hostile\n\nfunc Helper() int { return Merge() + 1 }\n\nfunc Extra() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "second")
	return repo
}

// TestNoVerbLeaksControlSequencesToTheTerminal is the sweep. Every argv here is a
// human-readable output mode; the machine formats are asserted separately below,
// because they must NOT be escaped.
func TestNoVerbLeaksControlSequencesToTheTerminal(t *testing.T) {
	repo := hostileRepo(t)
	cacheDir := t.TempDir()

	for _, verb := range [][]string{
		{"search", "--query", "merge", "--format", "text"},
		{"search", "--query", "merge", "--format", "agent"},
		{"def", "--symbol", "Helper", "--format", "text"},
		{"def", "--symbol", "Helper", "--format", "agent"},
		// Merge, not Helper: Merge's BODY is what carries the sequence, so this is
		// the case that exercises the numbered-source sink rather than only the
		// locator above it.
		{"def", "--symbol", "Merge", "--format", "text"},
		{"def", "--symbol", "Merge", "--format", "agent"},
		{"neighbors", "--symbol", "Merge", "--format", "text"},
		{"neighbors", "--symbol", "Merge", "--format", "agent"},
		{"impact", "--symbol", "Merge", "--format", "text"},
		// explain reads a build error from stdin; runVerb feeds it one naming a
		// symbol that lives in the hostile file. It was missing from the first
		// version of this sweep and was still leaking a raw ESC because of it.
		{"explain", "--format", "text"},
		{"explain", "--format", "agent"},
		{"diff", "--base", "HEAD~1", "--head", "HEAD"},
		// --progress is swept for the diff it still prints to stdout, NOT for the
		// per-file progress line: that line is throttled to one per second and
		// forced only every hundredth file (see emitProgressEvent in
		// sem/analyze.go), so a fixture this size never emits one and no
		// repository-derived path reaches stderr here. That sink is covered
		// directly by TestDiffProgressLineEscapesGitFilename instead.
		{"diff", "--base", "HEAD~1", "--head", "HEAD", "--progress"},
	} {
		t.Run(strings.Join(verb, " "), func(t *testing.T) {
			stdout, stderr := runVerb(t, repo, cacheDir, verb)
			assertNoEscape(t, "stdout", stdout)
			// stderr carries --progress, whose file= field is a raw Git pathname.
			assertNoEscape(t, "stderr", stderr)
			// Something hostile must still be REPORTED, or the sweep would pass
			// just as well against a tool that printed nothing at all. Either
			// marker will do: which one surfaces depends on whether the verb
			// prints the hostile FILENAME or the hostile BODY.
			output := stdout + stderr
			if !strings.Contains(output, "evil") && !strings.Contains(output, "SPOOFED") {
				t.Errorf("no output carried a hostile value, so this case proves nothing:\n%s", output)
			}
		})
	}
}

// TestMachineFormatsKeepTheRepositoryBytes is the other half of the contract, and
// the reason the escaping lives in the renderers rather than in the search
// pipeline: a consumer of the machine formats is entitled to the exact pathname
// Git reported, and encoding/json already keeps it off the terminal.
func TestMachineFormatsKeepTheRepositoryBytes(t *testing.T) {
	repo := hostileRepo(t)
	cacheDir := t.TempDir()

	stdout, _ := runVerb(t, repo, cacheDir, []string{"search", "--query", "merge", "--format", "json"})
	if strings.ContainsRune(stdout, 0x1b) {
		t.Errorf("raw ESC in the JSON stream; encoding/json should have escaped it:\n%s", stdout)
	}
	var response struct {
		Results []struct {
			FilePath string `json:"file_path"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode search response: %v\n%s", err, stdout)
	}
	var found bool
	for _, result := range response.Results {
		if strings.Contains(result.FilePath, escapeSequence) {
			found = true
		}
	}
	if !found {
		t.Errorf("JSON no longer reports the repository's own pathname bytes: %#v", response.Results)
	}
}

func runVerb(t *testing.T, repo, cacheDir string, argv []string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	argv = append(append([]string{}, argv...), "--repo", repo)
	if err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Stdin:   strings.NewReader("undefined: Helper\nundefined: Merge\n"),
	}, argv); err != nil {
		t.Fatalf("%v: %v\nstdout:\n%s\nstderr:\n%s", argv, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func assertNoEscape(t *testing.T, stream, output string) {
	t.Helper()
	if index := strings.IndexRune(output, 0x1b); index >= 0 {
		start := max(index-120, 0)
		t.Errorf("raw ESC reached %s at byte %d:\n%q", stream, index, output[start:min(index+120, len(output))])
	}
	// C1 CSI is the same primitive in one byte; a terminal in UTF-8 mode acts on
	// it exactly as it acts on "ESC [".
	if index := strings.Index(output, "\u009b"); index >= 0 {
		t.Errorf("raw C1 CSI reached %s at byte %d", stream, index)
	}
}
