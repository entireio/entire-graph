package sem

import (
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
	// The sample count is a floor (posting totals are content-only; the sample also counts path
	// matches), so a term can never look rarer than the sample already proves it is.
	if df["rare"] != 90 || df["common"] != 1500 {
		t.Fatalf("df = %v, want rare=90 (sample floor) common=1500 (corpus)", df)
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
	}{{"no whole-corpus pass", 0}, {"corpus smaller than sample", 50}} {
		df, files := searchCorpusIDFStatistics(sample, 96, map[string]int{"term": 1}, c.corpusFiles)
		if files != 96 || df["term"] != 7 {
			t.Fatalf("%s: got df=%v files=%d, want the sample (7, 96)", c.name, df, files)
		}
	}
	if _, files := searchCorpusIDFStatistics(sample, 0, nil, 0); files != 1 {
		t.Fatalf("empty sample must still give a positive file count, got %d", files)
	}
}
