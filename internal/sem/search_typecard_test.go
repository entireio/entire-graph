package sem

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------------------------

// cardFixture is a small Go-shaped repo: one method whose body touches a field, returns a type,
// calls a function and binds two locals. Every case below is that repo with one thing changed.
func cardFixture() relatedFixture {
	return relatedFixture{
		files: []relatedFile{
			{path: "tsdb/append.go", lines: []string{
				"package tsdb", // 1
				"",             // 2
				"func (s *series) prepare(t int64) (c *memChunk) {", // 3
				"\tcutoff := computeCutoff(t)",                      // 4
				"\tunusedOnce := t * 2",                             // 5
				"\tc = s.headChunks",                                // 6
				"\tif c == nil || unusedOnce > cutoff {",            // 7
				"\t\tc = s.cut(cutoff)",                             // 8
				"\t}",                                               // 9
				"\treturn c",                                        // 10
				"}",                                                 // 11
				"",                                                  // 12
				"func computeCutoff(t int64) int64 { return t / 2 }", // 13
			}},
			{path: "tsdb/head.go", lines: []string{
				"package tsdb",                   // 1
				"",                               // 2
				"type series struct {",           // 3
				"\t// The current open chunk.",   // 4
				"\theadChunks *memChunk",         // 5
				"}",                              // 6
				"",                               // 7
				"// memChunk is one open chunk.", // 8
				"type memChunk struct {",         // 9
				"\t// The chunk's own bounds.",   // 10
				"\tminTime, maxTime int64",       // 11
				"\tprev *memChunk",               // 12
				"}",                              // 13
			}},
		},
		symbols: []SymbolRecord{
			coverSymbol("m:prepare", "prepare", "series.prepare", "method", "tsdb/append.go", 3, 11),
			coverSymbol("f:computeCutoff", "computeCutoff", "computeCutoff", "function", "tsdb/append.go", 13, 13),
			coverSymbol("t:series", "series", "series", "type", "tsdb/head.go", 3, 6),
			coverSymbol("fl:headChunks", "headChunks", "series.headChunks", "field", "tsdb/head.go", 5, 5),
			coverSymbol("t:memChunk", "memChunk", "memChunk", "type", "tsdb/head.go", 9, 13),
		},
		relations: []RelationRecord{
			{FromID: "m:prepare", ToID: "fl:headChunks", Type: "READS_FIELD", Confidence: 0.9},
			{FromID: "m:prepare", ToID: "t:series", Type: "PARAM_TYPE", Confidence: 0.85},
			{FromID: "m:prepare", ToID: "f:computeCutoff", Type: "CALLS", Confidence: 0.92},
		},
	}
}

// cardFor runs the card the way search does and returns "name|decl" per entry.
func cardFor(t *testing.T, fixture relatedFixture, results []SearchResult, anchorID string, byteCap int) []string {
	t.Helper()
	byID, byFile := fixture.indexes()
	card := selectSearchTypeCard(
		results, []SymbolRecord{fixture.symbol(t, anchorID)}, fixture.relations,
		byID, byFile, fixture.reader(), byteCap,
	)
	rendered := make([]string, 0, len(card))
	for _, entry := range card {
		rendered = append(rendered, entry.Name+"|"+entry.Decl)
	}
	return rendered
}

// completeBodyResult is the payload shape the binding rule requires: the anchor's whole body,
// returned as a complete symbol.
func completeBodyResult(path string, start, end int) SearchResult {
	return SearchResult{
		Rank: 1, FilePath: path, StartLine: start, EndLine: end, FocusLine: start,
		SnippetStartLine: start, SnippetEndLine: end,
		Signals: []string{searchCompleteSymbolSignal},
	}
}

// ---------------------------------------------------------------------------------------------
// selection
// ---------------------------------------------------------------------------------------------

