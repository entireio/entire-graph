package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnapshotOutputWritesTheRecordsItself closes the saved-snapshot flow the
// compact format exists for:
//
//	entire graph snapshot --format compact-ndjson --output graph.compact.ndjson
//	entire graph snapshot-query --input graph.compact.ndjson --symbol validateToken
//
// Saving it used to require a shell redirect, and the shell decides the
// encoding: Windows PowerShell 5.1's `>` is Out-File, whose default is
// UTF-16LE, so the documented flow produced a file the compact loader refuses.
// Nothing in this repository could fix that from the reading end -- a UTF-16
// artifact is UTF-16 all the way down -- so the writing end has to be able to
// produce the file without a shell in between.
//
// The bytes must be the ones stdout would have received, or `--output` would
// be a second, subtly different encoder. Both record paths are checked,
// because they write through different calls: the streaming encoder on a cache
// miss, and a single buffered write on a cache hit.
func TestSnapshotOutputWritesTheRecordsItself(t *testing.T) {
	t.Parallel()
	repo := truncatedSnapshotRepo(t)

	run := func(t *testing.T, cacheDir string, calls *int, extra ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := append([]string{"snapshot", "--format", "compact-ndjson", "--repo", repo, "--cache-dir", cacheDir}, extra...)
		if err := Run(t.Context(), Options{
			Env:            EntireEnv{RepoRoot: repo},
			Stdout:         &stdout,
			Stderr:         &stderr,
			streamSnapshot: streamMarkingSummary(false, calls),
		}, args); err != nil {
			t.Fatalf("%v: %v (stderr %q)", args, err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	warm := t.TempDir()
	calls := 0

	// What a redirect would have captured, and the entry the next run hits.
	redirected, _ := run(t, warm, &calls)
	if redirected == "" {
		t.Fatal("non-vacuity: the snapshot wrote nothing to stdout, so comparing against it proves nothing")
	}
	if calls != 1 {
		t.Fatalf("the first run should have built the snapshot once, not %d time(s)", calls)
	}

	// Three destinations, because the records reach three different writers:
	// one buffered write on a cache hit, a tee into the cache entry on a miss,
	// and the bare streaming encoder when there is no cache at all. A flag
	// that reaches only two of them is a flag that silently keeps printing.
	for _, tc := range []struct {
		name      string
		cacheDir  string
		extra     []string
		wantCalls int
	}{
		{name: "served from the cache", cacheDir: warm, wantCalls: 1},
		{name: "built by the streaming encoder, teed into the cache", cacheDir: t.TempDir(), wantCalls: 2},
		{name: "built with no cache at all", cacheDir: t.TempDir(), extra: []string{"--no-cache"}, wantCalls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := filepath.Join(t.TempDir(), "graph.compact.ndjson")
			stdout, _ := run(t, tc.cacheDir, &calls, append([]string{"--output", saved}, tc.extra...)...)
			if stdout != "" {
				t.Fatalf("--output must take the records off stdout, got %d byte(s) there", len(stdout))
			}
			if calls != tc.wantCalls {
				t.Fatalf("stream calls = %d, want %d: this case did not exercise the path it names", calls, tc.wantCalls)
			}
			written, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != redirected {
				t.Fatalf("--output wrote %d byte(s), a redirect of the same command wrote %d; they must be the same artifact", len(written), len(redirected))
			}

			// The whole point of the file: the query reads it back.
			var queryOut, queryErr bytes.Buffer
			if err := Run(t.Context(), Options{
				Env:    EntireEnv{RepoRoot: repo},
				Stdout: &queryOut,
				Stderr: &queryErr,
			}, []string{"snapshot-query", "--input", saved, "--symbol", "validateToken"}); err != nil {
				t.Fatalf("snapshot-query on the saved file: %v (stderr %q)", err, queryErr.String())
			}
			if !strings.Contains(queryOut.String(), "validateToken") {
				t.Fatalf("the saved snapshot did not answer for its own symbol: %q", queryOut.String())
			}
		})
	}
}

// TestSnapshotOutputRefusalsLeaveNoFile pins the two things a destination flag
// gets wrong. A rejected invocation must not have touched the path already --
// os.Create truncates, so opening it before the flags are judged would empty
// yesterday's snapshot on a typo -- and a path that cannot be created has to
// say so as itself rather than as some later failure.
func TestSnapshotOutputRefusalsLeaveNoFile(t *testing.T) {
	t.Parallel()
	repo := truncatedSnapshotRepo(t)

	run := func(args ...string) error {
		var stdout, stderr bytes.Buffer
		return Run(t.Context(), Options{
			Env:    EntireEnv{RepoRoot: repo},
			Stdout: &stdout,
			Stderr: &stderr,
		}, args)
	}

	t.Run("a rejected flag combination does not truncate the destination", func(t *testing.T) {
		saved := filepath.Join(t.TempDir(), "graph.compact.ndjson")
		if err := os.WriteFile(saved, []byte("yesterday's snapshot\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// compact-ndjson is whole-snapshot only, so --to is refused.
		err := run("snapshot", "--format", "compact-ndjson", "--to", "validateToken", "--repo", repo, "--output", saved)
		if err == nil {
			t.Fatal("a targeted compact snapshot must still be refused")
		}
		kept, readErr := os.ReadFile(saved)
		if readErr != nil {
			t.Fatalf("the destination was removed by a refused command: %v", readErr)
		}
		if string(kept) != "yesterday's snapshot\n" {
			t.Fatalf("a refused command truncated the destination: %q", string(kept))
		}
	})

	t.Run("a path that cannot be created is named", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-directory", "graph.compact.ndjson")
		err := run("snapshot", "--format", "compact-ndjson", "--repo", repo, "--output", missing)
		if err == nil {
			t.Fatal("writing into a directory that does not exist must fail")
		}
		if !strings.Contains(err.Error(), "--output") {
			t.Fatalf("the failure must name the flag that caused it: %v", err)
		}
	})

	t.Run("the flag requires a value", func(t *testing.T) {
		if _, _, err := parseProviderFlags([]string{"--output"}); err == nil || !strings.Contains(err.Error(), "--output requires a value") {
			t.Fatalf("a trailing --output must be refused: %v", err)
		}
	})
}

// TestEdgesOutputWritesTheMatchedRelationsToAFile covers the fourth writer.
// A targeted edge query answers from the same cache entry an unfiltered run
// fills, through a single buffered write rather than the record encoder, so a
// destination flag wired through the encoders alone would still print this
// one to stdout.
func TestEdgesOutputWritesTheMatchedRelationsToAFile(t *testing.T) {
	t.Parallel()
	repo := truncatedSnapshotRepo(t)
	cacheDir := t.TempDir()
	calls := 0

	run := func(t *testing.T, extra ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := append([]string{"edges", "--format", "ndjson", "--to", "validateToken", "--repo", repo, "--cache-dir", cacheDir}, extra...)
		if err := Run(t.Context(), Options{
			Env:            EntireEnv{RepoRoot: repo},
			Stdout:         &stdout,
			Stderr:         &stderr,
			streamSnapshot: streamMarkingSummary(false, &calls),
		}, args); err != nil {
			t.Fatalf("%v: %v (stderr %q)", args, err, stderr.String())
		}
		return stdout.String()
	}

	redirected := run(t)
	if redirected == "" {
		t.Fatal("non-vacuity: the targeted query wrote nothing to stdout")
	}
	if calls != 1 {
		t.Fatalf("the first run should have built the graph once, not %d time(s)", calls)
	}

	saved := filepath.Join(t.TempDir(), "edges.ndjson")
	if stdout := run(t, "--output", saved); stdout != "" {
		t.Fatalf("--output must take the matched relations off stdout, got %d byte(s) there", len(stdout))
	}
	if calls != 1 {
		t.Fatalf("the second run was not served from the cache (%d stream call(s), want 1), so it did not exercise the buffered write", calls)
	}
	written, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != redirected {
		t.Fatalf("--output wrote %d byte(s), a redirect wrote %d; they must be the same artifact", len(written), len(redirected))
	}
}
