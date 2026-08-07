package sem

import (
	"sort"
	"strings"
)

// Same-file span merging
// ======================
//
// The ranking is allowed to return several hits from ONE file, and when it does they come back
// as separate blocks with a hole between them. That hole is what an agent pays to close.
//
// Measured across 113 real Opus search payloads: 45 of them (40%) carry at least two ranked hits
// in the same file whose printed spans are less than 40 lines apart (R30Oeg 12/25, R30OegBEST
// 12/30, R30OegV9 9/29, C15opeg_r1 12/29). Two independent lenses caught what that costs.
//
//   - Read side. Of 53 answered pre-edit Read calls only 41 carry a line range, and of those only
//     22 (53.7%) overlap the payload snippet at all; the payload covers a mean 18.2% of the
//     requested span. The nominal 62.0% read coverage is therefore worth ~33% in practice — the
//     agent is asking for the surrounding region, not for the block it already has.
//   - Suite level. The tool makes +19.6% MORE Read calls than the no-tool baseline while total
//     tool calls fall 14.5% (n=55 paired instances). Bodies removed greps and added reads.
//
// The call-by-call case is fluentd-3328: gold sat at rank 1 (in_tail.rb:395-425, score 135.8,
// complete-symbol, and the agent said so — "Top hit is exact"), and the session still cost 1.64x
// the baseline, because ranks 1/2/3 were three DISJOINT spans of one file (349-360, 395-425,
// 427-443) and turn 2 was `sed -n '200,270p'; sed -n '330,470p'` — 6.9 kB re-reading a contiguous
// superset of what the payload already held. pylint-7080 turn 2 is the same shape: `cat
// pylint/lint/expand_modules.py`, re-fetching rank 2 verbatim.
//
// So when two printed bodies of one file are close enough that the reader would bridge them
// anyway, the gap is filled and the two blocks become ONE span, labelled with the ranks it
// absorbed and with an explicit "nothing elided" — because the whole reason the agent re-reads is
// that it cannot tell whether the hole hides something.
//
// Three bounds, and each one is load-bearing rather than decorative. Filling gaps ADDS bytes, and
// under a fixed ceiling added bytes can evict the last ranked hit — which is how a payload that
// already works (Haiku, -25.0%) would lose recall:
//
//  1. searchSpanMergeMaxGapLines caps how far apart two hits may be to be bridged at all.
//  2. searchSpanMergeMaxSpanLines caps the merged span, so a chain of near hits cannot walk the
//     length of a file. (On fluentd-3328 this is what keeps 349-425 and 427-443 apart: bridging
//     all three would be 95 lines.)
//  3. Every merge is costed against the payload's own byte ceiling BEFORE it is committed, one
//     merge at a time. A merge that would not fit simply does not happen, so no ranked hit is
//     ever evicted to pay for one.
//
// And the property that makes the change safe to measure against an unmerged arm: merging never
// REORDERS. A run's survivor is its best-ranked member, survivors keep their relative order, and
// a merged-away hit is inside the span that replaced it — so the rank at which a file first
// appears can only stay the same or improve, never worsen.
const (
	// searchSpanMergeMaxGapLines is the largest hole between two printed spans of one file that
	// is worth filling. Sized from what the agents actually asked for: over 128 post-search reads
	// the median requested window was 50 lines and the median gap from the printed body 42 lines
	// (the same measurement that sized searchHeadWindowLines), so 40 lines is the distance at
	// which a reader is already going to pull both blocks in one range read.
	searchSpanMergeMaxGapLines = 40

	// searchSpanMergeMaxSpanLines caps the merged span. Two complete bodies plus the bridge is a
	// region an agent reads; half a file is a context dump, and the ranking is not evidence that
	// everything between hit 1 and hit 5 is relevant.
	searchSpanMergeMaxSpanLines = 80

	// searchSpanMergedSignal marks a result whose snippet is a contiguous region covering more
	// than one ranked hit. It is additive to complete-symbol rather than a replacement for it:
	// the span still contains the whole callable that earned the body, so the "no follow-up read
	// needed" promise is strictly stronger, and the renderers that key off complete-symbol must
	// keep printing the block in full.
	searchSpanMergedSignal = "contiguous-span"
)

