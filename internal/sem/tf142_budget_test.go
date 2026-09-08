package sem

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The three tests below all pin the same property from different angles: the
// wall-clock ceiling has to be observable at the granularity where the work
// actually happens, not only at phase boundaries. A guard that runs between
// phases cannot stop work already inside one, so the advertised ceiling is
// overshot by whatever the current phase still has left to do.

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

// TestTF142BudgetInterruptsInFlightFileParse reproduces the finding at
// provider.go:1126. Handing workCtx to the worker pipeline stops the pipeline
// from handing out NEW files, but a worker already inside one file runs to
// completion, and the pipeline's cleanup waits for every worker. The dominant
// per-file term is not tree-sitter (13-76ms on this input) but the entity walk,
// which is superlinear in nesting depth and, before this change, observed no
// context at all: 1.6s at 2k nested functions, 8.9s at 4k, 36s at 8k, 3m35s at
// 20k. So the overshoot was not "roughly the 5s parser timeout" -- it was
// unbounded, because the 5s cap only covers the tree-sitter call.
func TestTF142BudgetInterruptsInFlightFileParse(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "deep.js", nestedRelationBomb(4000))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "nested")

	const budget = 500 * time.Millisecond
	start := time.Now()
	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", ProviderSnapshotOptions{MaxDuration: budget})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	// The residual is one in-flight file's remaining work. Everything else --
	// dispatch, reduction, the relation phase -- is already guarded.
	const ceiling = 4 * time.Second
	t.Logf("budget=%s elapsed=%s overshoot=%s", budget, elapsed.Round(time.Millisecond), (elapsed - budget).Round(time.Millisecond))
	if elapsed > ceiling {
		t.Fatalf("budget %s was overshot to %s: the in-flight per-file parse/walk does not observe the deadline", budget, elapsed)
	}
}

// TestTF142DeadlineStopsStructuralRelationProducer reproduces the finding at
// provider.go:1198. emitStructuralRelations and emitStructuralRelationsCompact
// had no stop predicate, so once the deadline was detected the sink returned
// immediately but the PRODUCER kept walking every remaining file and symbol -- a
// full O(symbol-count) pass charged entirely to the overshoot.
//
// The assertion is on records produced, not on elapsed time. An earlier version
// of this test measured the residual with a stopwatch against a 3ms ceiling; it
// passed on macOS and Windows and failed on the shared ubuntu runner at 3.14ms
// (the producer HAD stopped -- one record emitted, then nothing -- the runner
// was simply slower than the ceiling). A wall-clock bound is not a property a
// shared CI runner can be asked to meet, and a flaky test on the check that is
// supposed to prove the ceiling works is worse than no test. The invariant the
// fix actually installs is "the producer stops at the next symbol", and that is
// what is asserted here, deterministically, on any machine. The timed
// end-to-end measurement it replaces is recorded in the PR description.
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

// TestTF142BudgetSkipsCoChangeSubprocess reproduces the finding at
// provider.go:1266. fileChangesWithRelations was handed the PARENT ctx and its
// result was checked only after it returned, so a full `git log` over the last
// 256 commits ran even when the budget had already expired -- the whole
// subprocess duration was charged to the overshoot. The parent ctx is the load
// bearing part: workCtx was already expired, so passing workCtx would have made
// exec refuse to start git at all.
func TestTF142BudgetSkipsCoChangeSubprocess(t *testing.T) {
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
	// must stay fast: source preparation runs outside the budget clock, so
	// slowing it would move the deadline instead of testing it.
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

	const budget = 500 * time.Millisecond
	var sleptUntil time.Time
	err = StreamSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile:     ProfileFull,
		MaxDuration: budget,
	}, func(record any) error {
		// Burn past the deadline at the first relation record, so the budget is
		// provably expired by the time the co-change stage is reached. The sleep
		// is a full budget wide rather than "until start+budget" because the
		// clock starts after source preparation, not when StreamSnapshot is
		// entered -- so sleeping one whole budget from any point inside the
		// parse/relation phases is what guarantees expiry.
		if _, ok := record.(RelationRecord); ok && sleptUntil.IsZero() {
			time.Sleep(budget + 100*time.Millisecond)
			sleptUntil = time.Now()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	if sleptUntil.IsZero() {
		t.Fatal("fixture produced no relation records; the probe never armed")
	}
	residual := time.Since(sleptUntil)
	t.Logf("budget=%s git-log shim cost=%s residual after deadline=%s", budget, gitLogCost, residual.Round(time.Millisecond))
	if residual >= gitLogCost {
		t.Fatalf("the co-change `git log` ran for %s after the budget expired: fileChangesWithRelations is entered without checking the deadline and is handed the parent context", residual)
	}
}
