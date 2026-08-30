package sem

import (
	"sort"
	"strings"
	"sync"
)

// Repository-wide literal lookup, without a second corpus pass
// ============================================================
//
// Two blocks need the same primitive: "where else in this repository does this exact string
// occur, and what symbol encloses each occurrence" — the same question `grep` answers, which is
// the call agents rate highest and which search cannot fold in without paying for a second scan
// of the corpus.
//
// It is not paid for twice. Preselection ALREADY matches every discovered file against every
// query term (search.go, scoreFile), so the per-term posting list is a by-product of work the
// search has already done; all that is added is the line numbers, read from at most
// searchNeedleMaxReads files. A needle is only ever looked up through a query term it CONTAINS,
// so the posting list for that term is a superset of the needle's own occurrences and the
// repository-wide total is exact rather than sampled.
//
// Three sources, in order of cost, and NONE of them is a repository walk:
//
//  1. posting lists from preselection — free, exact, available whenever preselection scanned the
//     whole corpus (the common case: any worktree search, and any HEAD search without a
//     preindexed graph).
//  2. `git grep -l` for the needle — one subprocess, exact. Used when preselection short-circuited
//     through Git and therefore never scanned content itself.
//  3. the corpus file list — only when it is small enough to read inside the read budget, which
//     is exactly the case where preselection also skipped its content pass.
//
// When none of the three can answer, the answer is NOTHING. A block built on a partial view of
// the repository would state a total it cannot know, and a wrong total is worse than no block:
// "4 hits" that is really 40 tells an agent it has seen the whole picture.
const (
	// searchNeedlePostingCap is how many paths per query term preselection remembers. Past it the
	// list is truncated and therefore no longer a usable superset, so the term is not offered as a
	// lookup route at all. It is deliberately several times the distinctiveness cap below: a term
	// can be much more common than the literal that contains it (`round` vs `"ROUNDUP"`).
	searchNeedlePostingCap = 96

	// searchNeedleMaxReads bounds files hydrated to turn a posting list into line numbers, across
	// every lookup in one search. The posting list is a superset, so this is the only real IO the
	// lookup adds — and it is small beside the thousands of files preselection already read.
	searchNeedleMaxReads = 96

	// searchNeedleCorpusReadCap is the largest corpus the lookup will read end-to-end when no
	// posting list and no Git are available. It matches the situation it exists for: a repository
	// small enough that preselection indexed every file, so every read is a content-cache hit.
	searchNeedleCorpusReadCap = 256
)

// searchNeedleHit is one occurrence of a needle: where it is, the line it sits on, and whether that
// line is indented. Indentation is carried because it is the one structural fact available about a
// line the graph holds no symbol for: an unindented line is a top-level declaration in every language
// in scope, an indented one is inside something.
type searchNeedleHit struct {
	filePath string
	line     int
	text     string
	indented bool
}

// searchTermPostings is the per-term path index preselection fills in as it scans. It is written
// from the preselection worker pool, so every mutation takes the lock; the lists are capped so a
// term that matches the whole repository cannot grow it without bound.
type searchTermPostings struct {
	mu     sync.Mutex
	files  map[string][]string
	totals map[string]int
}

func newSearchTermPostings() *searchTermPostings {
	return &searchTermPostings{files: map[string][]string{}, totals: map[string]int{}}
}

// record adds one file's matched terms. `matched` is indexed by `terms`, exactly as
// searchTermMatcher returns it.
func (postings *searchTermPostings) record(filePath string, matched []bool, terms []string) {
	if postings == nil {
		return
	}
	postings.mu.Lock()
	defer postings.mu.Unlock()
	for index, hit := range matched {
		if !hit || index >= len(terms) {
			continue
		}
		term := terms[index]
		postings.totals[term]++
		if len(postings.files[term]) < searchNeedlePostingCap {
			postings.files[term] = append(postings.files[term], filePath)
		}
	}
}

// snapshot returns the accumulated lists, sorted so a lookup is deterministic.
func (postings *searchTermPostings) snapshot() (map[string][]string, map[string]int) {
	if postings == nil {
		return nil, nil
	}
	postings.mu.Lock()
	defer postings.mu.Unlock()
	files := make(map[string][]string, len(postings.files))
	for term, paths := range postings.files {
		copied := append([]string(nil), paths...)
		sort.Strings(copied)
		files[term] = copied
	}
	totals := make(map[string]int, len(postings.totals))
	for term, total := range postings.totals {
		totals[term] = total
	}
	return files, totals
}

// searchNeedleIndex answers "where does this exact string occur" from whichever of the three
// sources above this search actually has.
type searchNeedleIndex struct {
	read           contentReader
	termFiles      map[string][]string
	termFileTotals map[string]int
	corpus         []string
	// grep, when non-nil, lists the files containing a fixed string case-sensitively. It is the
	// Git route, and it reports ok=false when Git could not answer.
	grep  func(pattern string) ([]string, bool)
	reads int
}

