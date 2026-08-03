package sem

import (
	"fmt"
	"strings"
)

// Snippet byte allocation.
//
// A search result whose snippet stops in the middle of the function that has to change
// buys the agent nothing: it still has to open the file, which costs a whole extra turn,
// and a turn re-reads the entire conversation context. Measured on agentic SWE-bench
// sessions, 95.9% of billed tokens are context re-read and a mean turn re-reads ~47.5k
// tokens to deliver ~390 tokens of new information, so removing one follow-up read is
// worth far more than the few hundred bytes a complete body costs.
//
// The allocator therefore spends the result byte budget by rank instead of spreading it
// evenly: a hit whose complete enclosing symbol fits is returned WHOLE, and when the
// budget is tight the tail of the ranking is demoted to terse locators to pay for it.
// A tail result's job is to say "also look here" (path, line, symbol name — all of which
// survive demotion); the head's job is to deliver code the agent can act on directly.
//
// How far down the ranking "the head" reaches is set by measurement, not by taste: on the
// same sessions the first hit is the file the agent ends up editing 35% of the time and one
// of the first three 46% of the time, so a two- or three-deep head leaves the majority of
// searches one Read short of an edit. Five is where the marginal body stops paying: it
// covers the ranks an agent actually reads before deciding, and five complete callables is
// still a fraction of the ~42.5k tokens a single extra turn costs.
const (
	// defaultSearchEnclosureMaxLines caps the complete-body upgrade. A symbol larger than
	// this is a context dump in its own right: returning it whole would consume the budget
	// an agent needs for the rest of the ranking, and it is cheaper to read such a file
	// directly. Bigger symbols keep their focused window (graceful degradation, never a
	// budget blow-out).
	defaultSearchEnclosureMaxLines = 160

	// searchEnclosureHeadRanks is the head of the ranking: the results that are never demoted
	// to a locator AND the only ones eligible for the complete-body upgrade. One constant, not
	// two, because both halves express the same judgement about where an agent stops reading —
	// a complete body below the head is paid for out of the same budget and almost never read.
	searchEnclosureHeadRanks = 5

	// searchEnclosureTailSnippetLines is the locator window a demoted tail result keeps.
	// Two lines still show the matching line and one line of its context.
	searchEnclosureTailSnippetLines = 2

	// searchEnclosureGrowthBytes is how far a payload may grow beyond what the ranker produced
	// in order to complete head bodies. It funds nothing else: the only edit the allocator can
	// make that costs bytes is replacing a focused window with its complete enclosing callable,
	// so this allowance is bounded by "the head's bodies", not by "whatever fits".
	//
	// Sized as the head (5) times a comfortably typical callable (~40 lines of source plus JSON
	// escaping, ~2 kB). Bodies are funded FIRST by demoting the tail, so in practice the
	// allowance covers only the remainder; it exists so that a search whose head genuinely
	// needs 5 whole functions can return them instead of stopping one Read short.
	searchEnclosureGrowthBytes = searchEnclosureHeadRanks * 2048

	// searchCompleteSymbolSignal marks a result whose snippet is the complete source of its
	// enclosing symbol, so an agent (and the tests) can tell a whole body from a window.
	searchCompleteSymbolSignal = "complete-symbol"

	// searchHeadWindowSignal marks a head result widened to a bounded READ WINDOW because no
	// enclosable callable was available for it. It is code you can read, but it is not a whole
	// body, so it never carries searchCompleteSymbolSignal.
	searchHeadWindowSignal = "head-window"

	// searchHeadWindowLines is how wide that window is. Sized from what the agents actually
	// asked for: over 128 post-search reads of a payload file, the MEDIAN requested window was
	// 50 lines and the median gap from the printed body was 42 lines. 60 lines centred on the
	// hit covers the median request while costing ~2 kB against a ~17,000-token turn.
	searchHeadWindowLines = 60
)

