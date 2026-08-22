package gitutil

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const directoryEntryProducerEnv = "ENTIRE_GRAPH_TEST_DIRECTORY_ENTRY_FIELDS"

const (
	directoryEntryProducerRecordBytesEnv = directoryEntryProducerEnv + "_RECORD_BYTES"
	directoryEntryProducerExtraNULEnv    = directoryEntryProducerEnv + "_EXTRA_NUL"
)

func TestDirectoryEntryScaleProducer(t *testing.T) {
	raw := os.Getenv(directoryEntryProducerEnv)
	if raw == "" {
		return
	}
	fields, err := strconv.Atoi(raw)
	if err != nil {
		os.Exit(2)
	}
	record := "vendor/file.o\x00"
	if rawRecordBytes := os.Getenv(directoryEntryProducerRecordBytesEnv); rawRecordBytes != "" {
		recordBytes, err := strconv.Atoi(rawRecordBytes)
		if err != nil || recordBytes < 1 || recordBytes > nestedIgnorePathMaxBytes+1 {
			os.Exit(2)
		}
		record = strings.Repeat("x", recordBytes-1) + "\x00"
	}
	writer := bufio.NewWriterSize(os.Stdout, 64<<10)
	for index := 0; index < fields; index++ {
		if _, err := writer.WriteString(record); err != nil {
			os.Exit(0)
		}
	}
	if os.Getenv(directoryEntryProducerExtraNULEnv) != "" {
		_ = writer.WriteByte(0)
	}
	if os.Getenv(directoryEntryProducerEnv+"_DIRS") != "" {
		_, _ = writer.WriteString("build/\x00dist/\x00build/\x00")
	}
	_ = writer.Flush()
	os.Exit(0)
}

func directoryEntryProducerCommand(t *testing.T, fields int, withDirs bool) *exec.Cmd {
	return directoryEntryProducerCommandWithRecordBytes(t, fields, len("vendor/file.o")+1, withDirs, false)
}

func directoryEntryProducerCommandWithRecordBytes(
	t *testing.T,
	fields int,
	recordBytes int,
	withDirs bool,
	extraNUL bool,
) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDirectoryEntryScaleProducer$")
	cmd.Env = append(
		cmd.Environ(),
		directoryEntryProducerEnv+"="+strconv.Itoa(fields),
		directoryEntryProducerRecordBytesEnv+"="+strconv.Itoa(recordBytes),
	)
	if withDirs {
		cmd.Env = append(cmd.Env, directoryEntryProducerEnv+"_DIRS=1")
	}
	if extraNUL {
		cmd.Env = append(cmd.Env, directoryEntryProducerExtraNULEnv+"=1")
	}
	return cmd
}

