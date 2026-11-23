package sem

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func confidenceResponse(scores []float64, files []string, section string) SearchResponse {
	results := make([]SearchResult, 0, len(scores))
	for index, score := range scores {
		path := "src/a.go"
		if index < len(files) {
			path = files[index]
		}
		result := SearchResult{Rank: index + 1, Score: score, FilePath: path}
		if index == 0 {
			result.Section = section
		}
		results = append(results, result)
	}
	return SearchResponse{Results: results}
}

func TestAssessSearchConfidence(t *testing.T) {
	t.Parallel()
	threeFiles := []string{"src/a.go", "src/b.go", "src/c.go"}
	oneFile := []string{"src/a.go", "src/a.go", "src/a.go"}
	cases := []struct {
		name       string
		scores     []float64
		files      []string
		section    string
		wantLow    bool
		wantReason string
	}{
		{
			name: "no results is never marked", scores: nil,
			wantLow: false,
		},
		{
			name:   "strong hit spread over files is not marked",
			scores: []float64{35.3, 33.3, 33.1}, files: threeFiles,
			wantLow: false,
		},
		{
			name:   "diffuse but above the ceiling is not marked",
			scores: []float64{14.0, 13.4, 12.7}, files: threeFiles,
			wantLow: false,
		},
		{
			name:   "exactly at the ceiling is not marked",
			scores: []float64{12.0, 11.0, 10.0}, files: threeFiles,
			wantLow: false,
		},
		{
			name:   "nonsense below the ceiling and dispersed is marked",
			scores: []float64{9.6, 8.4, 8.1}, files: threeFiles,
			wantLow: true, wantReason: "top results agree on nothing",
		},
		{
			name:   "weak but concentrated in one file is not marked",
			scores: []float64{11.5, 11.0, 10.0}, files: oneFile,
			wantLow: false,
		},
		{
			name:   "below the floor is marked even when concentrated",
			scores: []float64{4.1, 3.0, 2.0}, files: oneFile,
			wantLow: true, wantReason: "no result scored above the floor",
		},
		{
			name:   "exactly at the floor still needs corroboration",
			scores: []float64{7.0, 6.0, 5.0}, files: oneFile,
			wantLow: false,
		},
		{
			name:   "weak with a non-code top hit is marked without dispersion",
			scores: []float64{11.0, 10.0, 9.0}, files: oneFile, section: searchSectionDocs,
			wantLow: true, wantReason: "top hit holds no program text",
		},
		{
			name:   "strong non-code top hit is not marked",
			scores: []float64{31.0, 10.0, 9.0}, files: oneFile, section: searchSectionDocs,
			wantLow: false,
		},
		{
			name:   "single weak result cannot be dispersed",
			scores: []float64{10.0}, files: []string{"src/a.go"},
			wantLow: false,
		},
		{
			name:   "single result below the floor is marked",
			scores: []float64{2.0}, files: []string{"src/a.go"},
			wantLow: true, wantReason: "no result scored above the floor",
		},
		{
			name:   "negative top score is marked",
			scores: []float64{-0.79, -1.0}, files: oneFile,
			wantLow: true, wantReason: "no result scored above the floor",
		},
		{
			name:   "dispersion beyond the window does not count",
			scores: []float64{11.0, 10.0, 9.0, 8.0}, files: []string{"src/a.go", "src/a.go", "src/a.go", "src/z.go"},
			wantLow: false,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := AssessSearchConfidence(
				confidenceResponse(testCase.scores, testCase.files, testCase.section))
			if got.Low != testCase.wantLow {
				t.Fatalf("Low = %v (reason %q, top %.2f), want %v",
					got.Low, got.Reason, got.TopScore, testCase.wantLow)
			}
			if got.Reason != testCase.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, testCase.wantReason)
			}
		})
	}
}

