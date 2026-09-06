package sem

import (
	"strings"
	"testing"
)

// The window a nameless hit keeps is bounded, is never grown, and always reports exactly the lines
// it returns — a header naming a span its own source does not contain sends the reader to the wrong
// lines, which is the failure the bare locator already had.
func TestSearchLocatorWindow(t *testing.T) {
	t.Parallel()
	wide := make([]string, 0, 60)
	for index := 1; index <= 60; index++ {
		wide = append(wide, "L")
	}
	wide[29] = "MATCHED"
	for _, testCase := range []struct {
		name       string
		result     SearchResult
		wantStart  int
		wantEnd    int
		wantSource string
	}{
		{
			// The scikit-learn__scikit-learn-14629 window, live from db24f74: five lines, inside the
			// cap, so it passes through whole and the range is the ranker's own.
			name: "short window passes through whole",
			result: SearchResult{
				StartLine: 22, EndLine: 26, FocusLine: 24, SnippetStartLine: 22, SnippetEndLine: 26,
				Snippet: "from .base import BaseEstimator\nfrom .base import ClassifierMixin\n" +
					"from .model_selection import cross_val_predict\nfrom .utils import check_array\n" +
					"from .utils.fixes import parallel_helper",
			},
			wantStart: 22, wantEnd: 26,
			wantSource: "from .base import BaseEstimator\nfrom .base import ClassifierMixin\n" +
				"from .model_selection import cross_val_predict\nfrom .utils import check_array\n" +
				"from .utils.fixes import parallel_helper",
		},
		{
			name: "no source means no window",
			result: SearchResult{
				StartLine: 3135, EndLine: 3137, FocusLine: 3135, SnippetStartLine: 3135, SnippetEndLine: 3137,
			},
			wantStart: 0, wantEnd: 0, wantSource: "",
		},
		{
			name: "a single line reports a single-line range",
			result: SearchResult{
				StartLine: 30, EndLine: 30, FocusLine: 30, SnippetStartLine: 30, SnippetEndLine: 30,
				Snippet: "shallowReactive,",
			},
			wantStart: 30, wantEnd: 30, wantSource: "shallowReactive,",
		},
		{
			// A 60-line head window can reach the locator path through the render diet's body cap.
			// It is clipped to the cap, centred so the matched line survives, and the reported range
			// moves with the clip.
			name: "a wide window is clipped around the matched line",
			result: SearchResult{
				StartLine: 1, EndLine: 60, FocusLine: 30, SnippetStartLine: 1, SnippetEndLine: 60,
				Snippet: strings.Join(wide, "\n"),
			},
			wantStart: 28, wantEnd: 33,
			wantSource: "L\nL\nMATCHED\nL\nL\nL",
		},
		{
			// A match at the very top clips forward, never off the front of the file.
			name: "a match at the head clips forward",
			result: SearchResult{
				StartLine: 1, EndLine: 60, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 60,
				Snippet: strings.Join(wide, "\n"),
			},
			wantStart: 1, wantEnd: 6, wantSource: "L\nL\nL\nL\nL\nL",
		},
		{
			// A match at the very end clips backward, never past the end of the window.
			name: "a match at the tail clips backward",
			result: SearchResult{
				StartLine: 1, EndLine: 60, FocusLine: 60, SnippetStartLine: 1, SnippetEndLine: 60,
				Snippet: strings.Join(wide, "\n"),
			},
			wantStart: 55, wantEnd: 60, wantSource: "L\nL\nL\nL\nL\nL",
		},
		{
			// No usable focus line: keep the head of the window rather than guessing a centre.
			name: "no focus keeps the head of the window",
			result: SearchResult{
				StartLine: 1, EndLine: 60, SnippetStartLine: 1, SnippetEndLine: 60,
				Snippet: strings.Join(wide, "\n"),
			},
			wantStart: 1, wantEnd: 6, wantSource: "L\nL\nL\nL\nL\nL",
		},
		{
			// Without a snippet range the ranked region is the anchor, so the reported range is
			// still the one the source actually spans.
			name: "falls back to the ranked start line",
			result: SearchResult{
				StartLine: 208, EndLine: 209, FocusLine: 208,
				Snippet: "unsigned long raxTouch(raxNode *n);\nvoid raxStart(raxIterator *it);",
			},
			wantStart: 208, wantEnd: 209,
			wantSource: "unsigned long raxTouch(raxNode *n);\nvoid raxStart(raxIterator *it);",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			start, end, source := SearchLocatorWindow(testCase.result)
			if start != testCase.wantStart || end != testCase.wantEnd {
				t.Errorf("range = %d-%d, want %d-%d", start, end, testCase.wantStart, testCase.wantEnd)
			}
			if source != testCase.wantSource {
				t.Errorf("source =\n%q\nwant\n%q", source, testCase.wantSource)
			}
			if source == "" {
				return
			}
			// THE INVARIANT: the reported range describes exactly the lines returned with it.
			if got := len(strings.Split(source, "\n")); got != end-start+1 {
				t.Errorf("range %d-%d spans %d lines but %d were returned", start, end, end-start+1, got)
			}
			// It is a ceiling, never a floor: the window can only ever shrink.
			if got := len(strings.Split(source, "\n")); got > searchLocatorWindowLines {
				t.Errorf("returned %d lines, cap is %d", got, searchLocatorWindowLines)
			}
			if len(source) > len(testCase.result.Snippet) {
				t.Errorf("window grew: %d bytes returned from a %d-byte snippet",
					len(source), len(testCase.result.Snippet))
			}
		})
	}
}
