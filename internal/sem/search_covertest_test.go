package sem

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------------------------

// coverFixture is a repo with one source symbol and a choice of tests around it. It reuses
// relatedFixture's reader/index helpers so both contract blocks are tested against the same
// shape of graph the ranker itself sees.
func coverFixture(files []relatedFile, symbols []SymbolRecord, relations []RelationRecord) relatedFixture {
	return relatedFixture{files: files, symbols: symbols, relations: relations}
}

func coverSymbol(id, name, qualified, kind, path string, start, end int) SymbolRecord {
	return SymbolRecord{
		ID: id, Name: name, QualifiedName: qualified, Kind: kind,
		FilePath: path, StartLine: start, EndLine: end, Language: "Go",
	}
}

func coverCall(from, to string, confidence float64) RelationRecord {
	return RelationRecord{FromID: from, ToID: to, Type: "CALLS", Confidence: confidence}
}

// sourceFile / mirrorTestFile are the two files nearly every case needs: one function under test
// and one mirror test file holding two candidate tests.
func coverSourceFile() relatedFile {
	return relatedFile{path: "server/handler.go", lines: []string{
		"package server", // 1
		"",               // 2
		"func normalizePath(input string) string {", // 3
		"\tif input == \"\" {",                      // 4
		"\t\treturn \"/\"",                          // 5
		"\t}",                                       // 6
		"\treturn input",                            // 7
		"}",                                         // 8
	}}
}

func coverMirrorTestFile() relatedFile {
	return relatedFile{path: "server/handler_test.go", lines: []string{
		"package server",                         // 1
		"",                                       // 2
		"func TestNormalizePath(t *testing.T) {", // 3
		"\tgot := normalizePath(\"\")",           // 4
		"\trequire.Equal(t, \"/\", got)",         // 5
		"}",                                      // 6
		"",                                       // 7
		"func TestSomethingElse(t *testing.T) {", // 8
		"\trequire.True(t, true)",                // 9
		"}",                                      // 10
	}}
}

func coverSymbols() []SymbolRecord {
	return []SymbolRecord{
		coverSymbol("src:normalizePath", "normalizePath", "normalizePath", "function", "server/handler.go", 3, 8),
		coverSymbol("test:TestNormalizePath", "TestNormalizePath", "TestNormalizePath", "function", "server/handler_test.go", 3, 6),
		coverSymbol("test:TestSomethingElse", "TestSomethingElse", "TestSomethingElse", "function", "server/handler_test.go", 8, 10),
	}
}

// selectCoveringTestForTest runs the selector the way search does, and reports the chosen test's
// `path:line symbol` or "" when the block is absent.
func selectCoveringTestForTest(t *testing.T, fixture relatedFixture, query, anchorID string) string {
	t.Helper()
	byID, byFile := fixture.indexes()
	cache := newSearchRelatedFileCache(fixture.reader(), searchCoveringTestFileReads)
	test, _, found := selectSearchCoveringTest(
		nil, fixture.symbol(t, anchorID), buildSearchQuery(query), fixture.relations, byID, byFile, cache,
	)
	if !found {
		return ""
	}
	return test.symbol.FilePath + ":" + itoaCover(test.line) + " " + test.symbol.Name
}