func TestSelectSearchTypeCardEntries(t *testing.T) {
	t.Parallel()
	t.Run("a touched field brings its own type with it", func(t *testing.T) {
		t.Parallel()
		card := cardFor(t, cardFixture(), nil, "m:prepare", searchTypeCardBytes)
		if len(card) < 2 {
			t.Fatalf("card = %v, want at least the field and its type", card)
		}
		if card[0] != "series.headChunks|headChunks *memChunk" {
			t.Fatalf("card[0] = %q, want the field declaration", card[0])
		}
		// The closure step: knowing the field is a *memChunk is only half an answer.
		if !strings.HasPrefix(card[1], "memChunk|") || !strings.Contains(card[1], "prev *memChunk") {
			t.Fatalf("card[1] = %q, want the memChunk declaration including its prev field", card[1])
		}
		// Comment-only lines inside the declaration are dropped: the byte cap must be spent on
		// members, not on prose about them.
		if strings.Contains(card[1], "chunk's own bounds") {
			t.Fatalf("card[1] = %q, want the interior comment dropped", card[1])
		}
	})

	t.Run("an identifier the graph cannot resolve is omitted", func(t *testing.T) {
		t.Parallel()
		fixture := cardFixture()
		// `s.cut` is called in the body and has no symbol and no edge: the card must not invent it.
		card := cardFor(t, fixture, nil, "m:prepare", searchTypeCardBytes)
		for _, entry := range card {
			if strings.HasPrefix(entry, "cut|") || strings.HasPrefix(entry, "series.cut|") {
				t.Fatalf("card invented an entry for an unresolvable name: %v", card)
			}
		}
	})

	t.Run("an anchor with no resolvable relations gets no card", func(t *testing.T) {
		t.Parallel()
		fixture := cardFixture()
		fixture.relations = nil
		if card := cardFor(t, fixture, nil, "m:prepare", searchTypeCardBytes); len(card) != 0 {
			t.Fatalf("card = %v, want none", card)
		}
	})

	t.Run("a relation below the confidence floor is not admitted", func(t *testing.T) {
		t.Parallel()
		fixture := cardFixture()
		for index := range fixture.relations {
			fixture.relations[index].Confidence = searchTypeCardMinConfidence - 0.01
		}
		if card := cardFor(t, fixture, nil, "m:prepare", searchTypeCardBytes); len(card) != 0 {
			t.Fatalf("card = %v, want none: every relation is below the floor", card)
		}
	})

	t.Run("a declaration already printed by the payload is not repeated", func(t *testing.T) {
		t.Parallel()
		fixture := cardFixture()
		// The payload already shows all of tsdb/head.go, so every declaration in it is in view.
		results := []SearchResult{completeBodyResult("tsdb/head.go", 1, 13)}
		for _, entry := range cardFor(t, fixture, results, "m:prepare", searchTypeCardBytes) {
			if strings.HasPrefix(entry, "series.headChunks|") || strings.HasPrefix(entry, "memChunk|") {
				t.Fatalf("card repeated a declaration the payload already prints: %q", entry)
			}
		}
	})

	t.Run("a test helper the graph mis-resolved as a callee is not admitted", func(t *testing.T) {
		t.Parallel()
		fixture := cardFixture()
		fixture.files = append(fixture.files, relatedFile{path: "tsdb/append_test.go", lines: []string{
			"package tsdb", // 1
			"",             // 2
			"func createDriver(t *testing.T) *series {", // 3
			"\treturn &series{}",                        // 4
			"}",                                         // 5
		}})
		fixture.symbols = append(fixture.symbols,
			coverSymbol("test:createDriver", "createDriver", "createDriver", "function", "tsdb/append_test.go", 3, 5))
		fixture.relations = []RelationRecord{
			{FromID: "m:prepare", ToID: "test:createDriver", Type: "CALLS", Confidence: 0.78},
		}
		if card := cardFor(t, fixture, nil, "m:prepare", searchTypeCardBytes); len(card) != 0 {
			t.Fatalf("card = %v, want none: a production body cannot depend on a test helper", card)
		}
	})

	t.Run("the budget cuts entries rather than truncating the card into nonsense", func(t *testing.T) {
		t.Parallel()
		fixture := cardFixture()
		full := cardFor(t, fixture, nil, "m:prepare", searchTypeCardBytes)
		tight := cardFor(t, fixture, nil, "m:prepare", 120)
		if len(tight) >= len(full) {
			t.Fatalf("tight card %v did not shrink from %v", tight, full)
		}
		if len(tight) > 0 && tight[0] != full[0] {
			t.Fatalf("tight card kept %q instead of the strongest entry %q", tight[0], full[0])
		}
		if none := cardFor(t, fixture, nil, "m:prepare", 8); len(none) != 0 {
			t.Fatalf("card = %v, want none at a budget that cannot hold one entry", none)
		}
	})
}

