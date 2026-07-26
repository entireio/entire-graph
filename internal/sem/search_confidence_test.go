package sem

import "testing"

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
