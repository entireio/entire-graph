package sem

import (
	"strconv"
	"strings"
	"testing"
)

func TestSearchTermPostingsCapsListsAndKeepsExactTotals(t *testing.T) {
	t.Parallel()
	postings := newSearchTermPostings()
	terms := []string{"round", "quotient"}
	for index := 0; index < searchNeedlePostingCap+5; index++ {
		postings.record("f"+strconv.Itoa(index)+".go", []bool{true, false}, terms)
	}
	postings.record("only.go", []bool{false, true}, terms)
	files, totals := postings.snapshot()
	if got := len(files["round"]); got != searchNeedlePostingCap {
		t.Fatalf("capped list length = %d, want %d", got, searchNeedlePostingCap)
	}
	if totals["round"] != searchNeedlePostingCap+5 {
		t.Fatalf("total = %d, want %d", totals["round"], searchNeedlePostingCap+5)
	}
	// The total is what tells a caller the list was truncated, so the two must disagree here.
	if totals["round"] == len(files["round"]) {
		t.Fatal("a truncated list must not look complete")
	}
	if len(files["quotient"]) != 1 || totals["quotient"] != 1 {
		t.Fatalf("rare term = %v/%d, want 1/1", files["quotient"], totals["quotient"])
	}
}

func TestSearchNeedleCandidatePathsPicksTheSmallestCompleteList(t *testing.T) {
	t.Parallel()
	index := &searchNeedleIndex{
		termFiles: map[string][]string{
			"bind":    {"a.go", "b.go", "c.go"},
			"default": {"a.go"},
		},
		termFileTotals: map[string]int{"bind": 3, "default": 1},
	}
	paths, ok := index.candidatePathsFromPostings("default_bind")
	if !ok {
		t.Fatal("no candidate paths for a needle containing two indexed terms")
	}
	if len(paths) != 1 || paths[0] != "a.go" {
		t.Fatalf("paths = %v, want the rarer term's list", paths)
	}
}

func TestSearchNeedleCandidatePathsRejectsUnusableRoutes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		index  *searchNeedleIndex
		needle string
	}{
		{
			name: "truncated list is not a superset",
			index: &searchNeedleIndex{
				termFiles:      map[string][]string{"bind": {"a.go"}},
				termFileTotals: map[string]int{"bind": 40},
			},
			needle: "default_bind",
		},
		{
			name: "term must be inside the needle, not around it",
			index: &searchNeedleIndex{
				termFiles:      map[string][]string{"default_bind": {"a.go"}},
				termFileTotals: map[string]int{"default_bind": 1},
			},
			needle: "bind",
		},
		{
			name:   "no route at all",
			index:  &searchNeedleIndex{},
			needle: "bind",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := testCase.index.candidatePathsFromPostings(testCase.needle); ok {
				t.Fatal("an unusable route must not be offered")
			}
		})
	}
}

func TestSearchNeedleCandidatePathsFallsBackToGrepThenCorpusThenCaller(t *testing.T) {
	t.Parallel()
	grepped := &searchNeedleIndex{grep: func(string) ([]string, bool) { return []string{"g.go"}, true }}
	if paths, ok := grepped.candidatePaths("Needle", []string{"ignored.go"}); !ok || paths[0] != "g.go" {
		t.Fatalf("grep route = %v/%v", paths, ok)
	}
	corpus := &searchNeedleIndex{corpus: []string{"c.go"}}
	if paths, ok := corpus.candidatePaths("Needle", []string{"ignored.go"}); !ok || paths[0] != "c.go" {
		t.Fatalf("corpus route = %v/%v", paths, ok)
	}
	// A corpus too large to read is not a route: reading it would blow the budget and the scan would
	// come back incomplete, which the callers must refuse anyway.
	huge := make([]string, searchNeedleCorpusReadCap+1)
	for index := range huge {
		huge[index] = "f" + strconv.Itoa(index) + ".go"
	}
	fallback := &searchNeedleIndex{corpus: huge}
	paths, ok := fallback.candidatePaths("Needle", []string{"b.go", "a.go", "a.go"})
	if !ok || strings.Join(paths, ",") != "a.go,b.go" {
		t.Fatalf("caller fallback = %v/%v, want deduped and sorted", paths, ok)
	}
}

func TestSearchNeedleLocateReportsExactTotalsAndSamplesPerFile(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"a.go": "NEEDLE\nx\nNEEDLE\nNEEDLE\n",
		"b.go": "y\nNEEDLE\n",
		"c.go": "nothing here\n",
	}
	index := &searchNeedleIndex{read: func(path string) (string, bool) {
		content, ok := files[path]
		return content, ok
	}}
	scan := index.locate("NEEDLE", []string{"a.go", "b.go", "c.go"}, 2, 10)
	if !scan.complete {
		t.Fatal("a scan inside its budget must report complete")
	}
	if scan.occurrences != 4 || scan.files != 2 {
		t.Fatalf("occurrences/files = %d/%d, want 4/2", scan.occurrences, scan.files)
	}
	if got := strings.Join(scan.matchingPaths, ","); got != "a.go,b.go" {
		t.Fatalf("matching provenance paths = %q, want a.go,b.go", got)
	}
	// Three occurrences in a.go, sampled at two, so the sample spans files rather than one file.
	if len(scan.hits) != 3 {
		t.Fatalf("hits = %d, want 3 (2 from a.go + 1 from b.go)", len(scan.hits))
	}
	if scan.hits[0].line != 1 || scan.hits[1].line != 3 || scan.hits[2].filePath != "b.go" {
		t.Fatalf("hits = %+v", scan.hits)
	}
}

func TestSearchNeedleLocateReportsIncompleteWhenTheBudgetRunsOut(t *testing.T) {
	t.Parallel()
	index := &searchNeedleIndex{
		read:  func(string) (string, bool) { return "NEEDLE\n", true },
		reads: searchNeedleMaxReads,
	}
	scan := index.locate("NEEDLE", []string{"a.go"}, 2, 10)
	if scan.complete {
		t.Fatal("an exhausted budget must be reported as incomplete, not as zero hits")
	}
}
