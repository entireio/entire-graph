package sem

import "testing"

// Snapshot / golden files record expected output, so they quote the symptom an issue
// reports verbatim and outrank the implementation the fix edits. Measured on ruff: a
// query naming rule code SIM201 put two snapshot files above the rule source that
// contains the same literal.
func TestClassifySearchFile_Fixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want searchFileClass
	}{
		// by extension, anywhere
		{"crates/ruff_linter/src/rules/flake8_simplify/snapshots/x__SIM201_SIM201.py.snap", searchFileClassFixture},
		{"tests/__snapshots__/render.ambr", searchFileClassFixture},
		{"pkg/report/report.golden", searchFileClassFixture},
		// by directory segment
		{"crates/ruff_linter/resources/test/fixtures/flake8_simplify/SIM201.py", searchFileClassFixture},
		{"internal/parser/testdata/basic.json", searchFileClassFixture},
		{"src/__fixtures__/user.js", searchFileClassFixture},
		// Prose extensions inside a fixture tree hit the doc class first. Deliberate: doc's
		// prior (0.5) demotes harder than fixture's (0.75), so the ranking outcome is at
		// least as good and only the label differs.
		{"internal/parser/testdata/basic.txt", searchFileClassDoc},
		{"test/baselines/reference/foo.js", searchFileClassFixture},
		// real source must stay source
		{"crates/ruff_linter/src/rules/flake8_simplify/rules/ast_unary_op.rs", searchFileClassSource},
		{"src/objects/Mesh.js", searchFileClassSource},
		// a fixture-named identifier in a filename is NOT a fixture tree
		{"src/fixtures-loader.go", searchFileClassSource},
		// existing classes keep precedence
		{"docs/snapshots/guide.md", searchFileClassDoc},
		{"vendor/foo/testdata/x.snap", searchFileClassVendored},
	}
	for _, c := range cases {
		if got := classifySearchFile(c.path); got != c.want {
			t.Errorf("classifySearchFile(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// The fixture prior must demote below source, stay above nothing, and switch OFF when the
// caller is actually asking about snapshots.
func TestSearchFileClassPrior_Fixtures(t *testing.T) {
	t.Parallel()

	snap := "crates/ruff_linter/src/rules/flake8_simplify/snapshots/x__SIM201.py.snap"
	src := "crates/ruff_linter/src/rules/flake8_simplify/rules/ast_unary_op.rs"

	q := buildSearchQuery("SIM201 and SIM202 fixes should be marked unsafe")
	snapPrior := searchFileClassPrior(q, snap)
	srcPrior := searchFileClassPrior(q, src)
	if !(snapPrior < srcPrior) {
		t.Errorf("snapshot prior %v must be below source prior %v", snapPrior, srcPrior)
	}
	if snapPrior <= 0 {
		t.Errorf("snapshot prior %v must stay positive so fixtures remain rankable", snapPrior)
	}

	// Asking about snapshots restores full strength.
	qSnap := buildSearchQuery("snapshot test output is stale")
	if got := searchFileClassPrior(qSnap, snap); got != 1 {
		t.Errorf("explicit snapshot intent: prior = %v, want 1", got)
	}
}
