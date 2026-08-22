package sem

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Round thirteen. Three open review findings surfaced after round twelve
// pushed: the fast C/C++ parser's two forward line scans still ran to EOF
// with no stop check of their own, a populated early return between the
// synchronous file read and the post-parse check went unbudgeted, and the
// Express/Fastify cross-file router producer materialized its whole result
// before the caller's per-relation stop check ever ran.

// tf142r13UnterminatedCFamilySource returns a C source whose first typedef
// never closes with a `;` or `}`, followed by n filler lines. Every call this
// forces into fastCFamilyStatementEnd must scan to EOF absent a stop check,
// which is exactly the unbounded-scan shape the finding describes.
func tf142r13UnterminatedCFamilySource(n int) string {
	var b strings.Builder
	b.WriteString("typedef struct {\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  int field%d;\n", i)
	}
	// Deliberately no closing `} Name;` — the statement never terminates.
	return b.String()
}

// TestTF142R13FastCFamilyEntitiesStopsMidScan reproduces the finding on
// fastCFamilyStatementEnd/fastCFamilyBraceEnd: an unterminated declaration
// made each one scan to end-of-file with no stop check of its own, even
// though fastCFamilyEntities' outer per-line loops already poll one.
func TestTF142R13FastCFamilyEntitiesStopsMidScan(t *testing.T) {
	t.Parallel()
	content := tf142r13UnterminatedCFamilySource(20000)

	visited := 0
	stop := func() bool { visited++; return visited > 5 }
	_ = fastCFamilyEntities("big.c", content, "C", stop)
	if visited > 20 {
		t.Fatalf("fastCFamilyEntities/fastCFamilyStatementEnd kept scanning past its stop predicate: %d poll(s) for a %d-line unterminated declaration", visited, 20001)
	}
}

// TestTF142R13FastCFamilyEntitiesUnbudgetedAreUnchanged is the widening
// direction: a nil stop predicate must reproduce the exact prior output.
func TestTF142R13FastCFamilyEntitiesUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	content := "int add(int a, int b) {\n  return a + b;\n}\n"
	got := fastCFamilyEntities("small.c", content, "C", nil)
	if len(got) != 1 || got[0].Name != "add" {
		t.Fatalf("a nil stop predicate changed fastCFamilyEntities output: got %#v", got)
	}
}

// TestTF142R13ProcessProviderFileStopsBetweenReadAndParse reproduces the
// provider_parallel.go finding: a budget that expires WHILE sc.read is in
// flight (the one synchronous step processProviderFile cannot interrupt) was
// only caught at the post-parse check, so every populated early return in
// between — E_FILE_TOO_LARGE, E_MINIFIED, the oversize branch — still ran and
// still emitted a file record after the ceiling. The read function here
// cancels the context itself, reproducing "expired during the read" without
// a real clock race.
func TestTF142R13ProcessProviderFileStopsBetweenReadAndParse(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	minified := strings.Repeat("x", 8000) + "\n"
	sc := sourceContext{
		key: "gh/example/repo",
		read: func(path string) (string, bool) {
			// The budget expires while this synchronous read is "in flight".
			cancel()
			return minified, true
		},
	}
	gate := budgetGate{work: ctx, now: time.Now}
	spec := resolveProfile(ProfileFast)

	result := processProviderFile(ctx, gate, sc, spec, 0, 0, "pkg.js")
	if result.file != nil {
		t.Fatalf("processProviderFile emitted a file record for a read that expired the budget in flight: %+v", result.file)
	}
	if len(result.failures) != 0 {
		t.Fatalf("processProviderFile emitted failures for a budget-truncated file, want none (silent drop like the post-parse check): %+v", result.failures)
	}
}

// TestTF142R13ProcessProviderFileUnbudgetedStillClassifiesMinified is the
// widening direction: with no budget in play, a minified file must still be
// recorded with E_MINIFIED exactly as before.
func TestTF142R13ProcessProviderFileUnbudgetedStillClassifiesMinified(t *testing.T) {
	t.Parallel()
	minified := strings.Repeat("x", 8000) + "\n"
	sc := sourceContext{
		key:  "gh/example/repo",
		read: func(path string) (string, bool) { return minified, true },
	}
	gate := budgetGate{work: context.Background(), now: time.Now}
	spec := resolveProfile(ProfileFast)

	result := processProviderFile(context.Background(), gate, sc, spec, 0, 0, "pkg.js")
	if result.file == nil {
		t.Fatal("processProviderFile dropped an unbudgeted minified file entirely, want a file record with E_MINIFIED")
	}
	var sawMinified bool
	for _, f := range result.failures {
		if f.Code == "E_MINIFIED" {
			sawMinified = true
		}
	}
	if !sawMinified {
		t.Fatalf("processProviderFile did not classify an unbudgeted minified file as E_MINIFIED: %+v", result.failures)
	}
}