// TestLowConfidenceCalibrationHolds replays the recorded score distributions the constants
// were chosen from (14 repos, 9 languages, 140 queries; see the comment in
// search_confidence.go). It pins the property the constants exist for: the marker must never
// fire on a query whose target is present, and must still catch most queries naming something
// the repo does not contain. Changing a constant without re-measuring breaks this test.
func TestLowConfidenceCalibrationHolds(t *testing.T) {
	t.Parallel()
	// (top score, distinct files in the top 3, top hit is non-code)
	type sample struct {
		top   float64
		files int
		docs  bool
	}
	good := []sample{
		{21.01, 3, false}, {21.48, 3, false}, {21.91, 3, false}, {23.44, 3, false},
		{24.16, 1, false}, {25.93, 2, true}, {27.56, 3, false}, {28.39, 3, false}, {28.98, 3, false},
		{29.37, 3, false}, {31.22, 3, false}, {32.17, 3, false}, {32.29, 3, false},
		{33.96, 3, false}, {34.42, 3, false}, {34.61, 2, false}, {35.55, 3, false},
		{35.65, 3, false}, {35.81, 3, false}, {36.75, 2, false}, {36.86, 3, false},
		{36.97, 2, false}, {37.68, 2, false}, {37.71, 2, false}, {38.59, 3, false},
		{38.71, 1, false}, {41.13, 2, false}, {41.25, 2, false}, {41.27, 2, false},
		{42.00, 3, false}, {42.01, 2, false}, {42.05, 3, false}, {42.41, 2, false},
		{42.68, 1, false}, {42.77, 2, false}, {43.03, 3, false}, {44.05, 3, false},
		{44.27, 1, false}, {44.29, 2, false}, {45.07, 2, false}, {45.82, 2, false},
		{45.83, 2, false}, {46.87, 1, false}, {47.27, 1, false}, {47.77, 2, false},
		{47.86, 3, false}, {47.89, 2, false}, {48.14, 1, false}, {48.74, 2, false},
		{50.33, 2, false}, {50.43, 2, false}, {51.94, 2, false}, {54.22, 3, false},
		{60.64, 3, false}, {63.14, 2, false}, {68.87, 1, false},
	}
	diffuse := []sample{
		{-0.79, 1, false}, {6.21, 3, false}, {7.43, 2, false}, {8.44, 2, false}, {8.72, 3, false},
		{9.46, 3, false}, {10.15, 3, false}, {10.65, 3, false}, {10.81, 3, false}, {11.37, 3, false},
		{11.50, 3, false}, {12.17, 3, false}, {12.50, 3, false}, {13.25, 3, false},
		{13.78, 3, false}, {13.86, 3, false}, {13.90, 3, false}, {13.99, 3, false},
		{14.23, 3, false}, {14.29, 2, false}, {14.61, 3, false}, {14.72, 3, false},
		{14.78, 3, false}, {14.79, 3, false}, {14.84, 3, false}, {14.86, 3, false},
		{15.03, 3, false}, {15.18, 3, false}, {15.37, 3, false}, {15.69, 3, false},
		{15.87, 3, false}, {16.06, 2, false}, {16.13, 3, false}, {16.79, 3, false},
		{16.93, 3, false}, {17.40, 3, false}, {18.01, 2, false}, {18.12, 3, false},
		{19.23, 3, false}, {19.97, 3, false}, {20.52, 2, false}, {22.14, 3, false},
	}
	absent := []sample{
		{1.64, 1, false}, {1.64, 1, false}, {3.40, 3, false}, {4.08, 2, false}, {4.67, 3, true},
		{5.50, 3, false}, {5.50, 3, false}, {6.21, 2, false}, {6.64, 2, false}, {7.22, 2, false},
		{7.69, 3, false}, {7.96, 3, false}, {8.02, 2, false}, {8.27, 3, false}, {8.50, 3, false},
		{8.68, 3, false}, {8.80, 3, false}, {8.86, 2, false}, {9.09, 3, false}, {9.28, 3, false},
		{9.69, 3, false}, {9.85, 3, false}, {10.63, 3, false}, {10.73, 3, false}, {11.42, 3, false},
		{11.55, 3, false}, {11.67, 3, false}, {11.74, 3, false}, {11.76, 3, false},
		{11.89, 3, false}, {12.06, 3, false}, {12.07, 3, false}, {12.31, 2, false},
		{12.51, 3, false}, {13.16, 3, false}, {13.42, 3, false}, {13.88, 2, false},
		{14.04, 3, false}, {14.84, 3, false}, {16.23, 2, false}, {16.51, 3, false},
		{17.57, 3, false},
	}
	fires := func(s sample) bool {
		files := make([]string, s.files)
		for index := range files {
			files[index] = string(rune('a'+index)) + ".go"
		}
		for len(files) < 3 {
			files = append(files, files[0])
		}
		section := ""
		if s.docs {
			section = searchSectionDocs
		}
		return AssessSearchConfidence(confidenceResponse(
			[]float64{s.top, s.top - 1, s.top - 2}, files, section)).Low
	}
	count := func(samples []sample) int {
		hits := 0
		for _, s := range samples {
			if fires(s) {
				hits++
			}
		}
		return hits
	}
	if got := count(good); got != 0 {
		t.Fatalf("marker fired on %d/%d queries whose target exists; it must never fire there",
			got, len(good))
	}
	// Measured: 9/42 (21.4%). The bound is the design constraint, not the exact count:
	// an earlier score-only attempt fired on 46% of ALL queries and was rejected as blunt.
	if got := count(diffuse); got > 12 {
		t.Fatalf("marker fired on %d/%d legitimate broad queries; too blunt", got, len(diffuse))
	}
	// Measured: 27/42 (64.3%). The residue is queries whose words genuinely occur in the
	// repo in another sense; the printed score is what lets a caller judge those.
	if got := count(absent); got < len(absent)/2 {
		t.Fatalf("marker caught only %d/%d queries naming absent technology", got, len(absent))
	}
	if got := count(good) + count(diffuse) + count(absent); got > (len(good)+len(diffuse)+len(absent))/3 {
		t.Fatalf("marker fired on %d of %d queries overall; too blunt to be trusted",
			got, len(good)+len(diffuse)+len(absent))
	}
}

