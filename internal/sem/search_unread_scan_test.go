package sem

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// searchTestScanFiles scans one in-memory file, path "f", through searchUnreadFiles.scan with
// windows of window bytes, recording the largest read the scan asks for and the bytes it reads
// in all.
func searchTestScanFiles(content string, window int, largest, total *int) searchUnreadFiles {
	return searchTestScanTree(map[string]string{"f": content}, window, largest, total)
}

// searchTestScanTree is searchTestScanFiles over several in-memory files.
func searchTestScanTree(tree map[string]string, window int, largest, total *int) searchUnreadFiles {
	return searchUnreadFiles{
		scanBytes: window,
		open: func(filePath string) (io.ReadCloser, int64, bool) {
			content, ok := tree[filePath]
			if !ok {
				return nil, 0, false
			}
			return io.NopCloser(&searchTestReadRecorder{reader: strings.NewReader(content), largest: largest, total: total}), int64(len(content)), true
		},
	}
}

type searchTestReadRecorder struct {
	reader         io.Reader
	largest, total *int
}

func (recorder *searchTestReadRecorder) Read(p []byte) (int, error) {
	if recorder.largest != nil {
		*recorder.largest = max(*recorder.largest, len(p))
	}
	n, err := recorder.reader.Read(p)
	if recorder.total != nil {
		*recorder.total += n
	}
	return n, err
}

// searchTestScanOnce runs one scan of "f" for q in a pass of its own, and reports whether it
// marked the file's terms in result.
func searchTestScanOnce(files searchUnreadFiles, q searchQuery, result []bool) bool {
	return files.scan(context.Background(), "f", newSearchQueryTermMatcher(q), result, files.newScans(q))
}

func searchTestScan(t *testing.T, query, content string, window int) []bool {
	t.Helper()
	q := buildSearchQuery(query)
	result := make([]bool, len(q.terms))
	if !searchTestScanOnce(searchTestScanFiles(content, window, nil, nil), q, result) {
		t.Fatalf("%q: scan failed", query)
	}
	return result
}

// Wherever a window boundary falls, in a long line, in short lines or inside a character
// outside ASCII, the scan finds an occurrence of a term as the matcher finds it in the whole
// file, and finds nothing a term does not occur in.
func TestSearchUnreadScanFindsATermAtEveryWindowEdge(t *testing.T) {
	t.Parallel()
	const window = 16
	for offset := range 4 * window {
		for _, tc := range []struct {
			query, content string
		}{
			{"quillwort", strings.Repeat("x", offset) + "QuillWort" + strings.Repeat("y", 3*window)},
			{"quillwort", strings.Repeat("x\n", offset/2) + strings.Repeat("x", offset%2) + "quillwort\n" + strings.Repeat("y\n", 2*window)},
			// The ohm sign lowers to ω, and each ж is two bytes, so windows end inside one.
			{`"ωμεγα"`, strings.Repeat("ж", offset) + "ΩΜΕΓΑ" + strings.Repeat("ж", 2*window)},
			// The Kelvin sign, three bytes, lowers to k, so this occurrence is longer than the term.
			{`"kelvin"`, strings.Repeat("x", offset) + "\u212Aelvin" + strings.Repeat("y", 3*window)},
		} {
			q := buildSearchQuery(tc.query)
			if whole := newSearchQueryTermMatcher(q).match(tc.content); !whole[0] {
				t.Fatalf("offset %d: the matcher misses %q in the whole file", offset, tc.query)
			}
			if result := searchTestScan(t, tc.query, tc.content, window); !result[0] {
				t.Fatalf("offset %d: scan of %q missed it in %q", offset, tc.query, tc.content)
			}
		}
	}
	if result := searchTestScan(t, "godwit", strings.Repeat("godwi\nt", 20), window); result[0] {
		t.Fatal("scan found a term that occurs only split across lines")
	}
}

// The scan never asks for more than a window and its overlap at once, however long the file
// and its one line, and it reads the file to its end, past the term, to rule out a NUL byte.
func TestSearchUnreadScanReadsABoundedWindow(t *testing.T) {
	t.Parallel()
	const window = 64
	q := buildSearchQuery("quillwort")
	overlap := searchUnreadScanOverlap(q)
	for _, content := range []string{"quillwort" + strings.Repeat("x", 1<<20), strings.Repeat("x", 1<<20) + "quillwort"} {
		largest, total := 0, 0
		result := make([]bool, len(q.terms))
		if !searchTestScanOnce(searchTestScanFiles(content, window, &largest, &total), q, result) || !result[0] {
			t.Fatalf("scan missed quillwort at offset %d of a 1 MiB line", strings.Index(content, "quillwort"))
		}
		if limit := overlap + window + 8; largest > limit {
			t.Fatalf("largest read = %d bytes, want at most %d", largest, limit)
		}
		if total != len(content) {
			t.Fatalf("scan read %d of %d bytes, want the whole file", total, len(content))
		}
	}
}

