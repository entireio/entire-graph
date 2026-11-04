package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// isolateHome points the user home at a temp dir, so setup never reads the real
// ~/.claude/settings.json. os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func runSetupForTest(t *testing.T, repo string, extra ...string) string {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"setup", "--repo", repo}, extra...)
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, args); err != nil {
		t.Fatalf("setup %v: %v\n%s", extra, err, out.String())
	}
	return out.String()
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
	isolateHome(t)

	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, repo)
	if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}

	noHook := gitRepoForTest(t, false)
	out = runSetupForTest(t, noHook, "--no-claude-hook")
	if got := localStatusLine(t, noHook); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("with --no-claude-hook: statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}
}

func TestSetupNoStatuslineFlagWritesNone(t *testing.T) {
	isolateHome(t)

	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, repo, "--no-statusline")
	if got := localStatusLine(t, repo); got != nil {
		t.Fatalf("--no-statusline wrote statusLine %v\n%s", got, out)
	}
}

func TestSetupKeepsUserStatusLineUnlessAsked(t *testing.T) {
	home := isolateHome(t)
	userSettings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(userSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userSettings, []byte(`{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, repo)
	if got := localStatusLine(t, repo); got != nil {
		t.Fatalf("setup hid the user's status line with %v\n%s", got, out)
	}

	out = runSetupForTest(t, repo, "--statusline")
	if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("with --statusline: statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}
}
