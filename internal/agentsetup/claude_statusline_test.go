package agentsetup

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeSettingsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func localSettingsPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(ClaudeLocalSettings))
}

func TestStatusLineInstallIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := localSettingsPath(root)

	_, _, changed, foreign, err := InstallClaudeStatusLineChanged(root)
	if err != nil || !changed || foreign {
		t.Fatalf("first install: changed %v, foreign %v, err %v", changed, foreign, err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, foreign, err = InstallClaudeStatusLineChanged(root)
	if err != nil || changed || foreign {
		t.Fatalf("second install: changed %v, foreign %v, err %v", changed, foreign, err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("second install rewrote the file:\n%s\n---\n%s", first, second)
	}
}

func TestStatusLineInstallKeepsForeignLocalStatusLine(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := localSettingsPath(root)
	original := `{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`
	writeSettingsFile(t, path, original)

	_, _, changed, foreign, err := InstallClaudeStatusLineChanged(root)
	if err != nil || changed || !foreign {
		t.Fatalf("install over a foreign line: changed %v, foreign %v, err %v", changed, foreign, err)
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Fatalf("foreign settings rewritten: %q", got)
	}
}

func TestForeignStatusLineFindsUserScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := t.TempDir()
	userSettings := filepath.Join(home, ".claude", "settings.json")
	writeSettingsFile(t, userSettings, `{"statusLine":{"type":"command","command":"node graft-statusline.cjs"}}`)

	got, err := ForeignStatusLine(root, home)
	if err != nil || got != userSettings {
		t.Fatalf("ForeignStatusLine = %q, %v; want %q", got, err, userSettings)
	}

	writeSettingsFile(t, userSettings, `{"statusLine":{"type":"command","command":"entire graph statusline"}}`)
	got, err = ForeignStatusLine(root, home)
	if err != nil || got != "" {
		t.Fatalf("ours in user scope: ForeignStatusLine = %q, %v; want \"\"", got, err)
	}
}

func TestStatusLineInstallPreservesOtherSettings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := localSettingsPath(root)
	original := `{
  "hooks": {"SubagentStart": [{"matcher": "*", "hooks": [{"type": "command", "command": "entire graph hook claude-subagent-start 2>/dev/null || true", "timeout": 10}]}]},
  "permissions": {"allow": ["Bash(ls)"]}
}`
	writeSettingsFile(t, path, original)
	before := decodeSettings(t, []byte(original))

	if _, _, changed, _, err := InstallClaudeStatusLineChanged(root); err != nil || !changed {
		t.Fatalf("install: changed %v, err %v", changed, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after := decodeSettings(t, content)
	beforeHooks := before["hooks"].(map[string]any)["SubagentStart"]
	afterHooks, _ := after["hooks"].(map[string]any)
	if !reflect.DeepEqual(beforeHooks, afterHooks["SubagentStart"]) {
		t.Errorf("hooks.SubagentStart changed:\nbefore %v\nafter  %v", beforeHooks, afterHooks["SubagentStart"])
	}
	if !reflect.DeepEqual(before["permissions"], after["permissions"]) {
		t.Errorf("permissions changed:\nbefore %v\nafter  %v", before["permissions"], after["permissions"])
	}
	if _, ok := after["statusLine"]; !ok {
		t.Errorf("no statusLine written:\n%s", content)
	}
}
