package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// StatusLineCommand is the Claude Code statusLine command setup writes, the same on every OS. A
// statusLine whose command CONTAINS it counts as ours, so one rewritten with an absolute path is
// still recognised.
//
// There is no "shell" key and no wrapper. On Windows Claude Code runs the command through Git Bash
// when that is installed, else PowerShell; a PowerShell try{} wrapper breaks under bash, and a POSIX
// `|| true` breaks under PowerShell 5.1. The subcommand fails open by itself, and an older binary's
// "unknown command" goes to stderr, which Claude Code never renders.
const StatusLineCommand = "entire graph statusline"

// statusLineValue is the statusLine object setup merges into settings.local.json.
func statusLineValue() map[string]any {
	return map[string]any{"type": "command", "command": StatusLineCommand}
}

// statusLineOf returns the statusLine entry of a settings document, or nil when it has none.
func statusLineOf(settings map[string]json.RawMessage) json.RawMessage {
	raw, ok := settings["statusLine"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}

func isOurStatusLine(raw json.RawMessage) bool {
	var line struct {
		Command string `json:"command"`
	}
	return json.Unmarshal(raw, &line) == nil && strings.Contains(line.Command, StatusLineCommand)
}

// mergeStatusLine adds our statusLine to a settings document. Every other key is preserved. An
// existing statusLine is never replaced: ours makes this a no-op, and anyone else's is reported as
// foreign and left exactly as it is.
func mergeStatusLine(existing []byte) (rendered []byte, changed, foreign bool, err error) {
	settings, err := parseSettingsObject(ClaudeLocalSettings, existing)
	if err != nil {
		return nil, false, false, err
	}
	if line := statusLineOf(settings); line != nil {
		return existing, false, !isOurStatusLine(line), nil
	}
	if settings["statusLine"], err = marshalSettingsValue(statusLineValue()); err != nil {
		return nil, false, false, err
	}
	out, err := marshalSettingsValue(settings)
	if err != nil {
		return nil, false, false, err
	}
	return append(out, '\n'), true, false, nil
}

func planClaudeStatusLine(repo *os.Root) (claudeSettingsState, []byte, bool, bool, error) {
	state, err := readClaudeSettings(repo)
	if err != nil {
		return state, nil, false, false, err
	}
	rendered, changed, foreign, err := mergeStatusLine(state.content)
	return state, rendered, changed, foreign, err
}

// CheckClaudeStatusLine is the preflight for InstallClaudeStatusLineChanged: settings.local.json
// must be readable through the same symlink and hard-link refusals as the hook, and an object.
func CheckClaudeStatusLine(root string) error {
	repo, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer repo.Close()
	if _, _, _, _, err := planClaudeStatusLine(repo); err != nil {
		return fmt.Errorf("setup: %w %s", err, claudeSettingsHint)
	}
	return nil
}

// InstallClaudeStatusLineChanged merges statusLine into <root>/.claude/settings.local.json.
// It never touches a foreign statusLine in that file.
func InstallClaudeStatusLineChanged(root string) (path string, created, changed, foreign bool, err error) {
	repo, err := os.OpenRoot(root)
	if err != nil {
		return "", false, false, false, fmt.Errorf("setup: %w", err)
	}
	defer repo.Close()
	state, rendered, changed, foreign, err := planClaudeStatusLine(repo)
	if err != nil {
		return "", false, false, false, fmt.Errorf("setup: %w %s", err, claudeSettingsHint)
	}
	path = filepath.Join(root, filepath.FromSlash(ClaudeLocalSettings))
	if !changed {
		return path, false, false, foreign, nil
	}
	if err := writeClaudeSettings(repo, state, rendered); err != nil {
		return path, false, false, false, fmt.Errorf("setup: %s: %w", ClaudeLocalSettings, err)
	}
	return path, state.file == nil, true, false, nil
}

// ForeignStatusLine reports which settings scope already defines a statusLine that
// is not ours, in Claude Code precedence order: project local, project shared
// (<root>/.claude/settings.json), user (<home>/.claude/settings.json). "" when none.
// home is a parameter so tests never read the real one.
//
// The first scope that defines a statusLine is the one Claude Code shows, so the scan stops there:
// ours in a higher scope means the lower ones are already overridden. An empty home skips the
// user scope.
func ForeignStatusLine(root, home string) (path string, err error) {
	scopes := []string{
		filepath.Join(root, filepath.FromSlash(ClaudeLocalSettings)),
		filepath.Join(root, ".claude", "settings.json"),
	}
	if home != "" {
		scopes = append(scopes, filepath.Join(home, ".claude", "settings.json"))
	}
	for _, scope := range scopes {
		content, err := readSettingsFile(scope)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		settings, err := parseSettingsObject(scope, content)
		if err != nil {
			return "", err
		}
		if line := statusLineOf(settings); line != nil {
			if isOurStatusLine(line) {
				return "", nil
			}
			return scope, nil
		}
	}
	return "", nil
}

// readSettingsFile reads a settings file for inspection only, bounded like the local read.
func readSettingsFile(path string) ([]byte, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	content, err := io.ReadAll(io.LimitReader(handle, maxClaudeSettingsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(content) > maxClaudeSettingsBytes {
		return nil, fmt.Errorf("%s: %w", path, errContainedFileTooLarge)
	}
	return content, nil
}