// The content pass skips a file holding a NUL byte anywhere, and Git's test reads only the
// first 8,000 bytes, so the scan marks nothing for a file with one past them, wherever a
// window puts it.
func TestSearchUnreadScanSkipsAFileHoldingANULByte(t *testing.T) {
	t.Parallel()
	const window = 64
	q := buildSearchQuery("quillwort")
	body := "quillwort\n" + strings.Repeat("x", 4*window)
	for offset := range len(body) + 1 {
		result := make([]bool, len(q.terms))
		if searchTestScanOnce(searchTestScanFiles(body[:offset]+"\x00"+body[offset:], window, nil, nil), q, result) || result[0] {
			t.Fatalf("NUL at byte %d: scan marked %v, want nothing", offset, result)
		}
	}
	if result := searchTestScan(t, "quillwort", body, window); !result[0] {
		t.Fatal("scan missed quillwort without the NUL byte")
	}
}

// One pass scans at most its budget in all: a file larger than what is left is not read, so
// a later smaller one still fits, and a file that grew past the budget since it was opened
// is cut off there and marks nothing. A file is scanned once a pass.
func TestSearchUnreadScanStaysWithinItsBudget(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("alpha beta gamma delta")
	matcher := newSearchQueryTermMatcher(q)
	total := 0
	files := searchTestScanTree(map[string]string{
		"a": "alpha\n" + strings.Repeat("x\n", 297), // 600 bytes
		"b": "beta\n" + strings.Repeat("x\n", 297),  // 599 bytes, over the 400 left
		"c": "gamma\n" + strings.Repeat("x\n", 147), // 300 bytes
		"d": "delta\n" + strings.Repeat("x\n", 22),  // 50 bytes
	}, 64, nil, &total)
	files.scanBudget = 1000
	scans := files.newScans(q)
	result := make([]bool, len(q.terms))
	for _, tc := range []struct {
		path      string
		ok        bool
		left, got int
	}{
		{path: "a", ok: true, left: 400, got: 600},
		{path: "b", ok: false, left: 400, got: 600},
		{path: "c", ok: true, left: 100, got: 900},
		{path: "d", ok: true, left: 50, got: 950},
		{path: "d", ok: false, left: 50, got: 950},
	} {
		if ok := files.scan(context.Background(), tc.path, matcher, result, scans); ok != tc.ok || scans.left != int64(tc.left) || total != tc.got {
			t.Fatalf("scan %s: ok = %v, budget left %d, bytes read %d; want %v, %d, %d", tc.path, ok, scans.left, total, tc.ok, tc.left, tc.got)
		}
	}
	requireSearchTermsMarked(t, "alpha beta gamma delta", q, result, map[string]bool{"alpha": true, "beta": false, "gamma": true, "delta": true})

	// The opener reports 100 bytes, but the file holds 5,000 by the time it is read.
	total = 0
	grown := searchTestScanFiles("beta\n"+strings.Repeat("x\n", 2500), 64, nil, &total)
	open := grown.open
	grown.open = func(filePath string) (io.ReadCloser, int64, bool) {
		file, _, ok := open(filePath)
		return file, 100, ok
	}
	grown.scanBudget = 1000
	scans = grown.newScans(q)
	result = make([]bool, len(q.terms))
	if grown.scan(context.Background(), "f", matcher, result, scans) || result[1] || total > 1001 || scans.left != 0 {
		t.Fatalf("grown file: marked %v, read %d bytes, budget left %d; want nothing, at most 1,001 bytes and none left", result, total, scans.left)
	}
}

