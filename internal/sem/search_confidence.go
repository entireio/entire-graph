package sem

import (
	"fmt"
	"math"
	"strings"
)

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

	// lowConfidenceTieGap is the ABSOLUTE top1-top2 score gap below which the ranking has not actually
	// CHOSEN. Measured on projectlombok__lombok-3486, which shipped a clean-looking payload at a gap of
	// 0.0100 and cost $2.22 while the agent worked out by hand which of two same-named methods it meant.
	//
	// ABSOLUTE, not relative, and the difference is the whole calibration. Read as a 5% relative gap
	// this fired on 56 of 56 calibration queries whose target provably exists — a marker that always
	// fires teaches the reader to ignore it, which is strictly worse than not having one. Real payloads
	// separate their top two by 4-5% routinely; a gap of 0.05 raw score points is a near-exact tie, which
	// is the thing lombok actually exhibited.
	lowConfidenceTieGap = 0.05

	// lowConfidenceWrapperLines is how many comment or literal lines a top hit needs before its CONTENT
	// is called out as "not an implementation". Deliberately conservative: a false LOW CONFIDENCE on a
	// real hit teaches the reader to ignore the marker, which is worse than not emitting it.
	lowConfidenceWrapperLines = 2
)

// searchConfidenceWrapperReason inspects the top hit's own snippet for the three shapes that look like
// an answer and are not one: a doc-comment block, an example-usage list, a deprecated forwarder. Each
// is a hit an agent reads, believes, and then has to back out of. The test is on text the payload
// already carries, so it needs no extra IO.
func searchConfidenceWrapperReason(result SearchResult) string {
	snippet := strings.TrimSpace(result.Snippet)
	if snippet == "" {
		return ""
	}
	commentPrefixes := []string{"//", "*", "/*", "#", "'''", `"""`}
	code, comment, literal := 0, 0, 0
	for _, line := range strings.Split(snippet, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isComment := false
		for _, prefix := range commentPrefixes {
			if strings.HasPrefix(trimmed, prefix) {
				isComment = true
				break
			}
		}
		switch {
		case isComment:
			comment++
		case strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") ||
			strings.HasPrefix(trimmed, "'") || strings.HasPrefix(trimmed, string(rune(34))):
			literal++
		default:
			code++
		}
	}
	if comment > code && comment >= lowConfidenceWrapperLines {
		return "top hit is a documentation comment, not an implementation"
	}
	if literal > code && literal >= lowConfidenceWrapperLines {
		return "top hit is an example-usage list, not an implementation"
	}
	lowered := strings.ToLower(snippet)
	if (strings.Contains(lowered, "@deprecated") || strings.Contains(lowered, "#[deprecated")) && code <= 4 {
		return "top hit is a deprecated forwarder, not the implementation"
	}
	return ""
}

const (
	lowConfidenceUnusedGuard = 0
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
	// The two MECHANICAL tests below run regardless of the score ladder above, because both describe a
	// payload that scores WELL and is still not an answer. A high top score with a tied runner-up is a
	// coin flip presented as a choice; a high top score on a doc comment is a hit the reader has to
	// back out of. Neither is visible to a score threshold.
	if !assessment.Low && len(results) > 1 && results[0].Score > 0 {
		// MAGNITUDE, not the signed difference. Rank 2 may legitimately carry the HIGHER score:
		// promoteFixSiteOverLeadingTest reorders without rescoring, so a payload whose rank-1 test
		// was displaced reports rank 2 above rank 1 on purpose. A signed subtraction reads that as
		// a negative gap, which is below any threshold, so the marker fired on exactly the payloads
		// where the ranking made a deliberate choice — the false positive this file's own
		// calibration says is worse than having no marker at all. What the test is asking is
		// whether the two scores are indistinguishable, and that question has no direction.
		if gap := math.Abs(results[0].Score - results[1].Score); gap < lowConfidenceTieGap {
			assessment.Low = true
			assessment.Reason = fmt.Sprintf(
				"ranks 1 and 2 are tied (%.4f apart) - the ranking did not choose", gap)
		}
	}
	if !assessment.Low {
		if reason := searchConfidenceWrapperReason(results[0]); reason != "" {
			assessment.Low = true
			assessment.Reason = reason
		}
	}
	return assessment
}

// LowConfidenceScoreCeiling exposes the calibrated ceiling for renderers and tests.
func LowConfidenceScoreCeiling() float64 { return lowConfidenceTopScore }
