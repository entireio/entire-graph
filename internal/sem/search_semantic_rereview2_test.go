package sem

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// rr2PrimaryRows mirrors the eval's scorer: primary rows only (no section), keyed by rank.
func rr2PrimaryRows(results []SearchResult) []string {
	var rows []string
	for _, r := range results {
		if r.Section != "" {
			continue
		}
		rows = append(rows, fmt.Sprintf("rank=%d %s sym=%s symspan=%d-%d span=%d-%d snip=%d-%d merged=%v sig=%v",
			r.Rank, r.FilePath, r.SymbolName, r.SymbolStartLine, r.SymbolEndLine, r.StartLine, r.EndLine, r.SnippetStartLine, r.SnippetEndLine, r.MergedRanks, r.Signals))
	}
	return rows
}

// rr2Hit is the eval's hit rule: same file, symbol span overlaps gold, rank <= 10, primary row.
func rr2Hit(results []SearchResult, file string, goldStart, goldEnd, maxRank int) (int, bool) {
	for _, r := range results {
		if r.Section != "" || r.Rank > maxRank || r.FilePath != file {
			continue
		}
		if r.SymbolStartLine == 0 {
			continue
		}
		if r.SymbolStartLine <= goldEnd && goldStart <= r.SymbolEndLine {
			return r.Rank, true
		}
	}
	return 0, false
}