// EDITABILITY: the forced full-unit upgrade (--full-unit-top)
// ==========================================================
//
// Everything above allocates bodies OPPORTUNISTICALLY: a head rank gets its complete callable when
// one is resolvable, when it is under defaultSearchEnclosureMaxLines, and when the growth allowance
// or a demotable tail can pay for it. Measured against real benchmark payloads, that leaves the
// share of needed edits whose replaced text appears VERBATIM in the payload at ~11%, and the misses
// are not budget misses — they are the three conditions themselves:
//
//   - no ENCLOSABLE CALLABLE. searchEnclosableSymbolKind excludes containers, so a hit inside a
//     class body, a constant table, a type declaration or a build script resolves to nothing and
//     falls through to a 6-line window (and, below the text renderer's second rank, to a bare
//     locator with no source at all — 2 of the 5 hits on redis__redis-10095).
//   - the 160-LINE CAP. A callable longer than that keeps its focused window, which is exactly the
//     case where reading the file back costs the most.
//   - NO DEMOTABLE TAIL at small --top-k. allocateSearchSnippets scans cuts down to
//     minInt(headRanks, len(results)); with --top-k 5 and headRanks 5 that loop can only ever
//     evaluate "demote nothing", so the whole body budget is the 10 kB growth allowance.
//
// --full-unit-top N bypasses all three for the first N ranks. The unit is resolved as the callable
// first and then as ANY enclosing symbol (that is what recovers type declarations and tables), it
// is bounded only by searchFullUnitMaxLines, and the allocator seats it BEFORE it prices anything
// else, demoting the tail to locators to pay for it. --max-context-bytes still wins: when the
// ceiling cannot hold the forced unit even with every other hit reduced to a locator, the forced
// ranks give their ranked snippets back deepest-first, so rank 1 is the last thing to yield.
const (
	// searchFullUnitMaxLines is the safety cap on ONE forced unit. It is a safety cap, not a
	// judgement about what is worth returning: 400 lines is far past any function an agent edits in
	// one go, so a unit that trips it is a container the caller almost certainly did not mean to
	// ask for whole. Such a unit is CLIPPED to a window containing the hit and says so, rather
	// than being silently dropped back to a 6-line snippet.
	searchFullUnitMaxLines = 400

	// searchFullUnitSignal marks a result rendered as its complete enclosing unit because
	// --full-unit-top asked for it. It is reported separately from complete-symbol because the two
	// promises differ: complete-symbol says "this is the whole callable and you need no follow-up
	// read", while full-unit says "the caller demanded the enclosing unit at this rank". A forced
	// unit that fit carries BOTH; one the safety cap clipped carries full-unit and unit-elided and
	// deliberately NOT complete-symbol, because a clipped unit cannot make the no-follow-up promise.
	searchFullUnitSignal = "full-unit"

	// searchFullUnitElidedSignal marks a forced unit the safety cap clipped.
	searchFullUnitElidedSignal = "unit-elided"

	// searchFullUnitGapRatio decides whether --full-unit-top 2 actually reaches rank 2. A second
	// forced unit is worth its bytes only when the ranking did NOT separate the two candidates: if
	// rank 1 is clearly ahead, rank 2's body is a second answer to a question that already has one.
	// The test is relative, because scores are not calibrated across queries.
	searchFullUnitGapRatio = 0.15
)

// FullUnitSignal is searchFullUnitSignal exported for renderers: a result carrying it was returned
// as its enclosing unit on the caller's orders and must never be abbreviated on the way out.
const FullUnitSignal = searchFullUnitSignal

// SearchUnitElisionNote renders the note a clipped unit carries: which of the unit's own lines are
// NOT printed above it, and that the unit continues past what is. It returns "" when nothing was
// clipped, so a caller can print it unconditionally.
//
// It is a separate LINE after the body, never an inline marker inside it. Agents copy body text
// verbatim as the `old_string` anchor of an edit, so decorating or interleaving the source would
// turn a navigation aid into a broken patch — the same reason focus= rides in the header.
func SearchUnitElisionNote(printedStart, printedEnd, unitStart, unitEnd int) string {
	if unitStart <= 0 || unitEnd < unitStart || printedStart <= 0 || printedEnd < printedStart {
		return ""
	}
	parts := make([]string, 0, 2)
	if unitStart < printedStart {
		parts = append(parts, fmt.Sprintf("%d–%d", unitStart, printedStart-1))
	}
	if unitEnd > printedEnd {
		parts = append(parts, fmt.Sprintf("%d–%d", printedEnd+1, unitEnd))
	}
	if len(parts) == 0 {
		return ""
	}
	return "…elided lines " + strings.Join(parts, ", ") + " (unit continues)"
}

// searchFullUnitForceRanks turns the --full-unit-top request into a rank count.
//
// Rank 1 is unconditional: it is the rank the agent reads and edits most (62% read / 54% edited on
// the measured sonnet sessions), so "the caller asked for the top unit" needs no further test.
// Every rank past the first is admitted only while its score is still within searchFullUnitGapRatio
// of rank 1's — one forced unit per genuinely ambiguous answer, not N bodies per search.
func searchFullUnitForceRanks(results []SearchResult, top int) int {
	if top <= 0 || len(results) == 0 {
		return 0
	}
	limit := minInt(top, len(results))
	ranks := 1
	for index := 1; index < limit; index++ {
		if !searchScoresWithinGap(results[0].Score, results[index].Score, searchFullUnitGapRatio) {
			break
		}
		ranks = index + 1
	}
	return ranks
}

// searchScoresWithinGap reports whether `next` is close enough to `top` that the ranking did not
// separate them. A non-positive top score carries no separation information, so it never claims one.
func searchScoresWithinGap(top, next, ratio float64) bool {
	if top <= 0 {
		return true
	}
	return (top-next)/top < ratio
}

// clipSearchUnitToCap picks the `cap`-line window of the unit [start,end] that is printed when the
// unit is too large to return whole. It is anchored at the unit's own start — the declaration and
// its first statements are what identify the unit — and slides only when the hit itself would fall
// outside that anchor, in which case the window is centred on the hit and clamped to the unit.
func clipSearchUnitToCap(start, end, focus, cap int) (int, int) {
	if cap <= 0 || end-start+1 <= cap {
		return start, end
	}
	if focus < start || focus > end {
		focus = start
	}
	clipStart := start
	if focus > start+cap-1 {
		clipStart = focus - cap/2
		if clipStart < start {
			clipStart = start
		}
		if clipStart+cap-1 > end {
			clipStart = end - cap + 1
		}
	}
	return clipStart, clipStart + cap - 1
}