// candidatePaths returns a superset of the files that can contain the needle, or ok=false when no
// source can produce one. `fallback` is consulted last and is for callers whose needle is not
// derived from the query (and therefore has no posting list): a bounded, explicitly-supplied set
// of files that are already known to be relevant.
func (index *searchNeedleIndex) candidatePaths(needle string, fallback []string) ([]string, bool) {
	if paths, ok := index.candidatePathsFromPostings(needle); ok {
		return paths, true
	}
	if index.grep != nil {
		if paths, ok := index.grep(needle); ok {
			sort.Strings(paths)
			return paths, true
		}
	}
	if len(index.corpus) > 0 && len(index.corpus) <= searchNeedleCorpusReadCap {
		return index.corpus, true
	}
	if len(fallback) > 0 {
		paths := append([]string(nil), fallback...)
		sort.Strings(paths)
		return dedupeSortedStrings(paths), true
	}
	return nil, false
}

// candidatePathsCheap is every route that costs no subprocess: the posting lists, then a corpus small
// enough to read. It is separate from candidatePaths so a caller that prices many candidates can do
// so without spawning a `git grep` per candidate.
func (index *searchNeedleIndex) candidatePathsCheap(needle string) ([]string, bool) {
	if paths, ok := index.candidatePathsFromPostings(needle); ok {
		return paths, true
	}
	if len(index.corpus) > 0 && len(index.corpus) <= searchNeedleCorpusReadCap {
		return index.corpus, true
	}
	return nil, false
}

// candidatePathsFromPostings is the FREE route: it reads posting lists and never touches the
// filesystem or Git.
func (index *searchNeedleIndex) candidatePathsFromPostings(needle string) ([]string, bool) {
	if index == nil || needle == "" {
		return nil, false
	}
	lower := strings.ToLower(needle)
	// The best posting list is the smallest one whose term the needle CONTAINS. Containment is the
	// direction that matters: a term inside the needle occurs wherever the needle does, so its
	// list is a superset. The reverse (a term that contains the needle) proves nothing.
	bestTotal := 0
	var best []string
	for term, paths := range index.termFiles {
		if len(term) < 2 || !strings.Contains(lower, term) {
			continue
		}
		total := index.termFileTotals[term]
		if total > searchNeedlePostingCap || total != len(paths) {
			// Truncated: no longer a superset, so it cannot bound anything.
			continue
		}
		if best == nil || total < bestTotal {
			best, bestTotal = paths, total
		}
	}
	return best, best != nil
}

// searchNeedleScan is what one lookup found: the sampled hits plus the exact repository-wide counts.
type searchNeedleScan struct {
	hits []searchNeedleHit
	// matchingPaths is every file behind the exact aggregate counts, not only
	// files whose occurrences fit in hits. It becomes replay provenance when a
	// literal cluster exposes those aggregate counts.
	matchingPaths []string
	// occurrences and files are the EXACT repository totals, not the sample's size. A caller that
	// cannot have them exactly must emit nothing; the whole value of the block is that the count is
	// the repository's count.
	occurrences int
	files       int
	// complete is false when the read budget ran out before every candidate file was checked.
	complete bool
}

// locate turns a candidate path set into the needle's actual occurrences.
//
// The hit sample is capped per file as well as in total, because a block that lists six occurrences
// of one file has told the reader nothing about the sweep — the point is the SET of places, and one
// file's tenth mention displaces another file's first.
func (index *searchNeedleIndex) locate(needle string, paths []string, perFile, maxHits int) searchNeedleScan {
	scan := searchNeedleScan{}
	if index == nil || needle == "" || len(paths) == 0 {
		return scan
	}
	for _, filePath := range paths {
		if index.reads >= searchNeedleMaxReads {
			return scan
		}
		content, ok := index.read(filePath)
		index.reads++
		if !ok || strings.IndexByte(content, 0) >= 0 || !strings.Contains(content, needle) {
			continue
		}
		scan.matchingPaths = append(scan.matchingPaths, filePath)
		scan.files++
		sampled := 0
		for offset, line := range strings.Split(content, "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			scan.occurrences++
			if sampled < perFile && len(scan.hits) < maxHits {
				sampled++
				scan.hits = append(scan.hits, searchNeedleHit{
					filePath: filePath,
					line:     offset + 1,
					text:     strings.TrimSpace(line),
					indented: len(line) > 0 && (line[0] == ' ' || line[0] == '\t'),
				})
			}
		}
	}
	scan.complete = true
	return scan
}

// dedupeSortedStrings lives in provider.go — the needle scan and the entity walk both wanted
// it and each grew its own copy. Same behaviour, one definition.
