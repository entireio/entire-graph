//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStatsMedianFallbackRefusesWorktreeJunction(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte(`gitdir: \\203.0.113.1\share\repo`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "inside.go"), []byte("package inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "outside.go"), []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(repo, "mounted")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, external).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v\n%s", err, output)
	}

	collector := newStatsCollector(t.Context(), repo)
	if got := collector.medianTrackedFileSize(); got != 0 {
		t.Fatalf("median across unsafe worktree = %d, want 0", got)
	}
	if !collector.worktreeSafeDone || collector.worktreeSafe {
		t.Fatalf("worktree safety cache = done:%v safe:%v, want done:true safe:false", collector.worktreeSafeDone, collector.worktreeSafe)
	}
}

func TestStatsMedianCompatibilityModeDoesNotEnterWorktreeJunction(t *testing.T) {
	t.Setenv("GODEBUG", "winsymlink=0")
	repo := t.TempDir()
	inside := []byte("package inside\n")
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte(`gitdir: \\203.0.113.1\share\repo`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "inside.go"), inside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "second.go"), inside, 0o600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "outside.go"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(repo, "mounted")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, external).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v\n%s", err, output)
	}

	collector := newStatsCollector(t.Context(), repo)
	if got := collector.medianTrackedFileSize(); got != int64(len(inside)) {
		t.Fatalf("compatibility-mode median = %d, want local file size %d", got, len(inside))
	}
}
