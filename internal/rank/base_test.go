package rank

import "testing"

// TestBaseScoreMatchesOriginalFormula pins that BaseScore still computes
// exactly `min(stars * (userPRs / totalPRs), 10000)` for an ordinary,
// uncapped input — the formula Section 3 says must be preserved untouched.
func TestBaseScoreMatchesOriginalFormula(t *testing.T) {
	t.Parallel()
	got := BaseScore(1000, 5, 10)
	if want := 500.0; got != want {
		t.Fatalf("BaseScore(1000, 5, 10) = %v, want %v", got, want)
	}
}

func TestBaseScoreCapsAt10000(t *testing.T) {
	t.Parallel()
	got := BaseScore(100000, 10, 10)
	if got != 10000 {
		t.Fatalf("BaseScore did not cap at 10000, got %v", got)
	}
}

// TestBaseScoreZeroTotalPRsReturnsZero covers the totalPRs==0 edge case: no
// PR population to compare against, so the reach ratio is undefined. The
// original raw formula divides by zero here; BaseScore must not.
func TestBaseScoreZeroTotalPRsReturnsZero(t *testing.T) {
	t.Parallel()
	if got := BaseScore(500, 3, 0); got != 0 {
		t.Fatalf("BaseScore(500, 3, 0) = %v, want 0", got)
	}
}

func TestBaseScoreZeroStarsIsZero(t *testing.T) {
	t.Parallel()
	if got := BaseScore(0, 5, 10); got != 0 {
		t.Fatalf("BaseScore(0, 5, 10) = %v, want 0", got)
	}
}

// TestBaseScoreUserPRsExceedingTotalIsClamped covers userPRs > totalPRs: an
// upstream data inconsistency that must not amplify the score past what a
// 100% PR share would produce.
func TestBaseScoreUserPRsExceedingTotalIsClamped(t *testing.T) {
	t.Parallel()
	clamped := BaseScore(1000, 20, 10)
	full := BaseScore(1000, 10, 10)
	if clamped != full {
		t.Fatalf("BaseScore(userPRs>totalPRs) = %v, want the same as a 100%% share (%v)", clamped, full)
	}
}

// TestBaseScoreNegativeInputsClampToZero covers negative/invalid values: they
// must not propagate into a negative or otherwise invalid score.
func TestBaseScoreNegativeInputsClampToZero(t *testing.T) {
	t.Parallel()
	if got := BaseScore(-50, -3, 10); got != 0 {
		t.Fatalf("BaseScore(-50, -3, 10) = %v, want 0", got)
	}
	if got := BaseScore(50, -3, 10); got != 0 {
		t.Fatalf("BaseScore(50, -3, 10) = %v, want 0 (negative userPRs clamps to 0, giving ratio 0)", got)
	}
}

func TestNormalizeBaseScoreRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base float64
		want float64
	}{
		{0, 0},
		{5000, 50},
		{10000, 100},
		{20000, 100}, // above the base cap still clamps to 100, never overshoots
	} {
		if got := NormalizeBaseScore(tc.base); got != tc.want {
			t.Errorf("NormalizeBaseScore(%v) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

// TestFinalScoreNeverAddsMismatchedScales is the guard against Section 8's
// explicit warning: a 0-100 engineering-impact value must never be added
// directly to a 0-10000 base score. FinalScore takes an ALREADY-normalized
// base score, so feeding it 100 (not 10000) must land at the BaseReach
// weight, not swamp the result.
func TestFinalScoreNeverAddsMismatchedScales(t *testing.T) {
	t.Parallel()
	w := DefaultWeights()
	if got := FinalScore(100, 0, w); got != w.BaseReach*100 {
		t.Fatalf("FinalScore(100, 0) = %v, want %v", got, w.BaseReach*100)
	}
	if got := FinalScore(0, 100, w); got != w.EngineeringImpact*100 {
		t.Fatalf("FinalScore(0, 100) = %v, want %v", got, w.EngineeringImpact*100)
	}
}

// TestFinalScoreEngineeringImpactDominates pins Section 8's requirement that
// Entire-derived engineering evidence is the DOMINANT signal in the default
// weighting: a developer with a low base score but a high engineering impact
// must outrank one with the opposite profile.
func TestFinalScoreEngineeringImpactDominates(t *testing.T) {
	t.Parallel()
	w := DefaultWeights()
	highReachLowImpact := FinalScore(NormalizeBaseScore(9000), 10, w)
	lowReachHighImpact := FinalScore(NormalizeBaseScore(500), 90, w)
	if lowReachHighImpact <= highReachLowImpact {
		t.Fatalf("engineering impact does not dominate: high-reach/low-impact=%v, low-reach/high-impact=%v",
			highReachLowImpact, lowReachHighImpact)
	}
}