func rr2WarmArms(t *testing.T, repo, query string, maxBytes int) (off, on SearchResponse) {
	t.Helper()
	server := httptest.NewServer(&fakeEmbedder{})
	t.Cleanup(server.Close)
	cacheDir := t.TempDir()
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version",
		ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSemanticIndex(t.Context(), repo, snapshot, cacheDir,
		SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}, false); err != nil {
		t.Fatal(err)
	}
	opts := func(semantic *SemanticConfig) SearchOptions {
		// Mirrors `query --head --profile full --format json --top-k 10` (CLI default 24 KiB budget).
		return SearchOptions{CacheDir: cacheDir, TopK: 10, Profile: ProfileFull, MaxContextBytes: maxBytes,
			OmitsRepoIgnoreDisclosureFloor: true, Semantic: semantic}
	}
	off, err = SearchRepository(t.Context(), repo, "test-version", query, opts(nil))
	if err != nil {
		t.Fatal(err)
	}
	on, err = SearchRepository(t.Context(), repo, "test-version", query,
		opts(&SemanticConfig{Endpoint: server.URL, Model: semanticFixtureModel}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []SearchResponse{off, on} {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if !off.Stats.IndexCacheHit || !on.Stats.IndexCacheHit {
		t.Fatalf("not warm: off=%v on=%v", off.Stats.IndexCacheHit, on.Stats.IndexCacheHit)
	}
	if on.Stats.SemanticStatus != SemanticStatusUsed {
		t.Fatalf("channel not used: %q", on.Stats.SemanticStatus)
	}
	return off, on
}

// RR2-SPANMERGE: warm path, lexical rank 1 in B' is the gold symbol. In C' the channel seats a
// semantic-only neighbour from the SAME file at rank 1 (E-first); both get complete bodies; the
// same-file span merge absorbs the lexical gold row into the semantic survivor, which keeps ITS OWN
// symbol_start_line/end_line. The eval's (file, symbol-span overlap) scorer then scores B' a hit
// at rank 1 and C' a MISS — the falsifier's "B' top-3 dropped" fires on an artifact of rendering.
func TestSemanticRereview2SpanMergeAbsorbsLexicalGold(t *testing.T) {
	// ACCEPTED (D1): the same-file span merge keeps the survivor's symbol identity by design; the
	// absorbed row's text is in the span (merged_ranks says so) and the eval scorer handles merged
	// spans (not changed here). Kept as a runnable record of the counterexample: ENTIRE_GRAPH_RUN_ACCEPTED=1.
	skipAcceptedRereview2(t, "D1 span merge absorbs a lexical row; the eval scorer handles merged spans")
	repo := t.TempDir()
	initRepo(t, repo)
	var src strings.Builder
	src.WriteString("package srv\n\n// QuitServer stops the server.\nfunc QuitServer() string {\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&src, "\tprintln(%d)\n", i)
	}
	src.WriteString("\treturn \"quit server\"\n}\n\n// AbandonStalled drops a stalled backend: abandon it.\nfunc AbandonStalled() {\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&src, "\tprintln(%d)\n", i)
	}
	src.WriteString("}\n")
	writeFile(t, repo, "srv/handler.go", src.String())
	goldStart, goldEnd := 4, 17
	writeFile(t, repo, "srv/other.go", "package srv\n\n// Other is unrelated.\nfunc Other() int {\n\treturn 1\n}\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "fixture")
	off, on := rr2WarmArms(t, repo, "quit sluggish server", 24*1024)
	t.Logf("OFF:\n%s", strings.Join(rr2PrimaryRows(off.Results), "\n"))
	t.Logf("ON:\n%s", strings.Join(rr2PrimaryRows(on.Results), "\n"))
	offRank, offHit := rr2Hit(off.Results, "srv/handler.go", goldStart, goldEnd, 10)
	onRank, onHit := rr2Hit(on.Results, "srv/handler.go", goldStart, goldEnd, 10)
	t.Logf("gold QuitServer 4-17: OFF hit=%v rank=%d ; ON hit=%v rank=%d ; ON merged_spans=%d", offHit, offRank, onHit, onRank, on.Stats.MergedSpans)
	if offHit && offRank <= 3 && !onHit {
		t.Errorf("DEFECT: B' top-3 gold (rank %d) is absent from C' under the eval scorer (span merge absorbed it)", offRank)
	}
}

func TestSemanticRereview2SpanMergeDebug(t *testing.T) {
	results := []SearchResult{
		{Rank: 1, FilePath: "a.go", StartLine: 7, EndLine: 11, SnippetStartLine: 7, SnippetEndLine: 11, Signals: []string{"complete-symbol"}},
		{Rank: 2, FilePath: "a.go", StartLine: 2, EndLine: 6, SnippetStartLine: 2, SnippetEndLine: 6, Signals: []string{"complete-symbol"}},
	}
	read := func(string) (string, bool) { return strings.Repeat("x\n", 12), true }
	out, spans, _ := mergeSameFileSearchSpans(results, read, 24*1024)
	t.Logf("spans=%d out=%+v", spans, out)
}

// RR2-RELATED-DISPLACE: related sites are funded byte-neutrally by DISPLACING tail locators whose
// file is named elsewhere (floor = searchEnclosureHeadRanks = 5). E-first seats B' rank 3 at C'
// rank 6 — outside the floor. When a semantic row from the same file sits in C”s head, that
// lexical gold row becomes displaceable; in B' (rank 3) it never is.
func TestSemanticRereview2RelatedSiteDisplacesPushedLexicalTop3(t *testing.T) {
	src := strings.Repeat("func f() {}\n", 40)
	read := func(string) (string, bool) { return src, true }
	row := func(file, sym string, start int, signals ...string) SearchResult {
		return SearchResult{FilePath: file, SymbolID: sym, SymbolName: sym, StartLine: start, EndLine: start + 3,
			FocusLine: start, SnippetStartLine: start, SnippetEndLine: start, SymbolStartLine: start, SymbolEndLine: start + 3,
			Snippet: "func f() {}", Signals: append([]string{"body"}, signals...)}
	}
	number := func(rows []SearchResult) []SearchResult {
		for i := range rows {
			rows[i].Rank = i + 1
		}
		return rows
	}
	site := searchRelatedSite{kind: "caller", symbol: SymbolRecord{ID: "caller1", Name: "caller1", FilePath: "q.go", StartLine: 30, EndLine: 33}, line: 31}
	gold := func(rows []SearchResult) (int, bool) {
		for _, r := range rows {
			if r.Section == "" && r.FilePath == "z.go" && r.SymbolID == "G" {
				return r.Rank, true
			}
		}
		return 0, false
	}
	// B' layout: lexical l1..l5 + tail; gold G (z.go) at rank 3.
	bPrime := number([]SearchResult{
		row("x.go", "L1", 1, "complete-symbol"), row("y.go", "L2", 1, "complete-symbol"), row("z.go", "G", 1, "complete-symbol"),
		row("u.go", "L4", 1), row("v.go", "L5", 1), row("w1.go", "L6", 1), row("w2.go", "L7", 1),
		row("w3.go", "L8", 1), row("w4.go", "L9", 1), row("w5.go", "L10", 1),
	})
	// C' layout (E-first): e1 z.go (semantic neighbour of gold, different symbol), l1, e2, l2, e3, l3=G ...
	cPrime := number([]SearchResult{
		row("z.go", "S1", 20, "semantic:embedding", "semantic:only", "complete-symbol"), row("x.go", "L1", 1, "complete-symbol"),
		row("e2.go", "S2", 1, "semantic:embedding", "semantic:only"), row("y.go", "L2", 1),
		row("e3.go", "S3", 1, "semantic:embedding", "semantic:only"), row("z.go", "G", 1),
		row("e4.go", "S4", 1, "semantic:embedding", "semantic:only"), row("u.go", "L4", 1),
		row("e5.go", "S5", 1, "semantic:embedding", "semantic:only"), row("v.go", "L5", 1),
	})
	bOut, bSites := mergeSearchRelatedSites(append([]SearchResult(nil), bPrime...), []searchRelatedSite{site}, read, 24*1024)
	cOut, cSites := mergeSearchRelatedSites(append([]SearchResult(nil), cPrime...), []searchRelatedSite{site}, read, 24*1024)
	bRank, bHit := gold(bOut)
	cRank, cHit := gold(cOut)
	t.Logf("B': related=%d gold hit=%v rank=%d ; C': related=%d gold hit=%v rank=%d", bSites, bHit, bRank, cSites, cHit, cRank)
	t.Logf("C' after:\n%s", strings.Join(rr2PrimaryRows(cOut), "\n"))
	if bHit && bRank <= 3 && !cHit {
		t.Errorf("DEFECT: related-site funding displaced B' rank-%d gold that E-first pushed to C' rank 6", bRank)
	}
}

// RR2-ALLDOCS: B' payload of only non-code hits keeps them PRIMARY (all-docs fallback); one
// semantic code row in C' flips every lexical docs row to section docs-and-fixtures, so a primary-
// only scorer stops seeing rows B' scored.
func TestSemanticRereview2AllDocsFallbackFlipsOnSemanticCodeRow(t *testing.T) {
	// ACCEPTED (D3): the all-docs fallback keys on the payload having no code row at all; a semantic
	// code row legitimately ends that condition. Record only: ENTIRE_GRAPH_RUN_ACCEPTED=1.
	skipAcceptedRereview2(t, "D3 all-docs fallback ends when any code row is present")
	q := buildSearchQuery("quit server")
	b := []SearchResult{{Rank: 1, FilePath: "docs/quit.md"}, {Rank: 2, FilePath: "fixtures/server.json"}}
	c := []SearchResult{{Rank: 1, FilePath: "srv/pool.go", Signals: []string{semanticSignal, semanticOnlySignal}},
		{Rank: 2, FilePath: "docs/quit.md"}, {Rank: 3, FilePath: "fixtures/server.json"}}
	b = assignSearchSections(b, q)
	c = assignSearchSections(c, q)
	t.Logf("B' sections: %q %q ; C' sections: %q %q %q", b[0].Section, b[1].Section, c[0].Section, c[1].Section, c[2].Section)
	if b[0].Section == "" && c[1].Section != "" {
		t.Errorf("DEFECT(minor): lexical docs row primary in B', sectioned %q in C'", c[1].Section)
	}
}

// skipAcceptedRereview2 skips a counterexample whose behaviour was reviewed and kept as designed.
func skipAcceptedRereview2(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("ENTIRE_GRAPH_RUN_ACCEPTED") == "" {
		t.Skip("accepted behaviour: " + reason)
	}
}
