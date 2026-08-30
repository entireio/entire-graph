package sem

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkGitMetadataSafeForSubprocess(b *testing.B) {
	repo := b.TempDir()
	gitDir := filepath.Join(repo, ".git")
	for _, dir := range []string{
		filepath.Join(gitDir, "objects", "info"),
		filepath.Join(gitDir, "refs", "heads"),
		filepath.Join(gitDir, "info"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"HEAD":   "ref: refs/heads/main\n",
		"config": "[core]\n\trepositoryformatversion = 0\n",
	} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		if !gitMetadataSafeForSubprocess(repo) {
			b.Fatal("valid local metadata refused")
		}
	}
}
