//go:build !windows

package agentsetup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadClaudeSettingsRefusesAFIFO pins the regular-file check on its own. A symlink is also
// caught by the later handle comparison, but a FIFO is not caught by anything else: opening one
// blocks until a writer appears, so without the check init-agents hangs on a hostile checkout.
func TestReadClaudeSettingsRefusesAFIFO(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(repo, ".claude", "settings.local.json"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		_, err := readClaudeSettings(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("readClaudeSettings accepted a FIFO")
		}
	case <-time.After(5 * time.Second):
		// Unblock the reader so the test process can exit.
		if writer, err := os.OpenFile(filepath.Join(repo, ".claude", "settings.local.json"), os.O_WRONLY, 0); err == nil {
			writer.Close()
		}
		t.Fatal("readClaudeSettings blocked opening a FIFO")
	}
}
