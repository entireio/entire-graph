package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

func runHookForTest(t *testing.T, stdin string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr},
		[]string{"hook", "claude-subagent-start"})
	if stderr.Len() != 0 {
		t.Errorf("hook wrote to stderr: %q", stderr.String())
	}
	return stdout.String(), err
}

func subagentEvent(cwd string) string {
	payload, _ := json.Marshal(map[string]string{
		"session_id": "s", "transcript_path": "/t.jsonl", "cwd": cwd, "permission_mode": "default",
		"hook_event_name": "SubagentStart", "agent_id": "a1", "agent_type": "Explore",
	})
	return string(payload)
}

// gitRepoForTest makes a directory holding a .git entry. A real `git init` is not needed: the hook
// deliberately runs no git subprocess and only looks for the entry.
func gitRepoForTest(t *testing.T, gitIsFile bool) string {
	t.Helper()
	repo := t.TempDir()
	if gitIsFile {
		if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestClaudeSubagentStartHookOutputShape(t *testing.T) {
	t.Parallel()
	for name, gitIsFile := range map[string]bool{"checkout": false, "linked worktree": true} {
		repo := gitRepoForTest(t, gitIsFile)
		nested := filepath.Join(repo, "pkg", "sub")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := runHookForTest(t, subagentEvent(nested))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Exactly the documented shape, and nothing else on stdout: Claude Code treats stdout
		// that is not this JSON as plain text shown to the user only.
		var decoded struct {
			HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
		}
		decoder := json.NewDecoder(strings.NewReader(out))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatalf("%s: stdout is not the hook JSON: %v\n%s", name, err, out)
		}
		if decoder.More() {
			t.Errorf("%s: trailing output after the hook JSON: %q", name, out)
		}
		want := map[string]string{"hookEventName": "SubagentStart", "additionalContext": agentsetup.SubagentDirective}
		if len(decoded.HookSpecificOutput) != len(want) {
			t.Errorf("%s: hookSpecificOutput = %v", name, decoded.HookSpecificOutput)
		}
		for key, value := range want {
			if decoded.HookSpecificOutput[key] != value {
				t.Errorf("%s: hookSpecificOutput[%q] = %q, want %q", name, key, decoded.HookSpecificOutput[key], value)
			}
		}
	}
}

func TestClaudeSubagentStartHookFailsOpen(t *testing.T) {
	t.Parallel()
	repo := gitRepoForTest(t, false)
	for name, stdin := range map[string]string{
		"empty":         "",
		"garbage":       "not json",
		"array":         `[]`,
		"truncated":     subagentEvent(repo)[:20],
		"other event":   strings.Replace(subagentEvent(repo), "SubagentStart", "SessionStart", 1),
		"no event name": `{"cwd":"` + repo + `"}`,
		// Valid JSON followed by whitespace stays valid when cut at the read bound, so only the
		// explicit size check can refuse it.
		"oversize":        subagentEvent(repo) + strings.Repeat(" ", maxHookEventBytes),
		"cwd not a dir":   subagentEvent(filepath.Join(repo, "missing")),
		"cwd wrong type":  `{"hook_event_name":"SubagentStart","cwd":7}`,
		"event name type": `{"hook_event_name":1,"cwd":"` + repo + `"}`,
	} {
		out, err := runHookForTest(t, stdin)
		if err != nil || out != "" {
			t.Errorf("%s: got err %v, stdout %q; a hook must print nothing and exit 0 on bad input", name, err, out)
		}
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &stdout}, []string{"hook", "claude-subagent-start"}); err != nil || stdout.Len() != 0 {
		t.Errorf("nil stdin: err %v, stdout %q", err, stdout.String())
	}
	// A wrong hook NAME is a misconfiguration, and it is the one thing that should say so.
	if err := Run(context.Background(), Options{Stdout: &stdout}, []string{"hook", "nope"}); err == nil {
		t.Error("unknown hook name accepted")
	}
}

