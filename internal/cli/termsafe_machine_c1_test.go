package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The C1 controls a repository can smuggle through a machine format.
//
// U+009D is OSC and U+009C is ST — the single-code-point forms of "ESC ]" and
// "ESC \" — so the pair brackets an OSC 52 clipboard write. They matter because
// they are the one shape of a control that survives encoding/json intact:
// encoding/json escapes the C0 range and coerces INVALID UTF-8 to U+FFFD, but
// U+0080-U+009F encode to a WELL-FORMED two-byte sequence (0xc2 0x9d, 0xc2 0x9c)
// that it copies through verbatim. termsafe's own rule already treats those bytes
// as controls — see escapedAt in internal/termsafe/termsafe.go, which escapes
// both the two-byte form and the stray byte — so a machine format that emits them
// raw is emitting exactly what the text renderers refuse to.
const (
	c1OSC = "\u009d"
	c1ST  = "\u009c"
)

// c1ClipboardWrite is OSC 52 ; c ; <base64> ST written with those code points.
const c1ClipboardWrite = c1OSC + "52;c;aGVsbG8=" + c1ST

// c1HostileRepo builds a repository that carries the sequence through both
// channels a scanned repo controls, and reports whether the second one was
// representable here.
//
// The BODY channel — a plain filename holding hostile bytes — is representable on
// every platform, so the assertions that rest on it are unconditional.
func c1HostileRepo(t *testing.T) (repo string, hostilePath bool) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	// Inside the body, not in a comment above the declaration: a snippet renderer
	// prints the declaration's own lines, so a sequence parked above it would ride
	// in no payload.
	write(t, repo, "main.go", "package hostile\n\nfunc Merge() int {\n\t// merge "+c1ClipboardWrite+"\n\treturn 1\n}\n")
	write(t, repo, "helper.go", "package hostile\n\nfunc Helper() int { return Merge() }\n")

	// The NAME channel. A Git pathname is a byte string that may hold anything but
	// NUL and '/', so a repository can put the sequence in a filename — and the
	// snapshot formats, which carry no source text, have no other way to reach it.
	//
	// It is skipped on Windows. Not because these code points are known to be
	// illegal in an NTFS name — this test cannot verify Windows pathname behaviour
	// from the platform it was written on, and so does not assert any — but
	// because a fixture that assumes a pathname is a byte string has broken this
	// repository's windows-latest job before, and nothing here needs it: the body
	// channel above carries the same two code points and every unconditional
	// assertion below rests on that one.
	if runtime.GOOS != "windows" {
		// Elsewhere the filesystem still has the final say, so probe rather than
		// report a platform limit as a security failure.
		name := "evil" + c1ClipboardWrite + "file.go"
		if err := os.WriteFile(filepath.Join(repo, name), []byte("package hostile\n\nfunc Named() int { return Merge() }\n"), 0o644); err != nil {
			t.Logf("filesystem rejects C1 code points in filenames, skipping the pathname channel: %v", err)
		} else {
			hostilePath = true
		}
	}

	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo, hostilePath
}

// TestMachineFormatsEscapeC1Controls is the half of the terminal-safety contract
// the machine formats never got.
//
// internal/termsafe/termsafe.go states that JSON and NDJSON are deliberately left
// unwrapped because "encoding/json already escapes control characters inside
// strings, so no raw byte reaches that stream". That is true of C0 and false of
// C1, and `search` defaults to --format json (parseSearchFlags in
// internal/cli/search.go), so the default invocation of the tool's most-used verb
// is the one that emits the raw control.
func TestMachineFormatsEscapeC1Controls(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	repo, hostilePath := c1HostileRepo(t)
	cacheDir := t.TempDir()

	// The body channel reaches the search payloads, so these run everywhere. The
	// first case passes no --format at all: it is the default the finding is about.
	for _, verb := range [][]string{
		{"search", "--query", "merge"},
		{"search", "--query", "merge", "--format", "json"},
		{"search", "--query", "merge", "--format", "ndjson"},
	} {
		t.Run(strings.Join(verb, " "), func(t *testing.T) {
			stdout, stderr := runVerb(t, repo, cacheDir, verb)
			assertNoRawC1(t, stdout)
			assertNoRawC1(t, stderr)
			// Without this the case would pass just as well against a tool that
			// printed nothing at all.
			assertCarriesC1AfterDecoding(t, stdout)
		})
	}

	if !hostilePath {
		return
	}
	// The snapshot formats carry no source text, so a hostile PATHNAME is their
	// only C1 channel and these cases exist only where one was representable.
	for _, verb := range [][]string{
		{"snapshot", "--format", "ndjson"},
		{"snapshot", "--format", "compact-ndjson"},
		{"neighbors", "--symbol", "Merge", "--format", "json"},
		{"impact", "--symbol", "Merge", "--format", "json"},
	} {
		t.Run(strings.Join(verb, " "), func(t *testing.T) {
			stdout, stderr := runVerb(t, repo, cacheDir, verb)
			assertNoRawC1(t, stdout)
			assertNoRawC1(t, stderr)
			assertCarriesC1AfterDecoding(t, stdout)
		})
	}
}

// assertNoRawC1 fails when a C1 control reaches the stream as the code point
// itself rather than as an escape. It reuses indexC1 — the helper the text sweep
// in termsafe_verbs_test.go already applies to every human-readable verb — so the
// two halves of the contract are checked against one definition of the rule.
func assertNoRawC1(t *testing.T, output string) {
	t.Helper()
	if index := indexC1(output); index >= 0 {
		start := max(index-120, 0)
		t.Errorf("raw C1 control at byte %d of the machine format:\n%q", index, output[start:min(index+120, len(output))])
	}
}

// assertCarriesC1AfterDecoding is the losslessness half: escaping a control as
// \u009d must not cost the consumer the byte, so decoding the stream has to yield
// the SAME code points the repository holds. It is also what keeps the assertion
// above honest — a stream that dropped the hostile value entirely would satisfy
// "no raw C1" and prove nothing.
func assertCarriesC1AfterDecoding(t *testing.T, stdout string) {
	t.Helper()
	var carried bool
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		var record any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("machine format is not decodable JSON: %v\n%q", err, line)
		}
		if decodedStringContains(record, c1ClipboardWrite) {
			carried = true
		}
	}
	if !carried {
		t.Errorf("no decoded value carried the repository's own C1 code points, so this case proves nothing:\n%s", stdout)
	}
}

// decodedStringContains walks a decoded JSON document for a string holding want.
// Walking beats naming a field: the same assertion then covers a search result's
// snippet, a snapshot record's path, and whatever a later record type calls it.
func decodedStringContains(record any, want string) bool {
	switch value := record.(type) {
	case string:
		return strings.Contains(value, want)
	case []any:
		for _, element := range value {
			if decodedStringContains(element, want) {
				return true
			}
		}
	case map[string]any:
		for key, element := range value {
			if strings.Contains(key, want) || decodedStringContains(element, want) {
				return true
			}
		}
	}
	return false
}
