package rank

import (
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// TestDemoTellsTheIntendedStory pins the three-developer demonstration
// Section 12 asks for: it is what `entire graph rank demo` shows, so its
// properties ARE the hackathon demo's narrative. Every value here comes from
// running the real AnalyzeCommit/AggregateDeveloper pipeline over the fixture
// in demo.go, not a hardcoded expectation.
func TestDemoTellsTheIntendedStory(t *testing.T) {
	t.Parallel()
	profiles := Demo(DefaultWeights())
	if len(profiles) != 3 {
		t.Fatalf("Demo returned %d profiles, want 3", len(profiles))
	}
	byName := map[string]DeveloperProfile{}
	for _, p := range profiles {
		byName[p.Username] = p
	}
	alice, bob, carol := byName["alice"], byName["bob"], byName["carol"]
	if alice.Username == "" || bob.Username == "" || carol.Username == "" {
		t.Fatalf("Demo did not return alice/bob/carol: %+v", byName)
	}

	// bob's commit touches far more symbols than alice's -- confirms the
	// fixture actually represents "huge diff" vs "small commit".
	if bob.ChangedSymbols <= alice.ChangedSymbols {
		t.Fatalf("bob.ChangedSymbols (%d) must exceed alice.ChangedSymbols (%d) for this to test diff size",
			bob.ChangedSymbols, alice.ChangedSymbols)
	}

	// lines/diff size ≠ engineering impact: despite touching far more
	// symbols, bob's near-zero downstream reach must not let him outscore
	// alice's small, well-connected change.
	if bob.EngineeringImpact >= alice.EngineeringImpact {
		t.Fatalf("bob (huge diff, no reach) EngineeringImpact %v >= alice (small, connected) %v",
			bob.EngineeringImpact, alice.EngineeringImpact)
	}
	if bob.FinalScore >= alice.FinalScore {
		t.Fatalf("bob FinalScore %v >= alice FinalScore %v -- diff size must not win the final ranking",
			bob.FinalScore, alice.FinalScore)
	}

	// incomplete evidence ≠ zero impact: carol's relationships are all
	// unresolved (name-only matches), so she must require verification --
	// but her engineering impact must still be a real, non-trivial number,
	// not collapsed toward zero.
	if carol.EvidenceState != sem.EvidenceRequiresVerification || !carol.VerificationRequired {
		t.Fatalf("carol.EvidenceState = %q (VerificationRequired=%v), want %q (true)",
			carol.EvidenceState, carol.VerificationRequired, sem.EvidenceRequiresVerification)
	}
	if carol.EngineeringImpact < 15 {
		t.Fatalf("carol.EngineeringImpact = %v, want a non-trivial score despite unresolved evidence "+
			"(unresolved must not be read as zero impact)", carol.EngineeringImpact)
	}

	// alice and bob, whose evidence is entirely high-precision resolved
	// relations, must not require verification.
	if alice.VerificationRequired {
		t.Fatalf("alice.VerificationRequired = true, want false (all-confirmed evidence): %+v", alice)
	}
	if bob.VerificationRequired {
		t.Fatalf("bob.VerificationRequired = true, want false (all-confirmed evidence): %+v", bob)
	}

	// Every profile must carry an Explain() a reader can actually trace back
	// to commit-level evidence (Section 9's "why this score" requirement).
	for _, p := range []DeveloperProfile{alice, bob, carol} {
		explanation := p.Explain()
		if explanation == "" {
			t.Fatalf("%s.Explain() is empty", p.Username)
		}
		if len(p.Commits) == 0 {
			t.Fatalf("%s has no Commits to trace the score back to", p.Username)
		}
		for _, c := range p.Commits {
			if c.Explain() == "" {
				t.Fatalf("%s's commit %s.Explain() is empty", p.Username, c.Commit)
			}
		}
	}
}

// TestDemoIsDeterministic: the whole point of a fixture-based demo is that it
// reproduces identically every run (Section 12) -- no wall-clock reads, no
// randomness.
func TestDemoIsDeterministic(t *testing.T) {
	t.Parallel()
	first := Demo(DefaultWeights())
	second := Demo(DefaultWeights())
	if len(first) != len(second) {
		t.Fatalf("Demo returned different lengths across runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].FinalScore != second[i].FinalScore || first[i].EngineeringImpact != second[i].EngineeringImpact {
			t.Fatalf("Demo is not deterministic at index %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}