// TestSelectSearchTypeCardBindings pins the body-local binding rule: it exists for the local that
// ONE line consumes, because that is the one an edit orphans.
func TestSelectSearchTypeCardBindings(t *testing.T) {
	t.Parallel()
	fixture := cardFixture()
	fixture.relations = nil // isolate the binding kind from the resolved kinds
	byID, byFile := fixture.indexes()
	cache := newSearchRelatedFileCache(fixture.reader(), searchTypeCardFileReads)
	results := []SearchResult{completeBodyResult("tsdb/append.go", 3, 11)}
	bindings := searchTypeCardBindings(results, fixture.symbol(t, "m:prepare"), byFile, cache)
	_ = byID

	found := map[string]searchTypeCardCandidate{}
	for _, candidate := range bindings {
		found[candidate.entry.Name] = candidate
	}
	unused, ok := found["unusedOnce"]
	if !ok {
		t.Fatalf("bindings %v are missing the single-use local", found)
	}
	if got := unused.entry.UseLines; len(got) != 1 || got[0] != 7 {
		t.Fatalf("unusedOnce use lines = %v, want [7]", got)
	}
	cutoff, ok := found["cutoff"]
	if !ok {
		t.Fatalf("bindings %v are missing the two-use local", found)
	}
	// Fewer uses ranks higher: the single-use local is the one a deletion orphans.
	if unused.tier <= cutoff.tier {
		t.Fatalf("single-use tier %d does not outrank two-use tier %d", unused.tier, cutoff.tier)
	}

	t.Run("a partial body claims nothing about where a local is used", func(t *testing.T) {
		t.Parallel()
		partial := []SearchResult{{
			Rank: 1, FilePath: "tsdb/append.go", StartLine: 3, EndLine: 11, FocusLine: 5,
			SnippetStartLine: 4, SnippetEndLine: 6,
		}}
		cache := newSearchRelatedFileCache(fixture.reader(), searchTypeCardFileReads)
		if got := searchTypeCardBindings(partial, fixture.symbol(t, "m:prepare"), byFile, cache); len(got) != 0 {
			t.Fatalf("bindings = %v, want none without a complete body", got)
		}
	})
}

func TestSearchBoundIdentifier(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		line string
		want string
	}{
		{"\textNotCaddyfileOrJSON := (a != \"\")", "extNotCaddyfileOrJSON"},
		{"var counter = 0", "counter"},
		{"  const timeout = 30", "timeout"},
		{"\tlet buffer = new Buffer()", "buffer"},
		// Re-assignment is not a declaration: `robj *value` may be declared above it.
		{"\tvalue = listTypePop(o,where);", ""},
		// Comparisons, arrows and multi-name binds are not single declarations.
		{"\tif counter == 3 {", ""},
		{"\thandler = () => 1", ""},
		{"\ta, b := f()", ""},
		{"\tx := 1", ""},
	} {
		t.Run(strings.TrimSpace(testCase.line), func(t *testing.T) {
			t.Parallel()
			got, ok := searchBoundIdentifier(testCase.line)
			if testCase.want == "" {
				if ok {
					t.Fatalf("searchBoundIdentifier(%q) = %q, want no binding", testCase.line, got)
				}
				return
			}
			if !ok || got != testCase.want {
				t.Fatalf("searchBoundIdentifier(%q) = %q/%v, want %q", testCase.line, got, ok, testCase.want)
			}
		})
	}
}

