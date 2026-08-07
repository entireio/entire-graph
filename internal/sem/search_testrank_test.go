package sem

import (
	"reflect"
	"testing"
)

func testRankCandidates(paths ...string) []searchCandidate {
	out := make([]searchCandidate, 0, len(paths))
	for index, path := range paths {
		out = append(out, searchCandidate{
			result: SearchResult{
				FilePath: path, Section: searchSectionPrimary,
				StartLine: 1, EndLine: 2, FocusLine: 1,
			},
			score: float64(100 - index),
		})
	}
	return out
}

func testRankPaths(candidates []searchCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.result.FilePath)
	}
	return out
}

func TestPromoteFixSiteOverLeadingTestLiftsTheBestFixSite(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "test at rank 1 is displaced by the first fix site",
			paths: []string{"tests/integration_test.go", "src/mapping.go", "src/other.go"},
			want:  []string{"src/mapping.go", "tests/integration_test.go", "src/other.go"},
		},
		{
			// The case that makes promotion the right shape: pushing only the first test down
			// would leave test/b_test.go at rank 1 AND swap which test comes first, which is the
			// one thing the VERIFY deriver reads out of the ranking.
			name:  "a run of tests is cleared in one move and keeps its own order",
			paths: []string{"test/a_test.go", "test/b_test.go", "src/mapping.go", "src/other.go"},
			want:  []string{"src/mapping.go", "test/a_test.go", "test/b_test.go", "src/other.go"},
		},
		{
			name:  "prose cannot displace a test, only editable code can",
			paths: []string{"tests/a_test.go", "README.md", "src/mapping.go"},
			want:  []string{"src/mapping.go", "tests/a_test.go", "README.md"},
		},
		{
			// Prose sitting between two tests must not let the promotion hop over the second one
			// and change which test is first.
			name:  "prose interleaved with tests does not reorder the tests",
			paths: []string{"test/a_test.go", "README.md", "test/b_test.go", "src/mapping.go"},
			want:  []string{"src/mapping.go", "test/a_test.go", "README.md", "test/b_test.go"},
		},
		{
			name:  "rank 1 is not a test - nothing moves",
			paths: []string{"src/mapping.go", "tests/a_test.go", "src/other.go"},
			want:  []string{"src/mapping.go", "tests/a_test.go", "src/other.go"},
		},
		{
			name:  "every hit is a test - the tests are the only answer there is",
			paths: []string{"tests/a_test.go", "test/b_test.go", "spec/c_spec.rb"},
			want:  []string{"tests/a_test.go", "test/b_test.go", "spec/c_spec.rb"},
		},
		{
			name:  "no editable hit at all - order untouched",
			paths: []string{"tests/a_test.go", "README.md", "CHANGELOG.md"},
			want:  []string{"tests/a_test.go", "README.md", "CHANGELOG.md"},
		},
		{
			name:  "a single hit is never reordered",
			paths: []string{"tests/a_test.go"},
			want:  []string{"tests/a_test.go"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := testRankPaths(promoteFixSiteOverLeadingTest(
				testRankCandidates(testCase.paths...), buildSearchQuery("mapping resolves the wrong suffix"),
			))
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("order = %v, want %v", got, testCase.want)
			}
		})
	}
}

// The gate has exactly one definition in the ranker, shared with the -12 in searchPathPrior: when
// the caller asked for tests, neither instrument runs.
func TestPromoteFixSiteOverLeadingTestHonoursExplicitTestIntent(t *testing.T) {
	t.Parallel()
	for _, query := range []string{
		"where are the tests for the mapping resolver",
		"the spec for ResolveSyntax",
		"fixtures for the syntax mapping",
	} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			paths := []string{"tests/integration_test.go", "src/mapping.go"}
			got := testRankPaths(promoteFixSiteOverLeadingTest(
				testRankCandidates(paths...), buildSearchQuery(query),
			))
			if !reflect.DeepEqual(got, paths) {
				t.Fatalf("an explicit test request was reordered anyway: %v", got)
			}
		})
	}
}