// mergeSameFileSearchSpans folds runs of near same-file results into one contiguous span each.
//
// It returns the rewritten ranking, how many spans were merged, and how many complete-symbol
// results were absorbed (the caller owes that subtraction to stats.CompleteSymbols, which was
// counted before this pass ran).
//
// Only results that already carry complete-symbol take part. That is deliberate and not merely
// conservative: complete-symbol is the one signal every renderer prints in FULL regardless of
// depth, so restricting the pass to it guarantees the merged header always describes text the
// reader is actually given. Merging a tail locator in would print a range whose body is not
// there — the exact failure ("the payload says 349-443 but shows me two lines") this pass exists
// to remove — and it would spend head bytes on ranks measured not to be read.
//
// budget is the --max-context-bytes ceiling (<= 0 means none). It is checked against the WHOLE
// prospective ranking, not against the merge's own delta, because the ceiling the response is
// validated on is a total.
func mergeSameFileSearchSpans(results []SearchResult, read contentReader, budget int) ([]SearchResult, int, int) {
	if len(results) < 2 || read == nil {
		return results, 0, 0
	}
	runs := planSameFileSearchSpans(results)
	if len(runs) == 0 {
		return results, 0, 0
	}
	fileLines := map[string][]string{}
	unreadable := map[string]bool{}
	// The working state is expressed in ORIGINAL indexes rather than in a rewritten slice,
	// because committing a merge renumbers the ranking: a second run identified by pre-merge
	// rank would then address the wrong result.
	keep := make([]int, len(results))
	for index := range keep {
		keep[index] = index
	}
	override := map[int]SearchResult{}
	merged, absorbed := 0, 0
	for _, run := range runs {
		anchor := results[run[0]]
		lines, cached := fileLines[anchor.FilePath]
		if !cached {
			if unreadable[anchor.FilePath] {
				continue
			}
			content, readable := read(anchor.FilePath)
			if !readable || strings.IndexByte(content, 0) >= 0 {
				unreadable[anchor.FilePath] = true
				continue
			}
			lines = strings.Split(content, "\n")
			fileLines[anchor.FilePath] = lines
		}
		survivor, span, ok := mergedSearchSpanResult(results, run, lines)
		if !ok {
			continue
		}
		trialKeep := dropSearchSpanMembers(keep, run, survivor)
		trialOverride := map[int]SearchResult{survivor: span}
		for index, result := range override {
			trialOverride[index] = result
		}
		trial := renderSearchSpanRanking(results, trialKeep, trialOverride)
		// The cost check is the whole safety story for a payload that already fits: a merge that
		// would push the ranking past its ceiling is dropped, and the run stays rendered exactly
		// as it was. Runs are considered in rank order, so which merges survive a tight budget is
		// deterministic and favours the head.
		if budget > 0 && searchRankingBytes(trial) > budget {
			continue
		}
		keep, override = trialKeep, trialOverride
		merged++
		absorbed += len(run) - 1
	}
	if merged == 0 {
		return results, 0, 0
	}
	return renderSearchSpanRanking(results, keep, override), merged, absorbed
}

// planSameFileSearchSpans groups the eligible results into runs that should become one span.
// Runs are returned as slices of indexes into results, in ascending source order within a file,
// and the runs themselves are ordered by their best rank so a byte-starved payload merges the
// head first.
func planSameFileSearchSpans(results []SearchResult) [][]int {
	byFile := map[string][]int{}
	order := []string{}
	for index, result := range results {
		if !eligibleForSearchSpanMerge(result) {
			continue
		}
		if _, seen := byFile[result.FilePath]; !seen {
			order = append(order, result.FilePath)
		}
		byFile[result.FilePath] = append(byFile[result.FilePath], index)
	}
	var runs [][]int
	for _, path := range order {
		indexes := byFile[path]
		if len(indexes) < 2 {
			continue
		}
		// Source order, not rank order: adjacency is a property of the file, and a run built in
		// rank order would bridge 349->427 while skipping 395 that lies between them.
		sort.SliceStable(indexes, func(a, b int) bool {
			left, right := results[indexes[a]], results[indexes[b]]
			if left.SnippetStartLine != right.SnippetStartLine {
				return left.SnippetStartLine < right.SnippetStartLine
			}
			return indexes[a] < indexes[b]
		})
		run := []int{indexes[0]}
		runStart, runEnd := results[indexes[0]].SnippetStartLine, results[indexes[0]].SnippetEndLine
		flush := func() {
			if len(run) > 1 {
				runs = append(runs, run)
			}
		}
		for _, index := range indexes[1:] {
			next := results[index]
			gap := next.SnippetStartLine - runEnd - 1
			end := maxInt(runEnd, next.SnippetEndLine)
			if gap > searchSpanMergeMaxGapLines || end-runStart+1 > searchSpanMergeMaxSpanLines {
				flush()
				run = []int{index}
				runStart, runEnd = next.SnippetStartLine, next.SnippetEndLine
				continue
			}
			run = append(run, index)
			runEnd = end
		}
		flush()
	}
	sort.SliceStable(runs, func(a, b int) bool {
		return bestSearchSpanRank(results, runs[a]) < bestSearchSpanRank(results, runs[b])
	})
	return runs
}

