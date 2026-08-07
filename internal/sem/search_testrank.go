package sem

// Rank-1 test files
// =================
//
// The ranked list is read as a list of candidate FIX SITES, and the top entry is read hardest: it
// is the one an agent quotes back as "top-ranked location" and opens first. A test file is never a
// fix site for a bug report — measured over the 30-instance SWE-bench Multilingual sample, 0/30
// gold patches touch a test file — so a test at rank 1 spends the most expensive slot in the
// payload on a file no patch can land in.
//
// searchPathPrior already demotes test paths by a flat -12 (see search.go). That is a SCORE
// adjustment, and it is the right instrument for the common case — suite-wide it holds test hits
// to 6% of the top-10 — but it has two failure modes this pass exists to cover:
//
//   - It is not always enough. A test that exercises the reported behaviour matches the issue's
//     exact code tokens at maximal density on a short document, so BM25 plus the capped 24-point
//     exact-code-token bonus can outrun -12. Measured at db24f74 on the rep30 sample, rank 1 is a
//     test on 3/30 instances; on sharkdp__bat-2260 and fmtlib__fmt-2457 the -12 demonstrably fires
//     (appending an explicit test word to the query, which switches the demotion off, lifts the
//     same hit by +13.6 and +15.3 respectively) and the test wins rank 1 anyway.
//
//   - Raising the subtraction is not a safe way to close that gap, because subtraction can eject
//     the test from the top-K entirely. searchVerifySubjectFor (search_verify.go) reads the ranking
//     to derive the VERIFY command, and when the covering-test section declined to reprint a test
//     the ranking already surfaced, the ranked test IS where the test path comes from. Measured on
//     gohugoio__hugo-12204, forcing the score demotion on moves VERIFY from the correct package
//     (`go test ./tpl/tplimpl`) to a wrong one — a bigger loss than the rank-1 slot it recovers.
//
// So this pass REORDERS rather than rescores, and it never removes anything:
//
//   - It PROMOTES the best fix site over the leading test, rather than pushing the test down.
//     Those are the same thing when one test leads, and they are not the same thing when several
//     do: pushing the first test down leaves the second one at rank 1 and swaps which test comes
//     first, which is exactly the thing VERIFY reads. Promotion cannot do either.
//   - Scores are NOT rewritten. Rank 2 legitimately carries a higher score than rank 1 after a
//     promotion. The score is the caller's only signal for "is this a real hit or is the repo
//     simply missing what I asked for", and falsifying it to make the list look monotonic would
//     destroy that signal to hide a deliberate ordering decision.
//
// Why promotion is VERIFY-neutral by construction. searchVerifySubjectFor scans in slice order and
// takes two things: the first PRIMARY, program-text, non-test hit as the fix site, and the first
// PRIMARY test hit as the ranked test path.
//
//   - The promoted hit is not a test, so the subsequence of test hits keeps its order and the
//     first test is unchanged.
//   - The promoted hit is the first hit that could have been the fix site anyway. Every hit ahead
//     of it is a test, is not program text, or is bound for the docs-and-fixtures section — and
//     assignSearchSections labels that last group away from primary before VERIFY runs, so none of
//     them was eligible. (Its one escape hatch, reverting when EVERY hit is non-code, cannot fire
//     here: if every hit were non-code there would be no promotable hit and this pass would not
//     run.) So the fix site is unchanged too.
//
// Escape hatches, both matching rules that already exist in the ranker:
//
//   - If there is no promotable hit, order is untouched — the tests are the only answer there is.
//     Same rule as assignSearchSections reverting when every hit is non-code.
//   - If the query asked for tests, the pass does not run. It reuses searchTestIntentWords, the
//     same gate that governs the -12, so "the caller asked for tests" has exactly one definition in
//     the ranker. That gate is deliberately NOT tightened here: on this sample only 1/30 queries
//     trips it at all, and every proposed tightening (requiring the intent word to carry code-like
//     weight, or to occur twice) also rejects the ordinary explicit request "where are the tests
//     for the parser", which is the case the gate exists to serve.

// searchTestIntentWords are the words that mean the caller asked for test files, so neither the
// test-path demotion in searchPathPrior nor promoteFixSiteOverLeadingTest should run.
//
// "regression"/"regressions" are deliberately absent. In issue prose "a regression" names
// behaviour that used to work, not a request for the regression suite; a caller who does want tests
// writes "test"/"spec"/"fixture", and the phrase "regression test" already contains "test".
// Including them let the commonest word in a bug report switch off the test demotion for the whole
// query.
var searchTestIntentWords = []string{
	"test", "tests", "testing", "spec", "specs", "fixture", "fixtures",
}

// promoteFixSiteOverLeadingTest lifts the best editable hit above a leading test-artifact hit,
// without changing any score and without dropping or duplicating anything. See the file header for
// why this is a reordering pass rather than a larger score demotion, and why it promotes rather
// than demotes.
func promoteFixSiteOverLeadingTest(selected []searchCandidate, q searchQuery) []searchCandidate {
	if len(selected) < 2 {
		return selected
	}
	if !searchTestArtifactPath(searchLowerPath(selected[0].result.FilePath)) {
		return selected
	}
	if searchQuerySupplied(q, searchTestIntentWords...) {
		return selected
	}
	target := -1
	for index := 1; index < len(selected); index++ {
		if searchPromotableOverLeadingTest(selected[index].result.FilePath, q) {
			target = index
			break
		}
	}
	if target < 0 {
		return selected
	}
	reordered := make([]searchCandidate, 0, len(selected))
	reordered = append(reordered, selected[target])
	reordered = append(reordered, selected[:target]...)
	reordered = append(reordered, selected[target+1:]...)
	return reordered
}

// searchPromotableOverLeadingTest reports whether a hit is a candidate fix site in its own right,
// and so is allowed to displace a leading test.
//
// The bar is exactly the one searchVerifySubjectFor applies when it picks the fix site out of the
// payload: the hit must be program text (a match in prose or serialized data cannot be edited to
// change behaviour), it must not itself be a test, and it must not be bound for the
// docs-and-fixtures section — assignSearchSections labels that last group out of the primary list,
// so promoting one would leave the test first among primary hits and buy nothing. Keeping this
// predicate aligned with that scan is what makes the promotion provably VERIFY-neutral.
func searchPromotableOverLeadingTest(filePath string, q searchQuery) bool {
	if filePath == "" || NonProgramTextPath(filePath) {
		return false
	}
	if searchTestArtifactPath(searchLowerPath(filePath)) {
		return false
	}
	return !searchDocsSectionPath(q, filePath)
}
