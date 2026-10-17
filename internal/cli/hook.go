package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

// claudeSubagentStartHook is the hook name Claude Code's SubagentStart entry invokes; see
// agentsetup.SubagentHookCommand, which init-agents writes into .claude/settings.local.json.
const claudeSubagentStartHook = "claude-subagent-start"

// maxHookEventBytes bounds the event read. A SubagentStart event is a few hundred bytes.
const maxHookEventBytes = 1 << 20

// runHook dispatches `entire graph hook <name>`. Only a wrong hook NAME is an error: that is a
// misconfiguration a user should see. Everything that happens while serving a known hook fails
// open, because a hook that errors or hangs degrades every subagent the user starts.
func runHook(opts Options, args []string) error {
	if len(args) != 1 || args[0] != claudeSubagentStartHook {
		return fmt.Errorf("usage: entire graph hook %s (reads the hook event JSON on stdin)", claudeSubagentStartHook)
	}
	writeClaudeSubagentStart(opts.Stdin, opts.Stdout)
	return nil
}

// writeClaudeSubagentStart answers a Claude Code SubagentStart event with the graph directive as
// additionalContext, or prints nothing at all.
//
// It prints nothing unless the input is a SubagentStart event and its working directory (the
// event's cwd, else the process's) is inside a git work tree. It never builds or opens an index:
// the answer is a constant, and the only filesystem work is stat calls walking up to a .git entry.
func writeClaudeSubagentStart(in io.Reader, out io.Writer) {
	defer func() { _ = recover() }()
	if in == nil || out == nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(in, maxHookEventBytes+1))
	if err != nil || len(data) > maxHookEventBytes {
		return
	}
	var event struct {
		HookEventName string `json:"hook_event_name"`
		Cwd           string `json:"cwd"`
	}
	if json.Unmarshal(data, &event) != nil || event.HookEventName != "SubagentStart" {
		return
	}
	dir := event.Cwd
	if dir == "" || !filepath.IsAbs(dir) {
		if dir, err = os.Getwd(); err != nil {
			return
		}
	}
	if !insideGitWorkTree(dir) {
		return
	}
	// Buffered, then written once, so a failure part-way leaves stdout empty rather than holding
	// half a JSON document. HTML escaping is off: the directive's <task> and <Name> placeholders
	// would otherwise reach the transcript as \u003ctask\u003e.
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "SubagentStart",
			"additionalContext": agentsetup.SubagentDirective,
		},
	}) != nil {
		return
	}
	_, _ = out.Write(payload.Bytes())
}

// insideGitWorkTree reports whether dir or an ancestor holds a .git entry (a directory in a normal
// checkout, a file in a linked worktree or submodule). It runs no git subprocess: the hook has to
// stay cheap on every subagent start, and a false positive only adds a short reminder.
func insideGitWorkTree(dir string) bool {
	dir = filepath.Clean(dir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		} else if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