// TestSearchTypeCardEntryDeclares pins the card's own correctness check: text printed as the
// declaration of X has to name X in declaring position.
func TestSearchTypeCardEntryDeclares(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		decl string
		kind string
		want bool
	}{
		{"memChunk", "type memChunk struct { prev *memChunk }", "type", true},
		{"headChunks", "headChunks *memChunk", "field", true},
		{"addReply", "void addReply(client *c, robj *obj)", "function", true},
		{"client", "typedef struct client { int resp; }", "struct", true},
		// An anonymous struct the provider filed under `robj`: it names robj only in a FIELD.
		{"robj", "typedef struct { robj *subject; int type; }", "struct", false},
		// A function the provider filed as a struct because a PARAMETER named the type.
		{"connection", "void connRemoved(struct connection *conn)", "struct", false},
		// The same text is a perfectly good declaration of the function itself.
		{"connRemoved", "void connRemoved(struct connection *conn)", "function", true},
		// A signature clipped before its own name says nothing about it.
		{"robj", "struct", "struct", false},
		{"memChunk", "", "type", false},
	} {
		t.Run(testCase.name+"/"+testCase.decl, func(t *testing.T) {
			t.Parallel()
			symbol := SymbolRecord{Name: testCase.name, Kind: testCase.kind}
			entry := TypeCardEntry{Name: testCase.name, Decl: testCase.decl}
			if got := searchTypeCardEntryDeclares(entry, symbol); got != testCase.want {
				t.Fatalf("declares(%q, %q) = %v, want %v",
					testCase.name, testCase.decl, got, testCase.want)
			}
		})
	}
}

// TestSelectSearchTypeCardPrefersLocalDeclaration pins the wrong-declaration guard: when one name
// is declared in two packages, the card shows the one in the anchor's own tree.
func TestSelectSearchTypeCardPrefersLocalDeclaration(t *testing.T) {
	t.Parallel()
	fixture := cardFixture()
	fixture.files = append(fixture.files, relatedFile{path: "agent/series.go", lines: []string{
		"package agent",        // 1
		"",                     // 2
		"type series struct {", // 3
		"\tlastTs int64",       // 4
		"\textra  int64",       // 5
		"\tmore   int64",       // 6
		"\tstill  int64",       // 7
		"}",                    // 8
	}})
	// The decoy is declared over MORE lines, so span alone would pick it; locality must win.
	fixture.symbols = append(fixture.symbols,
		coverSymbol("t:agentSeries", "series", "series", "type", "agent/series.go", 3, 8))
	// Point the PARAM_TYPE edge at the decoy, exactly as a name-only resolution would.
	for index := range fixture.relations {
		if fixture.relations[index].Type == "PARAM_TYPE" {
			fixture.relations[index].ToID = "t:agentSeries"
		}
	}
	fixture.relations = []RelationRecord{
		{FromID: "m:prepare", ToID: "t:agentSeries", Type: "PARAM_TYPE", Confidence: 0.75},
	}
	card := cardFor(t, fixture, nil, "m:prepare", searchTypeCardBytes)
	if len(card) == 0 {
		t.Fatal("card = none, want the series declaration")
	}
	if !strings.Contains(card[0], "headChunks") {
		t.Fatalf("card[0] = %q, want the series declared beside the anchor", card[0])
	}
	if strings.Contains(card[0], "lastTs") {
		t.Fatalf("card[0] = %q, want the decoy in agent/ rejected", card[0])
	}
}
