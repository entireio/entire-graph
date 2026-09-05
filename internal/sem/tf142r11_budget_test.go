package sem

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// Round eleven. Three open review findings from the same class as prior
// rounds: work that runs past the point the wall-clock budget expired because
// the guard checked the wrong place, or checked two unrelated places
// independently.

// tf142r11TestRelationsFixture builds one test symbol ("TestFoo") plus
// `candidates` same-named non-test candidate symbols ("Foo" declared in
// `candidates` different files), which is exactly the shape
// resolveTestSubject's candidate scan is quadratic on: a common subject name
// in a large repository resolves against every symbol sharing it.
func tf142r11TestRelationsFixture(candidates int) (map[string][]SymbolRecord, map[string][]SymbolRecord) {
	test := SymbolRecord{
		RecordType: "symbol",
		ID:         "repo:Go:a_test.go:function:TestFoo",
		Kind:       "function",
		Name:       "TestFoo",
		FilePath:   "a_test.go",
	}
	recordsByFile := map[string][]SymbolRecord{"a_test.go": {test}}
	var subjects []SymbolRecord
	for i := 0; i < candidates; i++ {
		subjects = append(subjects, SymbolRecord{
			RecordType: "symbol",
			ID:         fmt.Sprintf("repo:Go:pkg%d.go:function:Foo", i),
			Kind:       "function",
			Name:       "Foo",
			FilePath:   fmt.Sprintf("pkg%d.go", i),
		})
	}
	symbolsByShortName := map[string][]SymbolRecord{"Foo": subjects}
	return recordsByFile, symbolsByShortName
}

// TestTF142R11TestRelationsStopsWhenTheBudgetIsGone is the narrowing
// direction of the provider.go:4056 finding: testRelations materializes its
// whole result slice before returning, and resolveTestSubject can itself scan
// a large same-name candidate set per call, so an already-expired budget must
// still do zero work rather than build the whole TESTS edge set (or even scan
// the whole candidate list for one symbol) before any caller-side check ever
// runs.
func TestTF142R11TestRelationsStopsWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()
	recordsByFile, symbolsByShortName := tf142r11TestRelationsFixture(64)
	expired := func() bool { return true }

	if got := testRelations(expired, recordsByFile, symbolsByShortName, nil); len(got) != 0 {
		t.Fatalf("testRelations ran to completion on an expired budget: built %d relation(s)", len(got))
	}

	visited := 0
	stop := func() bool { visited++; return visited > 1 }
	if _, _, ok := resolveTestSubject(stop, "Foo", SymbolRecord{ID: "test"}, symbolsByShortName["Foo"], nil); ok {
		t.Fatal("resolveTestSubject resolved a subject after its stop predicate fired")
	}
	if visited > 3 {
		t.Fatalf("resolveTestSubject kept scanning its candidate list after stop fired: %d poll(s) for %d candidate(s)", visited, len(symbolsByShortName["Foo"]))
	}
}

// TestTF142R11TestRelationsUnbudgetedAreUnchanged is the widening direction:
// a nil predicate (the unbudgeted path forEachRelation uses when no
// MaxDuration is set) must still resolve the TESTS edge.
func TestTF142R11TestRelationsUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	recordsByFile, symbolsByShortName := tf142r11TestRelationsFixture(1)
	got := testRelations(nil, recordsByFile, symbolsByShortName, nil)
	if len(got) != 1 {
		t.Fatalf("a nil predicate changed testRelations output: want 1 relation, got %d", len(got))
	}
	if got[0].Type != "TESTS" || got[0].ToID != "repo:Go:pkg0.go:function:Foo" {
		t.Fatalf("unexpected relation: %#v", got[0])
	}
}

// TestTF142R11RegistrationAliasScanStopsIteratingNotJustReading strengthens
// round eight's coverage of the provider.go:1165 finding: a gated reader that
// merely refuses reads is not enough, because a refused read still
// `continue`s the loop. This proves the loop itself breaks on the predicate
// -- stop is polled once per path and the loop halts the iteration after it
// first reports true, rather than being polled 200 times for 200 paths.
func TestTF142R11RegistrationAliasScanStopsIteratingNotJustReading(t *testing.T) {
	t.Parallel()
	paths := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		paths = append(paths, fmt.Sprintf("commands/cmd%d.json", i))
	}
	read := func(string) (string, bool) { return `{"function":"handler"}`, true }

	visited := 0
	stop := func() bool { visited++; return visited > 1 }
	collectRegistrationAliases(stop, paths, read)
	if visited > 3 {
		t.Fatalf("collectRegistrationAliases kept iterating past its stop predicate: %d poll(s) for %d path(s)", visited, len(paths))
	}
}

