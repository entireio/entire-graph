package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func confidenceSearchResponse(scores []float64, paths []string, section string) sem.SearchResponse {
	results := make([]sem.SearchResult, 0, len(scores))
	for index, score := range scores {
		path := "src/a.go"
		if index < len(paths) {
			path = paths[index]
		}
		result := sem.SearchResult{
			Rank: index + 1, Score: score, FilePath: path,
			StartLine: 10, EndLine: 12, FocusLine: 11,
			SnippetStartLine: 10, SnippetEndLine: 12,
			SymbolName: fmt.Sprintf("symbol%d", index+1),
			Snippet:    "line one\nline two\nline three\n",
			Signals:    []string{},
		}
		if index == 0 {
			result.Section = section
		}
		results = append(results, result)
	}
	return sem.SearchResponse{
		Query: "q", Results: results,
		Warnings: []sem.ProviderWarning{}, PartialFailures: []sem.PartialFailure{},
	}
}

// TestAgentSearchPrintsScore is the core of the reported defect: `--format agent` hid the
// only signal that separates a real hit from the best of a bad lot.
func TestAgentSearchPrintsScore(t *testing.T) {
	t.Parallel()
	response := confidenceSearchResponse([]float64{35.337, 33.271, 33.145},
		[]string{"src/a.go", "src/b.go", "src/c.go"}, "")
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 0); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"s=35.3", "s=33.3", "s=33.1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent output is missing %q:\n%s", want, text)
		}
	}
}

func TestAgentSearchScoreSurvivesTightBudgets(t *testing.T) {
	t.Parallel()
	response := confidenceSearchResponse([]float64{35.337, 33.271, 33.145},
		[]string{"src/a.go", "src/b.go", "src/c.go"}, "")
	// Walk budgets down from comfortable to too-small. Wherever a ranked location is
	// printed at all, its score must be printed with it; the score is only allowed to
	// disappear together with the rank and the symbol name (the minimal variant).
	for budget := 400; budget >= 40; budget -= 20 {
		var out bytes.Buffer
		if err := writeAgentSearch(&out, response, budget); err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		text := out.String()
		if len(text) > budget {
			t.Fatalf("budget %d exceeded: %d bytes", budget, len(text))
		}
		if strings.Contains(text, "1. src/a.go") && !strings.Contains(text, "s=35.3") {
			t.Fatalf("budget %d printed a ranked location without its score:\n%s", budget, text)
		}
	}
}

func TestAgentSearchLowConfidenceMarker(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		scores []float64
		paths  []string
		want   bool
	}{
		{
			name:   "strong payload has no marker",
			scores: []float64{35.3, 33.3, 33.1},
			paths:  []string{"src/a.go", "src/b.go", "src/c.go"},
		},
		{
			name:   "weak dispersed payload is marked",
			scores: []float64{9.6, 8.4, 8.1},
			paths:  []string{"src/a.go", "src/b.go", "src/c.go"},
			want:   true,
		},
		{
			name:   "diffuse payload above the ceiling is not marked",
			scores: []float64{14.0, 13.4, 12.7},
			paths:  []string{"src/a.go", "src/b.go", "src/c.go"},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := confidenceSearchResponse(testCase.scores, testCase.paths, "")
			var agent bytes.Buffer
			if err := writeAgentSearch(&agent, response, 0); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(agent.String(), "LOW CONFIDENCE"); got != testCase.want {
				t.Fatalf("agent marker = %v, want %v:\n%s", got, testCase.want, agent.String())
			}
			var text bytes.Buffer
			if err := writeTextSearch(&text, response); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(text.String(), "LOW CONFIDENCE"); got != testCase.want {
				t.Fatalf("text marker = %v, want %v:\n%s", got, testCase.want, text.String())
			}
		})
	}
}

func TestSearchLowConfidenceNoticeRetainsWeakScoreThreshold(t *testing.T) {
	t.Parallel()
	response := confidenceSearchResponse([]float64{9.6, 8.4, 8.1},
		[]string{"src/a.go", "src/b.go", "src/c.go"}, "")
	full, _ := searchLowConfidenceNotices(response)
	want := "top score 9.6 (weak, below 12) and top results agree on nothing"
	if !strings.Contains(string(full), want) {
		t.Fatalf("low-score notice is missing %q:\n%s", want, full)
	}
}

