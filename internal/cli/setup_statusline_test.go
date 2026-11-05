package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

// runSetupInteractive runs setup as if at a terminal, answering any prompt with stdin.
func runSetupInteractive(t *testing.T, home func() string, repo, stdin string, extra ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"setup", "--repo", repo}, extra...)
	err := Run(context.Background(), Options{Stdout: &out, Stderr: &out, Stdin: strings.NewReader(stdin), homeDir: home,
		interactive: func() bool { return true }}, args)
	return out.String(), err
}

func writeLocalSettings(t *testing.T, repo, content string) {
	t.Helper()
	path := filepath.Join(repo, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSetupLeavesTheStatusLineOffUnlessChosen catches a status line installed without the user
// choosing it: a non-interactive setup writes none, and --statusline writes ours.
func TestSetupLeavesTheStatusLineOffUnlessChosen(t *testing.T) {
	t.Parallel()
	home := tempHome(t)
	repo := gitRepoForTest(t, false)
	out := runSetupForTest(t, home, repo)
	if got := localStatusLine(t, repo); got != nil {
		t.Fatalf("a plain non-interactive setup wrote statusLine %v\n%s", got, out)
	}
	out = runSetupForTest(t, home, repo, "--statusline")
	if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
		t.Fatalf("with --statusline: statusLine = %v, want %v\n%s", got, wantStatusLine, out)
	}
}

// TestSetupAsksBeforeInstallingTheStatusLine catches a prompt that defaults to yes: only an
// explicit "y" installs the status line.
func TestSetupAsksBeforeInstallingTheStatusLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stdin string
		want        any
	}{
		{name: "yes", stdin: "y\n", want: wantStatusLine},
		{name: "enter", stdin: "\n", want: nil},
		{name: "empty stdin", stdin: "", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := gitRepoForTest(t, false)
			out, err := runSetupInteractive(t, tempHome(t), repo, tc.stdin)
			if err != nil {
				t.Fatalf("setup: %v\n%s", err, out)
			}
			if !strings.Contains(out, "[y/N]") {
				t.Errorf("setup did not ask:\n%s", out)
			}
			if got := localStatusLine(t, repo); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("statusLine = %v, want %v\n%s", got, tc.want, out)
			}
		})
	}
}

// TestSetupDoesNotAskWhenAStatusLineExists catches a prompt shown, or a line written, when a status
// line is already in place: ours stays ours, and a foreign one in the user scope is kept.
func TestSetupDoesNotAskWhenAStatusLineExists(t *testing.T) {
	t.Parallel()
	t.Run("ours", func(t *testing.T) {
		t.Parallel()
		repo := gitRepoForTest(t, false)
		writeLocalSettings(t, repo, `{"statusLine":{"type":"command","command":"entire graph statusline"}}`)
		out, err := runSetupInteractive(t, tempHome(t), repo, "y\n")
		if err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("setup asked although ours is installed:\n%s", out)
		}
		if got := localStatusLine(t, repo); !reflect.DeepEqual(got, wantStatusLine) {
			t.Fatalf("statusLine = %v, want %v\n%s", got, wantStatusLine, out)
		}
	})
	t.Run("foreign in the user scope", func(t *testing.T) {
		t.Parallel()
		home := tempHome(t)
		writeUserSettings(t, home, `{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`)
		repo := gitRepoForTest(t, false)
		out, err := runSetupInteractive(t, home, repo, "y\n")
		if err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("setup asked although the user has a status line:\n%s", out)
		}
		if got := localStatusLine(t, repo); got != nil {
			t.Fatalf("setup hid the user's status line with %v\n%s", got, out)
		}
		if !strings.Contains(out, "kept your status line") {
			t.Errorf("the row must say the user's line was kept:\n%s", out)
		}
	})
}

// TestSetupNoClaudeHookIgnoresStatusLineSettingsUnlessChosen catches finding #2: a malformed
// settings.local.json must not fail `setup --no-claude-hook` when the status line is not chosen.
func TestSetupNoClaudeHookIgnoresStatusLineSettingsUnlessChosen(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"array":      "[]",
		"bom object": "\xef\xbb\xbf{}",
	} {
		for _, interactive := range []bool{false, true} {
			t.Run(name+"/interactive="+strconv.FormatBool(interactive), func(t *testing.T) {
				t.Parallel()
				repo := gitRepoForTest(t, false)
				writeLocalSettings(t, repo, content)
				var out string
				var err error
				if interactive {
					out, err = runSetupInteractive(t, tempHome(t), repo, "\n", "--no-claude-hook")
				} else {
					var buffer bytes.Buffer
					err = Run(context.Background(), Options{Stdout: &buffer, Stderr: &buffer, homeDir: tempHome(t)},
						[]string{"setup", "--repo", repo, "--no-claude-hook"})
					out = buffer.String()
				}
				if err != nil {
					t.Fatalf("setup --no-claude-hook: %v\n%s", err, out)
				}
				if _, err := os.Stat(filepath.Join(repo, agentsetup.Path)); err != nil {
					t.Fatalf("guide not written: %v\n%s", err, out)
				}
			})
		}
	}
}

// TestSetupHintNamesOnlyTheApplicableOptOut catches a refusal that suggests an opt-out which would
// not get past it.
func TestSetupHintNamesOnlyTheApplicableOptOut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		args         []string
		want, absent string
	}{
		{name: "hook step", args: nil, want: "--no-claude-hook", absent: "--no-statusline"},
		{name: "status line step", args: []string{"--statusline", "--no-claude-hook"}, want: "--no-statusline", absent: "--no-claude-hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := gitRepoForTest(t, false)
			writeLocalSettings(t, repo, "[]")
			var out bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &out, Stderr: &out, homeDir: tempHome(t)},
				append([]string{"setup", "--repo", repo}, tc.args...))
			if err == nil {
				t.Fatalf("setup %v succeeded over a malformed settings.local.json\n%s", tc.args, out.String())
			}
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.absent) {
				t.Fatalf("err = %q, want it to name %s and not %s", err, tc.want, tc.absent)
			}
		})
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

// TestSetupKeepsUserStatusLineUnlessAsked catches a user status line hidden by ours: at a terminal it
// is kept without asking, and only --statusline overrides it.
func TestSetupKeepsUserStatusLineUnlessAsked(t *testing.T) {
	t.Parallel()
	home := tempHome(t)
	// With the UTF-8 BOM Windows PowerShell 5.1 writes: it must still be recognised as foreign.
	writeUserSettings(t, home, "\xef\xbb\xbf"+`{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`)

	repo := gitRepoForTest(t, false)
	out, err := runSetupInteractive(t, home, repo, "y\n")
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
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
	out, err := runSetupInteractive(t, home, repo, "y\n")
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
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
