package gate

import (
	"fmt"
	"sort"
	"strings"
)

// coverageFindingLimit bounds the coverage section. Unchecked entities beyond
// it are still counted in the header and still rank in the review order.
const coverageFindingLimit = 10

// Coverage reports the entities nobody checked.
//
// The collect layer resolves each entity's CoverageState; this decides what is
// worth reporting. Unchecked entities with dependents lead, because "nothing
// tests this and other code depends on it" is a different claim from "nothing
// tests this dead corner".
//
// NoResolver is never reported as a finding. Not being able to look for tests
// is a limit of Gate, not a defect in the change, and charging the author for
// it would be the quiet dishonesty this tool exists to avoid. It surfaces in
// the header count and in Availability instead.
func Coverage(entities []ChangedEntity) []Finding {
	var withDependents, without []Finding
	for _, e := range entities {
		if e.Coverage != Unchecked {
			continue
		}
		finding := Finding{
			Dimension: DimCoverage,
			Subject:   e.Anchor,
			Summary: fmt.Sprintf("%s %s is unchecked: no test covers it (%d dependents)",
				e.Kind, e.ChangeType, e.Dependents),
		}
		if e.Dependents > 0 {
			withDependents = append(withDependents, finding)
		} else {
			without = append(without, finding)
		}
	}

	findings := append(withDependents, without...)
	if len(findings) > coverageFindingLimit {
		findings = findings[:coverageFindingLimit]
	}
	return findings
}

// testPathMarkers are the path shapes that mean "this file holds tests". The
// list is convention, not proof, and it is the reason CoverageState has a
// NoResolver arm: a language whose tests match none of these is reported as
// unresolvable rather than as untested.
var testPathMarkers = []string{"_test.", "test_", ".test.", ".spec.", "/tests/", "/test/"}

// IsTestPath reports whether a repository-relative path looks like a test file.
func IsTestPath(path string) bool {
	for _, marker := range testPathMarkers {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return strings.HasPrefix(path, "test/") || strings.HasPrefix(path, "tests/")
}

// ResolveCoverage decides, for each entity, whether a test exercises it.
//
// Coverage is read off the graph rather than by running a search per entity:
// an incoming dependency edge from a symbol that lives in a test file is direct
// evidence that a test reaches this code, and the edges are already loaded. A
// per-entity search would be more thorough and would cost seconds each, which
// on an agent-sized change set is minutes.
//
// hasTests says whether the repository contains any test files at all. When it
// does not, every entity is NoResolver rather than Unchecked: "this repo has no
// test tree we can read" is not the same claim as "this change is untested",
// and Gate must not convert one into the other.
func ResolveCoverage(entities []ChangedEntity, ix *Index, hasTests bool) {
	for i := range entities {
		entity := &entities[i]
		if !hasTests {
			entity.Coverage = NoResolver
			continue
		}
		ids := ix.Resolve(entity.Name, entity.Path)
		if len(ids) == 0 {
			// The diff named an entity the graph does not know — an
			// unsupported language, or a file that failed to parse. Absence of
			// a symbol is absence of evidence, not evidence of absence.
			entity.Coverage = NoResolver
			continue
		}
		entity.Coverage = Unchecked
		for _, test := range ix.Dependents(ids, 1) {
			if IsTestPath(test.Path) {
				entity.Coverage = Verified
				entity.CoveringTests = append(entity.CoveringTests, test.Name)
			}
		}
		sort.Strings(entity.CoveringTests)
	}
}