// The reason this pass reorders instead of subtracting harder. searchVerifySubjectFor takes the
// FIRST non-test hit as the fix site and the FIRST test hit as the ranked test, both by a scan in
// slice order, so a move that preserves the relative order within each group cannot change either
// answer. A larger score demotion can, because it ejects the test from the payload entirely.
func TestPromoteFixSiteOverLeadingTestIsVerifyNeutral(t *testing.T) {
	t.Parallel()
	for _, paths := range [][]string{
		{"tests/integration_test.go", "src/mapping.go", "src/other.go"},
		{"test/a_test.go", "test/b_test.go", "src/mapping.go", "src/other.go"},
		{"tests/a_test.go", "README.md", "src/mapping.go"},
		{"tests/a_test.go", "test/b_test.go", "spec/c_spec.rb"},
	} {
		before := testRankCandidates(paths...)
		beforeResults := make([]SearchResult, 0, len(before))
		for _, candidate := range before {
			beforeResults = append(beforeResults, candidate.result)
		}
		afterResults := make([]SearchResult, 0, len(before))
		for _, candidate := range promoteFixSiteOverLeadingTest(
			testRankCandidates(paths...), buildSearchQuery("mapping resolves the wrong suffix"),
		) {
			afterResults = append(afterResults, candidate.result)
		}
		wantSubject, wantOK := searchVerifySubjectFor(beforeResults)
		gotSubject, gotOK := searchVerifySubjectFor(afterResults)
		if wantOK != gotOK || wantSubject != gotSubject {
			t.Fatalf("reorder changed the VERIFY subject for %v:\n before %+v (%v)\n after  %+v (%v)",
				paths, wantSubject, wantOK, gotSubject, gotOK)
		}
	}
}

// Reordering must never lose, duplicate or rescore a hit: it is a permutation and nothing else.
func TestPromoteFixSiteOverLeadingTestIsAPurePermutation(t *testing.T) {
	t.Parallel()
	paths := []string{"tests/a_test.go", "test/b_test.go", "README.md", "src/mapping.go", "src/other.go"}
	before := testRankCandidates(paths...)
	scores := map[string]float64{}
	for _, candidate := range before {
		scores[candidate.result.FilePath] = candidate.score
	}
	after := promoteFixSiteOverLeadingTest(testRankCandidates(paths...), buildSearchQuery("mapping suffix bug"))
	if len(after) != len(before) {
		t.Fatalf("length changed: %d -> %d", len(before), len(after))
	}
	seen := map[string]int{}
	for _, candidate := range after {
		seen[candidate.result.FilePath]++
		if candidate.score != scores[candidate.result.FilePath] {
			t.Fatalf("score for %s was rewritten: %v -> %v",
				candidate.result.FilePath, scores[candidate.result.FilePath], candidate.score)
		}
	}
	for _, path := range paths {
		if seen[path] != 1 {
			t.Fatalf("%s appears %d times after the reorder", path, seen[path])
		}
	}
}

func TestSearchTestIntentWordsGovernBothInstruments(t *testing.T) {
	t.Parallel()
	// searchPathPrior's -12 and the reorder must agree on what "the caller asked for tests" means,
	// or a query can switch one off and leave the other on.
	for _, word := range searchTestIntentWords {
		if !searchQuerySupplied(buildSearchQuery("resolve "+word+" ordering"), searchTestIntentWords...) {
			t.Fatalf("%q is listed as a test-intent word but does not satisfy the gate", word)
		}
	}
	// "regression" is deliberately not one of them: it is the commonest word in a bug report.
	if searchQuerySupplied(buildSearchQuery("this is a regression in suffix handling"), searchTestIntentWords...) {
		t.Fatal("\"regression\" switched the test-intent gate on")
	}
}