// TestTF142R11FallbackEntitiesStopWhenTheBudgetIsGone is the narrowing
// direction of the parser.go:269 finding: fallbackEntities received no stop
// predicate at all, so a deadline expiring during a maximum-sized Markdown,
// HTML, XML or JSON file could not interrupt that single linear pass. Each
// per-format extractor now polls stop the same way the cached selective
// derivation's per-record loops do.
func TestTF142R11FallbackEntitiesStopWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()
	expired := func() bool { return true }

	if got := markdownEntities("# Heading\n\n```go\ncode\n```\n", expired); len(got) != 0 {
		t.Fatalf("markdownEntities ran on an already-expired budget: got %d entities", len(got))
	}
	if got := jsonLikeEntities("{\n  \"a\": 1,\n  \"b\": 2\n}\n", expired); len(got) != 0 {
		t.Fatalf("jsonLikeEntities ran on an already-expired budget: got %d entities", len(got))
	}
	if got := xmlEntities("<a>\n<b/>\n</a>\n", expired); len(got) != 0 {
		t.Fatalf("xmlEntities ran on an already-expired budget: got %d entities", len(got))
	}
	if got := cssEntities(".a {\n}\n.b {\n}\n", expired); len(got) != 0 {
		t.Fatalf("cssEntities ran on an already-expired budget: got %d entities", len(got))
	}
	// htmlEntities always emits one unconditional document entity before its
	// budgeted per-line id scan; the invariant is that the scan itself does
	// not run, not that the slice is empty.
	if got := htmlEntities("index.html", "<div id=\"a\"></div>\n<div id=\"b\"></div>\n", expired); len(got) != 1 {
		t.Fatalf("htmlEntities ran its id scan on an already-expired budget: got %d entities, want the 1 unconditional document entity", len(got))
	}
	if got := fallbackEntities("index.html", "<div id=\"a\"></div>\n", "HTML", expired); len(got) != 1 {
		t.Fatalf("fallbackEntities did not thread stop into the HTML extractor: got %d entities", len(got))
	}
}

// TestTF142R11FallbackEntitiesUnbudgetedAreUnchanged is the widening
// direction: a nil predicate must reproduce the exact prior output for every
// format that gained one.
func TestTF142R11FallbackEntitiesUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	if got := markdownEntities("# Heading\n\n```go\ncode\n```\n", nil); len(got) != 3 {
		t.Fatalf("nil predicate changed markdownEntities output: got %d entities, want 3 (heading, opening fence, closing fence)", len(got))
	}
	if got := jsonLikeEntities("{\n  \"a\": 1,\n  \"b\": 2\n}\n", nil); len(got) != 2 {
		t.Fatalf("nil predicate changed jsonLikeEntities output: got %d entities, want 2", len(got))
	}
	if got := htmlEntities("index.html", "<div id=\"a\"></div>\n<div id=\"b\"></div>\n", nil); len(got) != 3 {
		t.Fatalf("nil predicate changed htmlEntities output: got %d entities, want 3 (1 document + 2 ids)", len(got))
	}
}

// tf142r11LaggingClockAfterSecondPoll returns the real time for the first two
// polls and a time far past the deadline on every later one. The first poll
// is options.now() deriving the budget deadline; the second is the very first
// gate.expired() check the derivation makes. Pairing that with a live-looking
// deadline reproduces the exact clock transition the search_cache.go:668
// finding is about: the budget goes from live to gone strictly BETWEEN the
// point that used to be the Files loop's own index-0 check and the point that
// used to be the Symbols loop's own, independent index-0 check.
func tf142r11LaggingClockAfterSecondPoll() func() time.Time {
	var polls atomic.Int64
	base := time.Now()
	return func() time.Time {
		if polls.Add(1) <= 2 {
			return base
		}
		return base.Add(2 * time.Hour)
	}
}

func tf142r11SelectiveRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "a.js", "function alpha(x) {\n  return x + 1;\n}\n")
	writeFile(t, repo, "b.js", "function beta(y) {\n  return y + 2;\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// TestTF142R11SelectiveDerivationKeepsFilesAndSymbolsAtomic reproduces the
// search_cache.go:668 finding: filtering full.Files and full.Symbols in two
// independent index-based loops let a budget that went live-to-gone strictly
// BETWEEN them retain a file's FileRecord while dropping every one of its
// SymbolRecords, because each loop polled the budget independently at its own
// index 0. The cold (non-cached) path never produces that state: a file it
// cannot finish in budget is dropped whole. The fix walks both slices in
// lockstep so a file crosses the truncation boundary whole here too.
func TestTF142R11SelectiveDerivationKeepsFilesAndSymbolsAtomic(t *testing.T) {
	repo := tf142r11SelectiveRepo(t)
	cacheDir := t.TempDir()

	if _, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir, false); err != nil {
		t.Fatalf("warm the complete snapshot: %v", err)
	}

	selective := ProviderSnapshotOptions{
		Profile:     ProfileFull,
		OnlyFiles:   []string{"a.js", "b.js"},
		MaxDuration: time.Hour,
		nowFn:       tf142r11LaggingClockAfterSecondPoll(),
	}
	snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", selective, cacheDir, false)
	if err != nil {
		t.Fatalf("a budget going live-to-gone mid-derivation must truncate, not fail: %v", err)
	}
	if len(snapshot.Files) > 0 && len(snapshot.Symbols) == 0 {
		t.Fatalf("retained %d file(s) with zero symbols: a file survived the Files filter while its symbols were dropped by a separately-polled Symbols filter", len(snapshot.Files))
	}
	for _, file := range snapshot.Files {
		found := false
		for _, symbol := range snapshot.Symbols {
			if symbol.FilePath == file.Path {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("file %q was retained but none of its symbols were: files and symbols were filtered inconsistently across the truncation boundary", file.Path)
		}
	}
}