// clipSearchUnitToBytes is the backstop a LINE cap cannot provide: a window of 25 lines is 700 bytes
// in ordinary source and 9.5 kB in a generated table (redis's src/commands.c packs one command per
// line, each ~400 bytes). Line caps are the right unit for a reader and the wrong unit for a budget,
// so both apply.
//
// Unlike the line cap it re-anchors on the HIT rather than on the unit's start: once the budget is
// this tight the declaration line is no longer affordable context, and the line the literal is
// actually on is the one thing that must survive. It grows outward from there while the budget allows,
// so the window stays contiguous and verbatim. A single line over budget is returned alone — one true
// line beats none.
func clipSearchUnitToBytes(lines []string, start, end, focus, maxBytes int) (int, int) {
	if maxBytes <= 0 || start < 1 || end > len(lines) || end < start {
		return start, end
	}
	if len(strings.Join(lines[start-1:end], "\n")) <= maxBytes {
		return start, end
	}
	if focus < start || focus > end {
		focus = start
	}
	low, high := focus, focus
	size := len(lines[focus-1])
	for {
		grew := false
		if high < end && size+1+len(lines[high]) <= maxBytes {
			size += 1 + len(lines[high])
			high++
			grew = true
		}
		if low > start && size+1+len(lines[low-2]) <= maxBytes {
			size += 1 + len(lines[low-2])
			low--
			grew = true
		}
		if !grew {
			return low, high
		}
	}
}

// CompleteSymbolSignal is searchCompleteSymbolSignal exported for renderers: a result carrying
// it is a whole callable and must never be abbreviated on the way out.
const CompleteSymbolSignal = searchCompleteSymbolSignal

// HeadWindowSignal is searchHeadWindowSignal exported for the same reason: the allocator spent budget
// widening this result to a readable window, and a renderer that then prints it as a locator throws
// those bytes away — measured on fmtlib__fmt-2457, where the 60-line window the allocator seated at
// rank 3 (1,709 B) was discarded and the rank came out as `include/fmt/ranges.h:682`.
const HeadWindowSignal = searchHeadWindowSignal

// searchEnclosure is the complete-body upgrade available for one ranked result: the true
// source span of the symbol enclosing the hit, taken from the graph's own symbol records,
// together with the file lines needed to materialize it. The zero value means no upgrade is
// available — unknown symbol, a container rather than a callable, an unreadable file, or a
// body past the line cap.
type searchEnclosure struct {
	start int
	end   int
	lines []string
	// symbol is the callable whose source the upgrade returns. A result that shows a whole
	// body must NAME that body: a region of a 3,000-line class carries the class's identity
	// from ranking, and reporting `class Foo` above the source of one method inside it is
	// wrong in the one field an agent uses to decide what it is looking at.
	symbol SymbolRecord
	// window marks an enclosure that is NOT a complete callable: a bounded read window centred
	// on the hit, used when no enclosable callable exists for a head rank (an unenclosable kind,
	// a body past the line cap, a hit outside its own symbol, a document/template). Such an
	// enclosure must never claim the complete-symbol signal — it is readable code, not a whole
	// body, and saying otherwise would be a lie in the one field an agent trusts.
	//
	// Why it exists, measured over 79 agent sessions: the gold file was present in the payload as
	// a LOCATOR ONLY (no code at all) in 20 of 74 sessions, 24 of those at rank <= 3, and 61
	// post-search Reads targeted a ranked file that carried no code. A rank-1 hit that shows two
	// lines is an instruction to spend a turn opening the file — ~17,000 tokens to recover what
	// ~700 bytes would have carried.
	window bool
	// forced marks an enclosure the CALLER demanded (--full-unit-top) rather than one the
	// allocator found affordable. A forced enclosure bypasses the enclosable-kind restriction, the
	// line cap and the "the snippet already covers it" short-circuit, and the allocator seats it
	// before it prices anything else. It is never produced unless --full-unit-top asked for it, so
	// the default payload is byte-for-byte unchanged.
	forced bool
	// unitStart/unitEnd are the unit's TRUE span, recorded only when searchFullUnitMaxLines clipped
	// the printed window out of a larger unit. They are what the elision note is rendered from; a
	// forced unit that fit whole leaves them zero.
	unitStart int
	unitEnd   int
}

func (enclosure searchEnclosure) available() bool {
	return enclosure.start > 0 && enclosure.end >= enclosure.start && len(enclosure.lines) >= enclosure.end
}

// searchEnclosableSymbolKind reports whether a symbol kind denotes a self-contained callable
// body — the unit an agent edits. Containers (classes, modules, types) are deliberately
// excluded: returning a whole class spends the budget on the members that are NOT the fix.
func searchEnclosableSymbolKind(kind string) bool {
	switch kind {
	case "function", "method", "constructor", "destructor", "closure", "lambda",
		"procedure", "subroutine", "accessor", "initializer", "operator", "macro":
		return true
	default:
		return false
	}
}

