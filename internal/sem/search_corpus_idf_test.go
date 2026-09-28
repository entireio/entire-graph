package sem

import (
	"fmt"
	"math"
	"testing"
)

// When preselection saw the whole corpus, idf uses repository-wide document frequencies; the
// preselected sample, chosen FOR containing the query words, makes every query word look common.
func TestSearchCorpusIDFStatisticsPrefersTheWholeCorpus(t *testing.T) {
	t.Parallel()
	sample := map[string]int{"rare": 90, "common": 95}
	corpus := map[string]int{"rare": 3, "common": 1500}
	df, files := searchCorpusIDFStatistics(sample, 96, corpus, 1700)
	if files != 1700 {
		t.Fatalf("file count = %d, want the corpus size 1700", files)
	}
	// One definition on every backend: content matches over the whole tree, no sample mixed in.
	if df["rare"] != 3 || df["common"] != 1500 {
		t.Fatalf("df = %v, want the corpus counts rare=3 common=1500", df)
	}
	idf := func(df, n int) float64 { return math.Log(1 + (float64(n-df)+0.5)/(float64(df)+0.5)) }
	sampleGap := idf(sample["rare"], 96) - idf(sample["common"], 96)
	corpusGap := idf(df["rare"], files) - idf(df["common"], files)
	if corpusGap <= sampleGap {
		t.Fatalf("corpus statistics should separate rare from common more than the sample: %f <= %f", corpusGap, sampleGap)
	}
}

// Without a whole-corpus pass (Git-narrowed or bounded preselection) the totals cover only part of
// the repository, so the sample statistics are kept rather than a guess.
func TestSearchCorpusIDFStatisticsKeepsTheSampleWithoutAWholeCorpusPass(t *testing.T) {
	t.Parallel()
	sample := map[string]int{"term": 7}
	for _, c := range []struct {
		name        string
		corpusFiles int
	}{{"no whole-corpus pass", 0}} {
		df, files := searchCorpusIDFStatistics(sample, 96, map[string]int{"term": 1}, c.corpusFiles)
		if files != 96 || df["term"] != 7 {
			t.Fatalf("%s: got df=%v files=%d, want the sample (7, 96)", c.name, df, files)
		}
	}
	if _, files := searchCorpusIDFStatistics(sample, 0, nil, 0); files != 1 {
		t.Fatalf("empty sample must still give a positive file count, got %d", files)
	}
}

// The same query must score the same whichever preselection backend served it. Cold (content pass
// over the whole tree) and warm (Git tree grep over the committed tree) used to disagree once idf
// came from the corpus on one backend and from the sample on the other.
func TestSearchCorpusIDFIsTheSameOnEveryBackend(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "a/target.go", "package a\n\n// Frobnicate reconciles the quorum ledger.\nfunc Frobnicate() {}\n")
	for i := 0; i < 6; i++ {
		write(t, repo, fmt.Sprintf("b/other%d.go", i), fmt.Sprintf("package b\n\n// Other%d touches the ledger.\nfunc Other%d() {}\n", i, i))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	const query = "reconciles quorum ledger"
	base := SearchOptions{Profile: ProfileFull, TopK: 5, MaxIndexedFiles: 2}
	cold := base
	cold.DisableCache = true
	coldResponse, err := SearchRepository(t.Context(), repo, "test-version", query, cold)
	if err != nil {
		t.Fatal(err)
	}
	warm := base
	warm.CacheDir = t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, warm.CacheDir); err != nil {
		t.Fatal(err)
	}
	warmResponse, err := SearchRepository(t.Context(), repo, "test-version", query, warm)
	if err != nil {
		t.Fatal(err)
	}
	if coldResponse.Stats.PreselectionBackend == warmResponse.Stats.PreselectionBackend {
		t.Fatalf("fixture drift: both runs used backend %q; the test needs two different ones", coldResponse.Stats.PreselectionBackend)
	}
	if len(coldResponse.Results) == 0 || len(warmResponse.Results) == 0 {
		t.Fatal("fixture drift: no results")
	}
	// idf is now backend-invariant, so the backends must agree on the RANKING and, for the head
	// hit, closely on the score. They need not agree exactly: BM25's average document length is
	// still taken over each backend's own candidate set (unchanged here). On main this fixture's
	// head score differed by 1.59 (12.15 vs 13.74); with corpus idf the residual is ~0.08.
	order := func(results []SearchResult) []string {
		var paths []string
		for _, result := range results {
			paths = append(paths, fmt.Sprintf("%s:%d", result.FilePath, result.StartLine))
		}
		return paths
	}
	// Git tree grep keeps every matched file while the content pass honours MaxIndexedFiles
	// (documented at the Git branch), so warm may return MORE results; the shared ones must come
	// in the same order.
	coldOrder, warmOrder := order(coldResponse.Results), order(warmResponse.Results)
	if len(warmOrder) < len(coldOrder) || fmt.Sprint(warmOrder[:len(coldOrder)]) != fmt.Sprint(coldOrder) {
		t.Fatalf("backends rank differently:\ncold (%s) %v\nwarm (%s) %v", coldResponse.Stats.PreselectionBackend, coldOrder,
			warmResponse.Stats.PreselectionBackend, warmOrder)
	}
	if gap := math.Abs(coldResponse.Results[0].Score - warmResponse.Results[0].Score); gap > 0.5 {
		t.Fatalf("head score differs by %.4f across backends; idf should no longer be the source of a gap this large", gap)
	}
}
