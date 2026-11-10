package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// prefixProbeRepo builds a committed repo with a root file and a subdirectory
// file, shared by the spawn-count test and the benchmark (testing.TB covers both).
func prefixProbeRepo(tb testing.TB) string {
	tb.Helper()
	repo := tb.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			tb.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.name", "Entire Graph Test")
	run("config", "user.email", "graph@example.com")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		tb.Fatal(err)
	}
	for path, content := range map[string]string{
		"root.txt":     "alpha\nbeta\n",
		"sub/leaf.txt": "gamma\ndelta\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return repo
}

// The per-blob fallbacks (ScanBlobLinesAtRev, OversizeBlobAtRev, ShowFile) each
// compose rev^{tree}:prefix+path, and the prefix is fixed for a directory. One
// `rev-parse --show-prefix` per DIRECTORY is the contract; one per blob doubled
// the subprocess count of exactly the loops these fallbacks serve (oversized
// grep-hit validation, the fallback oversize registry).
func TestRepoTreePathResolvesThePrefixOncePerDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git wrapper is a POSIX shell script")
	}
	repo := prefixProbeRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	logFile := filepath.Join(wrapperDir, "show-prefix.log")
	const script = `#!/bin/sh
for arg do
	if [ "$arg" = "--show-prefix" ]; then
		echo spawn >> "$ENTIRE_GRAPH_SHOW_PREFIX_LOG"
	fi
done
exec "$REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("ENTIRE_GRAPH_SHOW_PREFIX_LOG", logFile)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	for range 3 {
		found, err := ScanBlobLinesAtRev(ctx, repo, "HEAD", "root.txt", func(line string) bool {
			return line == "beta"
		})
		if err != nil || !found {
			t.Fatalf("ScanBlobLinesAtRev(root.txt) = %v, %v, want found", found, err)
		}
	}
	if _, over, err := OversizeBlobAtRev(ctx, repo, "HEAD", "root.txt", 1); err != nil || !over {
		t.Fatalf("OversizeBlobAtRev(root.txt, 1) = %v, %v, want oversized", over, err)
	}
	// A subdirectory is a distinct directory with a distinct prefix: the blob
	// resolving at all proves the cached root prefix was not reused for it.
	sub := filepath.Join(repo, "sub")
	found, err := ScanBlobLinesAtRev(ctx, sub, "HEAD", "leaf.txt", func(line string) bool {
		return line == "delta"
	})
	if err != nil || !found {
		t.Fatalf("ScanBlobLinesAtRev(sub, leaf.txt) = %v, %v, want found", found, err)
	}

	log, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("no --show-prefix spawn was recorded: %v", err)
	}
	if got := strings.Count(string(log), "spawn"); got != 2 {
		t.Fatalf("rev-parse --show-prefix spawned %d times across 5 per-blob reads, want 2 (once per directory)", got)
	}

	// A relative repo argument names whatever directory the cwd makes it, so
	// the cache must key on the directory, not the spelling: "." from sub must
	// resolve sub's own prefix, not a "." entry minted at the root. leaf.txt
	// resolving at all proves it — with the root's empty prefix the lookup
	// would miss and report the blob absent.
	t.Chdir(repo)
	found, err = ScanBlobLinesAtRev(ctx, ".", "HEAD", "root.txt", func(line string) bool {
		return line == "beta"
	})
	if err != nil || !found {
		t.Fatalf("ScanBlobLinesAtRev(\".\" at root) = %v, %v, want found", found, err)
	}
	t.Chdir(sub)
	found, err = ScanBlobLinesAtRev(ctx, ".", "HEAD", "leaf.txt", func(line string) bool {
		return line == "delta"
	})
	if err != nil || !found {
		t.Fatalf("ScanBlobLinesAtRev(\".\" at sub) = %v, %v, want sub's own prefix", found, err)
	}
}

// BenchmarkScanBlobLinesAtRevSameRepo measures the per-blob cost of the
// fallback the search preselection loop pays once per over-cap grep hit.
func BenchmarkScanBlobLinesAtRevSameRepo(b *testing.B) {
	repo := prefixProbeRepo(b)
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		found, err := ScanBlobLinesAtRev(ctx, repo, "HEAD", "root.txt", func(line string) bool {
			return line == "beta"
		})
		if err != nil || !found {
			b.Fatalf("ScanBlobLinesAtRev = %v, %v", found, err)
		}
	}
}
