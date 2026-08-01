package sem

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// the mention-check budget counts HYDRATIONS, not candidates
// ---------------------------------------------------------------------------------------------

// wideMirrorTestFile is a mirror test file with more test methods than the old per-candidate
// mention cap allowed, all in ONE file. It is the Java/C# shape: twenty methods, one file, one
// hydration. `TestPathZ` is deliberately last and deliberately the shortest body, so it can only
// win if it was scored at all.
func wideMirrorTestFile(methods int) relatedFile {
	lines := []string{"package server", ""}
	for index := 1; index <= methods; index++ {
		lines = append(lines,
			"func TestPath"+string(rune('A'+index-1))+"(t *testing.T) {",
			"\tgot := normalizePath(\"\")",
			"\trequire.Equal(t, \"/\", got)",
			"}",
			"")
	}
	return relatedFile{path: "server/handler_test.go", lines: lines}
}

func wideMirrorSymbols(methods int) []SymbolRecord {
	symbols := []SymbolRecord{
		coverSymbol("src:normalizePath", "normalizePath", "normalizePath", "function",
			"server/handler.go", 3, 8),
	}
	for index := 1; index <= methods; index++ {
		name := "TestPath" + string(rune('A'+index-1))
		start := 3 + (index-1)*5
		symbols = append(symbols, coverSymbol("test:"+name, name, name, "function",
			"server/handler_test.go", start, start+3))
	}
	return symbols
}

// TestSelectSearchCoveringTestScoresEveryCandidateInOneFile is the regression for the budget bug:
// with every candidate in a single already-hydrated file, ALL of them must be scored, so the
// covering-test set is the whole file's tests rather than the first few the counter happened to
// reach.
func TestSelectSearchCoveringTestScoresEveryCandidateInOneFile(t *testing.T) {
	t.Parallel()
	const methods = 12
	if methods <= searchCoveringTestMentionChecks {
		t.Fatalf("fixture must exceed the mention cap of %d to test it", searchCoveringTestMentionChecks)
	}
	fixture := coverFixture(
		[]relatedFile{coverSourceFile(), wideMirrorTestFile(methods)},
		wideMirrorSymbols(methods),
		nil,
	)
	byID, byFile := fixture.indexes()
	cache := newSearchRelatedFileCache(fixture.reader(), searchCoveringTestFileReads)
	shown, peers, found := selectSearchCoveringTest(
		nil, fixture.symbol(t, "src:normalizePath"), buildSearchQuery("normalize path"),
		fixture.relations, byID, byFile, cache,
	)
	if !found {
		t.Fatal("no covering test selected for a mirror file full of covering tests")
	}
	if got := len(peers) + 1; got != methods {
		t.Fatalf("scored %d of %d candidates; the mention budget rejected the rest", got, methods)
	}
	if !strings.HasPrefix(shown.symbol.Name, "TestPath") {
		t.Fatalf("chosen test = %q, want one of the mirror file's tests", shown.symbol.Name)
	}
}

// ---------------------------------------------------------------------------------------------
// the coverage note
// ---------------------------------------------------------------------------------------------

func coverageAnchor() SymbolRecord {
	return coverSymbol("src:normalizePath", "normalizePath", "normalizePath", "function",
		"server/handler.go", 3, 8)
}

func coveringOf(name string, line int) searchCoveringTest {
	return searchCoveringTest{
		symbol: coverSymbol("test:"+name, name, name, "function", "server/handler_test.go", line, line+3),
		line:   line + 1,
	}
}

