package sem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedWorktreePolicyUsesCapturedExplicitBytes(t *testing.T) {
	repo := t.TempDir()
	for path, content := range map[string]string{"a.go": "package p\nfunc A(){}\n", "b.go": "package p\nfunc B(){}\n", ".graphignore": "b.go\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options, err := CaptureProviderCachePolicy(repo, ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, ExtractionCacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".graphignore"), []byte("a.go\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
	if err != nil {
		t.Fatal(err)
	}
	a, b := false, false
	for _, symbol := range snapshot.Symbols {
		a = a || symbol.Name == "A"
		b = b || symbol.Name == "B"
	}
	if !a || b {
		t.Fatalf("used live policy instead of captured policy: A=%v B=%v", a, b)
	}
}

func TestCapturedPolicyContentReaderDoesNotReread(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".graphignore")
	policy := &capturedIgnorePolicy{graphIgnore: capturedIgnoreFile{path: path, content: []byte("before"), present: true}}
	read := capturedPolicyContentReader(repo, policy, func(string) (string, bool) { t.Fatal("policy reread"); return "", false })
	got, ok := read(".graphignore")
	if !ok || got != "before" {
		t.Fatal("lost captured policy bytes")
	}
}
