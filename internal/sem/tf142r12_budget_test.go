package sem

import (
	"fmt"
	"testing"
)

// Round twelve. Three open review findings: relation producers that build a
// complete slice with no interruption inside them, a whole-corpus index build
// that runs before relation generation even starts, and a per-file
// post-processing pass that can outrun the post-parse stop check that used to
// be the only guard around it.

// tf142r12DockerfileFixture returns recordsByFile/readContent for n trivial
// Dockerfiles, each naming a FROM stage — the shape
// dockerfileResourceDependsOnRelations iterates one path at a time to build.
func tf142r12DockerfileFixture(n int) (map[string][]SymbolRecord, contentReader) {
	recordsByFile := map[string][]SymbolRecord{}
	content := map[string]string{}
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("svc%d/Dockerfile", i)
		recordsByFile[path] = []SymbolRecord{
			{
				RecordType: "symbol", ID: fmt.Sprintf("dockerfile:%s:build", path), Kind: "stage", Language: "Dockerfile",
				FilePath: path, Name: "build", QualifiedName: "build", StartLine: 1, EndLine: 1,
			},
			{
				RecordType: "symbol", ID: fmt.Sprintf("dockerfile:%s:final", path), Kind: "stage", Language: "Dockerfile",
				FilePath: path, Name: "final", QualifiedName: "final", StartLine: 2, EndLine: 3,
			},
		}
		content[path] = "FROM golang:1.22 AS build\nFROM alpine AS final\nCOPY --from=build /app /app\n"
	}
	read := func(path string) (string, bool) {
		c, ok := content[path]
		return c, ok
	}
	return recordsByFile, read
}

// TestTF142R12ResourceDependsOnProducersStopMidLoop is the narrowing
// direction: each per-format producer resourceDependsOnRelations dispatches to
// now takes stop and checks it at the top of its own per-path loop, so an
// expired budget halts a single producer partway through its own path list
// instead of only being observed between producers (or not at all, since
// resourceDependsOnRelations previously took no stop parameter at all).
func TestTF142R12ResourceDependsOnProducersStopMidLoop(t *testing.T) {
	t.Parallel()
	recordsByFile, read := tf142r12DockerfileFixture(200)

	visited := 0
	stop := func() bool { visited++; return visited > 1 }
	got := dockerfileResourceDependsOnRelations(recordsByFile, read, stop)
	if visited > 3 {
		t.Fatalf("dockerfileResourceDependsOnRelations kept iterating past its stop predicate: %d poll(s) for %d path(s)", visited, len(recordsByFile))
	}
	if len(got) == len(recordsByFile) {
		t.Fatalf("dockerfileResourceDependsOnRelations ran to completion despite a stop predicate firing on the second path")
	}

	if got := resourceDependsOnRelations(recordsByFile, read, func() bool { return true }); len(got) != 0 {
		t.Fatalf("resourceDependsOnRelations ran a sub-producer to completion on an already-expired budget: got %d relation(s)", len(got))
	}
}

// TestTF142R12ResourceDependsOnProducersUnbudgetedAreUnchanged is the
// widening direction: a nil stop predicate must reproduce the exact prior
// output.
func TestTF142R12ResourceDependsOnProducersUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	recordsByFile, read := tf142r12DockerfileFixture(3)
	got := dockerfileResourceDependsOnRelations(recordsByFile, read, nil)
	if len(got) != 3 {
		t.Fatalf("a nil stop predicate changed dockerfileResourceDependsOnRelations output: got %d relation(s), want 3", len(got))
	}
}

// tf142r12SymbolIndexFixture returns n files with one symbol each, the shape
// recordIndexes walks to build its two id lookups.
func tf142r12SymbolIndexFixture(n int) ([]FileRecord, map[string][]SymbolRecord) {
	files := make([]FileRecord, 0, n)
	recordsByFile := map[string][]SymbolRecord{}
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("pkg%d.go", i)
		files = append(files, FileRecord{RecordType: "file", ID: "file:" + path, Path: path})
		recordsByFile[path] = []SymbolRecord{{RecordType: "symbol", ID: "sym:" + path, FilePath: path}}
	}
	return files, recordsByFile
}