func TestBuildSearchCoverageNote(t *testing.T) {
	t.Parallel()
	shown := coveringOf("TestA", 3)
	cases := []struct {
		name      string
		peers     []searchCoveringTest
		budget    int
		wantNil   bool
		wantTotal int
		wantPeers []string
		wantMore  int
		wantFile  string
	}{
		{
			name:    "a single covering test needs no note",
			peers:   nil,
			budget:  searchCoverageNoteBytes,
			wantNil: true,
		},
		{
			name:      "two tests: the peer is named and the count is stated",
			peers:     []searchCoveringTest{coveringOf("TestB", 8)},
			budget:    searchCoverageNoteBytes,
			wantTotal: 2,
			wantPeers: []string{"TestB"},
			wantFile:  "server/handler_test.go",
		},
		{
			name: "more peers than the naming cap: the surplus is counted",
			peers: []searchCoveringTest{
				coveringOf("TestB", 8), coveringOf("TestC", 13), coveringOf("TestD", 18),
				coveringOf("TestE", 23), coveringOf("TestF", 28), coveringOf("TestG", 33),
				coveringOf("TestH", 38),
			},
			budget:    0,
			wantTotal: 8,
			wantPeers: []string{"TestB", "TestC", "TestD", "TestE", "TestF", "TestG"},
			wantMore:  1,
			wantFile:  "server/handler_test.go",
		},
		{
			name: "two peers of the same name are named once and counted twice",
			peers: []searchCoveringTest{
				coveringOf("TestB", 8),
				{
					symbol: coverSymbol("test:TestB", "TestB", "TestB", "function",
						"server/handler_test.go", 20, 23),
					line: 21,
				},
				coveringOf("TestC", 30),
			},
			budget:    searchCoverageNoteBytes,
			wantTotal: 4,
			wantPeers: []string{"TestB", "TestC"},
			wantMore:  1,
			wantFile:  "server/handler_test.go",
		},
		{
			name: "peers are named in declaration order, not score order",
			peers: []searchCoveringTest{
				coveringOf("TestLater", 30), coveringOf("TestEarlier", 8),
			},
			budget:    searchCoverageNoteBytes,
			wantTotal: 3,
			wantPeers: []string{"TestEarlier", "TestLater"},
			wantFile:  "server/handler_test.go",
		},
		{
			name: "peers spread over files report no single path",
			peers: []searchCoveringTest{{
				symbol: coverSymbol("test:TestB", "TestB", "TestB", "function",
					"server/other_test.go", 3, 6),
				line: 4,
			}},
			budget:    searchCoverageNoteBytes,
			wantTotal: 2,
			wantPeers: []string{"TestB"},
			wantFile:  "",
		},
		{
			name:      "a budget too small for even one name drops the note",
			peers:     []searchCoveringTest{coveringOf("TestB", 8)},
			budget:    1,
			wantNil:   true,
			wantTotal: 0,
		},
		{
			name: "peers the graph cannot name produce no note",
			peers: []searchCoveringTest{{
				symbol: SymbolRecord{ID: "test:anon", Kind: "function",
					FilePath: "server/handler_test.go", StartLine: 8, EndLine: 11},
				line: 9,
			}},
			budget:  searchCoverageNoteBytes,
			wantNil: true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			note := buildSearchCoverageNote(coverageAnchor(), shown, testCase.peers, testCase.budget)
			if testCase.wantNil {
				if note != nil {
					t.Fatalf("note = %#v, want none", note)
				}
				return
			}
			if note == nil {
				t.Fatal("no note built")
			}
			if note.Symbol != "normalizePath" {
				t.Errorf("note symbol = %q, want the anchor's name", note.Symbol)
			}
			if note.Total != testCase.wantTotal {
				t.Errorf("total = %d, want %d", note.Total, testCase.wantTotal)
			}
			if strings.Join(note.Peers, ",") != strings.Join(testCase.wantPeers, ",") {
				t.Errorf("peers = %v, want %v", note.Peers, testCase.wantPeers)
			}
			if note.More != testCase.wantMore {
				t.Errorf("more = %d, want %d", note.More, testCase.wantMore)
			}
			if note.FilePath != testCase.wantFile {
				t.Errorf("file = %q, want %q", note.FilePath, testCase.wantFile)
			}
			if cost := searchCoverageNoteCost(note); testCase.budget > 0 && cost > testCase.budget {
				t.Errorf("note costs %d bytes, over its %d allowance", cost, testCase.budget)
			}
		})
	}
}

func TestRenderSearchCoverageNote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		note *SearchCoverageNote
		want string
	}{
		{name: "nil renders nothing", note: nil, want: ""},
		{
			name: "a note with no names renders nothing",
			note: &SearchCoverageNote{Symbol: "go", Total: 4},
			want: "",
		},
		{
			name: "the count leads and the path closes",
			note: &SearchCoverageNote{
				Symbol: "DruidLeaderClient", Total: 6,
				Peers:    []string{"testServerFailureAndRedirect", "testRedirection"},
				More:     3,
				FilePath: "server/src/test/java/DruidLeaderClientTest.java",
			},
			want: "  ALSO COVERING DruidLeaderClient (6 tests cover it; all must still pass): " +
				"testServerFailureAndRedirect, testRedirection +3 more in " +
				"server/src/test/java/DruidLeaderClientTest.java\n",
		},
		{
			name: "no surplus and no shared path",
			note: &SearchCoverageNote{Symbol: "normalizePath", Total: 2, Peers: []string{"TestB"}},
			want: "  ALSO COVERING normalizePath (2 tests cover it; all must still pass): TestB\n",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := string(RenderSearchCoverageNote(testCase.note)); got != testCase.want {
				t.Errorf("render = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestMergeSearchContractContextDropsTheNoteFirst pins the yielding order under a hard ceiling:
// the note is the first block given up, and it can never outlive the test it describes.
func TestMergeSearchContractContextDropsTheNoteFirst(t *testing.T) {
	t.Parallel()
	results := contractPayload()
	entry := contractTestEntry()
	note := &SearchCoverageNote{Symbol: "head", Total: 3, Peers: []string{"TestHeadTwo"}, More: 1}

	// No ceiling: everything is seated.
	_, _, seated, tests, _ := mergeSearchContractContext(
		results, searchContractContext{test: &entry, note: note, card: contractCard()}, 0,
	)
	if tests != 1 || seated == nil {
		t.Fatalf("note not seated with no ceiling forcing a choice (tests=%d)", tests)
	}

	// A ceiling that cannot fit the card and the note: the note goes before the card's entries.
	baseline := serializedSearchResultBytes(results)
	_, card, tight, tests, _ := mergeSearchContractContext(
		results, searchContractContext{test: &entry, note: note, card: contractCard()}, baseline,
	)
	if tight != nil && len(card) == len(contractCard()) && tests == 1 {
		t.Fatal("nothing yielded under a baseline ceiling: the accounting is not binding")
	}

	// No test means no note, whatever the budget.
	_, _, orphan, _, _ := mergeSearchContractContext(
		results, searchContractContext{note: note, card: contractCard()}, 0,
	)
	if orphan != nil {
		t.Fatalf("note survived without the covering test it describes: %#v", orphan)
	}
}
