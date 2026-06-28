package lsp

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestManualResolve is a guarded integration check against a real LSP server.
// Run with: ENTIRE_LSP_REPO=/path/to/crate ENTIRE_LSP_LANG=rust go test ./internal/sem/lsp/ -run TestManualResolve -v
func TestManualResolve(t *testing.T) {
	repo := os.Getenv("ENTIRE_LSP_REPO")
	if repo == "" {
		t.Skip("set ENTIRE_LSP_REPO to run")
	}
	lang := os.Getenv("ENTIRE_LSP_LANG")
	if lang == "" {
		lang = "rust"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, ok, err := ResolveCalls(ctx, repo, lang)
	if err != nil {
		t.Fatalf("ResolveCalls: %v", err)
	}
	if !ok {
		t.Fatalf("no LSP server available for %s", lang)
	}
	t.Logf("symbols=%d calls=%d load_errors=%d stalled=%d", len(res.Symbols), len(res.Calls), res.LoadErrors, res.Stalled)
	for i, e := range res.Calls {
		if i >= 8 {
			break
		}
		t.Logf("  %s:%d %s -> %s:%d %s", e.FromFile, e.FromLine, e.FromName, e.ToFile, e.ToLine, e.ToName)
	}
}
