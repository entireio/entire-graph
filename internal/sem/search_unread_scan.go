package sem

import (
	"bytes"
	"io"
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
// before the cut (searchUnreadScanOverlap), so every occurrence of a term lies wholly in one
// window, past the character the cut or that start may fall inside. A word either falls
// inside is read in pieces for the alias tokens, so in such a line an abbreviation can be
// missed, or found in part of a longer word. It marks in result the terms it finds and stops
// as soon as done reports true. It reports false when the file cannot be opened or read; what
// it marked stays marked.
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
	for {
		n, err := io.ReadFull(file, buf[kept:])
		end := kept + n
		last := err != nil
		if last && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false
		}
		cut, next := end, end
		if !last {
			if newline := bytes.LastIndexByte(buf[:end], '\n'); newline >= 0 {
				cut, next = newline+1, newline+1
			} else {
				next = max(0, cut-overlap)
			}
		}
		markSearchHits(result, matcher.match(string(buf[:cut])))
		if last || done() {
			return true
		}
		kept = copy(buf, buf[next:end])
	}
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
