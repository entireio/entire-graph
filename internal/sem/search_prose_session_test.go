package sem

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestSearchRepositoryRanksDistributedMarkdownSession catches the failure where
// individually weak heading hits from one prose session never get to combine
// their evidence before top-k selection.
func TestSearchRepositoryRanksDistributedMarkdownSession(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", `# Amber

opening note

## Orchard

second note

## Ledger

third note

## Braided

fourth note

## Lantern

closing note
`)
	for index := 0; index < 17; index++ {
		write(t, repo, fmt.Sprintf("sessions/distractor-%02d.md", index), fmt.Sprintf(
			"# Amber orchard ledger braided marker%d ridge%d vessel%d\n\nUnrelated archive.\n",
			index, index, index,
		))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "amber orchard ledger braided lantern", SearchOptions{
			Worktree:        true,
			Profile:         ProfileSyntaxOnly,
			TopK:            10,
			MaxIndexedFiles: 32,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "6efd6795a9f0d1f2969f167676c81e22f6cd9aaa74ec7270c6d5f98e10ec9878")
	wantHead := []string{
		"sessions/distractor-00.md",
		"sessions/distractor-01.md",
		"sessions/distractor-02.md",
		"sessions/distractor-03.md",
		"sessions/distractor-04.md",
	}
	for index, path := range wantHead {
		if response.Results[index].FilePath != path {
			t.Fatalf("preserved head[%d] = %q, want %q", index, response.Results[index].FilePath, path)
		}
	}
	for _, result := range response.Results {
		if result.FilePath != "sessions/focus.md" {
			continue
		}
		if result.Rank != 6 {
			t.Fatalf("distributed session rank = %d, want first fair tail slot 6", result.Rank)
		}
		if !containsString(result.Signals, "retrieval_mode=prose-parent") {
			t.Fatalf("focus result lacks prose-parent retrieval mode: %#v", result)
		}
		return
	}
	t.Fatalf("distributed-evidence session missing from top 10: %#v", response.Results)
}

// TestSearchRepositoryRanksSafePluralMarkdownSession catches the one-way
// morphology failure: a singular prose query must see safe plural headings at
// full strength without changing code-token scoring.
func TestSearchRepositoryRanksSafePluralMarkdownSession(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", `# Ambers

## Orchards

## Ledgers

## Lanterns

## Vessels
`)
	for index := 0; index < 11; index++ {
		write(t, repo, fmt.Sprintf("sessions/distractor-%02d.md", index), fmt.Sprintf(
			"# Amber orchard ledger lantern marker%d ridge%d route%d\n",
			index, index, index,
		))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "amber orchard ledger lantern vessel", SearchOptions{
			Worktree:        true,
			Profile:         ProfileSyntaxOnly,
			TopK:            10,
			MaxIndexedFiles: 32,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "402dfe1fae86d736a735d742dbcbd38871a61d9a4b280349291fe2c88ec72d0b")
	for _, result := range response.Results {
		if result.FilePath == "sessions/focus.md" &&
			containsString(result.Signals, "retrieval_mode=prose-parent") {
			if result.Rank != 6 {
				t.Fatalf("plural session rank = %d, want first fair tail slot 6", result.Rank)
			}
			return
		}
	}
	t.Fatalf("safe plural session missing from top 10: %#v", response.Results)
}

func TestSearchRepositoryRanksSafeSingularEvidenceForPluralQuery(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", `# Amber

## Orchard

## Ledger

## Lantern

## Vessel
`)
	for index := 0; index < 11; index++ {
		write(t, repo, fmt.Sprintf("sessions/distractor-%02d.md", index), fmt.Sprintf(
			"# Ambers orchards ledgers lanterns marker%d ridge%d route%d\n",
			index, index, index,
		))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "ambers orchards ledgers lanterns vessels", SearchOptions{
			Worktree:        true,
			Profile:         ProfileSyntaxOnly,
			TopK:            10,
			MaxIndexedFiles: 32,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "186bc7d9a4265a3cce3f49a7b0d953d64bd37ceeb121de18f91cc5c22baa43aa")
	for _, result := range response.Results {
		if result.FilePath == "sessions/focus.md" &&
			containsString(result.Signals, "retrieval_mode=prose-parent") {
			return
		}
	}
	t.Fatalf("safe singular-evidence session missing from top 10: %#v", response.Results)
}

func TestSearchRepositoryDoesNotConflateUnsafeProseSuffixes(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", `# Gases

## Orchard

## Ledger

## Braided

## Lantern
`)
	for index := 0; index < 12; index++ {
		write(t, repo, fmt.Sprintf("sessions/distractor-%02d.md", index), fmt.Sprintf(
			"# Orchard ledger braided lantern marker%d ridge%d route%d\n",
			index, index, index,
		))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "gas orchard ledger braided lantern", SearchOptions{
			Worktree:        true,
			Profile:         ProfileSyntaxOnly,
			TopK:            10,
			MaxIndexedFiles: 32,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "f656311a14a2817e340eaa0fdeae3abc4f36c9f481712684c722c830c2f725b3")
	for _, result := range response.Results {
		if result.FilePath == "sessions/focus.md" {
			t.Fatalf("unsafe gas/gases suffix promoted focus session: %#v", response.Results)
		}
	}
}

func TestSearchRepositoryProseLaneRequiresFourParents(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 3; index++ {
		write(t, repo, fmt.Sprintf("notes/session-%d.md", index),
			fmt.Sprintf("# Amber orchard ledger note%d\n", index))
	}
	response, err := SearchRepository(
		t.Context(), repo, "test", "amber orchard ledger", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "2afe5f1129c722084b50d1570b7a06a30b4af04162c01b22948efe6e6dd708f6")
	for _, result := range response.Results {
		if containsString(result.Signals, "retrieval_mode=prose-parent") {
			t.Fatalf("small corpus activated prose-parent mode: %#v", response.Results)
		}
	}
}

func TestSearchRepositoryCodeResultOrderRemainsByteIdentical(t *testing.T) {
	repo := t.TempDir()
	for index, name := range []string{"Fast", "Queued", "Retried", "Timed"} {
		write(t, repo, fmt.Sprintf("delivery/%02d_%s.go", index, name), fmt.Sprintf(`package delivery

// %sDelivery retries delivery with exponential backoff.
func %sDelivery() {}
`, name, name))
	}
	response, err := SearchRepository(
		t.Context(), repo, "test", "retry delivery exponential backoff", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "4e88510273830fef30a7aeb44845893c24e723fdf53dfd68f879c44a6f1dbf35")
	identities := make([][3]any, len(response.Results))
	for index, result := range response.Results {
		if len(result.Passages) != 0 {
			t.Fatalf("ordinary code rank %d received prose passages: %#v", result.Rank, result.Passages)
		}
		identities[index] = [3]any{result.FilePath, result.StartLine, result.SymbolName}
	}
	got, err := json.Marshal(identities)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`[["delivery/00_Fast.go",2,"FastDelivery"],["delivery/01_Queued.go",2,"QueuedDelivery"],["delivery/02_Retried.go",2,"RetriedDelivery"],["delivery/03_Timed.go",2,"TimedDelivery"]]`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("code result identity JSON changed:\n got %s\nwant %s", got, want)
	}
}

func TestSearchRepositoryProseParentReturnsBoundedNativeContextByDefault(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/00-melanie.md", `# Melanie music preferences

Melanie often talks about the music she enjoys listening to.

`+strings.Repeat("A neutral diary line with no additional preference.\n", 42)+`
Her lasting preference is classical music by composers such as Bach and Mozart.
`)
	for index := 1; index < 5; index++ {
		write(t, repo, fmt.Sprintf("sessions/%02d-peer.md", index), fmt.Sprintf(`
# Melanie music archive %d

This unrelated session records a concert ticket number %d.
`, index, index))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "melanie music preferences", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].FilePath != "sessions/00-melanie.md" {
		t.Fatalf("focus session is not the top result: %#v", response.Results)
	}
	result := response.Results[0]
	if !containsString(result.Signals, proseParentRetrievalSignal) {
		t.Fatalf("focus result lacks prose-parent mode: %#v", result.Signals)
	}
	if !containsString(result.Signals, searchHeadWindowSignal) {
		t.Fatalf("focus result lacks native head window: %#v", result.Signals)
	}
	if !strings.Contains(result.Snippet, "classical music by composers such as Bach and Mozart") {
		t.Fatalf("native prose context omits the distant answer-bearing fact:\n%s", result.Snippet)
	}
}

func TestResolvedSearchHeadWindowLinesIsProseOnlyAndHonorsExplicitValue(t *testing.T) {
	prose := []SearchResult{{Signals: []string{proseParentRetrievalSignal}}}
	if got := resolvedSearchHeadWindowLines(prose, 0); got != defaultProseParentHeadWindowLines {
		t.Fatalf("prose default = %d, want %d", got, defaultProseParentHeadWindowLines)
	}
	if got := resolvedSearchHeadWindowLines(prose, 23); got != 23 {
		t.Fatalf("explicit prose window = %d, want 23", got)
	}
	if got := resolvedSearchHeadWindowLines([]SearchResult{{Signals: []string{"path"}}}, 0); got != 0 {
		t.Fatalf("ordinary code window = %d, want disabled", got)
	}
}

func TestResolvedSearchSnippetGrowthLetsProseUseCallerBudget(t *testing.T) {
	prose := []SearchResult{{Signals: []string{proseParentRetrievalSignal}}}
	if got := resolvedSearchSnippetGrowth(prose, 128_000); got != 128_000 {
		t.Fatalf("prose growth = %d, want caller budget", got)
	}
	code := []SearchResult{{Signals: []string{"body"}}}
	if got := resolvedSearchSnippetGrowth(code, 128_000); got != searchEnclosureGrowthBytes {
		t.Fatalf("code growth = %d, want %d", got, searchEnclosureGrowthBytes)
	}
	if got := resolvedSearchSnippetGrowth(prose, 0); got != searchEnclosureGrowthBytes {
		t.Fatalf("unbounded prose growth = %d, want conservative default %d", got, searchEnclosureGrowthBytes)
	}
}

func TestSearchRepositoryProseCanExpandMultipleLongSessionsWithinCallerBudget(t *testing.T) {
	repo := t.TempDir()
	filler := strings.Repeat("neutral archive detail ", 18) + "\n"
	write(t, repo, "sessions/00-orion.md", `# Orion alpha inventory

The Orion alpha inventory is tracked in this session.

`+strings.Repeat(filler, 55)+`
The final alpha inventory count is 2,000 units.
`)
	write(t, repo, "sessions/01-orion.md", `# Orion beta inventory

The Orion beta inventory is tracked in this session.

`+strings.Repeat(filler, 55)+`
The final beta inventory count is 10,000 units.
`)
	for index := 2; index < 5; index++ {
		write(t, repo, fmt.Sprintf("sessions/%02d-peer.md", index), fmt.Sprintf(`
# Orion inventory archive %d

This unrelated inventory note contains no alpha or beta count.
`, index))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "Orion alpha beta inventory count", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5, MaxContextBytes: 128_000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]SearchResult, len(response.Results))
	for _, result := range response.Results {
		byPath[result.FilePath] = result
	}
	for path, fact := range map[string]string{
		"sessions/00-orion.md": "final alpha inventory count is 2,000 units",
		"sessions/01-orion.md": "final beta inventory count is 10,000 units",
	} {
		result, ok := byPath[path]
		if !ok {
			t.Fatalf("missing evidence session %s: %#v", path, response.Results)
		}
		if !containsString(result.Signals, searchHeadWindowSignal) {
			t.Fatalf("%s was not expanded under the caller budget: %#v", path, result.Signals)
		}
		if !strings.Contains(result.Snippet, fact) {
			t.Fatalf("%s omitted distant fact %q", path, fact)
		}
	}
}

func TestSearchRepositoryProseLaneRequiresEightyPercentProseParents(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 4; index++ {
		write(t, repo, fmt.Sprintf("notes/session-%d.md", index),
			fmt.Sprintf("# Amber orchard ledger note%d\n", index))
	}
	for index := 0; index < 2; index++ {
		write(t, repo, fmt.Sprintf("src/worker%d.go", index), fmt.Sprintf(`package src

// Worker%d handles amber orchard ledger records.
func Worker%d() {}
`, index, index))
	}
	response, err := SearchRepository(
		t.Context(), repo, "test", "amber orchard ledger", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 6,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultGolden(t, response.Results, "e9733475f04142cfe8fd78b823651105cd55eac581d38430c1b92424aa4a2a5b")
	for _, result := range response.Results {
		if containsString(result.Signals, "retrieval_mode=prose-parent") {
			t.Fatalf("two-thirds prose corpus activated prose-parent mode: %#v", response.Results)
		}
	}
}

func TestSearchRepositoryProseParentOrderIsExactlyDeterministic(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", "# Amber\n\n## Orchard\n\n## Ledger\n\n## Lantern\n")
	for index := 0; index < 8; index++ {
		write(t, repo, fmt.Sprintf("sessions/peer-%02d.md", index), fmt.Sprintf(
			"# Amber orchard ledger marker%d ridge%d\n", index, index,
		))
	}
	var first []byte
	for run := 0; run < 8; run++ {
		response, err := SearchRepository(
			t.Context(), repo, "test", "amber orchard ledger lantern", SearchOptions{
				Worktree: true, Profile: ProfileSyntaxOnly, TopK: 8, MaxIndexedFiles: 32,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(response.Results)
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			first = encoded
			assertSearchResultGolden(t, response.Results, "3e0c28f472059df45dd2ffaf2528bbf0c28fc6de55336f19591315f28f4a5c7c")
			continue
		}
		if !reflect.DeepEqual(encoded, first) {
			t.Fatalf("run %d result JSON differs:\nfirst %s\n  got %s", run, first, encoded)
		}
	}
}

func TestSelectSearchCandidatesReturnsBestPassageForAdmittedParent(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	candidates := make([]searchCandidate, 0, 8)
	for index := 0; index < 6; index++ {
		candidates = append(candidates, proseTestCandidate(
			fmt.Sprintf("sessions/peer-%02d.md", index), 1, 20-float64(index),
			"# Amber marker", map[string]int{"amber": 1},
		))
	}
	candidates = append(candidates,
		proseTestCandidate("sessions/focus.md", 10, 3, "# Lantern", map[string]int{"lantern": 1}),
		proseTestCandidate("sessions/focus.md", 80, 8, "# Amber overview", map[string]int{"amber": 1}),
	)
	sortSearchCandidates(candidates)
	selected := selectSearchCandidates(candidates, q, 6, 3)
	assertSearchResultGolden(t, searchCandidateResults(selected), "5ce0ba0a0d46c828e61092bbd325931fccd418b5b0bd6cae67ff4680bdcad95a")
	for _, candidate := range selected {
		if candidate.result.FilePath != "sessions/focus.md" {
			continue
		}
		if candidate.result.StartLine != 80 {
			t.Fatalf("admitted parent returned seed line %d, want best full-query line 80", candidate.result.StartLine)
		}
		return
	}
	t.Fatalf("focus parent was not admitted: %#v", selected)
}

func TestSelectSearchCandidatesHasLinearProseWorkingSet(t *testing.T) {
	q := buildSearchQuery("amber orchard ledger lantern")
	q.matchableWords = []string{"amber", "orchard", "ledger", "lantern"}
	small := proseScaleCandidates(500)
	large := proseScaleCandidates(1000)
	smallAllocs := testing.AllocsPerRun(3, func() {
		_ = selectSearchCandidates(small, q, 10, 3)
	})
	largeAllocs := testing.AllocsPerRun(3, func() {
		_ = selectSearchCandidates(large, q, 10, 3)
	})
	if largeAllocs > smallAllocs*2.4 {
		t.Fatalf("allocations grew superlinearly: 500=%0.f 1000=%0.f", smallAllocs, largeAllocs)
	}
	scale := proseScaleCandidates(4000)
	selected := selectSearchCandidates(scale, q, 10, 3)
	if len(selected) != 10 {
		t.Fatalf("selected %d candidates, want top-k 10", len(selected))
	}
	assertSearchResultGolden(t, searchCandidateResults(selected), "1839afd5464312390c65f319d2dda081904001e1fedddbc3b13d3ec32cea651b")

	durations := make([]time.Duration, 100)
	for run := range durations {
		started := time.Now()
		_ = selectSearchCandidates(scale, q, 10, 3)
		durations[run] = time.Since(started)
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	if p95 := durations[94]; p95 > 20*time.Millisecond {
		t.Fatalf("4000-parent selection p95 = %s, want <= 20ms", p95)
	}
}

func assertSearchResultGolden(t *testing.T, results []SearchResult, want string) {
	t.Helper()
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if got != want {
		t.Errorf("result JSON SHA-256 = %s, want %s", got, want)
	}
}

func searchCandidateResults(candidates []searchCandidate) []SearchResult {
	results := make([]SearchResult, len(candidates))
	for index := range candidates {
		results[index] = candidates[index].result
	}
	return results
}

func proseTestCandidate(
	path string, line int, score float64, snippet string, termCounts map[string]int,
) searchCandidate {
	return searchCandidate{
		result: SearchResult{
			FilePath: path, StartLine: line, EndLine: line, FocusLine: line,
			SnippetStartLine: line, SnippetEndLine: line, Language: "Markdown",
			Kind: "section", SymbolID: fmt.Sprintf("%s:%d", path, line),
			SymbolName: fmt.Sprintf("section-%d", line), Snippet: snippet,
		},
		termCounts: termCounts,
		docLength:  4,
		score:      score,
	}
}

func proseScaleCandidates(count int) []searchCandidate {
	candidates := make([]searchCandidate, 0, count)
	for index := 0; index < count; index++ {
		candidates = append(candidates, proseTestCandidate(
			fmt.Sprintf("sessions/session-%05d.md", index),
			1,
			float64(count-index),
			"# Amber orchard ledger lantern",
			map[string]int{"amber": 1, "orchard": 1, "ledger": 1, "lantern": 1},
		))
	}
	return candidates
}

func BenchmarkSelectSearchCandidatesProse4000(b *testing.B) {
	q := buildSearchQuery("amber orchard ledger lantern")
	q.matchableWords = []string{"amber", "orchard", "ledger", "lantern"}
	candidates := proseScaleCandidates(4000)
	b.ReportAllocs()
	b.ResetTimer()
	var selected []searchCandidate
	for iteration := 0; iteration < b.N; iteration++ {
		selected = selectSearchCandidates(candidates, q, 10, 3)
	}
	if len(selected) != 10 {
		b.Fatalf("selected %d candidates, want 10", len(selected))
	}
}
