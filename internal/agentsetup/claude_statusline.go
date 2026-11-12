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

// StatusLineKind says whose a settings scope's statusLine is.
type StatusLineKind int

const (
	// StatusLineForeign is anyone else's status line.
	StatusLineForeign StatusLineKind = iota
	// StatusLineOurs runs StatusLineCommand.
	StatusLineOurs
	// StatusLineLegacy runs the compatibility shim scripts/entire-graph-statusline.sh, the line the
	// plugin manifest and docs installed before the subcommand existed. It is ours, only older:
	// setup names it and the command to switch to, and --statusline replaces it in
	// settings.local.json.
	StatusLineLegacy
)

// legacyStatusLineScript is the shim a legacy statusLine command runs.
const legacyStatusLineScript = "entire-graph-statusline.sh"

func statusLineKindOf(raw json.RawMessage) StatusLineKind {
	var line struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &line) != nil {
		return StatusLineForeign
	}
	switch {
	case strings.Contains(line.Command, StatusLineCommand):
		return StatusLineOurs
	case strings.Contains(line.Command, legacyStatusLineScript):
		return StatusLineLegacy
	}
	return StatusLineForeign
}

// mergeStatusLine adds our statusLine to a settings document. Every other key is preserved. An
// existing statusLine is never replaced, with one exception: ours makes this a no-op, anyone
// else's is reported as foreign and left exactly as it is, and the legacy shim line (ours, older)
// is replaced by ours.
func mergeStatusLine(existing []byte) (rendered []byte, changed, foreign bool, err error) {
	settings, err := parseSettingsObject(ClaudeLocalSettings, existing)
	if err != nil {
		return nil, false, false, err
	}
	if line := statusLineOf(settings); line != nil {
		switch statusLineKindOf(line) {
		case StatusLineOurs:
			return existing, false, false, nil
		case StatusLineForeign:
			return existing, false, true, nil
		}
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
		return fmt.Errorf("setup: %w %s", err, claudeStatusLineSettingsHint)
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
		return "", false, false, false, fmt.Errorf("setup: %w %s", err, claudeStatusLineSettingsHint)
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

// ActiveStatusLine reports the statusLine Claude Code shows: path is the first settings scope, in
// Claude Code precedence order, that defines one, and kind says whose that line is. The order is
// project local, project shared (<root>/.claude/settings.json), user (<configDir>/settings.json,
// where configDir is CLAUDE_CONFIG_DIR when set, else <home>/.claude, resolved by the caller). path
// is "" when no scope defines one. configDir is a parameter so tests never read the real one; an
// empty configDir skips the user scope.
//
// The first scope that defines a statusLine is the one Claude Code shows, so the scan stops there:
// a line in a higher scope means the lower ones are already overridden.
//
// A scope that cannot be inspected stops the scan too, since it may hold a status line that
// overrides the next: err is then non-nil, path names that scope and kind is StatusLineForeign. A
// parse error does not name the file again; path does. Both project files are read through the
// repository root, never following a symlink, a hard link or a non-regular file, because a
// checkout controls them. One leading UTF-8 BOM is tolerated in the two files setup only reads
// (Windows PowerShell 5.1 writes one); settings.local.json, which setup rewrites, is refused as
// before by CheckClaudeStatusLine.
func ActiveStatusLine(root, configDir string) (path string, kind StatusLineKind, err error) {
	repo, err := os.OpenRoot(root)
	if err != nil {
		return root, StatusLineForeign, err
	}
	defer repo.Close()
	type scope struct {
		path  string
		local bool
		read  func() ([]byte, error)
	}
	repoScope := func(name string) scope {
		return scope{
			path:  filepath.Join(root, filepath.FromSlash(name)),
			local: name == ClaudeLocalSettings,
			read: func() ([]byte, error) {
				state, err := readRepoClaudeSettings(repo, name)
				if err == nil && state.file == nil {
					return nil, fs.ErrNotExist
				}
				return state.content, err
			},
		}
	}
	scopes := []scope{repoScope(ClaudeLocalSettings), repoScope(claudeSharedSettings)}
	if configDir != "" {
		user := filepath.Join(configDir, "settings.json")
		scopes = append(scopes, scope{path: user, read: func() ([]byte, error) { return readSettingsFile(user) }})
	}
	for _, scope := range scopes {
		content, err := scope.read()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return scope.path, StatusLineForeign, err
		}
		if !scope.local {
			content = bytes.TrimPrefix(content, utf8BOM)
		}
		settings, err := parseSettingsObject("the file", content)
		if err != nil {
			return scope.path, StatusLineForeign, err
		}
		if line := statusLineOf(settings); line != nil {
			return scope.path, statusLineKindOf(line), nil
		}
	}
	return "", StatusLineForeign, nil
}

// claudeSharedSettings is the project's shared, usually committed, Claude Code settings file.
const claudeSharedSettings = ".claude/settings.json"

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// readSettingsFile reads the user's own settings file for inspection only, bounded like the local
// read. It is outside the repository, so no checkout controls it.
func readSettingsFile(path string) ([]byte, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	content, err := io.ReadAll(io.LimitReader(handle, maxClaudeSettingsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxClaudeSettingsBytes {
		return nil, errContainedFileTooLarge
	}
	return content, nil
}
