package sem

import (
	"bytes"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// searchUnreadScanBytes is how much of a file too large to read whole searchUnreadFiles.scan
// reads at a time.
const searchUnreadScanBytes = 1 << 20

// scan matches a corpus file too large for the content pass to read with the query's matcher,
// without holding it whole: it reads files.scanBytes at a time (searchUnreadScanBytes when
// unset), so memory stays bounded whatever the file's size or line lengths. Git would load
// the whole file to grep it. A window ends after its last newline, and no query term holds
// one, so the matcher sees whole lines, as in a file it reads whole. A line longer than a
// window is cut where the window ends instead, and the next window starts overlap bytes
// before the cut (searchUnreadScanOverlap), or at the start of the word the cut falls inside
// when that is further back but within half a window. So every occurrence of a term lies
// wholly in one window, past the character the cut or that start may fall inside, and so does
// every word no longer than half a window. A window's alias tokens leave out a word it starts
// or ends inside, which goes on past it (searchWholeWords): an abbreviation is never found in
// part of a longer word, and is missed only in a word longer than half a window. It marks in
// result the terms it finds and stops as soon as done reports true. It reports false when the
// file cannot be opened or read; what it marked stays marked.
func (files searchUnreadFiles) scan(filePath string, matcher searchTermMatcher, overlap int, result []bool, done func() bool) bool {
	if files.open == nil {
		return false
	}
	file, _, ok := files.open(filePath)
	if !ok {
		return false
	}
	defer file.Close()
	window := files.scanBytes
	if window <= 0 {
		window = searchUnreadScanBytes
	}
	buf := make([]byte, overlap+window)
	kept := 0
	lead := false
	for {
		n, err := io.ReadFull(file, buf[kept:])
		end := kept + n
		last := err != nil
		if last && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false
		}
		cut, next, trail := end, end, false
		if !last {
			if newline := bytes.LastIndexByte(buf[:end], '\n'); newline >= 0 {
				cut, next = newline+1, newline+1
			} else {
				next, trail = max(0, cut-overlap), true
				if start := searchWordStart(buf[:cut], window/2); start >= 0 {
					next = min(next, start)
				}
			}
		}
		found := matcher.match(string(buf[:cut]))
		if lead || trail {
			matcher.matchAliasTokens(found, string(searchWholeWords(buf[:cut], lead, trail)))
		}
		markSearchHits(result, found)
		if last || done() {
			return true
		}
		lastRune, size := utf8.DecodeLastRune(buf[:next])
		lead = next > 0 && searchWordRune(lastRune, size)
		kept = copy(buf, buf[next:end])
	}
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
