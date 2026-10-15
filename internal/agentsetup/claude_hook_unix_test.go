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

// TestSettingsRenameReplacesASwappedInSymlinkNotItsTarget pins the property the write's last step
// relies on. Every check before the rename can lose a race: a writer can swap settings.local.json
// for a symlink after the final Lstat. That is harmless only because rename(2) replaces the
// destination DIRECTORY ENTRY and never follows a symlink there, so the link itself is replaced
// and its target (here a stand-in for package.json) is untouched. This exercises the same
// os.Root.Rename call writeClaudeSettings makes, with the symlink already in place.
func TestSettingsRenameReplacesASwappedInSymlinkNotItsTarget(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	target := filepath.Join(repo, "package.json")
	if err := os.WriteFile(target, []byte(`{"name":"victim"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../package.json", filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	dir, err := os.OpenRoot(filepath.Join(repo, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := dir.WriteFile(".staged", []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dir.Rename(".staged", "settings.local.json"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != `{"name":"victim"}` {
		t.Fatalf("rename wrote through the symlink into its target: %q", got)
	}
	info, err := os.Lstat(filepath.Join(repo, ".claude", "settings.local.json"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("settings.local.json is not the new regular file (info %v, err %v)", info, err)
	}
}
