package sem

import (
	"math"
	"sort"
	"strings"
)

const (
	searchPreselectionRRFConstant       = 60
	searchPreselectionMaxWideningFactor = 4
	searchPreselectionMinConfidence     = 0.15
	searchPreselectionDirectoryTarget   = 3
)

// searchPreselectionEvidence holds independent, file-level signals gathered
// before the selective graph is built. Constraint evidence only controls how
// far the cold candidate list opens; ranking the graph remains independent.
type searchPreselectionEvidence struct {
	Path            string
	OriginalScore   float64
	PathScore       float64
	ContentScore    float64
	ConstraintScore float64
	MatchedTerms    []bool

	rrfScore float64
}

type searchPreselectionAssessment struct {
	Files      []string
	Passes     int
	Examined   int
	Confidence float64
	Coverage   float64
	Diversity  float64
	Widened    bool
	Bounded    bool
}

// fuseSearchPreselectionEvidence applies reciprocal-rank fusion to the
// independent path, content, and constraint channels. The original score is
// retained only as a stable secondary ordering signal, never replaced.
func fuseSearchPreselectionEvidence(evidence []searchPreselectionEvidence) []searchPreselectionEvidence {
	fused := make([]searchPreselectionEvidence, len(evidence))
	for index, item := range evidence {
		item.MatchedTerms = append([]bool(nil), item.MatchedTerms...)
		item.rrfScore = 0
		fused[index] = item
	}
	channels := []func(searchPreselectionEvidence) float64{
		func(item searchPreselectionEvidence) float64 { return item.PathScore },
		func(item searchPreselectionEvidence) float64 { return item.ContentScore },
		func(item searchPreselectionEvidence) float64 { return item.ConstraintScore },
	}
	for _, channel := range channels {
		ranked := make([]int, 0, len(fused))
		for index, item := range fused {
			if channel(item) > 0 {
				ranked = append(ranked, index)
			}
		}
		sort.SliceStable(ranked, func(left, right int) bool {
			leftItem, rightItem := fused[ranked[left]], fused[ranked[right]]
			leftScore, rightScore := channel(leftItem), channel(rightItem)
			if leftScore != rightScore {
				return leftScore > rightScore
			}
			if leftItem.OriginalScore != rightItem.OriginalScore {
				return leftItem.OriginalScore > rightItem.OriginalScore
			}
			return canonicalSearchPathLess(leftItem.Path, rightItem.Path)
		})
		for rank, index := range ranked {
			fused[index].rrfScore += 1 / float64(searchPreselectionRRFConstant+rank+1)
		}
	}
	sort.SliceStable(fused, func(left, right int) bool {
		if fused[left].rrfScore != fused[right].rrfScore {
			return fused[left].rrfScore > fused[right].rrfScore
		}
		if fused[left].OriginalScore != fused[right].OriginalScore {
			return fused[left].OriginalScore > fused[right].OriginalScore
		}
		return canonicalSearchPathLess(fused[left].Path, fused[right].Path)
	})
	return fused
}

