package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// namelessLocatorResponse is the scikit-learn__scikit-learn-14629 payload shape, taken from the live
// db24f74 binary at the shipped defaults (--top-k 10 --max-snippet-lines 6 --max-context-bytes
// 16384) for the issue title "AttributeError with cross_val_predict(method='predict_proba') when
// using MultiOuputClassifier".
//
// Ranks 1-3 take the render diet's three body slots. Rank 4 has a symbol name, so its demotion keeps
// a locator that still says what it is and how to fetch it. Rank 5 is `sklearn/multioutput.py:24` —
// `from .model_selection import cross_val_predict`, the import coupling the issue is about, in the
// file the fix lands in — and it has no enclosing indexed symbol, so it has no name. It came back as
// coordinates and nothing else while the ranker had already computed and allocated its window.
func namelessLocatorResponse() sem.SearchResponse {
	return sem.SearchResponse{Results: []sem.SearchResult{
		{
			Rank: 1, FilePath: "sklearn/model_selection/_validation.py", StartLine: 698, EndLine: 777,
			FocusLine: 774, SnippetStartLine: 771, SnippetEndLine: 776, Score: 85.7534,
			SymbolName: "cross_val_predict", Kind: "function",
			Signals: []string{"body", "exact-symbol", "symbol-name"},
			Snippet: "    if not _check_is_permutation(test_indices, _num_samples(X)):\n" +
				"        raise ValueError('cross_val_predict only works for partitions')",
		},
		{
			Rank: 2, FilePath: "sklearn/ensemble/gradient_boosting.py", StartLine: 2197, EndLine: 2225,
			FocusLine: 2224, SnippetStartLine: 2197, SnippetEndLine: 2225, Score: 83.6608,
			SymbolName: "GradientBoostingClassifier.predict_proba", Kind: "method",
			Signals: []string{"body", "exact-symbol", "complete-symbol"},
			Snippet: "    def predict_proba(self, X):\n        raw_predictions = self.decision_function(X)",
		},
		{
			Rank: 3, FilePath: "sklearn/base.py", StartLine: 632, EndLine: 645,
			FocusLine: 632, SnippetStartLine: 632, SnippetEndLine: 645, Score: 82.7824,
			SymbolName: "is_classifier", Kind: "function",
			Signals: []string{"graph:calls", "complete-symbol"},
			Snippet: "def is_classifier(estimator):\n    return getattr(estimator, \"_estimator_type\", None) == \"classifier\"",
		},
		{
			Rank: 4, FilePath: "sklearn/ensemble/voting.py", StartLine: 326, EndLine: 342,
			FocusLine: 340, SnippetStartLine: 326, SnippetEndLine: 342, Score: 80.1,
			SymbolName: "VotingClassifier.predict_proba", Kind: "method",
			Signals: []string{"body", "exact-symbol"},
			Snippet: "    def predict_proba(self):\n        return self._predict_proba",
		},
		{
			Rank: 5, FilePath: "sklearn/multioutput.py", StartLine: 22, EndLine: 26,
			FocusLine: 24, SnippetStartLine: 22, SnippetEndLine: 26, Score: 79.4,
			Signals: []string{"body", "symbol-usage"},
			Snippet: "from .base import BaseEstimator, clone, MetaEstimatorMixin\n" +
				"from .base import RegressorMixin, ClassifierMixin, is_classifier\n" +
				"from .model_selection import cross_val_predict\n" +
				"from .utils import check_array, check_X_y, check_random_state\n" +
				"from .utils.fixes import parallel_helper",
		},
	}}
}

// A demoted hit with no symbol name must not come back as coordinates alone. It has no name for the
// `[body: def NAME]` follow-up either, so the bare form leaves an agent with a file read as the only
// way to act on the line — which is the cost the payload exists to remove.
func TestWriteTextSearchKeepsSourceForANamelessLocator(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, namelessLocatorResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// THE DEFECT: the bare locator, coordinates and nothing else.
	if strings.Contains(out, "5. sklearn/multioutput.py:24\n") {
		t.Fatalf("nameless hit rendered as a bare locator:\n%s", out)
	}
	// The source the ranker had already computed is what the line carries instead.
	if !strings.Contains(out, "from .model_selection import cross_val_predict") {
		t.Fatalf("nameless hit lost the window the ranker allocated it:\n%s", out)
	}
	// The header names exactly the lines printed under it, and keeps the matched line, which was the
	// bare locator's only piece of information.
	if !strings.Contains(out, "5. sklearn/multioutput.py:22-26 focus=24\n") {
		t.Fatalf("nameless hit lost its range or its focus line:\n%s", out)
	}
	// The named demotions are untouched: this changes what a locator does only when there is no name
	// to put on one.
	if !strings.Contains(out, "4. sklearn/ensemble/voting.py:340 VotingClassifier.predict_proba  [body: def VotingClassifier.predict_proba]\n") {
		t.Fatalf("a NAMED locator changed shape:\n%s", out)
	}
	if strings.Contains(out, "return self._predict_proba") {
		t.Fatalf("a named locator gained a body it was denied:\n%s", out)
	}
	// The body diet still holds at three.
	if got := strings.Count(out, "score="); got != 3 {
		t.Fatalf("body count changed: %d bodied hits\n%s", got, out)
	}
}

// A nameless hit that carries no source at all has nothing to keep, so it still falls back to the
// bare locator. A renderer that printed an empty body instead would read as a truncation bug.
func TestWriteTextSearchStillEmitsABareLocatorWithoutSource(t *testing.T) {
	t.Parallel()
	response := namelessLocatorResponse()
	response.Results[4].Snippet = ""
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "5. sklearn/multioutput.py:24\n") {
		t.Fatalf("a sourceless nameless hit lost its locator:\n%s", out)
	}
}

// The cap is a ceiling on what a demoted hit keeps, not a licence to print whatever some other lever
// widened the window to. A 60-line head window reaching this path through the diet's body cap keeps
// six lines centred on the match, not sixty.
func TestWriteTextSearchClipsAWideNamelessWindow(t *testing.T) {
	t.Parallel()
	lines := make([]string, 0, 60)
	for index := 1; index <= 60; index++ {
		lines = append(lines, "line"+string(rune('a'+index%26))+"()")
	}
	lines[29] = "MATCHED_LINE()"
	response := namelessLocatorResponse()
	response.Results[4] = sem.SearchResult{
		Rank: 5, FilePath: "py/objint.h", StartLine: 1, EndLine: 60, FocusLine: 30,
		SnippetStartLine: 1, SnippetEndLine: 60, Score: 79.4,
		Signals: []string{"head-window"}, Snippet: strings.Join(lines, "\n"),
	}
	// A head window claims a complete body, so force it down the locator path the way the diet's
	// body cap does: three bodies are already spent above it.
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "MATCHED_LINE()") {
		t.Fatalf("clipping removed the matched line:\n%s", out)
	}
	if !strings.Contains(out, "5. py/objint.h:28-33 focus=30\n") {
		t.Fatalf("clipped window did not report the lines it printed:\n%s", out)
	}
}
