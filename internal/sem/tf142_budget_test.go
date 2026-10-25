package sem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The three tests below all pin the same property from different angles: a
// cancellation (or the caller's own deadline) has to be observable at the
// granularity where the work actually happens, not only at phase boundaries. A
// guard that runs between phases cannot stop work already inside one, so a
// cancelled run keeps going for whatever the current phase still has left.

// tf142FlatSymbolFile is a file whose parse is linear and cheap but which
// contributes many symbols, so it stresses the relation producer's iteration
// count without making the parse phase the dominant cost.
func tf142FlatSymbolFile(n int) string {
	var sb strings.Builder
	sb.WriteString("// flat symbol file\n")
	for i := range n {
		fmt.Fprintf(&sb, "function flatFn%d(a) {\n  return a + %d;\n}\n", i, i)
	}
	return sb.String()
}

// TestTF142DeadlineInterruptsInFlightFileParse: handing the context to the
// worker pipeline stops the pipeline from handing out NEW files, but a worker
// already inside one file runs to completion, and the pipeline's cleanup waits
// for every worker. The dominant per-file term is not tree-sitter (13-76ms on
// this input) but the entity walk, which is superlinear in nesting depth and
// used to observe no context at all: 1.6s at 2k nested functions, 8.9s at 4k,
// 36s at 8k, 3m35s at 20k. So the residual after a caller's deadline was not
// "roughly the 5s parser timeout" -- it was unbounded, because the 5s cap only
// covers the tree-sitter call.
func TestTF142DeadlineInterruptsInFlightFileParse(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "deep.js", nestedRelationBomb(4000))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "nested")

	const deadline = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()
	start := time.Now()
	_, err := BuildProviderSnapshotWithOptions(ctx, repo, "test", ProviderSnapshotOptions{})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a run past its caller's deadline must return context.DeadlineExceeded, got %v", err)
	}
	// The residual is one in-flight file's remaining work. Everything else --
	// dispatch, reduction, the relation phase -- is already guarded.
	const ceiling = 4 * time.Second
	t.Logf("deadline=%s elapsed=%s residual=%s", deadline, elapsed.Round(time.Millisecond), (elapsed - deadline).Round(time.Millisecond))
	if elapsed > ceiling {
		t.Fatalf("deadline %s was overrun to %s: the in-flight per-file parse/walk does not observe the context", deadline, elapsed)
	}
}

