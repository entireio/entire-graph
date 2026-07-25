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
const (
	// defaultSearchEnclosureMaxLines caps the complete-body upgrade. A symbol larger than
	// this is a context dump in its own right: returning it whole would consume the budget
	// an agent needs for the rest of the ranking, and it is cheaper to read such a file
	// directly. Bigger symbols keep their focused window (graceful degradation, never a
	// budget blow-out).
	defaultSearchEnclosureMaxLines = 160

	// searchEnclosureHeadRanks is the number of leading results never demoted to a locator.
	searchEnclosureHeadRanks = 3

	// searchEnclosureTailSnippetLines is the locator window a demoted tail result keeps.
	// Two lines still show the matching line and one line of its context.
	searchEnclosureTailSnippetLines = 2

	// searchEnclosureGrowthBytes is how far a payload may grow to complete a body the agent
	// would otherwise have to re-read. Completing bodies is funded FIRST by demoting the
	// tail; this allowance covers only the remainder, so search stays inside roughly the
	// same per-call cost instead of expanding to fill whatever ceiling it was given.
	// (Same reasoning as the top-k/snippet-line defaults above it: a search payload is
	// re-read by every later turn, so its size is a recurring cost, not a one-off.)
	searchEnclosureGrowthBytes = 1024

	// searchCompleteSymbolSignal marks a result whose snippet is the complete source of its
	// enclosing symbol, so an agent (and the tests) can tell a whole body from a window.
	searchCompleteSymbolSignal = "complete-symbol"
)

// searchEnclosure is the complete-body upgrade available for one ranked result: the true
// source span of the symbol enclosing the hit, taken from the graph's own symbol records,
// together with the file lines needed to materialize it. The zero value means no upgrade is
// available — unknown symbol, a container rather than a callable, an unreadable file, or a
// body past the line cap.
type searchEnclosure struct {
	start int
	end   int
	lines []string
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
) []searchEnclosure {
	if len(results) == 0 {
		return nil
	}
	if maxLines <= 0 {
		maxLines = defaultSearchEnclosureMaxLines
	}
	enclosures := make([]searchEnclosure, len(results))
	fileLines := map[string][]string{}
	unreadable := map[string]bool{}
	for index, result := range results {
		symbol, ok := enclosingCallableForResult(result, symbolsByID, symbolsByFile)
		if !ok {
			continue
		}
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
		start, end := clampRegion(symbol.StartLine, symbol.EndLine, len(lines))
		if start == 0 || end-start+1 > maxLines {
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
		enclosures[index] = searchEnclosure{start: start, end: end, lines: lines}
	}
	return enclosures
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
	result.Signals = appendUnique(result.Signals, searchCompleteSymbolSignal)
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
		plan, bodies, size := planWithDemotionFrom(results, enclosures, budget, headRanks, tailLines, demoteFrom)
		if bodies == 0 {
			continue
		}
		value := completeBodyValue(plan)
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

// completeBodyValue scores an allocation plan by which ranks it managed to complete, with the
// value of a complete body decaying as 1/rank: a whole body at rank 1 removes a read the agent
// was almost certainly going to make, one at rank 8 probably does not.
func completeBodyValue(plan []SearchResult) float64 {
	value := 0.0
	for index, result := range plan {
		for _, signal := range result.Signals {
			if signal == searchCompleteSymbolSignal {
				value += 1 / float64(index+1)
				break
			}
		}
	}
	return value
}

// planWithDemotionFrom builds one allocation plan: results from index demoteFrom onwards are
// demoted to locators, then complete bodies are applied greedily by rank while the payload
// fits the budget. It returns the plan, the number of complete bodies it delivers, and its
// serialized size.
func planWithDemotionFrom(
	results []SearchResult,
	enclosures []searchEnclosure,
	budget, headRanks, tailLines, demoteFrom int,
) ([]SearchResult, int, int) {
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
	bodies := 0
	for index := range plan {
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
		bodies++
	}
	return plan, bodies, total
}