// enclosingCallableForResult resolves the callable that contains a result. The result's own
// symbol identity wins when it has one; otherwise the smallest enclosing callable in the
// same file is used, which is what recovers a body for region/sparse hits that carry no
// symbol of their own.
func enclosingCallableForResult(
	result SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
) (SymbolRecord, bool) {
	if result.SymbolID != "" {
		symbol, ok := symbolsByID[result.SymbolID]
		if ok && symbol.FilePath == result.FilePath && searchEnclosableSymbolKind(symbol.Kind) {
			return symbol, true
		}
	}
	return smallestSearchSymbolContainingLineWhere(
		symbolsByFile[result.FilePath], result.FocusLine,
		func(symbol SymbolRecord) bool { return searchEnclosableSymbolKind(symbol.Kind) },
	)
}

// enclosingUnitForResult resolves the unit a FORCED body is cut from. It is deliberately wider than
// enclosingCallableForResult: the callable wins when there is one, and otherwise ANY enclosing
// symbol will do — a class, a type declaration, a constant table, an object literal in a build
// script. That widening is the point of the flag. The opportunistic path must keep excluding
// containers (a whole class spends the head's budget on the members that are not the fix), but a
// caller who asked for the top unit by name has accepted that cost, and the measured misses are
// concentrated exactly there: on redis__redis-10095 two of five hits are container/declaration
// sites that the opportunistic path returns with no source at all.
func enclosingUnitForResult(
	result SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
) (SymbolRecord, bool) {
	if callable, ok := enclosingCallableForResult(result, symbolsByID, symbolsByFile); ok {
		return callable, true
	}
	if result.SymbolID != "" {
		if symbol, ok := symbolsByID[result.SymbolID]; ok && symbol.FilePath == result.FilePath {
			return symbol, true
		}
	}
	return smallestSearchSymbolContainingLineWhere(symbolsByFile[result.FilePath], result.FocusLine, nil)
}

// planSearchEnclosures computes, for every ranked result, the complete-body upgrade the
// allocator may spend budget on. Files are read through the shared content cache, so a
// result whose file was already hydrated for ranking costs no extra IO.
// forceRanks is the --full-unit-top depth: the first `forceRanks` results get their complete
// enclosing UNIT unconditionally (see the editability comment above), planned here so the allocator
// only has to seat what it is given.
func planSearchEnclosures(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	maxLines int,
	contextLines int,
	bodyHeadRanks int,
	windowLines int,
	forceRanks int,
) []searchEnclosure {
	if len(results) == 0 {
		return nil
	}
	if maxLines <= 0 {
		maxLines = defaultSearchEnclosureMaxLines
	}
	if bodyHeadRanks <= 0 {
		bodyHeadRanks = searchEnclosureHeadRanks
	}
	if forceRanks < 0 {
		forceRanks = 0
	}
	// A forced rank is planned even when it sits past the body head: the caller asked for that
	// rank's unit, and the body-head depth is a budget judgement about the OPPORTUNISTIC upgrade.
	planRanks := maxInt(bodyHeadRanks, forceRanks)
	enclosures := make([]searchEnclosure, len(results))
	fileLines := map[string][]string{}
	unreadable := map[string]bool{}
	for index, result := range results {
		// A rank the allocator can never upgrade needs no enclosure planned for it, and planning
		// one costs a file read. This is the only place that read happens, so bounding it here
		// bounds it everywhere.
		if index >= planRanks {
			continue
		}
		symbol, hasCallable := enclosingCallableForResult(result, symbolsByID, symbolsByFile)
		// The file has to be readable either way: a window needs its lines just as a body does.
		lines, cached := fileLines[result.FilePath]
		if !cached {
			if unreadable[result.FilePath] {
				continue
			}
			content, readable := read(result.FilePath)
			if !readable || strings.IndexByte(content, 0) >= 0 {
				unreadable[result.FilePath] = true
				continue
			}
			lines = strings.Split(content, "\n")
			fileLines[result.FilePath] = lines
		}
		if index < forceRanks {
			if forced, ok := planForcedSearchUnit(result, symbolsByID, symbolsByFile, lines); ok {
				enclosures[index] = forced
				continue
			}
			// No indexed symbol covers the hit at all. A forced rank must still not come back as
			// six lines, so it falls through to the bounded read window even when the caller did
			// not ask for one — the flag's promise is source at this rank, and a window is the
			// widest honest thing left. It is marked forced so the allocator seats it with the rest
			// of the prefix; `window` still governs what it CLAIMS, so it reports head-window and
			// never complete-symbol.
			if window, ok := planSearchHeadWindow(result, lines, maxInt(windowLines, searchHeadWindowLines)); ok {
				window.forced = true
				enclosures[index] = window
			}
			continue
		}
		if index >= bodyHeadRanks {
			continue
		}
		if !hasCallable {
			if windowLines > 0 {
				if window, ok := planSearchHeadWindow(result, lines, windowLines); ok {
					enclosures[index] = window
				}
			}
			continue
		}
		start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
		if start == 0 || end-start+1 > maxLines {
			// Too large to return whole. A head rank still must not come back as two lines, so
			// fall back to the bounded window rather than to a locator.
			if windowLines > 0 {
				if window, ok := planSearchHeadWindow(result, lines, windowLines); ok {
					enclosures[index] = window
				}
			}
			continue
		}
		// The symbol must actually contain the hit, and there must be something to gain:
		// a snippet that already spans the whole body needs no upgrade.
		if result.FocusLine < start || result.FocusLine > end {
			continue
		}
		if result.SnippetStartLine <= start && result.SnippetEndLine >= end {
			continue
		}
		// The margin is rank-1 only and never widens the reported symbol: `symbol` still names the
		// callable, so a padded body cannot misattribute itself to its neighbours. It is capped by
		// maxLines like any other body, so a padded body can never exceed the unpadded ceiling.
		if index == 0 && contextLines > 0 {
			padStart, padEnd := start-contextLines, end+contextLines
			if padStart < 1 {
				padStart = 1
			}
			if padEnd > len(lines) {
				padEnd = len(lines)
			}
			if padEnd-padStart+1 <= maxLines {
				start, end = padStart, padEnd
			}
		}
		enclosures[index] = searchEnclosure{start: start, end: end, lines: lines, symbol: symbol}
	}
	return enclosures
}

