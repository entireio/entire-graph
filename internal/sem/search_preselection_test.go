package sem

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"testing"
)

func TestFuseSearchPreselectionEvidenceUsesStableRRFAndPathTieBreak(t *testing.T) {
	base := []searchPreselectionEvidence{
		{Path: "zeta/early.go", OriginalScore: 3, PathScore: 3, ContentScore: 1},
		{Path: "alpha/middle.go", OriginalScore: 3, PathScore: 2, ContentScore: 3},
		{Path: "middle/late.go", OriginalScore: 3, PathScore: 1, ContentScore: 2, ConstraintScore: 3},
	}
	want := []string{"middle/late.go", "alpha/middle.go", "zeta/early.go"}
	for seed := int64(0); seed < 50; seed++ {
		shuffled := append([]searchPreselectionEvidence(nil), base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		fused := fuseSearchPreselectionEvidence(shuffled)
		got := evidencePaths(fused)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d fused paths = %#v, want %#v", seed, got, want)
		}
	}
}

func TestFuseSearchPreselectionEvidenceUsesExactRRFChannelRanks(t *testing.T) {
	fused := fuseSearchPreselectionEvidence([]searchPreselectionEvidence{
		{Path: "zeta/early.go", OriginalScore: 3, PathScore: 3, ContentScore: 1},
		{Path: "alpha/middle.go", OriginalScore: 3, PathScore: 2, ContentScore: 3},
		{Path: "middle/late.go", OriginalScore: 3, PathScore: 1, ContentScore: 2, ConstraintScore: 3},
	})
	want := map[string]float64{
		"zeta/early.go":   1.0/61 + 1.0/63,
		"alpha/middle.go": 1.0/62 + 1.0/61,
		"middle/late.go":  1.0/63 + 1.0/62 + 1.0/61,
	}
	for _, item := range fused {
		if math.Abs(item.rrfScore-want[item.Path]) > 1e-15 {
			t.Fatalf("%s RRF = %.17g, want %.17g", item.Path, item.rrfScore, want[item.Path])
		}
	}
}

func TestFuseSearchPreselectionEvidenceSkipsZeroChannelsAndBreaksTiesByPath(t *testing.T) {
	fused := fuseSearchPreselectionEvidence([]searchPreselectionEvidence{
		{Path: "zeta/tied.go", OriginalScore: 2, PathScore: 2},
		{Path: "alpha/tied.go", OriginalScore: 2, PathScore: 2},
		{Path: "middle/content.go", OriginalScore: 1, ContentScore: 3},
		{Path: "first/zero.go", OriginalScore: 100},
	})
	wantPaths := []string{"alpha/tied.go", "middle/content.go", "zeta/tied.go", "first/zero.go"}
	if got := evidencePaths(fused); !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("fused paths = %#v, want %#v", got, wantPaths)
	}
	wantScores := []float64{1.0 / 61, 1.0 / 61, 1.0 / 62, 0}
	for index, item := range fused {
		if math.Abs(item.rrfScore-wantScores[index]) > 1e-15 {
			t.Fatalf("%s RRF = %.17g, want %.17g", item.Path, item.rrfScore, wantScores[index])
		}
	}
}

func TestFuseSearchPreselectionEvidenceUsesOriginalScoreBeforePathTieBreak(t *testing.T) {
	fused := fuseSearchPreselectionEvidence([]searchPreselectionEvidence{
		{Path: "zeta/high.go", OriginalScore: 10, PathScore: 2},
		{Path: "alpha/high.go", OriginalScore: 10, PathScore: 2},
		{Path: "middle/low.go", OriginalScore: 1, PathScore: 2},
	})
	wantPaths := []string{"alpha/high.go", "zeta/high.go", "middle/low.go"}
	if got := evidencePaths(fused); !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("fused paths = %#v, want score before path tie-break %#v", got, wantPaths)
	}
	wantScores := []float64{1.0 / 61, 1.0 / 62, 1.0 / 63}
	for index, item := range fused {
		if math.Abs(item.rrfScore-wantScores[index]) > 1e-15 {
			t.Fatalf("%s RRF = %.17g, want %.17g", item.Path, item.rrfScore, wantScores[index])
		}
	}
}

func TestProgressivePreselectionStopsAtConfidentCoveredDiverseBase(t *testing.T) {
	q := preselectionTestQuery("alpha", "beta")
	evidence := fuseSearchPreselectionEvidence([]searchPreselectionEvidence{
		{Path: "cmd/main.go", OriginalScore: 9, PathScore: 9, ContentScore: 9, ConstraintScore: 9, MatchedTerms: []bool{true, true}},
		{Path: "internal/service.go", OriginalScore: 8, PathScore: 8, ContentScore: 8, ConstraintScore: 8, MatchedTerms: []bool{true, true}},
		{Path: "pkg/model.go", OriginalScore: 7, PathScore: 7, ContentScore: 7, ConstraintScore: 7, MatchedTerms: []bool{true, true}},
		{Path: "tools/task.go", OriginalScore: 6, PathScore: 6, ContentScore: 6, ConstraintScore: 6, MatchedTerms: []bool{true, true}},
		{Path: "archive/old.go", OriginalScore: 1, PathScore: 1, MatchedTerms: []bool{true, true}},
	})
	got := selectProgressiveSearchFiles(evidence, []bool{true, true}, q, 4)
	if got.Passes != 1 || got.Widened || got.Bounded {
		t.Fatalf("assessment = %#v, want a single confident base pass", got)
	}
	if !reflect.DeepEqual(got.Files, []string{"cmd/main.go", "internal/service.go", "pkg/model.go", "tools/task.go"}) {
		t.Fatalf("files = %#v", got.Files)
	}
	if got.BoundaryGap < searchPreselectionMinBoundaryGap || got.Confidence != 1 || got.Coverage != 1 || got.Diversity != 1 {
		t.Fatalf("assessment metrics = %#v", got)
	}
}

