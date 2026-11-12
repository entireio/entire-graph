//go:build !windows

package sem

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A subprocess is required here: SQLite can block inside the filesystem open, before it can
// observe a cancelled query context. The parent kills a regressed reader instead of hanging too.
func TestSearchStoreReadersRejectFIFO(t *testing.T) {
	const helperEnv = "ENTIRE_GRAPH_TEST_FIFO_STORE_READER"
	if mode := os.Getenv(helperEnv); mode != "" {
		entry := cacheEntry{root: os.Getenv("ENTIRE_GRAPH_TEST_FIFO_STORE_ROOT"), relative: "entry.json.gz"}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		var err error
		switch mode {
		case "focused":
			_, db, readErr := openExactSearchStoreForEntry(ctx, entry, "tree", cacheEntry.digest, true)
			if db != nil {
				_ = db.Close()
			}
			err = readErr
		case "exact":
			_, db, readErr := openExactSearchStoreForEntry(ctx, entry, "tree", cacheEntry.digest, false)
			if db != nil {
				_ = db.Close()
			}
			err = readErr
		case "full":
			_, err = loadSearchStore(ctx, filepath.Join(entry.root, "entry.sqlite"), "")
		default:
			t.Fatalf("unknown reader %q", mode)
		}
		if err == nil {
			t.Fatal("FIFO was accepted as a store")
		}
		return
	}

	for _, entryPresent := range []bool{false, true} {
		entryState := "missing-entry"
		if entryPresent {
			entryState = "existing-entry"
		}
		t.Run(entryState, func(t *testing.T) {
			for _, linked := range []bool{false, true} {
				shape := "fifo"
				if linked {
					shape = "symlink-to-fifo"
				}
				t.Run(shape, func(t *testing.T) {
					root := t.TempDir()
					fifo := filepath.Join(root, "entry.sqlite")
					if linked {
						fifo = filepath.Join(root, "pipe")
					}
					if err := syscall.Mkfifo(fifo, 0o600); err != nil {
						t.Fatal(err)
					}
					if linked {
						if err := os.Symlink(fifo, filepath.Join(root, "entry.sqlite")); err != nil {
							t.Fatal(err)
						}
					}
					if entryPresent {
						if err := (cacheEntry{root: root, relative: "entry.json.gz"}).write("fixture", cachedSearchSnapshot{}); err != nil {
							t.Fatal(err)
						}
					}
					for _, mode := range []string{"focused", "exact", "full"} {
						t.Run(mode, func(t *testing.T) {
							ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
							defer cancel()
							cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSearchStoreReadersRejectFIFO$")
							cmd.Env = append(os.Environ(), helperEnv+"="+mode, "ENTIRE_GRAPH_TEST_FIFO_STORE_ROOT="+root)
							output, err := cmd.CombinedOutput()
							if errors.Is(ctx.Err(), context.DeadlineExceeded) {
								t.Fatalf("%s reader blocked on a FIFO instead of falling back", mode)
							}
							if err != nil {
								t.Fatalf("reader failed: %v\n%s", err, strings.TrimSpace(string(output)))
							}
						})
					}
				})
			}
		})
	}
}
