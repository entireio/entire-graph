package sem

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Round three. Both tests pin the same property the first two rounds pinned for
// the tree-sitter walk, at the two per-file code paths the walk's stop predicate
// never reached: the ProfileFast C/C++ parser, which the walk is not used for at
// all, and the language-specific supplemental extractors, which run AFTER the
// walk returns. Neither observed the deadline, so the "one in-flight file per
// worker" residual the PR discloses was not the bound it claimed to be.

// tf142r3CFamilyBomb is C that never closes a statement: every line matches
// cFamilyTypeLineRe at brace depth 0, and fastCFamilyStatementEnd then scans
// forward to end-of-file looking for the `;` or `}` that never arrives. That is
// one O(lines) scan per line, so the parse is quadratic in file length -- and it
// is a plain Go loop, not a cgo call, so nothing about it is inherently
// un-interruptible.
func tf142r3CFamilyBomb(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "typedef struct s%d\n", i)
	}
	return sb.String()
}

// tf142r3JSExportBomb is JavaScript whose tree-sitter walk is cheap but whose
// supplemental extractor javascriptExportedVariableEntities is superlinear: it
// calls strings.Count(content[:match], "\n") once per match, re-scanning the
// whole prefix every time.
func tf142r3JSExportBomb(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "export const jsConst%d = %d;\nMyThing.prototype.m%d = function (a) { return a + %d; };\nfunction fn%d(a) { return helper%d(a); }\n", i, i, i, i, i, i)
	}
	return sb.String()
}

// TestTF142R3FastCFamilyParserObservesBudget reproduces the finding at
// provider.go:1339. parseWithProfile takes a ctx and hands it to
// ParseWithStatusCtx, but under ProfileFast a C/C++ file never goes through the
// tree-sitter parser at all -- fastCFamilyEntities gets the path, the content
// and the language, and no way to be stopped.
func TestTF142R3FastCFamilyParserObservesBudget(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "bomb.c", tf142r3CFamilyBomb(30000))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "cbomb")

	const budget = time.Second
	start := time.Now()
	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test",
		ProviderSnapshotOptions{Profile: ProfileFast, MaxDuration: budget})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	const ceiling = 6 * time.Second
	t.Logf("budget=%s elapsed=%s overshoot=%s", budget, elapsed.Round(time.Millisecond), (elapsed - budget).Round(time.Millisecond))
	if elapsed > ceiling {
		t.Fatalf("budget %s was overshot to %s: the ProfileFast C/C++ parser does not observe the deadline", budget, elapsed)
	}
}

// TestTF142R3SupplementalExtractorsObserveBudget reproduces the finding at
// parser.go:334. Cancellation stops walkEntities, but every language-specific
// supplemental pass, the binding collapse and the final sort run afterwards
// regardless -- and processProviderFile then throws the whole file away because
// ctx expired, so all of it is work charged to the overshoot for a result that
// is discarded.
func TestTF142R3SupplementalExtractorsObserveBudget(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "exports.js", tf142r3JSExportBomb(15000))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "jsbomb")

	const budget = 500 * time.Millisecond
	start := time.Now()
	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test",
		ProviderSnapshotOptions{MaxDuration: budget})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	const ceiling = 6 * time.Second
	t.Logf("budget=%s elapsed=%s overshoot=%s", budget, elapsed.Round(time.Millisecond), (elapsed - budget).Round(time.Millisecond))
	if elapsed > ceiling {
		t.Fatalf("budget %s was overshot to %s: the post-walk supplemental extractors do not observe the deadline", budget, elapsed)
	}
}
