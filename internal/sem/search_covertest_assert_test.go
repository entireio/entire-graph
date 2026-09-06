package sem

import "testing"

// ---------------------------------------------------------------------------------------------
// name-only candidates must ASSERT something
// ---------------------------------------------------------------------------------------------

// fixtureInputFile is the shape that used to win the block: a path under a test tree, a
// test-shaped symbol name that happens to contain the anchor's name, and no expectation anywhere.
// It is INPUT to a linter, not a test of it.
func fixtureInputFile() relatedFile {
	return relatedFile{path: "resources/test/fixtures/style/PT006.py", lines: []string{
		"# fixture", // 1
		"",          // 2
		"def test_list_expressions(param1, param2):", // 3
		"    ...", // 4
	}}
}

func realAssertingTestFile() relatedFile {
	return relatedFile{path: "src/style_test.go", lines: []string{
		"package style", // 1
		"",              // 2
		"func TestExpressionRewrite(t *testing.T) {", // 3
		"\tgot := expression(\"a\")",                 // 4
		"\trequire.Equal(t, \"b\", got)",             // 5
		"}",                                          // 6
	}}
}

func coverAnchorSourceFile() relatedFile {
	return relatedFile{path: "src/analyze.go", lines: []string{
		"package style",                       // 1
		"",                                    // 2
		"func expression(in string) string {", // 3
		"\treturn in",                         // 4
		"}",                                   // 5
	}}
}

func TestSelectSearchCoveringTestRejectsNameOnlyCandidatesThatAssertNothing(t *testing.T) {
	t.Parallel()
	anchor := coverSymbol("src:expression", "expression", "expression", "function",
		"src/analyze.go", 3, 5)
	fixtureSymbol := coverSymbol("fx:test_list_expressions", "test_list_expressions",
		"test_list_expressions", "function", "resources/test/fixtures/style/PT006.py", 3, 4)
	realSymbol := coverSymbol("t:TestExpressionRewrite", "TestExpressionRewrite",
		"TestExpressionRewrite", "function", "src/style_test.go", 3, 6)
	cases := []struct {
		name    string
		files   []relatedFile
		symbols []SymbolRecord
		want    string
	}{
		{
			name:    "a name-only candidate with no assertion is refused",
			files:   []relatedFile{coverAnchorSourceFile(), fixtureInputFile()},
			symbols: []SymbolRecord{anchor, fixtureSymbol},
			want:    "",
		},
		{
			name:    "a name-only candidate that asserts is admitted",
			files:   []relatedFile{coverAnchorSourceFile(), fixtureInputFile(), realAssertingTestFile()},
			symbols: []SymbolRecord{anchor, fixtureSymbol, realSymbol},
			want:    "TestExpressionRewrite",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := coverFixture(testCase.files, testCase.symbols, nil)
			byID, byFile := fixture.indexes()
			cache := newSearchRelatedFileCache(fixture.reader(), searchCoveringTestFileReads)
			shown, _, found := selectSearchCoveringTest(
				nil, anchor, buildSearchQuery("expression rewrite"), nil, byID, byFile, cache,
			)
			if testCase.want == "" {
				if found {
					t.Fatalf("admitted %q, want no covering test", shown.symbol.Name)
				}
				return
			}
			if !found {
				t.Fatalf("no covering test, want %s", testCase.want)
			}
			if shown.symbol.Name != testCase.want {
				t.Fatalf("chose %q, want %s", shown.symbol.Name, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// only a symbol with behaviour can anchor the block
// ---------------------------------------------------------------------------------------------

// TestCoveringTestAnchorKind pins which kinds have a contract a test can cover, and — through the
// selector — that a field-named-after-its-type can no longer drag the block onto a name match.
func TestCoveringTestAnchorKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind string
		want bool
	}{
		{kind: "function", want: true},
		{kind: "method", want: true},
		{kind: "constructor", want: true},
		{kind: "class", want: true},
		{kind: "struct", want: true},
		{kind: "trait", want: true},
		{kind: "module", want: true},
		{kind: "field", want: false},
		{kind: "property", want: false},
		{kind: "constant", want: false},
		{kind: "variable", want: false},
		{kind: "", want: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.kind, func(t *testing.T) {
			t.Parallel()
			if got := searchCoveringTestAnchorKind(testCase.kind); got != testCase.want {
				t.Errorf("searchCoveringTestAnchorKind(%q) = %v, want %v",
					testCase.kind, got, testCase.want)
			}
		})
	}
	t.Run("a field anchor selects nothing even when a test names it", func(t *testing.T) {
		t.Parallel()
		field := SymbolRecord{
			ID: "src:holder.expression", Kind: "field", Name: "expression",
			QualifiedName: "Holder.expression", FilePath: "src/holder.go",
			StartLine: 4, EndLine: 4, Language: "Go",
		}
		fixture := coverFixture(
			[]relatedFile{coverAnchorSourceFile(), realAssertingTestFile()},
			[]SymbolRecord{field, coverSymbol("t:TestExpressionRewrite", "TestExpressionRewrite",
				"TestExpressionRewrite", "function", "src/style_test.go", 3, 6)},
			nil,
		)
		byID, byFile := fixture.indexes()
		cache := newSearchRelatedFileCache(fixture.reader(), searchCoveringTestFileReads)
		if _, _, found := selectSearchCoveringTest(
			nil, field, buildSearchQuery("expression rewrite"), nil, byID, byFile, cache,
		); found {
			t.Fatal("a field anchored the covering-test block")
		}
	})
}

// ---------------------------------------------------------------------------------------------
// the entry degrades to a locator rather than disappearing
// ---------------------------------------------------------------------------------------------

func TestSearchCoveringTestResultDegradesToALocator(t *testing.T) {
	t.Parallel()
	lines := []string{"package x", "", "func TestThing(t *testing.T) {", "\trequire.Equal(t, 1, 1)", "}"}
	test := searchCoveringTest{
		symbol: coverSymbol("t:TestThing", "TestThing", "pkg.deeply.nested.path.TestThing",
			"function", "pkg/deeply/nested/module/path/thing_test.go", 3, 5),
		line: 4,
	}
	full, ok := searchCoveringTestResult(test, lines, 0)
	if !ok || full.Snippet == "" {
		t.Fatalf("an unbudgeted entry must carry its body: ok=%v snippet=%q", ok, full.Snippet)
	}
	// A budget that cannot hold one line of body still has to name the test.
	locatorOnly := full
	locatorOnly.Snippet = ""
	locatorBytes := serializedSearchResultBytes([]SearchResult{locatorOnly})
	locator, ok := searchCoveringTestResult(test, lines, locatorBytes)
	if !ok {
		t.Fatal("the entry vanished instead of degrading to a locator")
	}
	if locator.Snippet != "" {
		t.Fatalf("the degraded entry carries a body it could not afford: %q", locator.Snippet)
	}
	if locator.FilePath != test.symbol.FilePath || locator.FocusLine != test.line {
		t.Fatalf("the locator lost its location: %#v", locator)
	}
	// A budget below even the locator is still a refusal.
	if _, ok := searchCoveringTestResult(test, lines, 8); ok {
		t.Fatal("an entry was emitted under an impossible budget")
	}
}
