package sem

import (
	"bytes"
	"context"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// searchUnreadScanBytes is how much of a file too large to read whole searchUnreadFiles.scan
// reads at a time.
const searchUnreadScanBytes = 1 << 20

// searchUnreadScanBudgetBytes is how much one recovery pass scans in all, over every file too
// large to read (searchUnreadFiles.scanBudget overrides it): four times the content pass's
// default read cap, so a few files just over it fit. A term only files past it hold is
// missed, which leaves the confidence high, never low.
const searchUnreadScanBudgetBytes = 16 << 20

// searchUnreadScans is one recovery pass's scan budget: the bytes it may still scan, the
// overlap its query needs (searchUnreadScanOverlap), and the files it has scanned, which a
// second scan would find no more in.
type searchUnreadScans struct {
	left    int64
	overlap int
	seen    map[string]bool
}

// newScans starts a recovery pass's scan budget for q.
func (files searchUnreadFiles) newScans(q searchQuery) *searchUnreadScans {
	budget := files.scanBudget
	if budget <= 0 {
		budget = searchUnreadScanBudgetBytes
	}
	return &searchUnreadScans{left: budget, overlap: searchUnreadScanOverlap(q), seen: map[string]bool{}}
}

// scan matches a corpus file too large for the content pass to read with the query's matcher,
// without holding it whole, as searchScanWindows does. The content pass skips a file holding a
// NUL byte anywhere, so the file's terms are marked in result only once it has been read to
// its end with none found; Git's own test reads only its first searchBinarySniffBytes. A file
// larger than what scans has left is not read, and the bytes read are taken from it, so one
// pass reads a bounded amount however many files it scans and however large they are. It
// reports whether it marked the file's terms: false when the file holds a NUL byte, is over
// the budget, was scanned already, or cannot be opened or read in full.
func (files searchUnreadFiles) scan(ctx context.Context, filePath string, matcher searchTermMatcher, result []bool, scans *searchUnreadScans) bool {
	if files.open == nil || scans.seen[filePath] {
		return false
	}
	scans.seen[filePath] = true
	file, size, ok := files.open(filePath)
	if !ok {
		return false
	}
	defer file.Close()
	if size > scans.left {
		return false
	}
	window := files.scanBytes
	if window <= 0 {
		window = searchUnreadScanBytes
	}
	// One byte past the budget tells a file that grew since it was opened from one that ends.
	limited := &io.LimitedReader{R: file, N: scans.left + 1}
	hits := make([]bool, len(result))
	whole := searchScanWindows(ctx, limited, matcher, scans.overlap, window, result, hits)
	scans.left = max(0, limited.N-1)
	if !whole || limited.N == 0 {
		return false
	}
	markSearchHits(result, hits)
	return true
}

// searchScanWindows matches reader with the query's matcher window bytes at a time, marking
// in hits the terms it finds, so memory stays bounded whatever its size or line lengths. A
// window ends after its last newline, and no query term holds one, so the matcher sees whole
// lines, as in a file it reads whole. A line longer than a window is cut where the window ends
// instead, and the next window starts overlap bytes before the cut (searchUnreadScanOverlap),
// or at the start of the word the cut falls inside when that is further back but within half a
// window (searchScanCut). So every occurrence of a term lies wholly in one window, past the
// character the cut or that start may fall inside, and so does every word no longer than half
// a window. A window's alias tokens leave out a word it starts or ends inside, which goes on
// past it (searchWholeWords): an abbreviation is never found in part of a longer word, and is
// missed only in a word longer than half a window. Once known and hits mark every term, the
// rest is only checked for a NUL byte. It reports whether it read reader to its end with none
// in it; it stops at the first one, a failed read, or ctx's end.
func searchScanWindows(ctx context.Context, reader io.Reader, matcher searchTermMatcher, overlap, window int, known, hits []bool) bool {
	buf := make([]byte, overlap+window)
	kept := 0
	lead := false
	for ctx.Err() == nil {
		n, err := io.ReadFull(reader, buf[kept:])
		end := kept + n
		last := err != nil
		if last && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false
		}
		if bytes.IndexByte(buf[kept:end], 0) >= 0 {
			return false
		}
		cut, next, trail := searchScanCut(buf[:end], last, overlap, window)
		if !searchEveryTermMarked(known, hits) {
			found := matcher.match(string(buf[:cut]))
			if lead || trail {
				matcher.matchAliasTokens(found, string(searchWholeWords(buf[:cut], lead, trail)))
			}
			markSearchHits(hits, found)
		}
		if last {
			return true
		}
		lastRune, size := utf8.DecodeLastRune(buf[:next])
		lead = next > 0 && searchWordRune(lastRune, size)
		kept = copy(buf, buf[next:end])
	}
	return false
}

// searchEveryTermMarked reports whether known or hits marks each term.
func searchEveryTermMarked(known, hits []bool) bool {
	for index := range hits {
		if !hits[index] && !searchTermMarked(known, index) {
			return false
		}
	}
	return true
}

// searchScanCut returns where the window text ends for matching (cut) and where the next one
// starts (next), and whether the cut falls inside a line (trail). The last window takes all of
// text. Another ends after its last newline, or, in a line longer than the window, at its end,
// and the next then starts overlap bytes back, or at the start of the word the cut falls
// inside when that is further back but within half a window (searchWordStart).
func searchScanCut(text []byte, last bool, overlap, window int) (cut, next int, trail bool) {
	end := len(text)
	if last {
		return end, end, false
	}
	if newline := bytes.LastIndexByte(text, '\n'); newline >= 0 {
		return newline + 1, newline + 1, false
	}
	next = max(0, end-overlap)
	if start := searchWordStart(text, window/2); start >= 0 {
		next = min(next, start)
	}
	return end, next, true
}

// searchWholeWords returns text without the word it starts inside, when lead is set, and
// without the word it ends inside, when trail is set. Such a word goes on past text, so its
// alias tokens are read only where it lies whole.
func searchWholeWords(text []byte, lead, trail bool) []byte {
	for trail && len(text) > 0 {
		r, size := utf8.DecodeLastRune(text)
		if !searchWordRune(r, size) {
			break
		}
		text = text[:len(text)-size]
	}
	for lead && len(text) > 0 {
		r, size := utf8.DecodeRune(text)
		if !searchWordRune(r, size) {
			break
		}
		text = text[size:]
	}
	return text
}

// searchWordStart returns where the word text ends inside starts, or len(text) when text ends
// after a character outside any word. It returns -1 when the word reaches further back than
// limit bytes, or to the start of text, before which it may go on.
func searchWordStart(text []byte, limit int) int {
	for start := len(text); start > 0 && len(text)-start <= limit; {
		r, size := utf8.DecodeLastRune(text[:start])
		if !searchWordRune(r, size) {
			return start
		}
		start -= size
	}
	return -1
}

// searchWordRune reports whether r, decoded from size bytes, can be part of a word
// searchSourceWordPattern matches. An invalid byte counts too, since it can be part of a
// character a window edge cuts through; a U+FFFD the text holds does not.
func searchWordRune(r rune, size int) bool {
	if r == utf8.RuneError {
		return size == 1
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("_./:+#-", r)
}

// searchUnreadScanOverlap is the overlap searchUnreadFiles.scan needs for q: an occurrence of
// a term is at most utf8.UTFMax bytes for each byte of it, since each of its characters is one
// that lowers to the term's, and no character is longer.
func searchUnreadScanOverlap(q searchQuery) int {
	longest := 0
	for _, term := range q.terms {
		longest = max(longest, len(term))
	}
	return utf8.UTFMax * longest
}