// TestSearchLowConfidenceNoticeRoundedScoreNeverContradictsThreshold: a raw score of 11.96
// is below the ceiling but DISPLAYS as 12.0, so the "below 12" suffix must stay off — the
// suffix follows the score the reader sees, not the raw value.
func TestSearchLowConfidenceNoticeRoundedScoreNeverContradictsThreshold(t *testing.T) {
	t.Parallel()
	response := confidenceSearchResponse([]float64{11.96, 10.0, 9.0},
		[]string{"src/a.go", "src/b.go", "src/c.go"}, "")
	full, _ := searchLowConfidenceNotices(response)
	text := string(full)
	want := "top score 12.0 and top results agree on nothing"
	if !strings.Contains(text, want) {
		t.Fatalf("rounded-score notice is missing %q:\n%s", want, text)
	}
	if strings.Contains(text, "below 12") {
		t.Fatalf("notice labels a displayed 12.0 as below 12:\n%s", text)
	}
}

func TestSearchLowConfidenceNoticeUsesActualHighScoreReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		response   sem.SearchResponse
		wantReason string
	}{
		{
			name: "near tie",
			response: confidenceSearchResponse([]float64{27.50, 27.51, 20.0},
				[]string{"src/a.go", "src/a.go", "src/a.go"}, ""),
			wantReason: "ranks 1 and 2 are tied (0.0100 apart) - the ranking did not choose",
		},
		{
			name: "documentation wrapper",
			response: func() sem.SearchResponse {
				response := confidenceSearchResponse([]float64{27.5, 20.0},
					[]string{"src/a.go", "src/a.go"}, "")
				response.Results[0].Snippet = "// Usage notes\n// More usage notes\nfunc wrapper() {}\n"
				return response
			}(),
			wantReason: "top hit is a documentation comment, not an implementation",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			full, _ := searchLowConfidenceNotices(testCase.response)
			text := string(full)
			if !strings.Contains(text, testCase.wantReason) {
				t.Fatalf("high-score notice is missing reason %q:\n%s", testCase.wantReason, text)
			}
			if strings.Contains(text, "below 12") {
				t.Fatalf("high-score notice falsely labels its score as below 12:\n%s", text)
			}
		})
	}
}

// TestAgentSearchLowConfidenceMarkerNeverBreaksTheByteCap: the marker is a notice, so under
// a cap too small to hold it the ranking wins and the cap is still honoured exactly.
func TestAgentSearchLowConfidenceMarkerNeverBreaksTheByteCap(t *testing.T) {
	t.Parallel()
	response := confidenceSearchResponse([]float64{9.6, 8.4, 8.1},
		[]string{"src/a.go", "src/b.go", "src/c.go"}, "")
	sawMarker, sawResultWithoutMarker := false, false
	for budget := 600; budget >= 20; budget -= 10 {
		var out bytes.Buffer
		if err := writeAgentSearch(&out, response, budget); err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		text := out.String()
		if len(text) > budget {
			t.Fatalf("budget %d exceeded: %d bytes\n%s", budget, len(text), text)
		}
		if strings.Contains(text, "LOW CONFIDENCE") || strings.Contains(text, "!LOW") {
			sawMarker = true
		} else if strings.Contains(text, "src/a.go") {
			sawResultWithoutMarker = true
		}
	}
	if !sawMarker {
		t.Fatal("the marker never appeared at any budget")
	}
	if !sawResultWithoutMarker {
		t.Fatal("the marker never yielded to the ranking at a tight budget")
	}
}

func TestSearchLowConfidenceNoticesEmptyForGoodAndEmptyPayloads(t *testing.T) {
	t.Parallel()
	strong := confidenceSearchResponse([]float64{35.3, 33.3},
		[]string{"src/a.go", "src/b.go"}, "")
	if full, compact := searchLowConfidenceNotices(strong); len(full) != 0 || len(compact) != 0 {
		t.Fatalf("strong payload produced a notice: %q / %q", full, compact)
	}
	empty := sem.SearchResponse{Query: "q", Results: nil}
	if full, compact := searchLowConfidenceNotices(empty); len(full) != 0 || len(compact) != 0 {
		t.Fatalf("empty payload produced a notice: %q / %q", full, compact)
	}
}

func TestSearchDeepFlagParsing(t *testing.T) {
	t.Parallel()
	shallow, _, err := parseSearchFlags([]string{"--query", "q"})
	if err != nil {
		t.Fatal(err)
	}
	if shallow.Deep {
		t.Fatal("--deep must default to off")
	}
	if shallow.TopK != 0 {
		t.Fatalf("TopK default = %d, want 0 (sem applies the default)", shallow.TopK)
	}
	// The regression the flag replaces: retrieval strategy must not change with --top-k.
	high, _, err := parseSearchFlags([]string{"--query", "q", "--top-k", "40"})
	if err != nil {
		t.Fatal(err)
	}
	if high.Deep {
		t.Fatal("--top-k 40 must not imply --deep")
	}
	deep, _, err := parseSearchFlags([]string{"--query", "q", "--deep"})
	if err != nil {
		t.Fatal(err)
	}
	if !deep.Deep {
		t.Fatal("--deep was not parsed")
	}
}
