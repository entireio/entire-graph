package agentsetup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// InstallChanged preserves the installer safety checks and reports paths whose
// contents changed, for callers exposing a machine-readable installation result.
func InstallChanged(root string, render func() (string, error)) ([]string, error) {
	snapshot := func() (map[string][]byte, error) {
		dir, err := os.OpenRoot(root)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		result := map[string][]byte{}
		for _, name := range []string{Path, "AGENTS.md", "CLAUDE.md", ".entire/graph-agent.md", ".entire/brain-agent.md"} {
			info, err := inspectInstructionFile(dir, name, filepath.Join(root, name))
			if err != nil {
				return nil, err
			}
			if info == nil {
				continue
			}
			content, err := readContainedFile(dir, name, 4<<20)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			result[name] = content
		}
		return result, nil
	}
	before, err := snapshot()
	if err != nil {
		return nil, err
	}
	if err := Install(root, render, io.Discard); err != nil {
		return nil, err
	}
	after, err := snapshot()
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for _, name := range []string{Path, "AGENTS.md", "CLAUDE.md", ".entire/graph-agent.md", ".entire/brain-agent.md"} {
		old, existed := before[name]
		next, exists := after[name]
		if exists && (!existed || !bytes.Equal(old, next)) {
			changed = append(changed, name)
		}
	}
	return changed, nil
}
