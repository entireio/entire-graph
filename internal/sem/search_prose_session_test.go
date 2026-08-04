package sem

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
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
	assertSearchResultGolden(t, response.Results, "cf344af38c8181f2713419a5dbe1dbb6e678cc80388655d9ae45b48d10210cd2")
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
	assertSearchResultGolden(t, response.Results, "ee130bf86f3530092984cb8afecaf562587ac92df82d2cb3e22753058d2de3d9")
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
	assertSearchResultGolden(t, response.Results, "96dc692b46c9aaa610615524728b09c0b30a75ed9ee865c724919c4fd0f7d482")
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
	assertSearchResultGolden(t, response.Results, "62fb330f73720bf8aee6aecc090f2058069ca41c54e158e0633923ac22370143")
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
			assertSearchResultGolden(t, response.Results, "9a910ba484b55dbbb18922249dcd2cb1c934bbcd29a1c5d924f3eb8607d13977")
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
