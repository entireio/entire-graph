package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestTargetedEdgeCacheOutputHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "main.go", "package p\nfunc caller(){ helper() }\nfunc helper(){}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		t.Run(name, func(t *testing.T) {
			cache := t.TempDir()
			args := []string{"edges", "--repo", repo, "--to", "helper", "--relation", "CALLS", "--format", "ndjson", "--cache-dir", cache}
			if warm {
				var out bytes.Buffer
				if err := Run(t.Context(), Options{Version: "cancel-review", Stdout: &out, Stderr: io.Discard}, args); err != nil {
					t.Fatal(err)
				}
				if len(recordsCacheEntries(t, cache)) != 1 || !bytes.Contains(out.Bytes(), []byte(`"record_type":"relation"`)) {
					t.Fatal("fixture did not populate a targeted edge cache")
				}
			}
			sink := newStalledPipe()
			defer close(sink.release)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, Options{Version: "cancel-review", Stdout: sink, Stderr: io.Discard}, args) }()
			select {
			case <-sink.entered:
			case err := <-done:
				t.Fatalf("returned before output: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("never reached output")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("targeted output ignored cancellation")
			}
		})
	}
}
