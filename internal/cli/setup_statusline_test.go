package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

// tempHome is the Options home seam pointed at a fresh temp dir, so setup never reads the real
// ~/.claude/settings.json.
func tempHome(t *testing.T) func() string {
	t.Helper()
	home := t.TempDir()
	return func() string { return home }
}

func runSetupForTest(t *testing.T, home func() string, repo string, extra ...string) string {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"setup", "--repo", repo}, extra...)
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out, homeDir: home}, args); err != nil {
		t.Fatalf("setup %v: %v\n%s", extra, err, out.String())
	}
	return out.String()
}

func writeUserSettings(t *testing.T, home func() string, content string) {
	t.Helper()
	path := filepath.Join(home(), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// localStatusLine decodes the statusLine subtree of the repo's settings.local.json; nil when the
// file or the key is absent.
func localStatusLine(t *testing.T, repo string) any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repo, ".claude", "settings.local.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, content)
	}
	return settings["statusLine"]
}

var wantStatusLine = map[string]any{"type": "command", "command": "entire graph statusline"}

func TestSetupInstallsStatusLineByDefault(t *testing.T) {
	t.Parallel()
	home := tempHome(t)

	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, home, repo)
	if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}

	noHook := gitRepoForTest(t, false)
	out = runSetupForTest(t, home, noHook, "--no-claude-hook")
	if got := localStatusLine(t, noHook); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("with --no-claude-hook: statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}
}

func TestSetupNoStatuslineFlagWritesNone(t *testing.T) {
	t.Parallel()
	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, tempHome(t), repo, "--no-statusline")
	if got := localStatusLine(t, repo); got != nil {
		t.Fatalf("--no-statusline wrote statusLine %v\n%s", got, out)
	}
}

func TestSetupKeepsUserStatusLineUnlessAsked(t *testing.T) {
	t.Parallel()
	home := tempHome(t)
	// With the UTF-8 BOM Windows PowerShell 5.1 writes: it must still be recognised as foreign.
	writeUserSettings(t, home, "\xef\xbb\xbf"+`{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`)

	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, home, repo)
	if got := localStatusLine(t, repo); got != nil {
		t.Fatalf("setup hid the user's status line with %v\n%s", got, out)
	}
	for _, want := range []string{"kept your status line (", "rerun with --statusline"} {
		if !strings.Contains(out, want) {
			t.Errorf("without --statusline the row must say %q:\n%s", want, out)
		}
	}

	out = runSetupForTest(t, home, repo, "--statusline")
	if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("with --statusline: statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}
	if !strings.Contains(out, "overrides ") {
		t.Errorf("with --statusline the row must say what it overrides:\n%s", out)
	}
}

// An unreadable user settings file is only inspected, so it skips the status line and never the
// guides; --statusline installs anyway.
func TestSetupSkipsStatusLineWhenAScopeIsUnreadable(t *testing.T) {
	t.Parallel()
	home := tempHome(t)
	writeUserSettings(t, home, "// comment\n{}")

	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, home, repo)
	if _, err := os.Stat(filepath.Join(repo, agentsetup.Path)); err != nil {
		t.Fatalf("guide not written: %v\n%s", err, out)
	}
	if got := localStatusLine(t, repo); got != nil {
		t.Fatalf("statusLine written over an uninspected scope: %v\n%s", got, out)
	}
	if !strings.Contains(out, "skipped (could not read ") {
		t.Errorf("the row must say the scope could not be read:\n%s", out)
	}

	out = runSetupForTest(t, home, repo, "--statusline")
	if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("with --statusline: statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}
}