// TestStreamNULDirectoryEntriesHandlesLargeMixedOutput reproduces the trail
// finding on ListIgnoredWorktreeDirectoryEntries: `--directory` only
// collapses a directory git classifies as wholly ignored. A directory
// ignored only by a file-pattern rule (e.g. `*.o`) alongside other content is
// never collapsed, so an untrusted checkout with a build or dependency tree
// matched by such a pattern makes git print every one of those filenames
// individually — potentially millions of them, entirely attacker-controlled.
// The prior implementation ran that listing through `run`, which buffers the
// COMPLETE subprocess stdout into memory before splitNULDirectoryEntries
// ever discarded a single non-directory field.
//
// This drives a synthetic producer standing in for such a listing (one
// million non-directory fields plus two real directory entries) through
// visitWorktreeDirectoryEntryOutput directly, so the test exercises the production pipe
// + incremental-filter code path rather than only its output-format parsing.
// It pins two things a regression to the old buffer-then-filter approach
// would break: exactly the directory entries come back (no leaked filenames,
// no lost/duplicated directories), and processing a million fields does not
// take remotely as long as buffering and then linearly re-scanning that data
// would — the streaming/filtering must happen in the same pass.
func TestStreamNULDirectoryEntriesHandlesLargeMixedOutput(t *testing.T) {
	const fields = 1_000_000

	start := time.Now()
	var dirs []string
	err := visitWorktreeDirectoryEntryOutput(directoryEntryProducerCommand(t, fields, true), func(dir string) bool {
		dirs = append(dirs, dir)
		return true
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("visitWorktreeDirectoryEntryOutput: %v", err)
	}

	want := map[string]bool{"build/": true, "dist/": true}
	if len(dirs) != len(want) {
		t.Fatalf("dirs = %#v, want exactly %v (no leaked filenames, duplicate build/ deduped)", dirs, want)
	}
	for _, d := range dirs {
		if !want[d] {
			t.Fatalf("dirs contains unexpected entry %q (a pattern-ignored filename leaked through): %#v", d, dirs)
		}
	}

	// A generous ceiling: this is a fast local subprocess pipe read of ~13MB
	// with a filter that is O(1) work per field. Multiple seconds would
	// indicate a regression back to materializing the whole listing (or
	// worse, a quadratic dedup/append path) rather than filtering as it
	// streams.
	const budget = 5 * time.Second
	if elapsed > budget {
		t.Fatalf("visitWorktreeDirectoryEntryOutput took %s to process %d fields (budget %s): "+
			"this smells like the listing is being buffered/rescanned instead of streamed", elapsed, fields, budget)
	}
}

// TestStreamNULDirectoryEntriesTruncatesAPathologicalFieldCount is the
// narrowing direction the trail finding on ListIgnoredWorktreeDirectoryEntries
// (git.go:152) is about: the prior fix above bounds this call to the
// ORDINARY case (filtering as fields arrive instead of buffering), but a
// scanned repository that arranges for a file-pattern ignore matching FAR
// more entries than the sweep's own directory budget, with zero of them ever
// collapsing into a directory, still made the loop read every one of them
// before returning -- unbounded CPU and pipe I/O ahead of the caller's own
// budget, and unbounded ahead of anything this test's sibling above proves
// bounded. This drives a producer well past maxIgnoredDirectoryFields with NO
// real directory entries anywhere in the stream (unlike the sibling test,
// which places two after a mere 1,000,000) and asserts the call gives up --
// reporting errIgnoredListingTruncated -- rather than draining the full
// stream, in time comparable to the bounded case above rather than scaling
// with the producer's true size.
func TestStreamNULDirectoryEntriesTruncatesAPathologicalFieldCount(t *testing.T) {
	const fields = maxIgnoredDirectoryFields * 3

	start := time.Now()
	var dirs []string
	err := visitWorktreeDirectoryEntryOutput(directoryEntryProducerCommand(t, fields, false), func(dir string) bool {
		dirs = append(dirs, dir)
		return true
	})
	elapsed := time.Since(start)

	if !errors.Is(err, errIgnoredListingTruncated) {
		t.Fatalf("visitWorktreeDirectoryEntryOutput err = %v, want errIgnoredListingTruncated", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("a truncated listing returned %d dir(s), want none returned alongside the error", len(dirs))
	}
	// A generous ceiling well under how long fully producing and draining
	// 3x the field bound would take: this call must give up at the bound,
	// not after the producer finishes emitting fields ~3x past it.
	const budget = 10 * time.Second
	if elapsed > budget {
		t.Fatalf("visitWorktreeDirectoryEntryOutput took %s to give up on a %d-field pathological listing (budget %s): "+
			"it is reading past maxIgnoredDirectoryFields instead of stopping there", elapsed, fields, budget)
	}
}

func TestVisitWorktreeDirectoryEntryOutputAllowsExactRawFieldBound(t *testing.T) {
	var dirs []string
	err := visitWorktreeDirectoryEntryOutput(
		directoryEntryProducerCommand(t, maxIgnoredDirectoryFields, false),
		func(dir string) bool {
			dirs = append(dirs, dir)
			return true
		},
	)
	if err != nil {
		t.Fatalf("exactly %d raw fields were truncated: %v", maxIgnoredDirectoryFields, err)
	}
	if len(dirs) != 0 {
		t.Fatalf("pattern-only output produced directory entries: %v", dirs)
	}
}

func TestVisitWorktreeDirectoryEntryOutputAllowsExactRawByteBound(t *testing.T) {
	const recordBytes = nestedIgnorePathMaxBytes
	if maxIgnoredDirectoryBytes%recordBytes != 0 {
		t.Fatalf("test record size %d does not divide byte bound %d", recordBytes, maxIgnoredDirectoryBytes)
	}
	fields := maxIgnoredDirectoryBytes / recordBytes
	err := visitWorktreeDirectoryEntryOutput(
		directoryEntryProducerCommandWithRecordBytes(t, fields, recordBytes, false, false),
		func(string) bool { return true },
	)
	if err != nil {
		t.Fatalf("exactly %d raw bytes were truncated: %v", maxIgnoredDirectoryBytes, err)
	}
}

func TestVisitWorktreeDirectoryEntryOutputTruncatesOneBytePastRawByteBound(t *testing.T) {
	const recordBytes = nestedIgnorePathMaxBytes
	if maxIgnoredDirectoryBytes%recordBytes != 0 {
		t.Fatalf("test record size %d does not divide byte bound %d", recordBytes, maxIgnoredDirectoryBytes)
	}
	fields := maxIgnoredDirectoryBytes / recordBytes
	err := visitWorktreeDirectoryEntryOutput(
		directoryEntryProducerCommandWithRecordBytes(t, fields, recordBytes, false, true),
		func(string) bool { return true },
	)
	if !errors.Is(err, errIgnoredListingTruncated) {
		t.Fatalf("%d raw bytes err = %v, want errIgnoredListingTruncated", maxIgnoredDirectoryBytes+1, err)
	}
}

func TestListBoundedWorktreePathsAllowsExactRawFieldBound(t *testing.T) {
	paths, err := listBoundedWorktreePaths(
		directoryEntryProducerCommandWithRecordBytes(t, maxWorktreeListingFields, 2, false, false),
	)
	if err != nil {
		t.Fatalf("exactly %d worktree fields were truncated: %v", maxWorktreeListingFields, err)
	}
	if len(paths) != 1 || paths[0] != "x" {
		t.Fatalf("deduplicated paths = %q, want [x]", paths)
	}
}

func TestListBoundedWorktreePathsTruncatesOneFieldPastRawBound(t *testing.T) {
	_, err := listBoundedWorktreePaths(
		directoryEntryProducerCommandWithRecordBytes(t, maxWorktreeListingFields+1, 2, false, false),
	)
	if !errors.Is(err, ErrWorktreeListingTruncated) {
		t.Fatalf("%d worktree fields err = %v, want ErrWorktreeListingTruncated", maxWorktreeListingFields+1, err)
	}
}

func TestWorktreeListingBudgetAllowsExactBytesAndRejectsOneMore(t *testing.T) {
	budget := worktreeListingBudget{bytes: maxWorktreeListingBytes - 2}
	if !budget.admit("x") {
		t.Fatal("exact aggregate worktree byte bound was rejected")
	}
	if budget.admit("") {
		t.Fatal("one byte beyond aggregate worktree byte bound was accepted")
	}
}

func TestVisitWorktreePathOutputChargesRecordsBeforeVisitorFiltering(t *testing.T) {
	budget := worktreeListingBudget{fields: maxWorktreeListingFields - 1}
	visited := 0
	err := visitBoundedWorktreePathOutputWithBudget(
		directoryEntryProducerCommandWithRecordBytes(t, 2, 2, false, false),
		&budget,
		func(string) bool {
			visited++
			return true // Model a caller that discards every record after this callback.
		},
	)
	if !errors.Is(err, ErrWorktreeListingTruncated) {
		t.Fatalf("discarded raw records err = %v, want ErrWorktreeListingTruncated", err)
	}
	if visited != 1 {
		t.Fatalf("visitor saw %d records, want 1 before the fixed raw bound stopped the stream", visited)
	}
}