func eligibleForSearchSpanMerge(result SearchResult) bool {
	// Sectioned entries answer a different question from the ranking (what the fix has to
	// achieve, where else it lands) and are rendered under their own headers, so folding one
	// into a candidate fix site would erase the distinction the sections exist to make.
	if result.Section != "" {
		return false
	}
	if result.SnippetStartLine < 1 || result.SnippetEndLine < result.SnippetStartLine {
		return false
	}
	return hasSearchSignal(result, searchCompleteSymbolSignal)
}

func bestSearchSpanRank(results []SearchResult, run []int) int {
	best := results[run[0]].Rank
	for _, index := range run[1:] {
		if rank := results[index].Rank; rank < best {
			best = rank
		}
	}
	return best
}

// mergedSearchSpanResult builds the single result that replaces a run: the best-ranked member,
// widened to the run's full span and carrying the verbatim file text of that whole range. It
// returns the surviving member's index alongside it.
func mergedSearchSpanResult(results []SearchResult, run []int, lines []string) (int, SearchResult, bool) {
	survivor := run[0]
	first := results[run[0]]
	start, end := first.SnippetStartLine, first.SnippetEndLine
	regionStart, regionEnd := first.StartLine, first.EndLine
	ranks := make([]int, 0, len(run))
	for _, index := range run {
		member := results[index]
		if member.Rank < results[survivor].Rank {
			survivor = index
		}
		start = minInt(start, member.SnippetStartLine)
		end = maxInt(end, member.SnippetEndLine)
		regionStart = minInt(regionStart, member.StartLine)
		regionEnd = maxInt(regionEnd, member.EndLine)
		ranks = append(ranks, member.Rank)
	}
	if start < 1 || end > len(lines) || end < start {
		return 0, SearchResult{}, false
	}
	if end-start+1 > searchSpanMergeMaxSpanLines {
		return 0, SearchResult{}, false
	}
	sort.Ints(ranks)
	span := results[survivor]
	span.SnippetStartLine, span.SnippetEndLine = start, end
	span.Snippet = strings.Join(lines[start-1:end], "\n")
	span.StartLine = maxInt(1, minInt(regionStart, start))
	span.EndLine = maxInt(regionEnd, end)
	span.FocusLine = minInt(maxInt(span.FocusLine, span.StartLine), span.EndLine)
	// The symbol identity stays the survivor's own. The span now holds more than that callable,
	// which is exactly what MergedRanks and the "nothing elided" label say; renaming it after the
	// widest thing it touches would report a container as the fix site.
	span.Signals = appendUnique(append([]string(nil), span.Signals...), searchSpanMergedSignal)
	span.MergedRanks = ranks
	return survivor, span, true
}

// dropSearchSpanMembers removes every member of a run except its survivor, preserving order.
func dropSearchSpanMembers(keep, run []int, survivor int) []int {
	dropped := make(map[int]bool, len(run))
	for _, index := range run {
		if index != survivor {
			dropped[index] = true
		}
	}
	trimmed := make([]int, 0, len(keep))
	for _, index := range keep {
		if dropped[index] {
			continue
		}
		trimmed = append(trimmed, index)
	}
	return trimmed
}

// renderSearchSpanRanking materializes a working state back into a ranking. Order comes from the
// ORIGINAL ranking, so the relative order of everything that survives is untouched.
//
// Ranks are renumbered to stay 1..N and dense: the response contract is positional (Validate
// requires Rank == index+1), so a merged-away hit cannot leave a hole. The numbers inside
// MergedRanks are the PRE-merge ranks the span absorbed, which is what tells the reader it is not
// missing a hit that simply is not printed separately any more.
func renderSearchSpanRanking(results []SearchResult, keep []int, override map[int]SearchResult) []SearchResult {
	ranking := make([]SearchResult, 0, len(keep))
	for position, index := range keep {
		result, replaced := override[index]
		if !replaced {
			result = results[index]
		}
		result.Rank = position + 1
		ranking = append(ranking, result)
	}
	return ranking
}

// searchRankingBytes is the serialized cost of a ranking, in the same terms as
// stats.ResultBytes, so the ceiling this pass checks is the ceiling the response is validated on.
func searchRankingBytes(results []SearchResult) int {
	return serializedSearchResultBytes(results)
}