// tf142r13ExpressRouterFixture returns n Express route files, each its own
// router mounted at its own prefix — the shape crossFileExpressRouterRelations
// scans once per file and then joins in a second, nested pass.
func tf142r13ExpressRouterFixture(n int) ([]FileRecord, map[string][]SymbolRecord, contentReader) {
	files := make([]FileRecord, 0, n)
	recordsByFile := map[string][]SymbolRecord{}
	content := map[string]string{}
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("routes/svc%d.js", i)
		files = append(files, FileRecord{RecordType: "file", ID: "file:" + path, Path: path})
		recordsByFile[path] = []SymbolRecord{
			{RecordType: "symbol", ID: "sym:" + path + ":handler", Kind: "function", Language: "JavaScript",
				FilePath: path, Name: "handler", QualifiedName: "handler", StartLine: 3, EndLine: 5},
		}
		content[path] = fmt.Sprintf("const router = require('express').Router();\n"+
			"app.use('/svc%d', router);\n"+
			"router.get('/x', handler);\n"+
			"function handler(req, res) {}\n", i)
	}
	read := func(path string) (string, bool) {
		c, ok := content[path]
		return c, ok
	}
	return files, recordsByFile, read
}

// TestTF142R13CrossFileExpressRouterRelationsStopsMidScan reproduces the
// finding: crossFileExpressRouterRelations took no stop predicate at all, so
// its first whole-corpus file-content scan and its second nested mount/route
// join both ran to completion regardless of the caller's per-relation stop
// check on the slice it returned.
func TestTF142R13CrossFileExpressRouterRelationsStopsMidScan(t *testing.T) {
	t.Parallel()
	files, recordsByFile, read := tf142r13ExpressRouterFixture(500)
	knownFiles := map[string]bool{}
	for _, f := range files {
		knownFiles[f.Path] = true
	}

	visited := 0
	stop := func() bool { visited++; return visited > 2 }
	got := crossFileExpressRouterRelations(files, recordsByFile, read, knownFiles, stop)
	if len(got) == len(files) {
		t.Fatalf("crossFileExpressRouterRelations ran to completion despite a stop predicate firing early: got %d relation(s) for %d file(s)", len(got), len(files))
	}
}

// TestTF142R13PythonIncludeRouterRelationsStopsMidScan is the sibling case for
// the Python include_router producer, which shares the same
// scan-then-nested-join shape.
func TestTF142R13PythonIncludeRouterRelationsStopsMidScan(t *testing.T) {
	t.Parallel()
	recordsByFile := map[string][]SymbolRecord{}
	content := map[string]string{}
	files := make([]FileRecord, 0, 500)
	for i := 0; i < 500; i++ {
		path := fmt.Sprintf("routes/svc%d.py", i)
		files = append(files, FileRecord{RecordType: "file", ID: "file:" + path, Path: path})
		content[path] = fmt.Sprintf("router = APIRouter()\napp.include_router(router, prefix='/svc%d')\n"+
			"@router.get('/x')\ndef handler(): ...\n", i)
	}
	read := func(path string) (string, bool) {
		c, ok := content[path]
		return c, ok
	}
	knownFiles := map[string]bool{}
	for _, f := range files {
		knownFiles[f.Path] = true
	}

	visited := 0
	stop := func() bool { visited++; return visited > 2 }
	got := pythonIncludeRouterRelations(files, recordsByFile, read, knownFiles, stop)
	if len(got) == len(files) {
		t.Fatalf("pythonIncludeRouterRelations ran to completion despite a stop predicate firing early: got %d relation(s) for %d file(s)", len(got), len(files))
	}
}

// TestTF142R13CrossFileExpressRouterRelationsUnbudgetedAreUnchanged is the
// widening direction: a nil stop predicate must still resolve a route through
// a local router mount exactly as before.
func TestTF142R13CrossFileExpressRouterRelationsUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	files, recordsByFile, read := tf142r13ExpressRouterFixture(1)
	knownFiles := map[string]bool{"routes/svc0.js": true}
	got := crossFileExpressRouterRelations(files, recordsByFile, read, knownFiles, nil)
	if len(got) != 1 {
		t.Fatalf("a nil stop predicate changed crossFileExpressRouterRelations output: got %d relation(s), want 1", len(got))
	}
}