func TestClaudeSubagentStartHookSilentOutsideGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Precondition, asserted rather than assumed: no ancestor of the temp dir is a git work tree.
	// If one is (a temp dir inside a checkout), the assertion below would be vacuous.
	if insideGitWorkTree(dir) {
		t.Skipf("temporary directory %s is inside a git work tree on this machine", dir)
	}
	out, err := runHookForTest(t, subagentEvent(dir))
	if err != nil || out != "" {
		t.Fatalf("outside a repository: err %v, stdout %q", err, out)
	}
	// And the same directory becomes eligible the moment it holds a .git entry, so the silence
	// above is the repository check and not some other failure.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, _ := runHookForTest(t, subagentEvent(dir)); out == "" {
		t.Fatal("hook stayed silent inside a repository")
	}
}

func settingsHookCount(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatalf("settings not JSON: %v\n%s", err, content)
	}
	n := 0
	for _, group := range settings.Hooks["SubagentStart"] {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, agentsetup.SubagentHookCommand) {
				n++
			}
		}
	}
	return n
}

func TestInitAgentsInstallsClaudeSubagentHook(t *testing.T) {
	t.Parallel()
	repo := gitRepoForTest(t, false)
	settingsPath := filepath.Join(repo, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"permissions":{"allow":["Bash(ls)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"./stop.sh"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	initAgents := func(extra ...string) (string, error) {
		var out bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, append([]string{"init-agents", "--repo", repo}, extra...))
		return out.String(), err
	}

	report, err := initAgents()
	if err != nil {
		t.Fatalf("%v\n%s", err, report)
	}
	if !strings.Contains(report, "updated "+settingsPath) {
		t.Errorf("init-agents did not report the settings change:\n%s", report)
	}
	if n := settingsHookCount(t, settingsPath); n != 1 {
		t.Fatalf("hook count = %d, want 1", n)
	}
	content, _ := os.ReadFile(settingsPath)
	for _, kept := range []string{`"Bash(ls)"`, `"./stop.sh"`} {
		if !strings.Contains(string(content), kept) {
			t.Errorf("existing setting %s lost:\n%s", kept, content)
		}
	}

	report, err = initAgents()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(settingsPath)
	if !bytes.Equal(content, again) || !strings.Contains(report, "unchanged "+settingsPath) {
		t.Fatalf("rerun was not an idempotent, reported no-op:\n%s\n%s", report, again)
	}
}

func TestInitAgentsClaudeHookOptOutAndPreflight(t *testing.T) {
	t.Parallel()
	t.Run("opt out writes nothing under .claude", func(t *testing.T) {
		repo := gitRepoForTest(t, false)
		var out bytes.Buffer
		if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo, "--no-claude-hook"}); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if _, err := os.Stat(filepath.Join(repo, ".claude")); !os.IsNotExist(err) {
			t.Fatalf("--no-claude-hook still created .claude (err %v)", err)
		}
		if _, err := os.Stat(filepath.Join(repo, agentsetup.Path)); err != nil {
			t.Fatalf("guide not written: %v", err)
		}
	})
	t.Run("unusable settings fail before any write", func(t *testing.T) {
		repo := gitRepoForTest(t, false)
		settingsPath := filepath.Join(repo, ".claude", "settings.local.json")
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settingsPath, []byte("// comment\n{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
		if err == nil || !strings.Contains(err.Error(), "--no-claude-hook") {
			t.Fatalf("err = %v, want a refusal naming the opt-out", err)
		}
		if _, statErr := os.Stat(filepath.Join(repo, agentsetup.Path)); !os.IsNotExist(statErr) {
			t.Fatalf("guide written despite the failed preflight (stat err %v)", statErr)
		}
		if got, _ := os.ReadFile(settingsPath); string(got) != "// comment\n{}" {
			t.Fatalf("user settings rewritten: %q", got)
		}
	})
}
