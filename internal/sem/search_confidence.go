package sem

// Telling "nothing matched" from "here is the answer"
// ==================================================
//
// `search` always returns its best N regions, formatted identically whether the repo
// contains what was asked for or not. Field report: a query about a technology entirely
// absent from a repo came back as six confident, well-formatted hits, and the only thing in
// the payload that could have distinguished them from a real answer was the relevance score
// — which `--format agent` did not print.
//
// Two fixes, both required:
//
//  1. the score is printed, so a caller can judge for itself;
//  2. a weak payload is marked explicitly, because a caller that has never seen this repo's
//     score distribution has no baseline to judge against.
//
// CALIBRATION
// -----------
// Measured over 14 repos in 9 languages (the SWE-bench Multilingual worktrees) on 140
// queries in three classes:
//
//	class     n   min    p25    median  p75    max     what it is
//	good      56  21.01  34.42  41.26   46.87  68.87   derived from each repo's OWN symbols,
//	                                                   so the target is guaranteed present
//	diffuse   42  -0.79  11.50  14.66   16.06  22.14   legitimate but broad engineering
//	                                                   questions ("how are errors reported")
//	absent    42   1.64   7.69   9.77   12.07  17.57   technologies verified absent from the
//	                                                   repo (0 occurrences of every key term)
//
// The good class never dipped below 21, so any ceiling at or below 21 is free of false
// positives on queries whose target exists. The diffuse and absent classes OVERLAP, which is
// why the rule is not a bare threshold: a diffuse question is a legitimate query and marking
// it is a cost. An earlier score-only attempt fired on 46% of queries, which trains callers
// to ignore the marker.
//
// The rule adopted below (ceiling 12 with corroboration, hard floor 7 without) measures:
//
//	good     0/56  ( 0.0%)   <- the constraint: never fire when the target exists
//	diffuse  9/42  (21.4%)
//	absent  27/42  (64.3%)
//	overall 36/140 (25.7%)   <- roughly half the earlier attempt's firing rate
//
// The residual absent misses are queries whose words genuinely occur in the repo in another
// sense ("resource", "definition", "controller"); no score threshold can separate those, and
// the printed score is what lets a caller do it.
const (
	// lowConfidenceTopScore is the ceiling below which a weak top score, CORROBORATED by
	// dispersion, is marked.
	lowConfidenceTopScore = 12.0

	// lowConfidenceFloorScore is the score below which no corroboration is required. It
	// sits far under the weakest measured score for a query whose target existed (21.0),
	// so a payload this weak is not an answer in any repo. It exists because the
	// dispersion test looks for a ranking spread over unrelated files, and the very
	// weakest payloads instead pile up in ONE incidental file — the most obviously
	// unanswerable case was the one the corroboration requirement excluded.
	lowConfidenceFloorScore = 7.0

	// lowConfidenceTopFiles is how many distinct files the head of the ranking must be
	// spread over before a weak score counts as corroborated. 3 means "no two of the top
	// three agree", which is what an answer assembled out of unrelated matches looks like.
	lowConfidenceTopFiles = 3

	// lowConfidenceWindow is how many head results the dispersion test looks at.
	lowConfidenceWindow = 3
)

// SearchConfidence summarises how much a payload's own numbers support treating its top
// result as an answer.
type SearchConfidence struct {
	// Low is true when the top score is below the hard floor, or below the ceiling AND
	// corroborated by dispersion.
	Low bool
	// TopScore is the top result's relevance score, 0 when there are no results.
	TopScore float64
	// TopFiles is how many distinct files the head of the ranking occupies.
	TopFiles int
	// Reason names why the payload was marked, empty when Low is false.
	Reason string
}

// AssessSearchConfidence evaluates a payload against the low-confidence rule.
//
// An empty payload is NOT marked: "no results" is already unambiguous, and a marker on top
// of it would only add bytes.
func AssessSearchConfidence(response SearchResponse) SearchConfidence {
	results := response.Results
	if len(results) == 0 {
		return SearchConfidence{}
	}
	window := results
	if len(window) > lowConfidenceWindow {
		window = window[:lowConfidenceWindow]
	}
	files := make(map[string]bool, len(window))
	for _, result := range window {
		files[result.FilePath] = true
	}
	assessment := SearchConfidence{TopScore: results[0].Score, TopFiles: len(files)}
	switch {
	case assessment.TopScore < lowConfidenceFloorScore:
		assessment.Low = true
		assessment.Reason = "no result scored above the floor"
	case assessment.TopScore >= lowConfidenceTopScore:
	case results[0].Section == searchSectionDocs:
		assessment.Low = true
		assessment.Reason = "top hit holds no program text"
	case len(files) >= lowConfidenceTopFiles:
		assessment.Low = true
		assessment.Reason = "top results agree on nothing"
	}
	return assessment
}

// LowConfidenceScoreCeiling exposes the calibrated ceiling for renderers and tests.
func LowConfidenceScoreCeiling() float64 { return lowConfidenceTopScore }
