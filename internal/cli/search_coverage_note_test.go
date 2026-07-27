package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func coverageNoteResponse(note *sem.SearchCoverageNote) sem.SearchResponse {
	response := contractSearchResponse()
	response.CoverageNote = note
	return response
}

// TestWriteTextSearchRendersCoverageNoteUnderTheTest pins WHERE the note goes and what it says: it
// belongs to the covering-test block, so it must be inside it — after the test's own body and
// before the next section header.
func TestWriteTextSearchRendersCoverageNoteUnderTheTest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		note       *sem.SearchCoverageNote
		wantInside string
		wantAbsent bool
	}{
		{
			name:       "no note leaves the block exactly as it was",
			note:       nil,
			wantAbsent: true,
		},
		{
			name: "a note names the peers inside the covering-test block",
			note: &sem.SearchCoverageNote{
				Symbol: "appendPreprocessor", Total: 4,
				Peers: []string{"TestAppendPreprocessorCut", "TestHeadAppend"}, More: 1,
				FilePath: "tsdb/head_test.go",
			},
			wantInside: "ALSO COVERING appendPreprocessor (4 tests cover it; all must still pass): " +
				"TestAppendPreprocessorCut, TestHeadAppend +1 more in tsdb/head_test.go",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := writeTextSearch(&buf, coverageNoteResponse(testCase.note)); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if testCase.wantAbsent {
				if strings.Contains(out, "ALSO COVERING") {
					t.Fatalf("a coverage line appeared without a note:\n%s", out)
				}
				return
			}
			testAt := strings.Index(out, searchTextTestHeader)
			noteAt := strings.Index(out, "ALSO COVERING")
			nextAt := strings.Index(out, searchTextTypesHeader)
			if testAt < 0 || noteAt < 0 || nextAt < 0 {
				t.Fatalf("missing a header or the note:\n%s", out)
			}
			if !(testAt < noteAt && noteAt < nextAt) {
				t.Fatalf("the note is outside the covering-test block:\n%s", out)
			}
			if !strings.Contains(out, testCase.wantInside) {
				t.Fatalf("note line = missing %q in:\n%s", testCase.wantInside, out)
			}
		})
	}
}

// TestWriteTextSearchResultWithoutSnippet pins the locator degradation at the rendering end: an
// entry whose byte allowance could not hold one line of body prints its header and stops, rather
// than printing the empty string as if it were source.
func TestWriteTextSearchResultWithoutSnippet(t *testing.T) {
	t.Parallel()
	response := contractSearchResponse()
	for index := range response.Results {
		if response.Results[index].Section == sem.SearchSectionCoveringTest {
			response.Results[index].Snippet = ""
		}
	}
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	testAt := strings.Index(out, searchTextTestHeader)
	nextAt := strings.Index(out, searchTextTypesHeader)
	if testAt < 0 || nextAt < 0 || nextAt < testAt {
		t.Fatalf("missing a header:\n%s", out)
	}
	block := out[testAt:nextAt]
	if !strings.Contains(block, "TestAppendPreprocessor") {
		t.Fatalf("the locator lost the test's name:\n%s", block)
	}
	if strings.Contains(block, "\n\n") {
		t.Fatalf("an empty body printed as blank lines:\n%q", block)
	}
}

// TestSearchNDJSONCarriesCoverageNote pins that a streaming consumer gets the note too: an
// agent-facing block that exists only in the text rendering is invisible to every other caller.
func TestSearchNDJSONCarriesCoverageNote(t *testing.T) {
	t.Parallel()
	note := &sem.SearchCoverageNote{
		Symbol: "appendPreprocessor", Total: 3, Peers: []string{"TestHeadAppend"}, More: 1,
	}
	var buf bytes.Buffer
	if err := writeNdjsonSearch(&buf, coverageNoteResponse(note)); err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("ndjson line is not json: %v", err)
		}
		if record["record_type"] == "search_summary" {
			summary = record
		}
	}
	if summary == nil {
		t.Fatal("no search_summary record in the stream")
	}
	carried, ok := summary["coverage_note"].(map[string]any)
	if !ok {
		t.Fatalf("summary has no coverage_note: %v", summary)
	}
	if carried["symbol"] != "appendPreprocessor" {
		t.Errorf("coverage_note symbol = %v, want appendPreprocessor", carried["symbol"])
	}
	if total, _ := carried["total"].(float64); int(total) != 3 {
		t.Errorf("coverage_note total = %v, want 3", carried["total"])
	}
}