// A promoted fix site legitimately carries a LOWER score than the test it displaced, because
// promoteFixSiteOverLeadingTest reorders without rescoring. The tie test asks whether the top two
// scores are indistinguishable, and that question has no direction — reading it as a signed
// subtraction made the marker fire on every reordered payload, which is the false positive this
// file's own calibration explicitly rejects ("a marker that always fires teaches the reader to
// ignore it").
func TestAssessSearchConfidenceIgnoresTheDirectionOfTheTopTwoGap(t *testing.T) {
	t.Parallel()
	// The measured sharkdp__bat-2260 payload: rank 1 is the promoted source at 73.3157, rank 2 the
	// displaced test at 77.3391 — 4.02 points apart, which is nobody's idea of a tie.
	promoted := confidenceResponse(
		[]float64{73.3157, 77.3391, 40.0},
		[]string{"src/a.go", "tests/a_test.go", "src/a.go"}, "")
	if assessment := AssessSearchConfidence(promoted); assessment.Low {
		t.Fatalf("a 4.02-point separation was reported as low confidence: %q", assessment.Reason)
	}
	// A genuine near-tie still fires, in either direction.
	for _, scores := range [][]float64{{40.0, 40.01, 20.0}, {40.01, 40.0, 20.0}} {
		assessment := AssessSearchConfidence(
			confidenceResponse(scores, []string{"src/a.go", "src/a.go", "src/a.go"}, ""))
		if !assessment.Low {
			t.Fatalf("a 0.01-point tie at %v was not reported", scores)
		}
	}
}

