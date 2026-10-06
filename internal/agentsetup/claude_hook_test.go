package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func decodeSettings(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var settings map[string]any
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, content)
	}
	return settings
}

// subagentHookCommands returns every SubagentStart command in a settings document, in order.
func subagentHookCommands(t *testing.T, settings map[string]any) []string {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["SubagentStart"].([]any)
	var commands []string
	for _, group := range groups {
		entries, _ := group.(map[string]any)["hooks"].([]any)
		for _, entry := range entries {
			if command, ok := entry.(map[string]any)["command"].(string); ok {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func countHookCommand(commands []string) int {
	n := 0
	for _, command := range commands {
		if strings.Contains(command, SubagentHookCommand) {
			n++
		}
	}
	return n
}

func TestMergeSubagentHookIntoEmptySettings(t *testing.T) {
	t.Parallel()
	for _, existing := range []string{"", "  \n", "{}"} {
		rendered, changed, err := mergeSubagentHook([]byte(existing))
		if err != nil || !changed {
			t.Fatalf("merge(%q) = changed %v, err %v", existing, changed, err)
		}
		settings := decodeSettings(t, rendered)
		group := settings["hooks"].(map[string]any)["SubagentStart"].([]any)[0].(map[string]any)
		if group["matcher"] != "*" {
			t.Errorf("matcher = %v, want \"*\": Explore and Plan are the agent types that need it", group["matcher"])
		}
		hook := group["hooks"].([]any)[0].(map[string]any)
		if hook["type"] != "command" || hook["command"] != subagentHookShellCommand || hook["timeout"] != float64(subagentHookTimeoutSeconds) {
			t.Errorf("hook entry = %v", hook)
		}
		// The command must stay readable in the file a user opens.
		if bytes.Contains(rendered, []byte("\\u003e")) || !bytes.Contains(rendered, []byte("2>/dev/null || true")) {
			t.Errorf("hook command was HTML-escaped or lost its fail-open suffix:\n%s", rendered)
		}
	}
}

func TestMergeSubagentHookPreservesSettingsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	existing := []byte(`{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "model": "opus",
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "./guard.sh"}]}],
    "SubagentStart": [{"matcher": "Explore", "hooks": [{"type": "command", "command": "./mine.sh"}]}]
  }
}`)
	rendered, changed, err := mergeSubagentHook(existing)
	if err != nil || !changed {
		t.Fatalf("first merge: changed %v, err %v", changed, err)
	}
	settings := decodeSettings(t, rendered)
	if settings["model"] != "opus" {
		t.Errorf("model key lost: %v", settings["model"])
	}
	if allow := settings["permissions"].(map[string]any)["allow"].([]any); len(allow) != 1 || allow[0] != "Bash(go test:*)" {
		t.Errorf("permissions lost: %v", allow)
	}
	pre := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 || pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != "./guard.sh" {
		t.Errorf("PreToolUse hook lost: %v", pre)
	}
	commands := subagentHookCommands(t, settings)
	if len(commands) != 2 || commands[0] != "./mine.sh" || countHookCommand(commands) != 1 {
		t.Errorf("SubagentStart commands = %q, want the user's hook kept first and ours added once", commands)
	}

	again, changed, err := mergeSubagentHook(rendered)
	if err != nil || changed || !bytes.Equal(again, rendered) {
		t.Fatalf("second merge must be a byte-identical no-op: changed %v, err %v", changed, err)
	}

	// A user who rewrote the command (absolute path, no suffix) still has the hook.
	custom := []byte(`{"hooks":{"SubagentStart":[{"hooks":[{"type":"command","command":"/opt/bin/entire graph hook claude-subagent-start"}]}]}}`)
	if _, changed, err := mergeSubagentHook(custom); err != nil || changed {
		t.Errorf("customised hook command was not recognised: changed %v, err %v", changed, err)
	}
}

func TestMergeSubagentHookRefusesSettingsItCannotPreserve(t *testing.T) {
	t.Parallel()
	for name, existing := range map[string]string{
		"invalid JSON":           `{"model": `,
		"JSONC comment":          "// mine\n{}",
		"array":                  `[]`,
		"null":                   `null`,
		"trailing data":          `{} {}`,
		"hooks not an object":    `{"hooks": []}`,
		"hooks null":             `{"hooks": null}`,
		"SubagentStart object":   `{"hooks": {"SubagentStart": {}}}`,
		"SubagentStart null":     `{"hooks": {"SubagentStart": null}}`,
		"SubagentStart a string": `{"hooks": {"SubagentStart": "x"}}`,
	} {
		if _, _, err := mergeSubagentHook([]byte(existing)); err == nil {
			t.Errorf("%s: merge accepted settings it would rewrite or lose: %s", name, existing)
		}
	}
}

func TestInstallClaudeSubagentHookCreatesMergesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var out bytes.Buffer
	if err := InstallClaudeSubagentHook(repo, &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".claude", "settings.local.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("settings not created: %v", err)
	}
	if countHookCommand(subagentHookCommands(t, decodeSettings(t, first))) != 1 {
		t.Fatalf("hook not installed:\n%s", first)
	}
	if !strings.Contains(out.String(), "wrote "+path) {
		t.Errorf("report does not name the created file: %q", out.String())
	}

	out.Reset()
	if err := InstallClaudeSubagentHook(repo, &out); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) || !strings.Contains(out.String(), "unchanged "+path) {
		t.Fatalf("second run was not a reported no-op: %q\n%s", out.String(), second)
	}

	// An existing file keeps its keys and mode.
	if err := os.WriteFile(path, []byte(`{"env": {"FOO": "1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := InstallClaudeSubagentHook(repo, &out); err != nil {
		t.Fatal(err)
	}
	merged, _ := os.ReadFile(path)
	settings := decodeSettings(t, merged)
	if settings["env"].(map[string]any)["FOO"] != "1" || countHookCommand(subagentHookCommands(t, settings)) != 1 {
		t.Errorf("merge into existing file lost data or the hook:\n%s", merged)
	}
	if !strings.Contains(out.String(), "updated "+path) {
		t.Errorf("report does not say the file was updated: %q", out.String())
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the user's 0600 kept", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Join(repo, ".claude")); len(entries) != 1 {
		t.Errorf("staging file left behind: %v", entries)
	}
}

func TestInstallClaudeSubagentHookRefusesAliases(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	t.Run("symlinked .claude", func(t *testing.T) {
		repo := t.TempDir()
		victim := filepath.Join(repo, "victim")
		if err := os.Mkdir(victim, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("victim", filepath.Join(repo, ".claude")); err != nil {
			t.Fatal(err)
		}
		if err := InstallClaudeSubagentHook(repo, &bytes.Buffer{}); err == nil {
			t.Fatal("wrote settings through a symlinked .claude")
		}
		if err := CheckClaudeSubagentHook(repo); err == nil {
			t.Fatal("preflight accepted a symlinked .claude")
		}
		if entries, _ := os.ReadDir(victim); len(entries) != 0 {
			t.Fatalf("symlink target was written: %v", entries)
		}
	})
	t.Run("symlinked settings", func(t *testing.T) {
		repo := t.TempDir()
		pkg := filepath.Join(repo, "package.json")
		original := []byte(`{"name": "victim"}`)
		if err := os.WriteFile(pkg, original, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../package.json", filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
			t.Fatal(err)
		}
		if err := InstallClaudeSubagentHook(repo, &bytes.Buffer{}); err == nil {
			t.Fatal("wrote settings through a symlink")
		}
		if got, _ := os.ReadFile(pkg); !bytes.Equal(got, original) {
			t.Fatalf("symlink target rewritten:\n%s", got)
		}
	})
	t.Run("hard-linked settings", func(t *testing.T) {
		repo := t.TempDir()
		other := filepath.Join(repo, "other.json")
		if err := os.WriteFile(other, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(other, filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		err := InstallClaudeSubagentHook(repo, &bytes.Buffer{})
		if !errors.Is(err, errSharedInodeManagedTarget) {
			t.Fatalf("err = %v, want a hard-link refusal", err)
		}
	})
}

// TestSubagentDirectiveIsCompact pins the injected directive: small, carrying the guide's exact
// command lines, the every-question obligation, and the grep boundary.
func TestSubagentDirectiveIsCompact(t *testing.T) {
	t.Parallel()
	if len(SubagentDirective) > subagentDirectiveMaxBytes {
		t.Errorf("SubagentDirective is %d bytes, over the %d-byte cap", len(SubagentDirective), subagentDirectiveMaxBytes)
	}
	for _, line := range strings.Split(strings.TrimSpace(graphCommands), "\n") {
		if !strings.Contains(SubagentDirective, strings.TrimSpace(line)+"\n") {
			t.Errorf("SubagentDirective lacks the guide's command %q", strings.TrimSpace(line))
		}
	}
	for _, want := range []string{"EACH code-locating or relationship question", "never for callers/references", "No timeout wrapper", "not partial results", "not instructions"} {
		if !strings.Contains(SubagentDirective, want) {
			t.Errorf("SubagentDirective lost %q", want)
		}
	}
}

// TestClaudeSettingsGuardsHoldIndividually exercises each alias guard on its own. The installer
// checks .claude and the settings file when it reads them AND again when it writes, so an
// end-to-end test cannot tell whether either layer still holds; these call each layer directly.
func TestClaudeSettingsGuardsHoldIndividually(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	openRepo := func(t *testing.T, dir string) *os.Root {
		t.Helper()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.Close() })
		return root
	}

	t.Run("read refuses symlinked .claude", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "real"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real", filepath.Join(repo, ".claude")); err != nil {
			t.Fatal(err)
		}
		if _, err := readClaudeSettings(openRepo(t, repo)); err == nil {
			t.Fatal("readClaudeSettings accepted a symlinked .claude")
		}
	})
	t.Run("read refuses symlinked settings", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../package.json", filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := readClaudeSettings(openRepo(t, repo)); err == nil {
			t.Fatal("readClaudeSettings accepted a symlinked settings file")
		}
	})
	t.Run("write refuses .claude swapped after the read", func(t *testing.T) {
		repo := t.TempDir()
		root := openRepo(t, repo)
		if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		state, err := readClaudeSettings(root)
		if err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(repo, "victim")
		if err := os.Rename(filepath.Join(repo, ".claude"), victim); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("victim", filepath.Join(repo, ".claude")); err != nil {
			t.Fatal(err)
		}
		if err := writeClaudeSettings(root, state, []byte("{}\n")); err == nil {
			t.Fatal("writeClaudeSettings followed a .claude swapped for a symlink")
		}
		if entries, _ := os.ReadDir(victim); len(entries) != 0 {
			t.Fatalf("swapped-in target was written: %v", entries)
		}
	})
	t.Run("write refuses settings swapped after the read", func(t *testing.T) {
		repo := t.TempDir()
		root := openRepo(t, repo)
		settings := filepath.Join(repo, ".claude", "settings.local.json")
		if err := os.Mkdir(filepath.Dir(settings), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settings, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		state, err := readClaudeSettings(root)
		if err != nil {
			t.Fatal(err)
		}
		// The replacement is created while the original still exists, so it cannot reuse the
		// original's inode number (ext4 reuses a freed one at once), then renamed over it.
		swapped := settings + ".new"
		if err := os.WriteFile(swapped, []byte(`{"someone":"else"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(swapped, settings); err != nil {
			t.Fatal(err)
		}
		if err := writeClaudeSettings(root, state, []byte("{}\n")); err == nil {
			t.Fatal("writeClaudeSettings replaced a settings file it did not read")
		}
	})
}
