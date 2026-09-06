package gate

import "testing"

func TestRiskReportsOnlyBreakingChanges(t *testing.T) {
	entities := []ChangedEntity{
		entity("VerifyToken", "pkg/auth.go", 88, SignatureChanged),
		entity("Require", "pkg/mw.go", 41, BodyChanged),
	}

	findings := Risk(entities, fixtureIndex(), 2)

	// Both entities have dependents, but a body change keeps its signature, so
	// its callers still compile and still mean what they meant. Reporting it
	// would bury the change that can actually break them.
	if len(findings) != 1 {
		t.Fatalf("Risk returned %d findings, want 1 (breaking changes only)", len(findings))
	}
	if findings[0].Subject.Name != "VerifyToken" {
		t.Fatalf("Risk reported %q, want VerifyToken", findings[0].Subject.Name)
	}
}

func TestRiskAnnotatesDependentsOnEveryEntity(t *testing.T) {
	entities := []ChangedEntity{
		entity("Require", "pkg/mw.go", 41, BodyChanged),
		entity("Load", "pkg/config.go", 31, BodyChanged),
	}

	Risk(entities, fixtureIndex(), 2)

	// A body change opens no finding, but the review order still ranks by
	// dependents, so the count has to be populated regardless.
	if entities[0].Dependents != 1 {
		t.Fatalf("Require has %d dependents, want 1 (Protected)", entities[0].Dependents)
	}
	if entities[1].Dependents != 0 {
		t.Fatalf("Load has %d dependents, want 0", entities[1].Dependents)
	}
}

func TestRiskClampsHopsToMax(t *testing.T) {
	// An unbounded walk pulls in most of a repository for any symbol a utility
	// function touches, so the cap is a correctness property of the output, not
	// a performance tweak.
	entities := []ChangedEntity{entity("VerifyToken", "pkg/auth.go", 88, SignatureChanged)}
	deep := []ChangedEntity{entity("VerifyToken", "pkg/auth.go", 88, SignatureChanged)}

	Risk(entities, fixtureIndex(), MaxHops)
	Risk(deep, fixtureIndex(), MaxHops+5)

	if entities[0].Dependents != deep[0].Dependents {
		t.Fatalf("hops beyond MaxHops widened the walk: %d vs %d",
			entities[0].Dependents, deep[0].Dependents)
	}
}

func TestResolveCoverageDistinguishesVerifiedFromUnchecked(t *testing.T) {
	entities := []ChangedEntity{
		entity("VerifyToken", "pkg/auth.go", 88, BodyChanged),
		entity("Load", "pkg/config.go", 31, BodyChanged),
	}

	ResolveCoverage(entities, fixtureIndex(), true)

	if entities[0].Coverage != Verified {
		t.Fatalf("VerifyToken coverage = %s, want %s (TestVerifyToken calls it)",
			entities[0].Coverage, Verified)
	}
	if len(entities[0].CoveringTests) != 1 || entities[0].CoveringTests[0] != "TestVerifyToken" {
		t.Fatalf("covering tests = %v, want [TestVerifyToken]", entities[0].CoveringTests)
	}
	if entities[1].Coverage != Unchecked {
		t.Fatalf("Load coverage = %s, want %s", entities[1].Coverage, Unchecked)
	}
}

func TestResolveCoverageReportsNoResolverRatherThanUntested(t *testing.T) {
	// These two cases are the reason CoverageState has three arms. Calling
	// either of them "unchecked" would be Gate asserting something it did not
	// establish, which is the exact failure the tool exists to avoid.
	cases := []struct {
		name     string
		entities []ChangedEntity
		hasTests bool
	}{
		{
			name:     "repository has no test tree to read",
			entities: []ChangedEntity{entity("VerifyToken", "pkg/auth.go", 88, BodyChanged)},
			hasTests: false,
		},
		{
			name:     "entity is absent from the graph",
			entities: []ChangedEntity{entity("ParsedByNobody", "pkg/thing.rb", 4, BodyChanged)},
			hasTests: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResolveCoverage(tc.entities, fixtureIndex(), tc.hasTests)
			if tc.entities[0].Coverage != NoResolver {
				t.Fatalf("coverage = %s, want %s", tc.entities[0].Coverage, NoResolver)
			}
		})
	}
}

func TestCoverageNeverReportsNoResolverAsAFinding(t *testing.T) {
	entities := []ChangedEntity{
		{Anchor: Anchor{Name: "Unreadable", Path: "x.rb"}, Coverage: NoResolver},
		{Anchor: Anchor{Name: "Untested", Path: "y.go"}, Coverage: Unchecked},
	}

	findings := Coverage(entities)

	// Not being able to look for tests is a limit of Gate, not a defect in the
	// change. Charging the author for it would be dishonest.
	if len(findings) != 1 {
		t.Fatalf("Coverage returned %d findings, want 1 (unchecked only)", len(findings))
	}
	if findings[0].Subject.Name != "Untested" {
		t.Fatalf("Coverage reported %q, want Untested", findings[0].Subject.Name)
	}
}

func TestCoverageRanksUncheckedWithDependentsFirst(t *testing.T) {
	entities := []ChangedEntity{
		{Anchor: Anchor{Name: "DeadCorner"}, Coverage: Unchecked, Dependents: 0},
		{Anchor: Anchor{Name: "LoadBearing"}, Coverage: Unchecked, Dependents: 9},
	}

	findings := Coverage(entities)

	// "Nothing tests this and other code depends on it" is a different claim
	// from "nothing tests this dead corner", and it should be read first.
	if findings[0].Subject.Name != "LoadBearing" {
		t.Fatalf("first finding is %q, want LoadBearing", findings[0].Subject.Name)
	}
}