// A capped cold selection that left out every file matching part of the query is marked
// whatever the ranking looks like, and ahead of the ranking's own reasons: the advice for it
// is to widen or re-query, not to conclude the repository lacks the answer.
func TestAssessSearchConfidenceMarksAColdIndexThatDroppedPartOfTheQuery(t *testing.T) {
	t.Parallel()
	threeFiles := []string{"src/a.go", "src/b.go", "src/c.go"}
	withStats := func(scores []float64, stats SearchStats) SearchResponse {
		response := confidenceResponse(scores, threeFiles, "")
		response.Stats = stats
		return response
	}
	cases := []struct {
		name       string
		response   SearchResponse
		wantLow    bool
		wantReason string
	}{
		{
			name:     "a complete capped selection is not marked",
			response: withStats([]float64{35.3, 33.3, 33.1}, SearchStats{PreselectionBounded: true, PreselectionConfidence: preselectionConfidenceOf(1)}),
		},
		{
			// Every path without a capped cold selection: the committed-tree grep, an
			// explicit --max-indexed-files, a corpus under the limit.
			name:     "no capped selection is not marked",
			response: withStats([]float64{35.3, 33.3, 33.1}, SearchStats{}),
		},
		{
			name:       "a strong ranking over a selection that dropped a term is marked",
			response:   withStats([]float64{35.3, 33.3, 33.1}, SearchStats{PreselectionBounded: true, PreselectionConfidence: preselectionConfidenceOf(0.5)}),
			wantLow:    true,
			wantReason: "the cold index left out every file matching part of the query (preselection confidence 0.50)",
		},
		{
			// The Git shortlist of a large worktree can drop a term before the cap is reached.
			name:       "a selection that dropped a term before its cap is marked",
			response:   withStats([]float64{35.3, 33.3, 33.1}, SearchStats{PreselectionConfidence: preselectionConfidenceOf(0.5)}),
			wantLow:    true,
			wantReason: "the cold index left out every file matching part of the query (preselection confidence 0.50)",
		},
		{
			name:       "a selection whose files hold no matched term is marked",
			response:   withStats([]float64{4.1, 3.0, 2.0}, SearchStats{PreselectionConfidence: preselectionConfidenceOf(0)}),
			wantLow:    true,
			wantReason: "the cold index left out every file matching part of the query (preselection confidence 0.00)",
		},
		{
			name:       "the dropped term outranks a weak score",
			response:   withStats([]float64{4.1, 3.0, 2.0}, SearchStats{PreselectionBounded: true, PreselectionConfidence: preselectionConfidenceOf(0.75)}),
			wantLow:    true,
			wantReason: "the cold index left out every file matching part of the query (preselection confidence 0.75)",
		},
		{
			name:       "the printed confidence never rounds up to 1",
			response:   withStats([]float64{35.3, 33.3, 33.1}, SearchStats{PreselectionBounded: true, PreselectionConfidence: preselectionConfidenceOf(0.996)}),
			wantLow:    true,
			wantReason: "the cold index left out every file matching part of the query (preselection confidence 0.99)",
		},
		{
			// No results there is not absence: the answer may be in a file the cut dropped.
			name:       "an empty payload is marked too",
			response:   SearchResponse{Stats: SearchStats{PreselectionBounded: true, PreselectionConfidence: preselectionConfidenceOf(0.5)}},
			wantLow:    true,
			wantReason: "the cold index left out every file matching part of the query (preselection confidence 0.50)",
		},
		{
			name:     "an empty payload from a complete selection is not marked",
			response: SearchResponse{Stats: SearchStats{PreselectionBounded: true, PreselectionConfidence: preselectionConfidenceOf(1)}},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := AssessSearchConfidence(testCase.response)
			if got.Low != testCase.wantLow || got.Reason != testCase.wantReason {
				t.Fatalf("assessment = Low %v reason %q, want Low %v reason %q",
					got.Low, got.Reason, testCase.wantLow, testCase.wantReason)
			}
			if (got.Advice != "") != (testCase.wantReason != "") {
				t.Fatalf("advice = %q, want advice only with the preselection reason", got.Advice)
			}
		})
	}
}

// TestSearchRepositoryReportsPreselectionConfidenceFromTheFilesKept runs the default cold
// working-tree path end to end. Four hundred files name "needle" in their path and body; one
// other file holds "quillwort" and nothing else, so it ranks last and the 384-file cap leaves
// it out. The selection is what it was before issue 273's fix: still widened to the cap. The
// confidence now says a query term is missing from every indexed file, and the notice says so.
func TestSearchRepositoryReportsPreselectionConfidenceFromTheFilesKept(t *testing.T) {
	t.Parallel()
	repo := writeNeedleQuillwortFixture(t)

	complete := searchColdWorktree(t, repo, "needle", false)
	stats := complete.Stats
	if stats.FilesIndexed != 384 || !stats.PreselectionBounded || stats.PreselectionPasses != 3 {
		t.Fatalf("stats = %#v, want the cold selection widened to its 384-file cap", stats)
	}
	if confidenceValue(stats.PreselectionConfidence) != 1 {
		t.Fatalf("preselection confidence = %v, want 1: every indexed file holds the only term", confidenceValue(stats.PreselectionConfidence))
	}
	if stats.PreselectionBoundaryGap <= 0 || stats.PreselectionBoundaryGap >= searchPreselectionMinBoundaryGap {
		t.Fatalf("boundary gap = %v, want the small RRF step that keeps widening", stats.PreselectionBoundaryGap)
	}

	dropped := searchColdWorktree(t, repo, "needle quillwort", false)
	if dropped.Stats.FilesIndexed != 384 || !dropped.Stats.PreselectionBounded {
		t.Fatalf("stats = %#v, want the 384-file cap", dropped.Stats)
	}
	requirePreselectionNotice(t, "needle quillwort", dropped)
}

