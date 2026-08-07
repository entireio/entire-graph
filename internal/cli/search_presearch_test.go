package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// presearchPayload is deliberately NOT anything the renderer could produce: if a byte of it is
// missing, added, or reordered, the call re-queried instead of echoing.
const presearchPayload = "PRE-DELIVERED PAYLOAD\nsrc/auth.py:1  validate_token\n"

// TestSearchEchoesPreDeliveredPayload pins zero-toll delivery: when the payload was computed before
// the agent started, `search` returns it byte-for-byte and does no work — not the repo, not the
// index, not the ranking. The nonexistent-repo case is the proof that no work happens: a live query
// cannot resolve that repo, so an echo that survives it cannot have run one.
func TestSearchEchoesPreDeliveredPayload(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "src/auth.py", "def validate_token(token):\n    return bool(token)\n")
	path := filepath.Join(t.TempDir(), "presearch.txt")
	if err := os.WriteFile(path, []byte(presearchPayload), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name    string
		envName string
		repo    string
	}{
		{name: "documented name", envName: envPresearch, repo: repo},
		{name: "harness alias", envName: envPresearchAlias, repo: repo},
		{name: "repo never resolved", envName: envPresearch, repo: filepath.Join(repo, "absent")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(testCase.envName, path)
			var out bytes.Buffer
			err := Run(t.Context(), Options{
				Version: "test",
				Env:     EnvFromOS(),
				Stdout:  &out,
				Stderr:  &out,
			}, []string{"search", "--repo", testCase.repo, "--query", "validate the token", "--format", "text"})
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != presearchPayload {
				t.Fatalf("payload is not byte-identical:\n got %q\nwant %q", out.String(), presearchPayload)
			}
			// Named separately because it is the failure that matters: the call went to the graph.
			if strings.Contains(out.String(), "def validate_token") {
				t.Fatalf("search re-queried the repo instead of echoing:\n%s", out.String())
			}
		})
	}

	// Both names set: the documented one wins, so a harness that leaves the alias behind cannot
	// silently serve a stale payload.
	t.Run("documented name wins", func(t *testing.T) {
		stale := filepath.Join(t.TempDir(), "stale.txt")
		if err := os.WriteFile(stale, []byte("STALE"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envPresearch, path)
		t.Setenv(envPresearchAlias, stale)
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "test", Env: EnvFromOS(), Stdout: &out, Stderr: &out},
			[]string{"search", "--repo", repo, "--query", "validate the token"}); err != nil {
			t.Fatal(err)
		}
		if out.String() != presearchPayload {
			t.Fatalf("alias overrode the documented name: %q", out.String())
		}
	})
}

// TestSearchRefusesUnreadablePresearchPayload pins that a payload the caller promised but cannot
// produce is an error, not a silent live query: a run that half-echoes and half-queries measures
// neither mode.
func TestSearchRefusesUnreadablePresearchPayload(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "src/auth.py", "def validate_token(token):\n    return bool(token)\n")
	t.Setenv(envPresearch, filepath.Join(t.TempDir(), "never-written.txt"))

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "test", Env: EnvFromOS(), Stdout: &out, Stderr: &out},
		[]string{"search", "--repo", repo, "--query", "validate the token", "--format", "text"})
	if err == nil {
		t.Fatalf("a missing payload fell back to a live query:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), envPresearch) {
		t.Fatalf("error does not name the variable that caused it: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout is not empty after a refused echo:\n%s", out.String())
	}
}

// TestSearchRefusesEmptyPresearchPayload pins the failure that is worse than an unreadable path,
// because every layer above it reads as success: a zero-byte payload used to be written to stdout
// and the process exited 0. The agent asks a question, receives nothing, and the harness, the exit
// code and the transcript all record a healthy call — which is how a whole measured cell can run
// without its search verb ever answering anything.
func TestSearchRefusesEmptyPresearchPayload(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "src/auth.py", "def validate_token(token):\n    return bool(token)\n")
	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, envName := range []string{envPresearch, envPresearchAlias} {
		t.Run(envName, func(t *testing.T) {
			t.Setenv(envName, empty)
			var out bytes.Buffer
			err := Run(t.Context(), Options{Version: "test", Env: EnvFromOS(), Stdout: &out, Stderr: &out},
				[]string{"search", "--repo", repo, "--query", "validate the token", "--format", "text"})
			if err == nil {
				t.Fatalf("an empty payload was reported as a successful search:\n%q", out.String())
			}
			if !strings.Contains(err.Error(), envPresearch) {
				t.Fatalf("error does not name the variable that caused it: %v", err)
			}
			// The path belongs in the message: the caller has to know WHICH file came back empty.
			if !strings.Contains(err.Error(), empty) {
				t.Fatalf("error does not name the empty file: %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout is not empty after a refused echo:\n%q", out.String())
			}
		})
	}
}