// selectProgressiveSearchFiles picks the smallest deterministic prefix that
// has a decisive boundary, covers every observed term, and spans the available
// top-level directories. It widens only a cold index's input, capped at 4x.
func selectProgressiveSearchFiles(
	evidence []searchPreselectionEvidence,
	matchedAnywhere []bool,
	q searchQuery,
	baseLimit int,
) searchPreselectionAssessment {
	if baseLimit <= 0 || len(evidence) == 0 {
		return searchPreselectionAssessment{Coverage: 1, Diversity: 1}
	}
	if !searchPreselectionFused(evidence) {
		evidence = fuseSearchPreselectionEvidence(evidence)
	} else {
		evidence = append([]searchPreselectionEvidence(nil), evidence...)
	}
	limit := minInt(baseLimit, len(evidence))
	ceiling := minInt(searchPreselectionMaxWideningFactor*baseLimit, len(evidence))
	assessment := searchPreselectionAssessment{}
	for {
		selected := evidence[:limit]
		assessment.Passes++
		assessment.Examined += len(selected)
		assessment.Files = make([]string, len(selected))
		for index, item := range selected {
			assessment.Files[index] = item.Path
		}
		assessment.Confidence = searchPreselectionConfidence(evidence, limit)
		assessment.Coverage = searchPreselectionCoverage(selected, matchedAnywhere, q)
		assessment.Diversity = searchPreselectionDiversity(selected, evidence)
		if assessment.Confidence >= searchPreselectionMinConfidence &&
			assessment.Coverage == 1 && assessment.Diversity == 1 {
			assessment.Widened = assessment.Passes > 1
			return assessment
		}
		if limit >= ceiling {
			assessment.Widened = assessment.Passes > 1
			assessment.Bounded = ceiling == searchPreselectionMaxWideningFactor*baseLimit
			return assessment
		}
		nextLimit := minInt(ceiling, limit*2)
		if nextLimit <= limit {
			assessment.Widened = assessment.Passes > 1
			return assessment
		}
		limit = nextLimit
	}
}

func searchPreselectionFused(evidence []searchPreselectionEvidence) bool {
	for _, item := range evidence {
		if item.rrfScore > 0 {
			return true
		}
	}
	return false
}

func searchPreselectionConfidence(evidence []searchPreselectionEvidence, limit int) float64 {
	if limit >= len(evidence) {
		return 1
	}
	current, next := evidence[limit-1].rrfScore, evidence[limit].rrfScore
	if current <= 0 {
		return 0
	}
	return math.Max(0, 1-next/current)
}

func searchPreselectionCoverage(selected []searchPreselectionEvidence, matchedAnywhere []bool, q searchQuery) float64 {
	denominator, numerator := 0.0, 0.0
	covered := make([]bool, len(matchedAnywhere))
	for index, matched := range matchedAnywhere {
		if !matched {
			continue
		}
		weight := 1.0
		if index < len(q.terms) && q.weights[q.terms[index]] > 0 {
			weight = q.weights[q.terms[index]]
		}
		denominator += weight
	}
	if denominator == 0 {
		return 1
	}
	for _, item := range selected {
		for index, matched := range item.MatchedTerms {
			if index < len(covered) && matched && matchedAnywhere[index] {
				covered[index] = true
			}
		}
	}
	for index, matched := range covered {
		if !matched {
			continue
		}
		weight := 1.0
		if index < len(q.terms) && q.weights[q.terms[index]] > 0 {
			weight = q.weights[q.terms[index]]
		}
		numerator += weight
	}
	return numerator / denominator
}

func searchPreselectionDiversity(selected, evidence []searchPreselectionEvidence) float64 {
	available := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		available[searchPreselectionDirectory(item.Path)] = true
	}
	target := minInt(searchPreselectionDirectoryTarget, len(available))
	if target == 0 {
		return 1
	}
	selectedDirectories := make(map[string]bool, target)
	for _, item := range selected {
		selectedDirectories[searchPreselectionDirectory(item.Path)] = true
	}
	return float64(minInt(len(selectedDirectories), target)) / float64(target)
}

func searchPreselectionDirectory(path string) string {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if path == "" || !strings.Contains(path, "/") {
		return "."
	}
	return path[:strings.IndexByte(path, '/')]
}

// searchFileConstraintEvidence remains deliberately shallow: it can widen the
// file pool for an explicit entity or phrase but cannot reject a lexical hit
// or alter the candidate-level agreement score.
func searchFileConstraintEvidence(content string, q searchConstraints) float64 {
	if len(q.Entities) == 0 && len(q.Phrases) == 0 {
		return 0
	}
	words := searchQueryWordSequence(strings.ToLower(content))
	whole := searchConstraintWholeTokens(content, words)
	score := 0.0
	for _, entity := range q.Entities {
		if whole[entity.Normalized] {
			score++
		}
	}
	for _, phrase := range q.Phrases {
		if orderedSearchPhraseMatch(content, phrase) {
			score += 2
		}
	}
	return score
}
