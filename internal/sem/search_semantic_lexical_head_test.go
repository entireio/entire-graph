package sem

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func lexicalHeadRow(file, symbol string, signals ...string) SearchResult {
	return SearchResult{
		FilePath: file, SymbolID: symbol, SymbolName: symbol, StartLine: 1, EndLine: 4, FocusLine: 1,
		SnippetStartLine: 1, SnippetEndLine: 1, SymbolStartLine: 1, SymbolEndLine: 4,
		Snippet: "func f() {}", Signals: append([]string{"body"}, signals...),
	}
}

// A lexical row the channel ALSO nominated is seated at the embedding's position, ahead of its
// lexical order. Reading lexical rank from order alone then undercounts every lexical row after it:
// here lexical rank 5 (g.go:L5) would read as position 6 and be sold to a related site. The rank
// stamped at fusion keeps it in the head. Runs the real fusion, stamp and merge.
func TestSemanticLexicalHeadSurvivesLiftedLexicalRow(t *testing.T) {
	lexicalFiles := []string{"a.go", "b.go", "c.go", "d.go", "g.go", "h.go", "i.go"}
	var lexical []searchCandidate
	for index, file := range lexicalFiles {
		lexical = append(lexical, searchCandidate{result: lexicalHeadRow(file, fmt.Sprintf("L%d", index+1)), score: float64(10 - index)})
	}
	lifted := lexical[6]
	lifted.result.Signals = append(append([]string(nil), lifted.result.Signals...), semanticSignal)
	semantic := []searchCandidate{lifted}
	for index, file := range []string{"g.go", "e3.go", "e4.go", "e5.go"} {
		semantic = append(semantic, searchCandidate{
			result:       lexicalHeadRow(file, fmt.Sprintf("S%d", index+2), semanticSignal),
			score:        0.5,
			semanticOnly: true,
		})
	}
	keyOf := func(candidate searchCandidate) semanticKey { return semanticCandidateKey(candidate, nil) }
	fused, _ := fuseSemanticCandidates(lexical, semantic, 10, keyOf)
	stampSemanticLexicalRanks(fused, lexical, keyOf)
	results := make([]SearchResult, len(fused))
	goldIndex := -1
	for index := range fused {
		results[index] = fused[index].result
		results[index].Rank = index + 1
		if results[index].SymbolID == "L5" {
			goldIndex = index
		}
	}
	if goldIndex < searchEnclosureHeadRanks {
		t.Fatalf("fixture no longer pushes lexical rank 5 below the index floor: index %d", goldIndex)
	}
	src := strings.Repeat("func f() {}\n", 40)
	site := searchRelatedSite{kind: "caller", symbol: SymbolRecord{ID: "caller1", Name: "caller1", FilePath: "q.go", StartLine: 30, EndLine: 33}, line: 31}
	merged, sites := mergeSearchRelatedSites(results, []searchRelatedSite{site}, func(string) (string, bool) { return src, true }, 24*1024)
	for _, row := range merged {
		if row.SymbolID == "L5" {
			return
		}
	}
	var rows []string
	for _, row := range merged {
		rows = append(rows, fmt.Sprintf("%d %s %s lexical=%d %v", row.Rank, row.FilePath, row.SymbolID, row.lexicalRank, row.Signals))
	}
	t.Fatalf("lexical rank 5 (fused index %d) was displaced to fund %d related site(s):\n%s",
		goldIndex, sites, strings.Join(rows, "\n"))
}

// Unfused, the protected set is EXACTLY the old index < floor head, whatever the rows look like —
// the guarantee behind a byte-identical unconfigured payload.
func TestSemanticLexicalHeadUnfusedEqualsIndexHead(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	sections := []string{"", "", searchSectionRelated, searchSectionCoveringTest, searchSectionDocs}
	for trial := 0; trial < 500; trial++ {
		results := make([]SearchResult, random.Intn(14))
		for index := range results {
			results[index] = lexicalHeadRow(fmt.Sprintf("f%d.go", random.Intn(4)), fmt.Sprintf("s%d", index))
			results[index].Section = sections[random.Intn(len(sections))]
		}
		for _, depth := range []int{0, 1, 2, searchEnclosureHeadRanks} {
			floor := minInt(len(results), depth)
			head := searchLexicalHeadRows(results, floor)
			for index := range results {
				if head[index] != (index < floor) {
					t.Fatalf("trial %d floor %d: row %d protected=%v, index rule says %v", trial, floor, index, head[index], index < floor)
				}
			}
		}
	}
}

// A lexical head row that the same-file span merge folds into a semantic survivor is carried by the
// survivor, so the protection survives the fold.
func TestSemanticLexicalHeadSurvivesSpanMerge(t *testing.T) {
	results := []SearchResult{
		{Rank: 1, FilePath: "a.go", StartLine: 7, EndLine: 11, SnippetStartLine: 7, SnippetEndLine: 11, Signals: []string{"complete-symbol", semanticSignal, semanticOnlySignal}},
		{Rank: 2, FilePath: "a.go", StartLine: 2, EndLine: 6, SnippetStartLine: 2, SnippetEndLine: 6, Signals: []string{"complete-symbol"}, lexicalRank: 3},
	}
	read := func(string) (string, bool) { return strings.Repeat("x\n", 12), true }
	merged, spans, _ := mergeSameFileSearchSpans(results, read, 24*1024)
	if spans != 1 || len(merged) != 1 {
		t.Fatalf("fixture did not merge: spans=%d rows=%d", spans, len(merged))
	}
	if merged[0].lexicalRank != 3 {
		t.Fatalf("merged span carries lexical rank %d, want 3", merged[0].lexicalRank)
	}
}
