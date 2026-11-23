package sem

import (
	"io"
	"slices"
	"strings"
	"testing"
)

// searchTestScanFiles scans one in-memory file, path "f", through searchUnreadFiles.scan with
// windows of window bytes, recording the largest read the scan asks for.
func searchTestScanFiles(content string, window int, largest *int, reads *int) searchUnreadFiles {
	return searchUnreadFiles{
		scanBytes: window,
		open: func(string) (io.ReadCloser, int64, bool) {
			return io.NopCloser(&searchTestReadRecorder{reader: strings.NewReader(content), largest: largest, reads: reads}), int64(len(content)), true
		},
	}
}

type searchTestReadRecorder struct {
	reader  io.Reader
	largest *int
	reads   *int
}

func (recorder *searchTestReadRecorder) Read(p []byte) (int, error) {
	if recorder.largest != nil {
		*recorder.largest = max(*recorder.largest, len(p))
	}
	if recorder.reads != nil {
		*recorder.reads++
	}
	return recorder.reader.Read(p)
}

func searchTestScan(t *testing.T, query, content string, window int) []bool {
	t.Helper()
	q := buildSearchQuery(query)
	result := make([]bool, len(q.terms))
	if !searchTestScanFiles(content, window, nil, nil).scan("f", newSearchQueryTermMatcher(q), searchUnreadScanOverlap(q), result, func() bool { return false }) {
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
// and its one line, and it stops reading once done reports true.
func TestSearchUnreadScanReadsABoundedWindow(t *testing.T) {
	t.Parallel()
	const window = 64
	q := buildSearchQuery("quillwort")
	overlap := searchUnreadScanOverlap(q)
	content := "quillwort" + strings.Repeat("x", 1<<20)
	largest, reads := 0, 0
	result := make([]bool, len(q.terms))
	files := searchTestScanFiles(content, window, &largest, &reads)
	if !files.scan("f", newSearchQueryTermMatcher(q), overlap, result, func() bool { return result[0] }) || !result[0] {
		t.Fatal("scan missed quillwort")
	}
	if limit := overlap + window + 8; largest > limit {
		t.Fatalf("largest read = %d bytes, want at most %d", largest, limit)
	}
	if reads > 2 {
		t.Fatalf("scan read %d times after finding the only term in its first window", reads)
	}
	largest = 0
	result[0] = false
	files = searchTestScanFiles(strings.Repeat("x", 1<<20)+"quillwort", window, &largest, nil)
	if !files.scan("f", newSearchQueryTermMatcher(q), overlap, result, func() bool { return false }) || !result[0] {
		t.Fatal("scan missed quillwort at the end of a 1 MiB line")
	}
	if limit := overlap + window + 8; largest > limit {
		t.Fatalf("largest read = %d bytes, want at most %d", largest, limit)
	}
}

func TestSearchUnreadScanReportsAFileItCannotOpen(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("quillwort")
	result := make([]bool, 1)
	files := searchUnreadFiles{open: func(string) (io.ReadCloser, int64, bool) { return nil, 0, false }}
	if files.scan("f", newSearchQueryTermMatcher(q), 36, result, func() bool { return false }) || result[0] {
		t.Fatal("scan of a file it cannot open reported success")
	}
	if (searchUnreadFiles{}).scan("f", newSearchQueryTermMatcher(q), 36, result, func() bool { return false }) {
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
			if !searchTestScanFiles(tc.content, window, nil, nil).scan("f", newSearchQueryTermMatcher(q), searchUnreadScanOverlap(q), result, func() bool { return false }) {
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
			if !searchTestScanFiles(content, tc.window, nil, nil).scan("f", newSearchQueryTermMatcher(q), searchUnreadScanOverlap(q), result, func() bool { return false }) {
				t.Fatal("scan failed")
			}
			if !result[alias] {
				t.Fatalf("window %d, offset %d: scan missed int in %q", tc.window, offset, tc.word)
			}
		}
	}
}