func TestProgressivePreselectionWidensForLowConfidence(t *testing.T) {
	q := preselectionTestQuery("alpha")
	evidence := make([]searchPreselectionEvidence, 0, 8)
	for index := 0; index < 8; index++ {
		evidence = append(evidence, searchPreselectionEvidence{
			Path:          "src/file" + string(rune('a'+index)) + ".go",
			OriginalScore: float64(8 - index),
			PathScore:     float64(8 - index),
			MatchedTerms:  []bool{true},
		})
	}
	got := selectProgressiveSearchFiles(fuseSearchPreselectionEvidence(evidence), []bool{true}, q, 4)
	if got.Passes != 2 || got.Examined != 12 || !got.Widened || got.Bounded || len(got.Files) != 8 {
		t.Fatalf("assessment = %#v, want widths 4 then 8", got)
	}
}

func TestProgressivePreselectionWidensForCoverage(t *testing.T) {
	q := preselectionTestQuery("alpha", "beta")
	evidence := make([]searchPreselectionEvidence, 0, 8)
	for index := 0; index < 8; index++ {
		matched := []bool{true, index == 5}
		evidence = append(evidence, searchPreselectionEvidence{
			Path:          "src/part" + string(rune('a'+index)) + ".go",
			OriginalScore: float64(8 - index),
			PathScore:     float64(8 - index),
			MatchedTerms:  matched,
		})
	}
	got := selectProgressiveSearchFiles(fuseSearchPreselectionEvidence(evidence), []bool{true, true}, q, 4)
	if got.Passes != 2 || !got.Widened || got.Coverage != 1 {
		t.Fatalf("assessment = %#v, want widened complete coverage", got)
	}
}

func TestProgressivePreselectionWidensForDirectoryDiversity(t *testing.T) {
	q := preselectionTestQuery("alpha")
	evidence := []searchPreselectionEvidence{
		{Path: "src/one.go", OriginalScore: 9, PathScore: 9, ContentScore: 9, ConstraintScore: 9, MatchedTerms: []bool{true}},
		{Path: "src/two.go", OriginalScore: 8, PathScore: 8, ContentScore: 8, ConstraintScore: 8, MatchedTerms: []bool{true}},
		{Path: "src/three.go", OriginalScore: 7, PathScore: 7, ContentScore: 7, ConstraintScore: 7, MatchedTerms: []bool{true}},
		{Path: "src/four.go", OriginalScore: 6, PathScore: 6, ContentScore: 6, ConstraintScore: 6, MatchedTerms: []bool{true}},
		{Path: "cmd/main.go", OriginalScore: 5, PathScore: 5, MatchedTerms: []bool{true}},
		{Path: "pkg/model.go", OriginalScore: 4, ContentScore: 5, MatchedTerms: []bool{true}},
		{Path: "tools/task.go", OriginalScore: 3, ConstraintScore: 5, MatchedTerms: []bool{true}},
		{Path: "docs/notes.go", OriginalScore: 2, PathScore: 1, MatchedTerms: []bool{true}},
	}
	got := selectProgressiveSearchFiles(fuseSearchPreselectionEvidence(evidence), []bool{true}, q, 4)
	if got.Passes != 2 || !got.Widened || got.Diversity != 1 {
		t.Fatalf("assessment = %#v, want widening to available directories", got)
	}
}

func TestProgressivePreselectionStopsAtFourXBound(t *testing.T) {
	q := preselectionTestQuery("alpha", "beta")
	evidence := make([]searchPreselectionEvidence, 0, 20)
	for index := 0; index < 20; index++ {
		evidence = append(evidence, searchPreselectionEvidence{
			Path:          "src/file" + string(rune('a'+index)) + ".go",
			OriginalScore: float64(20 - index),
			PathScore:     float64(20 - index),
			MatchedTerms:  []bool{true, false},
		})
	}
	got := selectProgressiveSearchFiles(fuseSearchPreselectionEvidence(evidence), []bool{true, true}, q, 2)
	if len(got.Files) != 8 || got.Examined != 14 || !got.Bounded || got.Coverage >= 1 {
		t.Fatalf("assessment = %#v, want the four-times cap with incomplete coverage", got)
	}
}