// --deep's sparse pass reads every candidate, the file left out included, but its candidates
// still compete for the same few results with the indexed ones. On this fixture the quillwort
// file first appears at rank 123 of --top-k 400, so the notice stays right.
func TestSearchRepositoryKeepsThePreselectionNoticeForDeepSearches(t *testing.T) {
	t.Parallel()
	deep := searchColdWorktree(t, writeNeedleQuillwortFixture(t), "needle quillwort", true)
	if deep.Stats.SparseFilesRead == 0 {
		t.Fatalf("stats = %#v, want a sparse pass", deep.Stats)
	}
	for _, result := range deep.Results {
		if result.FilePath == "docs/zz_other.md" {
			t.Fatalf("the deep ranking now reaches the file the cold index left out; " +
				"the preselection notice should account for the sparse pass")
		}
	}
	requirePreselectionNotice(t, "needle quillwort", deep)
}

// writeNeedleQuillwortFixture writes 400 files that name "needle" in their path and body and
// one that holds "quillwort" and nothing else, so it ranks last and the 384-file cap leaves
// it out.
func writeNeedleQuillwortFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for index := 0; index < 400; index++ {
		write(t, repo, fmt.Sprintf("docs/needle_%03d.md", index), "needle\n")
	}
	write(t, repo, "docs/zz_other.md", "quillwort\n")
	return repo
}

func searchColdWorktree(t *testing.T, repo, query string, deep bool) SearchResponse {
	t.Helper()
	response, err := SearchRepository(t.Context(), repo, "test", query, SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		Deep:     deep,
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// requirePreselectionNotice fails unless the response reports a confidence below 1 and the
// assessment carries the preselection notice with its advice.
func requirePreselectionNotice(t *testing.T, query string, response SearchResponse) {
	t.Helper()
	if confidence := confidenceValue(response.Stats.PreselectionConfidence); confidence < 0 || confidence >= 1 {
		t.Fatalf("%q: preselection confidence = %v, want below 1: no indexed file holds the second term", query, confidence)
	}
	assessment := AssessSearchConfidence(response)
	if !assessment.Low || assessment.Advice != searchPreselectionLowAdvice ||
		!strings.HasPrefix(assessment.Reason, "the cold index left out every file matching part of the query") {
		t.Fatalf("%q: assessment = %#v, want the preselection notice", query, assessment)
	}
}

// A TopK-adaptive limit is not one the caller chose, so its fixed cut is assessed like the
// capped selection's; an explicit --max-indexed-files of the same size reports nothing.
func TestSearchRepositoryReportsPreselectionConfidenceForAnAdaptiveLimit(t *testing.T) {
	t.Parallel()
	repo := writeNeedleQuillwortFixture(t)
	const query = "needle quillwort"
	response, err := SearchRepository(t.Context(), repo, "test", query, SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     40,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Stats.FilesIndexed != 120 {
		t.Fatalf("files indexed = %d, want the adaptive 120", response.Stats.FilesIndexed)
	}
	requirePreselectionNotice(t, query, response)

	response, err = SearchRepository(t.Context(), repo, "test", query, SearchOptions{
		Worktree:        true,
		Profile:         ProfileSyntaxOnly,
		TopK:            40,
		MaxIndexedFiles: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Stats.PreselectionConfidence != nil {
		t.Fatalf("explicit limit: preselection confidence = %v, want none", *response.Stats.PreselectionConfidence)
	}
}

func preselectionConfidenceOf(value float64) *float64 { return &value }

// confidenceValue reads a reported preselection confidence, -1 when none was reported.
func confidenceValue(confidence *float64) float64 {
	if confidence == nil {
		return -1
	}
	return *confidence
}

// A capped selection whose kept files hold none of the matched terms reports 0, which must
// stay distinguishable in JSON from a query that never ran the capped selection.
func TestSearchStatsKeepAZeroPreselectionConfidenceInJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stats SearchStats
		want  bool
	}{
		{stats: SearchStats{PreselectionConfidence: preselectionConfidenceOf(0)}, want: true},
		{stats: SearchStats{}, want: false},
	} {
		encoded, err := json.Marshal(tc.stats)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"preselection_confidence":0`) != tc.want {
			t.Fatalf("json = %s, want preselection_confidence present: %v", encoded, tc.want)
		}
	}
}
