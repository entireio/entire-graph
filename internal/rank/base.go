package rank

// BaseScore is the original Hacker House reach signal, preserved exactly:
//
//	score = min(stars * (userPRs / totalPRs), 10000)
//
// It is GitHub popularity/reach, not engineering quality — see FinalScore for
// where it gets combined with (and outweighed by) Entire's engineering-impact
// evidence, and package rank's doc comment for why stars are never used as a
// quality proxy on their own.
//
// Edge cases the raw formula does not handle safely are handled here instead
// of at every call site:
//   - totalPRs <= 0 has no PR population to compare against, so the reach
//     ratio is undefined; this returns 0 rather than dividing by zero.
//   - negative stars/userPRs are clamped to 0 rather than propagated (a
//     negative reach score is never meaningful).
//   - userPRs > totalPRs is an upstream data inconsistency (a PR count that
//     is impossible on its face); the ratio is capped at 1.0 rather than
//     amplifying the base score past what a 100% share would produce.
func BaseScore(stars, userPRs, totalPRs int) float64 {
	if stars < 0 {
		stars = 0
	}
	if userPRs < 0 {
		userPRs = 0
	}
	if totalPRs <= 0 {
		return 0
	}
	ratio := float64(userPRs) / float64(totalPRs)
	if ratio > 1 {
		ratio = 1
	}
	return clamp(float64(stars)*ratio, 0, 10000)
}

// NormalizeBaseScore rescales a 0-10000 BaseScore onto the same 0-100 range
// EngineeringImpact already uses, so FinalScore never adds a 0-100 value to a
// 0-10000 one.
func NormalizeBaseScore(baseScore float64) float64 {
	return clamp(baseScore/10000*100, 0, 100)
}

// FinalScore combines the normalized GitHub reach signal with Entire's
// engineering-impact evidence using named, configurable weights (see
// Weights.BaseReach / Weights.EngineeringImpact) rather than a hardcoded
// 0.4/0.6 split baked into the expression.
func FinalScore(normalizedBaseScore, engineeringImpact float64, w Weights) float64 {
	w = w.Normalize()
	return clamp(normalizedBaseScore*w.BaseReach+engineeringImpact*w.EngineeringImpact, 0, 100)
}