// planForcedSearchUnit builds the --full-unit-top enclosure for one rank: the complete span of the
// unit enclosing the hit, clipped only by searchFullUnitMaxLines.
//
// It differs from the opportunistic planner in every condition that planner applies. There is no
// enclosable-kind restriction (enclosingUnitForResult falls back to any symbol), no 160-line cap
// (an over-cap unit is clipped and reports the elision instead of degrading to a window), and no
// "the snippet already covers it" short-circuit — a result whose ranked snippet happens to equal its
// unit is still marked, because "this is the whole unit" is the fact the caller asked for and a
// payload that shows a complete function without saying so is read as a fragment. redis__redis-10095
// rank 1 is exactly that case: `lpopCommand` is five lines, printed whole, and unsignalled.
func planForcedSearchUnit(
	result SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	lines []string,
) (searchEnclosure, bool) {
	symbol, ok := enclosingUnitForResult(result, symbolsByID, symbolsByFile)
	if !ok {
		return searchEnclosure{}, false
	}
	start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
	if start == 0 {
		return searchEnclosure{}, false
	}
	// A forced unit must never print LESS source than the ranking already did. A symbol's recorded
	// span starts at its declaration, while a ranked region routinely starts a line or two above it
	// (the doc comment, a decorator, an attribute) — and that preamble is often the most useful thing
	// on the screen: on redis__redis-10095 rank 1 it is `/* LPOP <key> [count] */`, the one line that
	// states the contract the bug is about. So the unit is UNIONED with what was already seated; the
	// safety cap below then applies to the union, exactly as it would to the bare unit.
	if snippetStart := result.SnippetStartLine; snippetStart >= 1 && snippetStart < start {
		start = snippetStart
	}
	if snippetEnd := result.SnippetEndLine; snippetEnd > end && snippetEnd <= len(lines) {
		end = snippetEnd
	}
	enclosure := searchEnclosure{lines: lines, symbol: symbol, forced: true}
	if end-start+1 > searchFullUnitMaxLines {
		enclosure.unitStart, enclosure.unitEnd = start, end
		start, end = clipSearchUnitToCap(start, end, result.FocusLine, searchFullUnitMaxLines)
	}
	enclosure.start, enclosure.end = start, end
	return enclosure, true
}

// planSearchHeadWindow builds the fallback read window for a head rank that has no enclosable
// callable: a document or template, an unenclosable container kind, a body past the line cap, or a
// hit that lies outside its own recorded symbol span. It centres `windowLines` on the focus line and
// clamps to the file, so the snippet stays a verbatim, correctly-numbered slice.
//
// It returns false when the result already shows at least this much source — a window must only ever
// ADD readable lines, never re-cut a snippet the ranker already made wider.
func planSearchHeadWindow(result SearchResult, lines []string, windowLines int) (searchEnclosure, bool) {
	if len(lines) == 0 || windowLines <= 0 {
		return searchEnclosure{}, false
	}
	focus := result.FocusLine
	if focus <= 0 {
		focus = result.SnippetStartLine
	}
	if focus <= 0 {
		return searchEnclosure{}, false
	}
	half := windowLines / 2
	start, end := focus-half, focus+half
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return searchEnclosure{}, false
	}
	if have := result.SnippetEndLine - result.SnippetStartLine + 1; have >= end-start+1 {
		return searchEnclosure{}, false
	}
	return searchEnclosure{start: start, end: end, lines: lines, window: true}, true
}

