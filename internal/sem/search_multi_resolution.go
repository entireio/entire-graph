package sem

import "sort"

// MULTI-RESOLUTION RETRIEVAL
//
// Ranking returns at most one result per file, because for code search a second region of a file
// the caller can already see is worth less than the first region of a file it cannot. That rule is
// right for code and wrong for prose: one markdown file is one document — a design note, a meeting
// log, a chat session — and the answer to a question about it is routinely spread across several
// distant regions of the SAME file. For those files the ranker already computes the extra regions
// and attaches them to their parent as `passages` (search_prose_passage.go), but a consumer that
// reads `results[]` and nothing else never sees them: the parent result carries one span, and the
// rest of the evidence sits in a field it did not ask about.
//
// So when the caller asked for more results than the ranking could fill with distinct files, the
// leftover slots are spent promoting those already-computed regions to first-class results. Two
// resolutions of the same document are then returned side by side: the parent's own span, which
// keeps the surrounding context, and the finer spans, which are the precise evidence.
//
// The rule that keeps this from becoming `--deep` (which traded breadth for depth and measured
// worse) is that promotion is STRICTLY ADDITIVE: it runs after selection, only into slots the
// distinct-file ranking left empty, and only within the byte ceiling the caller set. It can never
// displace a file. `--top-k` still means "number of returned regions", and when the ranking already
// filled it, the output is byte-identical to single-resolution.
//
// Ordinary code search is untouched: no code result carries passages, so there is nothing to
// promote.

const proseResolutionSignal = "retrieval_mode=prose-passage"

// prosePromotion identifies one passage of one parent result.
type prosePromotion struct {
	parent  int
	passage int
}

// planProseResolutionPromotions orders promotions round-robin by depth, so a single long document
// cannot spend every free slot before a second document has contributed anything. A passage whose
// text the payload already prints is not promotable at all — its slot goes to the next passage in
// the round robin instead.
func planProseResolutionPromotions(results []SearchResult, limit int) []prosePromotion {
	if limit <= 0 {
		return nil
	}
	deepest := 0
	for index := range results {
		if count := len(results[index].Passages); count > deepest {
			deepest = count
		}
	}
	order := make([]prosePromotion, 0, limit)
	for depth := 0; depth < deepest && len(order) < limit; depth++ {
		for index := range results {
			if depth >= len(results[index].Passages) {
				continue
			}
			if prosePassageAlreadyPrinted(results, results[index].FilePath, results[index].Passages[depth]) {
				continue
			}
			order = append(order, prosePromotion{parent: index, passage: depth})
			if len(order) == limit {
				break
			}
		}
	}
	return order
}

// prosePassageAlreadyPrinted reports whether the passage's span lies entirely inside the PRINTED
// span of some result from the same file, so promoting it would return its text twice.
//
// This is the promotion-side counterpart of dropContainedProseResults, and it exists for the same
// reason: dropSelectedProsePassages keeps passages disjoint from the ranked spans as SELECTED, and
// allocateProseParentPassages re-checks a passage only against its OWN parent's grown window — a
// window grown on a DIFFERENT result of the same file is checked by neither, so a passage planned
// for a distant section can sit inside the head result's grown window. The passage stays attached
// to its parent (that field's bytes were already fitted); it just cannot spend a result slot on
// lines the payload already shows.
func prosePassageAlreadyPrinted(results []SearchResult, path string, passage SearchPassage) bool {
	for index := range results {
		result := results[index]
		if result.FilePath != path || result.SnippetStartLine <= 0 || result.SnippetEndLine < result.SnippetStartLine {
			continue
		}
		if result.SnippetStartLine <= passage.StartLine && passage.EndLine <= result.SnippetEndLine {
			return true
		}
	}
	return false
}