// TestProgressivePreselectionConfidenceIsNotTheRRFStep pins issue 273. Five hundred files
// that match the one query term equally well sit in a single fusion channel, so each fused
// score is 1/(61+rank) and the boundary gap is the RRF step at the cut, 1/445 at the
// 384-file cap. That gap still decides widening, so the selection is unchanged, but it said
// nothing about the files kept and preselection_confidence used to report it as if it did.
// Every kept file holds the term, so the confidence reported now is 1.
func TestProgressivePreselectionConfidenceIsNotTheRRFStep(t *testing.T) {
	q := preselectionTestQuery("alpha")
	evidence := make([]searchPreselectionEvidence, 0, 500)
	for index := 0; index < 500; index++ {
		evidence = append(evidence, searchPreselectionEvidence{
			Path:          fmt.Sprintf("src/file%03d.go", index),
			OriginalScore: float64(500 - index),
			ContentScore:  float64(500 - index),
			MatchedTerms:  []bool{true},
		})
	}
	got := selectProgressiveSearchFiles(fuseSearchPreselectionEvidence(evidence), []bool{true}, q, 96)
	if got.Passes != 3 || len(got.Files) != 384 || !got.Widened || !got.Bounded {
		t.Fatalf("assessment = passes %d files %d widened %v bounded %v, want widths 96, 192, 384 and the cap",
			got.Passes, len(got.Files), got.Widened, got.Bounded)
	}
	if want := 1.0 / 445; math.Abs(got.BoundaryGap-want) > 1e-12 {
		t.Fatalf("boundary gap = %.17g, want the RRF step %.17g", got.BoundaryGap, want)
	}
	if got.Confidence != 1 || got.Coverage != 1 {
		t.Fatalf("confidence = %v coverage = %v, want 1: every kept file holds the query term", got.Confidence, got.Coverage)
	}
}

// TestProgressivePreselectionConfidenceFallsWhenTheCapDropsATerm: the only file holding
// "beta" ranks below twenty files that match "alpha" by path and content, so the 8-file cap
// leaves it out and the kept files hold half of the query's matched-term weight.
func TestProgressivePreselectionConfidenceFallsWhenTheCapDropsATerm(t *testing.T) {
	q := preselectionTestQuery("alpha", "beta")
	evidence := make([]searchPreselectionEvidence, 0, 21)
	for index := 0; index < 20; index++ {
		evidence = append(evidence, searchPreselectionEvidence{
			Path:          fmt.Sprintf("src/alpha%02d.go", index),
			OriginalScore: float64(40 - index),
			PathScore:     float64(20 - index),
			ContentScore:  float64(20 - index),
			MatchedTerms:  []bool{true, false},
		})
	}
	evidence = append(evidence, searchPreselectionEvidence{
		Path: "src/other.go", OriginalScore: 1, ContentScore: 1, MatchedTerms: []bool{false, true},
	})
	got := selectProgressiveSearchFiles(fuseSearchPreselectionEvidence(evidence), []bool{true, true}, q, 2)
	if len(got.Files) != 8 || !got.Bounded {
		t.Fatalf("assessment = %#v, want the 8-file cap", got)
	}
	for _, path := range got.Files {
		if path == "src/other.go" {
			t.Fatalf("files = %#v, want the beta file left out", got.Files)
		}
	}
	if got.Confidence != 0.5 || got.Coverage != 0.5 {
		t.Fatalf("confidence = %v coverage = %v, want 0.5", got.Confidence, got.Coverage)
	}
}

func evidencePaths(evidence []searchPreselectionEvidence) []string {
	paths := make([]string, len(evidence))
	for index, item := range evidence {
		paths[index] = item.Path
	}
	return paths
}

func preselectionTestQuery(terms ...string) searchQuery {
	weights := make(map[string]float64, len(terms))
	for _, term := range terms {
		weights[term] = 1
	}
	return searchQuery{terms: terms, weights: weights}
}

// A kept file the content pass never scored, such as one the registration bridge adds, counts
// the terms it holds on both sides of the coverage: it holds them, so they were found in the
// repository. Left out of the denominator, a term only it holds would not count at all.
func TestSearchKeptTermCoverageCountsTheTermsABridgedFileHolds(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("alpha beta gamma")
	marks := func(terms ...string) []bool {
		marked := make([]bool, len(q.terms))
		for index, term := range q.terms {
			marked[index] = slices.Contains(terms, term)
		}
		return marked
	}
	files := []searchFileCandidate{
		{path: "kept.go", matchedTerms: marks("alpha")},
		{path: "dropped.go", matchedTerms: marks("gamma")},
	}
	termsOf := func(path string) []bool {
		if path == "bridged.go" {
			return marks("beta")
		}
		return nil
	}
	// gamma is only in a file the content pass scored and the cut left out.
	got := searchKeptTermCoverage(files, []string{"kept.go", "bridged.go"}, marks("alpha", "gamma"), q, termsOf)
	weight := func(term string) float64 { return q.weights[term] }
	if want := (weight("alpha") + weight("beta")) / (weight("alpha") + weight("beta") + weight("gamma")); math.Abs(got-want) > 1e-9 {
		t.Fatalf("coverage = %v, want %v", got, want)
	}
}