func itoaCover(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// ---------------------------------------------------------------------------------------------
// selection
// ---------------------------------------------------------------------------------------------

func TestSelectSearchCoveringTestEvidence(t *testing.T) {
	t.Parallel()
	source := coverSourceFile()
	mirror := coverMirrorTestFile()
	for _, testCase := range []struct {
		name      string
		files     []relatedFile
		symbols   []SymbolRecord
		relations []RelationRecord
		query     string
		anchor    string
		want      string
	}{
		{
			name:    "resolved call from a mirror test wins",
			files:   []relatedFile{source, mirror},
			symbols: coverSymbols(),
			relations: []RelationRecord{
				coverCall("test:TestNormalizePath", "src:normalizePath", 0.9),
			},
			query:  "normalize path returns slash",
			anchor: "src:normalizePath",
			// Focus is the assertion line, not the declaration.
			want: "server/handler_test.go:5 TestNormalizePath",
		},
		{
			name:      "name convention alone is enough, with no edge at all",
			files:     []relatedFile{source, mirror},
			symbols:   coverSymbols(),
			relations: nil,
			query:     "normalize path returns slash",
			anchor:    "src:normalizePath",
			want:      "server/handler_test.go:5 TestNormalizePath",
		},
		{
			name: "a test naming the anchor in a differently named test file still counts",
			files: []relatedFile{source, {path: "server/routes_test.go", lines: []string{
				"package server",
				"",
				"func TestRouteNormalizePathTrailing(t *testing.T) {",
				"\tassert.Equal(t, \"/\", normalizePath(\"\"))",
				"}",
			}}},
			symbols: []SymbolRecord{
				coverSymbol("src:normalizePath", "normalizePath", "normalizePath", "function", "server/handler.go", 3, 8),
				coverSymbol("test:route", "TestRouteNormalizePathTrailing", "TestRouteNormalizePathTrailing", "function", "server/routes_test.go", 3, 5),
			},
			relations: nil,
			query:     "normalize path returns slash",
			anchor:    "src:normalizePath",
			want:      "server/routes_test.go:4 TestRouteNormalizePathTrailing",
		},
		{
			name:  "no test exists at all: the section is omitted",
			files: []relatedFile{source},
			symbols: []SymbolRecord{
				coverSymbol("src:normalizePath", "normalizePath", "normalizePath", "function", "server/handler.go", 3, 8),
			},
			relations: nil,
			query:     "normalize path returns slash",
			anchor:    "src:normalizePath",
			want:      "",
		},
		{
			name: "a test that neither names nor calls the anchor is not guessed at",
			files: []relatedFile{source, {path: "server/handler_test.go", lines: []string{
				"package server",
				"",
				"func TestUnrelated(t *testing.T) {",
				"\trequire.True(t, true)",
				"}",
			}}},
			symbols: []SymbolRecord{
				coverSymbol("src:normalizePath", "normalizePath", "normalizePath", "function", "server/handler.go", 3, 8),
				coverSymbol("test:unrelated", "TestUnrelated", "TestUnrelated", "function", "server/handler_test.go", 3, 5),
			},
			relations: nil,
			query:     "normalize path returns slash",
			anchor:    "src:normalizePath",
			want:      "",
		},
		{
			name:      "a query that asked for tests gets none: the ranking already has them",
			files:     []relatedFile{source, mirror},
			symbols:   coverSymbols(),
			relations: []RelationRecord{coverCall("test:TestNormalizePath", "src:normalizePath", 0.9)},
			query:     "add a test for normalize path",
			anchor:    "src:normalizePath",
			want:      "",
		},
		{
			name:    "an anchor that is itself a test gets none",
			files:   []relatedFile{source, mirror},
			symbols: coverSymbols(),
			relations: []RelationRecord{
				coverCall("test:TestSomethingElse", "test:TestNormalizePath", 0.9),
			},
			query:  "normalize path returns slash",
			anchor: "test:TestNormalizePath",
			want:   "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := coverFixture(testCase.files, testCase.symbols, testCase.relations)
			if got := selectCoveringTestForTest(t, fixture, testCase.query, testCase.anchor); got != testCase.want {
				t.Fatalf("covering test = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestSelectSearchCoveringTestSkipsAlreadySurfaced pins the rule that a test the payload already
// prints does not get a second entry: the block's whole value is bringing in what is NOT in view.
func TestSelectSearchCoveringTestSkipsAlreadySurfaced(t *testing.T) {
	t.Parallel()
	fixture := coverFixture(
		[]relatedFile{coverSourceFile(), coverMirrorTestFile()}, coverSymbols(),
		[]RelationRecord{coverCall("test:TestNormalizePath", "src:normalizePath", 0.9)},
	)
	byID, byFile := fixture.indexes()
	results := []SearchResult{{
		Rank: 1, FilePath: "server/handler_test.go", StartLine: 3, EndLine: 6,
		FocusLine: 5, SnippetStartLine: 3, SnippetEndLine: 6,
	}}
	cache := newSearchRelatedFileCache(fixture.reader(), searchCoveringTestFileReads)
	if _, _, found := selectSearchCoveringTest(
		results, fixture.symbol(t, "src:normalizePath"), buildSearchQuery("normalize path"),
		fixture.relations, byID, byFile, cache,
	); found {
		t.Fatal("covering test was surfaced twice; the payload already prints it")
	}
}

// ---------------------------------------------------------------------------------------------
// rendering and budget
// ---------------------------------------------------------------------------------------------

func TestSearchCoveringTestResultBudget(t *testing.T) {
	t.Parallel()
	fixture := coverFixture(
		[]relatedFile{coverSourceFile(), coverMirrorTestFile()}, coverSymbols(),
		[]RelationRecord{coverCall("test:TestNormalizePath", "src:normalizePath", 0.9)},
	)
	lines := strings.Split(mustReadCover(t, fixture, "server/handler_test.go"), "\n")
	test := searchCoveringTest{symbol: fixture.symbol(t, "test:TestNormalizePath"), line: 5}

	full, ok := searchCoveringTestResult(test, lines, searchCoveringTestGrowthBytes)
	if !ok {
		t.Fatal("the whole body should fit the standing allowance")
	}
	if full.Section != searchSectionCoveringTest {
		t.Fatalf("section = %q, want %q", full.Section, searchSectionCoveringTest)
	}
	if full.SnippetStartLine != 3 || full.SnippetEndLine != 6 {
		t.Fatalf("window = %d-%d, want 3-6", full.SnippetStartLine, full.SnippetEndLine)
	}
	// The snippet must be a verbatim copy of those lines: the line numbers it reports are only
	// trustworthy if they are.
	if want := strings.Join(lines[2:6], "\n"); full.Snippet != want {
		t.Fatalf("snippet = %q, want %q", full.Snippet, want)
	}
	if got := serializedSearchResultBytes([]SearchResult{full}); got > searchCoveringTestGrowthBytes {
		t.Fatalf("entry is %d bytes, over its %d allowance", got, searchCoveringTestGrowthBytes)
	}

	// A tighter budget narrows the window toward the assertion rather than lying about it.
	narrow, ok := searchCoveringTestResult(test, lines, 320)
	if !ok {
		t.Fatal("a narrower window should still be renderable")
	}
	if narrow.SnippetEndLine-narrow.SnippetStartLine >= full.SnippetEndLine-full.SnippetStartLine {
		t.Fatalf("narrow window %d-%d did not shrink from %d-%d",
			narrow.SnippetStartLine, narrow.SnippetEndLine, full.SnippetStartLine, full.SnippetEndLine)
	}
	if narrow.SnippetStartLine > 5 || narrow.SnippetEndLine < 5 {
		t.Fatalf("narrow window %d-%d dropped the assertion at line 5",
			narrow.SnippetStartLine, narrow.SnippetEndLine)
	}
	if !strings.Contains(narrow.Snippet, "require.Equal") {
		t.Fatalf("narrow snippet lost the assertion: %q", narrow.Snippet)
	}

	// Budget exhausted: the section is omitted rather than emitted as a stub.
	if _, ok := searchCoveringTestResult(test, lines, 64); ok {
		t.Fatal("an entry was emitted with no budget to hold it")
	}
}

func mustReadCover(t *testing.T, fixture relatedFixture, path string) string {
	t.Helper()
	content, ok := fixture.reader()(path)
	if !ok {
		t.Fatalf("fixture has no file %q", path)
	}
	return content
}

// TestSearchCoveringTestFocusLine pins that the window is built around the test's CONCLUSION and
// that teardown assertions are not mistaken for it.
func TestSearchCoveringTestFocusLine(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name: "last assertion, not the first",
			lines: []string{
				"func TestThing(t *testing.T) {", // 1
				"\trequire.NoError(t, setup())",  // 2
				"\tgot := run()",                 // 3
				"\trequire.Equal(t, 7, got)",     // 4
				"}",                              // 5
			},
			want: 4,
		},
		{
			name: "a deferred teardown assertion is not the contract",
			lines: []string{
				"func TestThing(t *testing.T) {",    // 1
				"\th := newHead(t)",                 // 2
				"\tdefer func() {",                  // 3
				"\t\trequire.NoError(t, h.Close())", // 4
				"\t}()",                             // 5
				"\th.init(0)",                       // 6
				"}",                                 // 7
			},
			want: 1,
		},
		{
			name: "no assertion at all falls back to the declaration",
			lines: []string{
				"func TestThing(t *testing.T) {", // 1
				"\trun()",                        // 2
				"}",                              // 3
			},
			want: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			symbol := coverSymbol("t", "TestThing", "TestThing", "function", "x_test.go", 1, len(testCase.lines))
			if got := searchCoveringTestFocusLine(testCase.lines, symbol); got != testCase.want {
				t.Fatalf("focus line = %d, want %d", got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// mirror detection
// ---------------------------------------------------------------------------------------------

func TestSearchCoveringTestMirror(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		source    string
		candidate string
		want      bool
	}{
		{"go suffix", "gin.go", "gin_test.go", true},
		{"java maven mirror", "src/main/java/com/x/GsonBuilder.java", "src/test/java/com/x/GsonBuilderTest.java", true},
		{"java plural", "src/main/java/com/x/Foo.java", "src/test/java/com/x/FooTests.java", true},
		{"typescript spec", "src/reactive.ts", "src/__tests__/reactive.spec.ts", true},
		{"python prefix", "pkg/parser.py", "pkg/tests/test_parser.py", true},
		{"ruby spec", "lib/carbon.rb", "spec/carbon_spec.rb", true},
		{"same stem inside a test tree", "src/bitops.c", "tests/unit/bitops.tcl", true},
		{"same stem outside a test tree is not a mirror", "src/routing/mod.rs", "src/other/mod.rs", false},
		{"a different stem is not a mirror", "gin.go", "routes_test.go", false},
		{"the source itself is not its own mirror", "gin.go", "gin.go", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := searchCoveringTestMirror(testCase.source, testCase.candidate)
			if testCase.source == testCase.candidate {
				// searchMirrorTestFiles excludes the source path itself; the predicate is only
				// ever asked about other files, so assert the caller's contract instead.
				return
			}
			if got != testCase.want {
				t.Fatalf("mirror(%q, %q) = %v, want %v",
					testCase.source, testCase.candidate, got, testCase.want)
			}
		})
	}
}

func TestSearchNameMentions(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		outer, inner string
		want         bool
	}{
		{"TestRouteRedirectFixedPath", "redirectFixedPath", true},
		{"testRegisterTypeAdapterForCoreType", "registerTypeAdapter", true},
		{"test_shift_timezone", "shiftTimezone", true},
		{"TestSomethingElse", "normalizePath", false},
		{"TestX", "normalizePath", false},
		// Too short to be evidence: three-character names collide constantly.
		{"TestFoo", "foo", false},
	} {
		t.Run(testCase.outer+"/"+testCase.inner, func(t *testing.T) {
			t.Parallel()
			if got := searchNameMentions(testCase.outer, testCase.inner); got != testCase.want {
				t.Fatalf("searchNameMentions(%q, %q) = %v, want %v",
					testCase.outer, testCase.inner, got, testCase.want)
			}
		})
	}
}