// A pass's terms only files past its budget hold are missed, which leaves the confidence high:
// searchUnreadTerms scans the files that fit and stops reading at the budget.
func TestSearchUnreadTermsScansWithinOnePassBudget(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("alpha beta")
	tree := map[string]string{}
	var unread []string
	for index := range 8 {
		filePath := fmt.Sprintf("big_%d.md", index)
		tree[filePath] = strings.Repeat("x\n", 300)
		unread = append(unread, filePath)
	}
	tree["big_3.md"] = "alpha\n" + tree["big_3.md"]
	tree["big_7.md"] = "beta\n" + tree["big_7.md"]
	total := 0
	files := searchTestScanTree(tree, 64, nil, &total)
	files.readFile = func(string) (string, bool) { return "", false }
	files.readPrefix = func(filePath string, limit int) (string, bool) {
		return tree[filePath][:min(limit, len(tree[filePath]))], true
	}
	// Room for big_0.md to big_3.md, which holds alpha, and none after.
	files.scanBudget = int64(3*len(tree["big_0.md"]) + len(tree["big_3.md"]))
	marked, examined, err := searchUnreadTerms(t.Context(), files, unread, q, newSearchQueryTermMatcher(q), make([]bool, len(q.terms)))
	if err != nil {
		t.Fatal(err)
	}
	requireSearchTermsMarked(t, "alpha beta", q, marked, map[string]bool{"alpha": true, "beta": false})
	if examined != len(unread) || total > int(files.scanBudget) {
		t.Fatalf("examined %d files and scanned %d bytes, want all %d examined and at most %d bytes", examined, total, len(unread), files.scanBudget)
	}
}

func TestSearchUnreadScanReportsAFileItCannotOpen(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("quillwort")
	result := make([]bool, 1)
	files := searchUnreadFiles{open: func(string) (io.ReadCloser, int64, bool) { return nil, 0, false }}
	if searchTestScanOnce(files, q, result) || result[0] {
		t.Fatal("scan of a file it cannot open reported success")
	}
	if searchTestScanOnce(searchUnreadFiles{}, q, result) {
		t.Fatal("scan without an opener reported success")
	}
}

// An alias matches a whole word, so where a window edge falls inside a line, the scan reads
// alias tokens only from the words lying whole in the window: a fragment of a longer word is
// never taken for the abbreviation, and the word itself is still found wherever it occurs.
func TestSearchUnreadScanTakesNoAliasFromPartOfAWord(t *testing.T) {
	t.Parallel()
	const window = 16
	q := buildSearchQuery("integer value")
	alias := slices.Index(q.terms, "int")
	if alias < 0 || !q.inferredAbbreviations["int"] {
		t.Fatalf("terms = %q, want the inferred alias int", q.terms)
	}
	for offset := range 4 * window {
		for _, tc := range []struct {
			content string
			want    bool
		}{
			// A window can start at "int", the end of the word "x...xint".
			{strings.Repeat("x", offset+1) + "int " + strings.Repeat("y", 3*window), false},
			// A window can end after "int", the start of the word "intx...".
			{strings.Repeat("x", offset) + " int" + strings.Repeat("x", 3*window), false},
			{strings.Repeat("x", offset) + " int " + strings.Repeat("y", 3*window), true},
		} {
			if whole := newSearchQueryTermMatcher(q).match(tc.content); whole[alias] != tc.want {
				t.Fatalf("offset %d: the matcher finds int = %v in the whole file", offset, whole[alias])
			}
			result := make([]bool, len(q.terms))
			if !searchTestScanOnce(searchTestScanFiles(tc.content, window, nil, nil), q, result) {
				t.Fatal("scan failed")
			}
			if result[alias] != tc.want {
				t.Fatalf("offset %d: scan found int = %v in %q, want %v", offset, result[alias], tc.content, tc.want)
			}
		}
	}
}

// A window that ends inside a word shorter than half a window hands the whole word to the
// next one, so an alias in an identifier longer than the overlap is found wherever the cut
// falls, and a U+FFFD in the text separates words as it does for the whole file.
func TestSearchUnreadScanFindsAnAliasInAWordAWindowEdgeCuts(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("integer value")
	alias := slices.Index(q.terms, "int")
	if alias < 0 || searchUnreadScanOverlap(q) >= 100 {
		t.Fatalf("terms = %q, overlap %d, want the inferred alias int and an overlap under 100 bytes", q.terms, searchUnreadScanOverlap(q))
	}
	for _, tc := range []struct {
		window    int
		word, sep string
	}{
		{256, strings.Repeat("a", 50) + "_int_" + strings.Repeat("b", 50), " "},
		// Only the U+FFFDs part int from the runs of x and y.
		{16, "\uFFFDint\uFFFD", ""},
	} {
		for offset := range 2 * tc.window {
			content := strings.Repeat("x", offset) + tc.sep + tc.word + tc.sep + strings.Repeat("y", 3*tc.window)
			if whole := newSearchQueryTermMatcher(q).match(content); !whole[alias] {
				t.Fatalf("offset %d: the matcher misses int in %q", offset, tc.word)
			}
			result := make([]bool, len(q.terms))
			if !searchTestScanOnce(searchTestScanFiles(content, tc.window, nil, nil), q, result) {
				t.Fatal("scan failed")
			}
			if !result[alias] {
				t.Fatalf("window %d, offset %d: scan missed int in %q", tc.window, offset, tc.word)
			}
		}
	}
}