// TestTF142DeadlineStopsStructuralRelationProducer reproduces the finding at
// provider.go:1198. emitStructuralRelations and emitStructuralRelationsCompact
// had no stop predicate, so once the deadline was detected the sink returned
// immediately but the PRODUCER kept walking every remaining file and symbol -- a
// full O(symbol-count) pass after the run had already stopped.
//
// The assertion is on records produced, not on elapsed time. An earlier version
// of this test measured the residual with a stopwatch against a 3ms ceiling; it
// passed on macOS and Windows and failed on the shared ubuntu runner at 3.14ms
// (the producer HAD stopped -- one record emitted, then nothing -- the runner
// was simply slower than the 3ms bound). Elapsed time is not a property a
// shared CI runner can be asked to meet, and a flaky test is worse than no
// test. The invariant the fix actually installs is "the producer stops at the
// next symbol", and that is what is asserted here, deterministically, on any
// machine.
func TestTF142DeadlineStopsStructuralRelationProducer(t *testing.T) {
	t.Parallel()
	const files, perFile = 40, 2000
	fileRecords := make([]FileRecord, 0, files)
	compact := map[string][]structuralSymbol{}
	records := map[string][]SymbolRecord{}
	for i := range files {
		path := fmt.Sprintf("pkg%03d/flat.js", i)
		fileRecords = append(fileRecords, FileRecord{RecordType: "file", ID: "file:" + path, Path: path, Language: "JavaScript"})
		for j := range perFile {
			id := fmt.Sprintf("sym%03d_%05d", i, j)
			compact[path] = append(compact[path], structuralSymbol{ID: id, FilePath: path})
			records[path] = append(records[path], SymbolRecord{
				ID: id, Kind: "function", Name: fmt.Sprintf("flatFn%d", j), FilePath: path,
			})
		}
	}
	// Every symbol emits exactly one DEFINES here (no container), so a producer
	// that ignores its predicate emits files*perFile records after the stop.
	// The producer checks once per file and once per symbol, so the bound after
	// the flag flips is the current symbol's own records.
	const ceiling = 4
	producers := map[string]func(shouldStop func() bool, emit func(RelationRecord)){
		"emitStructuralRelations": func(shouldStop func() bool, emit func(RelationRecord)) {
			emitStructuralRelations("test", fileRecords, records, shouldStop, emit)
		},
		"emitStructuralRelationsCompact": func(shouldStop func() bool, emit func(RelationRecord)) {
			emitStructuralRelationsCompact("test", fileRecords, compact, shouldStop, emit)
		},
	}
	for name, run := range producers {
		t.Run(name, func(t *testing.T) {
			emitted := 0
			stop := false
			run(func() bool { return stop }, func(RelationRecord) {
				emitted++
				stop = true // the deadline fires at the very first record
			})
			if emitted == 0 {
				t.Fatal("fixture produced no relation records; the probe never armed")
			}
			if emitted > ceiling {
				t.Fatalf("the producer emitted %d records after the deadline was detected at the first one (%d symbols in the workspace): it does not observe the stop predicate",
					emitted, files*perFile)
			}
		})
	}
}

// TestTF142CancellationSkipsCoChangeSubprocess: fileChangesWithRelations runs
// a `git log` over the last 256 commits and returns a fully materialised slice,
// and its result used to be checked only after it returned, so a run cancelled
// during the relation phase still paid for the whole subprocess before it
// noticed.
func TestTF142CancellationSkipsCoChangeSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The probe needs a PATH-shadowing `git` shim, which is written as a
		// POSIX shell script; there is no portable equivalent on Windows.
		t.Skip("git shim is a POSIX shell script")
	}
	repo := t.TempDir()
	initRepo(t, repo)
	// Two files that change together, so FileCochanges returns a non-empty set
	// and the FILE_CHANGES_WITH stage has real work.
	for commit := range 3 {
		writeFile(t, repo, "auth.js", fmt.Sprintf("function validateToken(t) {\n  return Boolean(t) && %d > 0;\n}\n", commit))
		writeFile(t, repo, "session.js", fmt.Sprintf("function newSession(u) {\n  return {u: u, v: %d};\n}\n", commit))
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", fmt.Sprintf("change %d", commit))
	}

	const gitLogCost = 3 * time.Second
	shimDir := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	// Slow ONLY the co-change `git log`. The inventory's `ls-tree --name-only`
	// must stay fast, or the fixture would measure source preparation instead.
	shim := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "log" ]; then
  sleep %d
fi
exec %q "$@"
`, int(gitLogCost/time.Second), realGit)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var canceledAt time.Time
	err = StreamSnapshot(ctx, repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, func(record any) error {
		// Cancel at the first relation record, so the run is provably cancelled
		// by the time the co-change stage is reached.
		if _, ok := record.(RelationRecord); ok && canceledAt.IsZero() {
			cancel()
			canceledAt = time.Now()
		}
		return nil
	})
	if canceledAt.IsZero() {
		t.Fatal("fixture produced no relation records; the probe never armed")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled run must return context.Canceled, got %v", err)
	}
	residual := time.Since(canceledAt)
	t.Logf("git-log shim cost=%s residual after cancel=%s", gitLogCost, residual.Round(time.Millisecond))
	if residual >= gitLogCost {
		t.Fatalf("the co-change `git log` ran for %s after the run was cancelled: fileChangesWithRelations is entered without checking the context", residual)
	}
}
