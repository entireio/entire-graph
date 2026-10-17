//go:build unix

package sem

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestVerifyAboveScopeRefusesFIFOManifest(t *testing.T) {
	repo := t.TempDir()
	scope := filepath.Join(repo, "sub")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	if err := syscall.Mkfifo(filepath.Join(repo, "go.mod"), 0600); err != nil {
		t.Fatal(err)
	}
	above, closeScope, ok := openSearchVerifyAboveScope(t.Context(), scope, "", false)
	if !ok {
		t.Fatal("valid physical checkout refused")
	}
	defer closeScope()
	start := time.Now()
	if _, found := above.read("go.mod"); found {
		t.Fatal("FIFO accepted as a manifest")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("FIFO refusal blocked for %v", elapsed)
	}
}
