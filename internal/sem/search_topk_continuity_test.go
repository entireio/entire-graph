package sem

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// continuityRepo builds a repo big enough that the retrieval path is observable: more files
// than defaultSearchMaxIndexedFiles so preselection matters, and several genuinely relevant
// symbols so a deeper top-k has somewhere to go.
func continuityRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for index := 0; index < 40; index++ {
		write(t, repo, fmt.Sprintf("pkg/handler%02d.go", index), fmt.Sprintf(`package pkg

// RetryDelivery%02d retries webhook delivery with exponential backoff.
func RetryDelivery%02d(attempt int) int {
	backoff := 1
	for i := 0; i < attempt; i++ {
		backoff = backoff * 2
	}
	return backoff
}
`, index, index))
	}
	for index := 0; index < 60; index++ {
		write(t, repo, fmt.Sprintf("other/filler%02d.go", index),
			"package other\n"+strings.Repeat("// unrelated filler line\n", 30))
	}
	return repo
}

func searchAtTopK(t *testing.T, repo string, topK int, deep bool) SearchResponse {
	t.Helper()
	response, err := SearchRepository(t.Context(), repo, "test",
		"retry webhook delivery with exponential backoff", SearchOptions{
			Worktree: true,
			Profile:  ProfileFull,
			TopK:     topK,
			Deep:     deep,
		})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// TestSearchIsContinuousAcrossTheTopKBoundary pins the whole reported regression: at
// --top-k 11 the tool used to switch retrieval strategy, which changed the scoring to
// 1/rank, changed the preselection backend, read every file in the repo, shrank the indexed
// file pool and stopped deduplicating regions. Asking for one more result must change only
// how many results come back.
func TestSearchIsContinuousAcrossTheTopKBoundary(t *testing.T) {
	repo := continuityRepo(t)
	at10 := searchAtTopK(t, repo, defaultSearchTopK, false)
	for _, topK := range []int{defaultSearchTopK + 1, defaultSearchTopK + 2, 20} {
		response := searchAtTopK(t, repo, topK, false)

		if response.Stats.PreselectionBackend != at10.Stats.PreselectionBackend {
			t.Fatalf("top-k %d flipped the preselection backend: %q -> %q",
				topK, at10.Stats.PreselectionBackend, response.Stats.PreselectionBackend)
		}
		if response.Stats.FilesIndexed < at10.Stats.FilesIndexed {
			t.Fatalf("top-k %d shrank the indexed file pool: %d -> %d",
				topK, at10.Stats.FilesIndexed, response.Stats.FilesIndexed)
		}
		if response.Stats.SparseCandidates != 0 {
			t.Fatalf("top-k %d silently enabled deep sparse retrieval (%d sparse candidates)",
				topK, response.Stats.SparseCandidates)
		}
		if response.Stats.SparseFilesRead != at10.Stats.SparseFilesRead {
			t.Fatalf("top-k %d changed how many files were read: %d -> %d",
				topK, at10.Stats.SparseFilesRead, response.Stats.SparseFilesRead)
		}
		if len(response.Results) == 0 || len(at10.Results) == 0 {
			t.Fatalf("top-k %d returned no results", topK)
		}
		if math.Abs(response.Results[0].Score-at10.Results[0].Score) > 1e-6 {
			t.Fatalf("top-k %d changed the top score: %.4f -> %.4f",
				topK, at10.Results[0].Score, response.Results[0].Score)
		}
		assertNotReciprocalRankScores(t, topK, response)
		assertDistinctRegions(t, topK, response)
	}
}

// TestCommittedSearchKeepsTheFastBackendAtEveryTopK covers the half of the regression that
// only shows on the committed-tree fast path: with a warm exact preindex, top-k 10 used the
// Git object-store scanner (0 files read) while top-k 11 fell back to reading every eligible
// file in the repo — an ~8x preselect-latency cliff triggered by asking for one more result.
func TestCommittedSearchKeepsTheFastBackendAtEveryTopK(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "test")
	for index := 0; index < 30; index++ {
		write(t, repo, fmt.Sprintf("src/deliver%02d.go", index), fmt.Sprintf(`package src

// RetryDelivery%02d retries webhook delivery with backoff.
func RetryDelivery%02d(attempt int) int { return attempt * %d }
`, index, index, index+1))
	}
	for index := 0; index < 80; index++ {
		write(t, repo, fmt.Sprintf("noise/file%02d.go", index),
			fmt.Sprintf("package noise\nfunc Noise%d() int { return %d }\n", index, index))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}
	committed := func(topK int) SearchResponse {
		response, err := SearchRepository(t.Context(), repo, "test",
			"retry webhook delivery with backoff", SearchOptions{
				Profile: ProfileSyntaxOnly, TopK: topK, MaxIndexedFiles: 1, CacheDir: cacheDir,
			})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	baseline := committed(defaultSearchTopK)
	if baseline.Stats.PreselectionBackend != "git-tree-grep" {
		t.Fatalf("baseline did not take the committed fast path: %#v", baseline.Stats)
	}
	for _, topK := range []int{defaultSearchTopK + 1, defaultSearchTopK + 5, 20} {
		response := committed(topK)
		if response.Stats.PreselectionBackend != baseline.Stats.PreselectionBackend {
			t.Fatalf("top-k %d flipped the backend: %q -> %q", topK,
				baseline.Stats.PreselectionBackend, response.Stats.PreselectionBackend)
		}
		if response.Stats.FilesContentRead != baseline.Stats.FilesContentRead {
			t.Fatalf("top-k %d changed preselection reads: %d -> %d", topK,
				baseline.Stats.FilesContentRead, response.Stats.FilesContentRead)
		}
		if response.Stats.FilesIndexed != baseline.Stats.FilesIndexed {
			t.Fatalf("top-k %d changed the indexed file pool: %d -> %d", topK,
				baseline.Stats.FilesIndexed, response.Stats.FilesIndexed)
		}
	}
}

// TestSearchDeepModeKeepsRealScoresAndDedupes covers the opt-in path: the deep ranking is
// fused by rank, but the reported score must still be a relevance score (it used to be
// literally 1, 0.5, 0.333...) and no region may occupy two ranks.
func TestSearchDeepModeKeepsRealScoresAndDedupes(t *testing.T) {
	repo := continuityRepo(t)
	response := searchAtTopK(t, repo, defaultSearchTopK+1, true)
	if response.Stats.SparseCandidates == 0 {
		t.Fatal("--deep did not run the sparse pass")
	}
	if len(response.Results) == 0 {
		t.Fatal("--deep returned no results")
	}
	assertNotReciprocalRankScores(t, defaultSearchTopK+1, response)
	assertDistinctRegions(t, defaultSearchTopK+1, response)
	if response.Results[0].Score <= 1.5 {
		t.Fatalf("--deep top score %.4f is not on the relevance scale", response.Results[0].Score)
	}
}

func assertNotReciprocalRankScores(t *testing.T, topK int, response SearchResponse) {
	t.Helper()
	matches := 0
	for index, result := range response.Results {
		if math.Abs(result.Score-1/float64(index+1)) < 1e-4 {
			matches++
		}
	}
	if matches == len(response.Results) && len(response.Results) > 2 {
		t.Fatalf("top-k %d reported 1/rank instead of relevance scores: %#v",
			topK, response.Results)
	}
}

func assertDistinctRegions(t *testing.T, topK int, response SearchResponse) {
	t.Helper()
	seen := make(map[string]int, len(response.Results))
	for _, result := range response.Results {
		key := fmt.Sprintf("%s:%d-%d", result.FilePath, result.StartLine, result.EndLine)
		seen[key]++
		if seen[key] > 1 {
			t.Fatalf("top-k %d returned region %s at %d ranks", topK, key, seen[key])
		}
	}
}

func TestSelectHybridCandidatesDedupesEveryBlock(t *testing.T) {
	t.Parallel()
	candidate := func(path string, start int, score float64) searchCandidate {
		return searchCandidate{
			score:  score,
			result: SearchResult{FilePath: path, StartLine: start, EndLine: start + 4},
		}
	}
	// The shared region is the top of BOTH rankings, which is exactly the case the
	// unchecked head appends used to seat twice.
	shared := candidate("src/a.go", 10, 30)
	semantic := []searchCandidate{
		shared,
		candidate("src/b.go", 20, 25),
		candidate("src/c.go", 30, 20),
	}
	sparse := []searchCandidate{
		shared,
		candidate("src/d.go", 40, 18),
		candidate("src/e.go", 50, 15),
	}
	selected := selectHybridCandidates(semantic, sparse, 6)
	seen := map[string]int{}
	for _, item := range selected {
		seen[searchCandidateLocationKey(item)]++
	}
	for key, count := range seen {
		if count > 1 {
			t.Fatalf("region %q selected %d times: %#v", key, count, selected)
		}
	}
	if len(selected) < 5 {
		t.Fatalf("dedup dropped coverage: %d selected, want >= 5", len(selected))
	}
}

func TestRescaleFusedCandidateScores(t *testing.T) {
	t.Parallel()
	candidate := func(path string, start int, score float64) searchCandidate {
		return searchCandidate{
			score:  score,
			result: SearchResult{FilePath: path, StartLine: start, EndLine: start + 4},
		}
	}
	semantic := []searchCandidate{
		candidate("src/a.go", 10, 30),
		candidate("src/b.go", 20, 20),
	}
	sparse := []searchCandidate{
		candidate("src/c.go", 30, 4),
		candidate("src/d.go", 40, 2),
	}
	// Simulate what fusion leaves behind: rank artifacts in place of scores.
	fused := []searchCandidate{
		candidate("src/a.go", 10, 1),
		candidate("src/c.go", 30, 0.5),
		candidate("src/b.go", 20, 0.3333),
		candidate("src/d.go", 40, 0.25),
	}
	got := rescaleFusedCandidateScores(fused, semantic, sparse)
	if got[0].score != 30 || got[2].score != 20 {
		t.Fatalf("semantic scores were not restored: %#v", got)
	}
	// The sparse block is anchored on the semantic maximum: 4 -> 30, 2 -> 15.
	if math.Abs(got[1].score-30) > 1e-6 || math.Abs(got[3].score-15) > 1e-6 {
		t.Fatalf("sparse scores were not rescaled onto the semantic scale: %#v", got)
	}
	// Ordering inside the sparse block is preserved.
	if got[1].score <= got[3].score-1e-9 {
		t.Fatalf("rescale inverted the sparse ordering: %#v", got)
	}
}

func TestRescaleFusedCandidateScoresWithoutSemanticSide(t *testing.T) {
	t.Parallel()
	candidate := func(path string, start int, score float64) searchCandidate {
		return searchCandidate{
			score:  score,
			result: SearchResult{FilePath: path, StartLine: start, EndLine: start + 4},
		}
	}
	sparse := []searchCandidate{candidate("src/c.go", 30, 4.5)}
	fused := []searchCandidate{candidate("src/c.go", 30, 0.0164)}
	got := rescaleFusedCandidateScores(fused, nil, sparse)
	if math.Abs(got[0].score-4.5) > 1e-6 {
		t.Fatalf("sparse-only score was not restored: %#v", got)
	}
	if rescaleFusedCandidateScores(nil, nil, sparse) != nil {
		t.Fatal("empty selection should stay empty")
	}
}