// TestTF142R12RecordIndexesStopsBuildingOnAnExpiredBudget reproduces the
// finding that recordIndexes builds its whole-corpus symbol and file id
// lookups with no stop check at all, so a deadline expiring during this
// whole-symbol pass (which runs BEFORE relation generation starts) was not
// observed until relationsShouldStop was next polled inside forEachRelation --
// charging the entire indexing pass to the overshoot on a large full-profile
// snapshot.
func TestTF142R12RecordIndexesStopsBuildingOnAnExpiredBudget(t *testing.T) {
	t.Parallel()
	files, recordsByFile := tf142r12SymbolIndexFixture(4096)

	symbolsByID, _ := recordIndexes(files, recordsByFile, func() bool { return true })
	if len(symbolsByID) == len(recordsByFile) {
		t.Fatalf("recordIndexes built the full %d-entry symbol index on an already-expired budget", len(recordsByFile))
	}
}

// TestTF142R12RecordIndexesUnbudgetedAreUnchanged is the widening direction: a
// nil stop predicate must still index every symbol and file.
func TestTF142R12RecordIndexesUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	files, recordsByFile := tf142r12SymbolIndexFixture(8)
	symbolsByID, filesByID := recordIndexes(files, recordsByFile, nil)
	if len(symbolsByID) != 8 || len(filesByID) != 8 {
		t.Fatalf("a nil stop predicate changed recordIndexes output: got %d symbols / %d files, want 8/8", len(symbolsByID), len(filesByID))
	}
}

// tf142r12ManySymbols returns n symbols, none of which look like a tool
// handler, spread one line apart so symbolBlockFromLines has real (if small)
// per-symbol work to do.
func tf142r12ManySymbols(n int) []SymbolRecord {
	symbols := make([]SymbolRecord, 0, n)
	for i := 0; i < n; i++ {
		symbols = append(symbols, SymbolRecord{
			RecordType: "symbol", Kind: "function", Language: "Go",
			FilePath: "pkg.go", Name: fmt.Sprintf("Fn%d", i), QualifiedName: fmt.Sprintf("Fn%d", i),
			StartLine: i + 1, EndLine: i + 1,
		})
	}
	return symbols
}

// TestTF142R12SyntheticBoundarySymbolsStopsMidScan reproduces the
// provider_parallel.go finding: the post-parse stop check ran once, before
// entitySymbols and syntheticBoundarySymbols, and neither of those was
// checked again -- so a deadline expiring during syntheticBoundarySymbols'
// rescan-per-symbol loop (superlinear in a file's symbol count) could not be
// caught until the whole pass finished. It now polls stop inside that loop
// and reports truncation so the caller drops the file atomically rather than
// emit entity symbols with a silently incomplete synthetic set.
func TestTF142R12SyntheticBoundarySymbolsStopsMidScan(t *testing.T) {
	t.Parallel()
	symbols := tf142r12ManySymbols(4096)
	content := ""
	for i := 0; i < len(symbols); i++ {
		content += fmt.Sprintf("func Fn%d() {}\n", i)
	}

	visited := 0
	stop := func() bool { visited++; return visited > 1 }
	got, truncated := syntheticBoundarySymbols("gh/example/repo", "pkg.go", "Go", content, symbols, stop)
	if !truncated {
		t.Fatal("syntheticBoundarySymbols did not report truncation for a stop predicate that fired mid-scan")
	}
	if got != nil {
		t.Fatalf("a truncated syntheticBoundarySymbols call returned %d symbol(s), want none: the caller must drop the file atomically, not append a partial synthetic list", len(got))
	}
	if visited > 3 {
		t.Fatalf("syntheticBoundarySymbols kept scanning past its stop predicate: %d poll(s) for %d symbol(s)", visited, len(symbols))
	}
}

// TestTF142R12SyntheticBoundarySymbolsUnbudgetedAreUnchanged is the widening
// direction: a nil stop predicate must reproduce the exact prior output,
// including for a file whose route/tool/workflow boundaries actually match.
func TestTF142R12SyntheticBoundarySymbolsUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	symbol := SymbolRecord{
		RecordType: "symbol", Kind: "function", Language: "Go",
		FilePath: "pkg.go", Name: "Tool", QualifiedName: "Tool",
		StartLine: 1, EndLine: 3,
	}
	content := "func Tool(schema string) {\n  execute(schema)\n}\n"
	got, truncated := syntheticBoundarySymbols("gh/example/repo", "pkg.go", "Go", content, []SymbolRecord{symbol}, nil)
	if truncated {
		t.Fatal("a nil stop predicate reported truncation")
	}
	if len(got) != 1 || got[0].Kind != "tool" {
		t.Fatalf("a nil stop predicate changed syntheticBoundarySymbols output: got %#v", got)
	}
}