// widenSearchResultToEnclosure rewrites a result to carry the complete body of its enclosing
// symbol. The reported region is widened along with the snippet so the response invariant
// StartLine <= SnippetStartLine <= SnippetEndLine <= EndLine keeps holding.
func widenSearchResultToEnclosure(result SearchResult, enclosure searchEnclosure) SearchResult {
	if !enclosure.available() {
		return result
	}
	result.StartLine = minInt(maxInt(1, result.StartLine), enclosure.start)
	result.EndLine = maxInt(result.EndLine, enclosure.end)
	result.SnippetStartLine = enclosure.start
	result.SnippetEndLine = enclosure.end
	result.Snippet = strings.Join(enclosure.lines[enclosure.start-1:enclosure.end], "\n")
	result.FocusLine = minInt(maxInt(result.FocusLine, result.StartLine), result.EndLine)
	if enclosure.window {
		// A window is readable code but not a whole callable. It gets its own signal so an agent
		// (and the tests) can tell the two apart, and it deliberately does NOT get
		// complete-symbol: that signal is the promise "you need no follow-up read", and a window
		// cannot make it.
		result.Signals = appendUnique(result.Signals, searchHeadWindowSignal)
		return result
	}
	// A forced unit the safety cap clipped is source the reader can act on, but it is not the whole
	// unit, so it reports the elision and withholds complete-symbol for the same reason a window does.
	clipped := enclosure.forced && enclosure.unitStart > 0 &&
		(enclosure.unitStart < enclosure.start || enclosure.unitEnd > enclosure.end)
	if enclosure.forced {
		result.Signals = appendUnique(result.Signals, searchFullUnitSignal)
	}
	if clipped {
		result.UnitStartLine, result.UnitEndLine = enclosure.unitStart, enclosure.unitEnd
		result.Signals = appendUnique(result.Signals, searchFullUnitElidedSignal)
	} else {
		result.Signals = appendUnique(result.Signals, searchCompleteSymbolSignal)
	}
	if enclosure.symbol.ID != "" && enclosure.symbol.ID != result.SymbolID {
		result.Kind = enclosure.symbol.Kind
		result.SymbolID = enclosure.symbol.ID
		result.SymbolName = enclosure.symbol.Name
		result.QualifiedName = enclosure.symbol.QualifiedName
		result.Signature = enclosure.symbol.Signature
	}
	return result
}

// tersifySearchResult re-slices a result's snippet down to at most maxLines lines around its
// focus. Re-slicing by LINE (rather than truncating bytes) keeps the snippet a verbatim copy
// of the file, so the line numbers a demoted result reports stay trustworthy.
func tersifySearchResult(result SearchResult, maxLines int) SearchResult {
	if maxLines <= 0 || result.Snippet == "" {
		return result
	}
	lines := strings.Split(result.Snippet, "\n")
	if len(lines) <= maxLines || len(lines) != result.SnippetEndLine-result.SnippetStartLine+1 {
		return result
	}
	start, end := focusedSnippetRegion(result.SnippetStartLine, result.SnippetEndLine, result.FocusLine, maxLines)
	offset := start - result.SnippetStartLine
	result.Snippet = strings.Join(lines[offset:offset+(end-start+1)], "\n")
	result.SnippetStartLine, result.SnippetEndLine = start, end
	return result
}

// countBudgetTruncatedResults reports how many seated results carry LESS than the ranker
// produced for them, after both the byte fitter and the snippet allocator have run. A result
// the allocator grew to a complete symbol body is not truncated even though it changed, which
// is why the count is derived from the surviving span rather than from string inequality.
func countBudgetTruncatedResults(ranked, seated []SearchResult) int {
	byRank := make(map[int]SearchResult, len(ranked))
	for _, result := range ranked {
		byRank[result.Rank] = result
	}
	truncated := 0
	for _, result := range seated {
		original, ok := byRank[result.Rank]
		if !ok {
			continue
		}
		// A completed body may also re-report its signature, because it now shows a different
		// (narrower) symbol than ranking attributed to it. That is an upgrade, not truncation,
		// so only span loss counts for such a result.
		if hasSearchSignal(result, searchCompleteSymbolSignal) {
			if result.SnippetStartLine > original.SnippetStartLine ||
				result.SnippetEndLine < original.SnippetEndLine {
				truncated++
			}
			continue
		}
		if result.Signature != original.Signature ||
			result.SnippetStartLine > original.SnippetStartLine ||
			result.SnippetEndLine < original.SnippetEndLine {
			truncated++
		}
	}
	return truncated
}

// searchResultsSize is the serialized size of a result slice given each element's own
// serialized size: json.Marshal writes "[" + elements joined by "," + "]".
func searchResultsSize(sizes []int) int {
	if len(sizes) == 0 {
		return len("[]")
	}
	total := 2 + len(sizes) - 1
	for _, size := range sizes {
		total += size
	}
	return total
}