// applyProseResolutionPromotions materializes the first `count` planned promotions. The promoted
// passages leave their parent, so no region is returned twice, and the parents keep the passages
// that were not promoted (a suffix of a source-ordered, non-overlapping list stays source-ordered
// and non-overlapping, which is what SearchResponse.Validate requires).
func applyProseResolutionPromotions(results []SearchResult, order []prosePromotion, count int) []SearchResult {
	promotedFrom := make([]map[int]bool, len(results))
	for _, promotion := range order[:count] {
		if promotedFrom[promotion.parent] == nil {
			promotedFrom[promotion.parent] = map[int]bool{}
		}
		promotedFrom[promotion.parent][promotion.passage] = true
	}
	expanded := make([]SearchResult, 0, len(results)+count)
	for index := range results {
		parent := results[index]
		if promotedFrom[index] != nil {
			kept := make([]SearchPassage, 0, len(parent.Passages))
			for passageIndex, passage := range parent.Passages {
				if !promotedFrom[index][passageIndex] {
					kept = append(kept, passage)
				}
			}
			if len(kept) == 0 {
				kept = nil
			}
			parent.Passages = kept
		}
		expanded = append(expanded, parent)
	}
	for _, promotion := range order[:count] {
		expanded = append(expanded, proseResolutionResult(results[promotion.parent], results[promotion.parent].Passages[promotion.passage]))
	}
	// results[] is documented as descending by score, and consumers truncate on it. Appending
	// promotions would break that: a promotion carries its parent's score, so a passage of a
	// high-scoring document landed after every low-scoring parent and the array stopped being
	// monotone. Re-sorting restores the invariant, and a STABLE sort is what makes it safe --
	// a promotion ties with its parent exactly, so it settles immediately after that parent
	// instead of anywhere else in the tie. Set membership is untouched, so promotion stays
	// strictly additive; only the order it is reported in changes.
	sort.SliceStable(expanded, func(i, j int) bool { return expanded[i].Score > expanded[j].Score })
	for index := range expanded {
		expanded[index].Rank = index + 1
	}
	return expanded
}

// proseResolutionResult turns one passage into a result. It inherits its parent's file, language
// and score — it is the same document and the same relevance evidence at a finer resolution, and a
// score invented for it here would be a number no ranking produced. Symbol identity is dropped:
// the passage is a region of the document, not a definition of the parent's symbol, and claiming
// otherwise would make two results advertise the same symbol at different lines.
func proseResolutionResult(parent SearchResult, passage SearchPassage) SearchResult {
	signals := make([]string, 0, len(parent.Signals)+1)
	signals = append(signals, parent.Signals...)
	return SearchResult{
		Score:            parent.Score,
		FilePath:         parent.FilePath,
		StartLine:        passage.StartLine,
		EndLine:          passage.EndLine,
		FocusLine:        passage.FocusLine,
		SnippetStartLine: passage.StartLine,
		SnippetEndLine:   passage.EndLine,
		Language:         parent.Language,
		Signals:          appendUnique(signals, proseResolutionSignal),
		Section:          parent.Section,
		Snippet:          passage.Snippet,
	}
}

// expandProseResolution spends the result slots the distinct-file ranking left empty on the finer
// regions it already computed, without breaching the caller's byte ceiling.
func expandProseResolution(results []SearchResult, topK, budget int) []SearchResult {
	if topK <= 0 || len(results) >= topK {
		return results
	}
	order := planProseResolutionPromotions(results, topK-len(results))
	if len(order) == 0 {
		return results
	}
	if budget <= 0 {
		return applyProseResolutionPromotions(results, order, len(order))
	}
	// Serialized size is strictly increasing in count: a promotion always moves a passage out of a
	// `passages` entry and into a full result record, which is strictly larger. The largest fitting
	// count is therefore found by bisection in ~log2(n) serializations rather than by materializing
	// and marshalling the whole result set once per candidate count.
	low, high := 0, len(order)
	for low < high {
		mid := (low + high + 1) / 2
		if serializedSearchResultBytes(applyProseResolutionPromotions(results, order, mid)) <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 {
		return results
	}
	return applyProseResolutionPromotions(results, order, low)
}
