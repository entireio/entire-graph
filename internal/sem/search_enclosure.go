package sem

import "strings"

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

// CompleteSymbolSignal is searchCompleteSymbolSignal exported for renderers: a result carrying
// it is a whole callable and must never be abbreviated on the way out.
const CompleteSymbolSignal = searchCompleteSymbolSignal

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

// planSearchEnclosures computes, for every ranked result, the complete-body upgrade the
// allocator may spend budget on. Files are read through the shared content cache, so a
// result whose file was already hydrated for ranking costs no extra IO.
func planSearchEnclosures(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	maxLines int,
	contextLines int,
	bodyHeadRanks int,
	windowLines int,
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
	enclosures := make([]searchEnclosure, len(results))
	fileLines := map[string][]string{}
	unreadable := map[string]bool{}
	for index, result := range results {
		// A rank the allocator can never upgrade needs no enclosure planned for it, and planning
		// one costs a file read. This is the only place that read happens, so bounding it here
		// bounds it everywhere.
		if index >= bodyHeadRanks {
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
	start, end := focus-half, focus-half+windowLines-1
	if start < 1 {
		start = 1
		end = minInt(len(lines), start+windowLines-1)
	}
	if end > len(lines) {
		end = len(lines)
		start = maxInt(1, end-windowLines+1)
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
	// Record the enclosing symbol's true extent whenever we know it, so a shard can announce what it
	// is a shard OF. Harmless when the body was returned whole: the bounds then equal the snippet's.
	if enclosure.symbol.StartLine > 0 && enclosure.symbol.EndLine >= enclosure.symbol.StartLine {
		result.SymbolStartLine = enclosure.symbol.StartLine
		result.SymbolEndLine = enclosure.symbol.EndLine
	}
	if enclosure.window {
		// A window is readable code but not a whole callable. It gets its own signal so an agent
		// (and the tests) can tell the two apart, and it deliberately does NOT get
		// complete-symbol: that signal is the promise "you need no follow-up read", and a window
		// cannot make it.
		result.Signals = appendUnique(result.Signals, searchHeadWindowSignal)
		return result
	}
	result.Signals = appendUnique(result.Signals, searchCompleteSymbolSignal)
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