// allocateSearchSnippets re-spends the bytes a payload already occupies so the results an
// agent will actually read come back complete.
//
// Contract:
//   - Results are never dropped here; only snippet size is allocated. Dropping stays the
//     job of fitSearchResultsToBudget, which reports it in results_dropped_by_budget.
//   - The payload may grow by at most `growth` bytes beyond what the ranker produced, and
//     never past `hardBudget` (the --max-context-bytes ceiling; <= 0 means no ceiling).
//     Everything else is funded by demoting the tail.
//   - Complete bodies are applied greedily in rank order while the plan fits, so a symbol
//     too large for the allowance degrades to its focused window instead of blowing it.
//   - Only the head (`headRanks`) is eligible for a body. Below it the agent is deciding
//     "is this worth opening", not editing, so a whole callable there spends the head's
//     budget on code that will not be read.
//   - Among the plans that deliver the most complete bodies, the CHEAPEST is chosen, so the
//     tail pays for a body before the growth allowance does and demotion never happens
//     without buying something. Ties go to the plan that demotes fewest results.
func allocateSearchSnippets(
	results []SearchResult,
	enclosures []searchEnclosure,
	hardBudget, growth, headRanks, tailLines int,
) ([]SearchResult, int, int) {
	if len(results) == 0 || len(enclosures) != len(results) {
		return results, 0, 0
	}
	if headRanks < 0 {
		headRanks = 0
	}
	if growth < 0 {
		growth = 0
	}
	// A forced unit is not a candidate the allocator prices; it is a decision the caller already
	// made. It therefore gets its own seating pass, which pays out of the tail before it consults
	// the growth allowance, and only then hands the remaining budget back to the greedy fill below.
	if forcedEnd := forcedSearchEnclosureEnd(enclosures); forcedEnd > 0 {
		return allocateForcedSearchUnits(results, enclosures, hardBudget, growth, headRanks, tailLines, forcedEnd)
	}
	sizes := make([]int, len(results))
	for index := range results {
		sizes[index] = serializedSearchResultBytes(results[index])
	}
	budget := searchResultsSize(sizes) + growth
	if hardBudget > 0 && hardBudget < budget {
		budget = hardBudget
	}

	// Demoting deeper frees bytes, but a greedy fill is not monotone in the cut (a body that
	// becomes affordable early can crowd out two cheaper ones later), so no single cut is
	// uniformly best. Score every cut on rank-discounted value — completing rank 1 is worth
	// more than completing ranks 6 and 7, because rank 1 is what the agent reads — and break
	// ties on total size, so the tail pays before the growth allowance does and demotion
	// never happens without buying something.
	var best []SearchResult
	bestValue, bestBodies, bestSize, bestDemoted := 0.0, 0, 0, 0
	// A ranking shorter than the protected head still needs the no-demotion plan evaluated,
	// hence the clamp: without it a 1- or 2-result payload would never be allocated at all.
	for demoteFrom := len(results); demoteFrom >= minInt(headRanks, len(results)); demoteFrom-- {
		plan, bodies, windows, size := planWithDemotionFrom(results, enclosures, budget, headRanks, tailLines, demoteFrom)
		// A plan that delivered only WINDOWS is still worth taking: a head rank with 60 readable
		// lines beats the same rank with two. So the "did this plan buy anything" test counts both,
		// while the reported body count and the plan score still count complete bodies only.
		if bodies+windows == 0 {
			continue
		}
		value := completeBodyValue(plan) + searchHeadWindowValue(plan)
		if best == nil || value > bestValue || (value == bestValue && size < bestSize) {
			best, bestValue, bestBodies, bestSize = plan, value, bodies, size
			bestDemoted = maxInt(0, len(results)-maxInt(demoteFrom, headRanks))
		}
	}
	if best == nil {
		return results, 0, 0
	}
	return best, bestBodies, bestDemoted
}

// forcedSearchEnclosureEnd reports how far the forced prefix reaches: one past the deepest rank
// carrying a usable forced enclosure, or 0 when --full-unit-top was not asked for (or resolved
// nothing at any rank, in which case the default allocator runs unchanged).
func forcedSearchEnclosureEnd(enclosures []searchEnclosure) int {
	end := 0
	for index, enclosure := range enclosures {
		if enclosure.forced && enclosure.available() {
			end = index + 1
		}
	}
	return end
}

// allocateForcedSearchUnits seats the --full-unit-top bodies and then spends what is left exactly as
// the default allocator would.
//
// The order is the whole contract, and it is the inverse of the opportunistic allocator's:
//
//  1. The forced units go in FIRST, unpriced. They are the reason the caller passed the flag.
//  2. If that breaches --max-context-bytes, the TAIL pays — deepest rank first, down to (but never
//     into) the forced prefix. A tail result's job is to say "also look here", and path, line and
//     symbol name all survive demotion; the head's job is to deliver text an agent can edit.
//  3. If the ceiling is still breached with the whole tail reduced to locators, the ceiling WINS:
//     forced ranks hand their ranked snippets back, deepest forced rank first, so rank 1 is the
//     last thing in the payload to yield. `--max-context-bytes` is a promise, not a preference.
//  4. Whatever budget remains funds the ordinary head bodies at the ranks the flag did not cover,
//     so turning the flag on can only ever ADD source, never remove an upgrade that already fit.
func allocateForcedSearchUnits(
	results []SearchResult,
	enclosures []searchEnclosure,
	hardBudget, growth, headRanks, tailLines, forcedEnd int,
) ([]SearchResult, int, int) {
	plan := append([]SearchResult(nil), results...)
	sizes := make([]int, len(plan))
	for index := range plan {
		sizes[index] = serializedSearchResultBytes(plan[index])
	}
	rankedSize := searchResultsSize(sizes)

	bodies := 0
	// Every available enclosure inside the prefix is seated, not only the forced ones: a forced rank
	// whose unit did not resolve carries the fallback read window instead, and skipping it here would
	// leave that rank with the six-line snippet the flag exists to remove.
	for index := 0; index < forcedEnd; index++ {
		if !enclosures[index].available() {
			continue
		}
		plan[index] = widenSearchResultToEnclosure(results[index], enclosures[index])
		sizes[index] = serializedSearchResultBytes(plan[index])
		if hasSearchSignal(plan[index], searchCompleteSymbolSignal) {
			bodies++
		}
	}
	total := searchResultsSize(sizes)
	demoted := 0
	if hardBudget > 0 {
		for index := len(plan) - 1; index >= forcedEnd && total > hardBudget; index-- {
			terse := tersifySearchResult(plan[index], tailLines)
			size := serializedSearchResultBytes(terse)
			if size >= sizes[index] {
				continue
			}
			total += size - sizes[index]
			plan[index], sizes[index] = terse, size
			demoted++
		}
		for index := forcedEnd - 1; index >= 0 && total > hardBudget; index-- {
			size := serializedSearchResultBytes(results[index])
			if size >= sizes[index] {
				continue
			}
			if hasSearchSignal(plan[index], searchCompleteSymbolSignal) {
				bodies--
			}
			total += size - sizes[index]
			plan[index], sizes[index] = results[index], size
		}
	}
	// The remaining head ranks are funded from the same allowance the default allocator uses, floored
	// at what the forced units already cost so step 3's verdict is never re-litigated here.
	budget := rankedSize + growth
	if hardBudget > 0 && hardBudget < budget {
		budget = hardBudget
	}
	if budget < total {
		budget = total
	}
	for index := forcedEnd; index < minInt(headRanks, len(plan)); index++ {
		if !enclosures[index].available() {
			continue
		}
		widened := widenSearchResultToEnclosure(results[index], enclosures[index])
		size := serializedSearchResultBytes(widened)
		if total-sizes[index]+size > budget {
			continue
		}
		total += size - sizes[index]
		plan[index], sizes[index] = widened, size
		if !enclosures[index].window {
			bodies++
		}
	}
	return plan, maxInt(0, bodies), demoted
}

// searchHeadWindowValue scores the read windows in a plan. A window is worth strictly less than a
// complete body at the same rank — it does not carry the "no follow-up read needed" promise — so it
// is discounted to a quarter. That keeps a plan that completes a body always preferred over one that
// merely widens the same rank to a window, while still ranking a window above nothing.
func searchHeadWindowValue(plan []SearchResult) float64 {
	value := 0.0
	for index, result := range plan {
		if hasSearchSignal(result, searchHeadWindowSignal) {
			value += 0.25 / float64(index+1)
		}
	}
	return value
}

// completeBodyValue scores an allocation plan by which ranks it managed to complete, with the
// value of a complete body decaying as 1/rank: a whole body at rank 1 removes a read the agent
// was almost certainly going to make, one at rank 8 probably does not.
func completeBodyValue(plan []SearchResult) float64 {
	value := 0.0
	for index, result := range plan {
		if hasSearchSignal(result, searchCompleteSymbolSignal) {
			value += 1 / float64(index+1)
		}
	}
	return value
}

func hasSearchSignal(result SearchResult, want string) bool {
	for _, signal := range result.Signals {
		if signal == want {
			return true
		}
	}
	return false
}

// planWithDemotionFrom builds one allocation plan: results from index demoteFrom onwards are
// demoted to locators, then complete bodies are applied greedily by rank while the payload
// fits the budget. It returns the plan, the number of complete bodies it delivers, and its
// serialized size.
func planWithDemotionFrom(
	results []SearchResult,
	enclosures []searchEnclosure,
	budget, headRanks, tailLines, demoteFrom int,
) ([]SearchResult, int, int, int) {
	plan := append([]SearchResult(nil), results...)
	cut := maxInt(demoteFrom, headRanks)
	sizes := make([]int, len(plan))
	for index := range plan {
		if index >= cut {
			plan[index] = tersifySearchResult(plan[index], tailLines)
		}
		sizes[index] = serializedSearchResultBytes(plan[index])
	}
	total := searchResultsSize(sizes)
	bodies, windows := 0, 0
	// The body upgrade is a head-only privilege; see allocateSearchSnippets' contract.
	bodyLimit := headRanks
	if bodyLimit > len(plan) {
		bodyLimit = len(plan)
	}
	for index := 0; index < bodyLimit; index++ {
		if !enclosures[index].available() {
			continue
		}
		widened := widenSearchResultToEnclosure(results[index], enclosures[index])
		size := serializedSearchResultBytes(widened)
		if total-sizes[index]+size > budget {
			continue
		}
		total += size - sizes[index]
		plan[index], sizes[index] = widened, size
		// A window is readable code but not a complete body. Counting it as a body would
		// inflate stats.complete_symbol_snippets, which is the number every measurement of
		// this allocator is read against.
		if enclosures[index].window {
			windows++
		} else {
			bodies++
		}
	}
	return plan, bodies, windows, total
}
