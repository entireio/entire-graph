package sem

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------------------------

type relatedFile struct {
	path  string
	lines []string
}

type relatedFixture struct {
	files     []relatedFile
	symbols   []SymbolRecord
	relations []RelationRecord
}

func (fixture relatedFixture) reader() contentReader {
	content := make(map[string]string, len(fixture.files))
	for _, file := range fixture.files {
		content[file.path] = strings.Join(file.lines, "\n")
	}
	return func(path string) (string, bool) {
		value, ok := content[path]
		return value, ok
	}
}

func (fixture relatedFixture) indexes() (map[string]SymbolRecord, map[string][]SymbolRecord) {
	byID := make(map[string]SymbolRecord, len(fixture.symbols))
	byFile := map[string][]SymbolRecord{}
	for _, symbol := range fixture.symbols {
		byID[symbol.ID] = symbol
		byFile[symbol.FilePath] = append(byFile[symbol.FilePath], symbol)
	}
	for path := range byFile {
		members := byFile[path]
		sort.Slice(members, func(i, j int) bool { return members[i].StartLine < members[j].StartLine })
	}
	return byID, byFile
}

func (fixture relatedFixture) symbol(t *testing.T, id string) SymbolRecord {
	t.Helper()
	for _, symbol := range fixture.symbols {
		if symbol.ID == id {
			return symbol
		}
	}
	t.Fatalf("fixture has no symbol %q", id)
	return SymbolRecord{}
}

func relatedFunction(id, name, path string, start, end int) SymbolRecord {
	return SymbolRecord{
		RecordType: "symbol", ID: id, Kind: "function", Name: name, QualifiedName: name,
		FilePath: path, StartLine: start, EndLine: end,
	}
}

func relatedMethod(id, name, qualified, path, container string, start, end int) SymbolRecord {
	return SymbolRecord{
		RecordType: "symbol", ID: id, Kind: "method", Name: name, QualifiedName: qualified,
		FilePath: path, StartLine: start, EndLine: end, ContainerID: container,
	}
}

func relatedClass(id, name, path string, start, end int) SymbolRecord {
	return SymbolRecord{
		RecordType: "symbol", ID: id, Kind: "class", Name: name, QualifiedName: name,
		FilePath: path, StartLine: start, EndLine: end,
	}
}

func relatedEdge(kind, from, to string) RelationRecord {
	return RelationRecord{RecordType: "relation", FromID: from, ToID: to, Type: kind, Confidence: 0.9}
}

// handlerBody is a body big enough to clear the near-duplicate size floor and share enough
// tokens with its twin to clear the clone bar: the two differ in exactly one identifier, which is
// the shape of a real copy-paste family (Str::trim / Str::ltrim, Round::up / Round::down).
func handlerBody(name string) []string {
	return []string{
		fmt.Sprintf("func %s(request Request, options Options) (Result, error) {", name),
		"\tif request.Empty {",
		"\t\treturn Result{}, errors.New(\"empty request payload provided\")",
		"\t}",
		"\tnormalized := normalize(request.Body, options.Charset, options.Mode)",
		fmt.Sprintf("\treturn Result{Value: normalized, Kind: %q}, nil", name),
		"}",
	}
}

func cloneFamilyFile(path string) relatedFile {
	lines := []string{"package text", ""}
	lines = append(lines, handlerBody("alphaHandler")...)
	lines = append(lines, "")
	lines = append(lines, handlerBody("betaHandler")...)
	lines = append(lines, "", "func tiny(count int) int {", "\treturn count * 2", "}")
	return relatedFile{path: path, lines: lines}
}

// cloneFamilyFixture: alphaHandler (3-9) and betaHandler (11-17) are near-duplicates in one file;
// tiny (19-21) is too small to compare. runFormat calls alphaHandler from another file; a test and
// an example call it too.
func cloneFamilyFixture() relatedFixture {
	caller := relatedFile{path: "src/text/caller.go", lines: []string{
		"package text",
		"",
		"func runFormat(request Request) Result {",
		"\toptions := defaultOptions()",
		"\tresult, err := alphaHandler(request, options)",
		"\tif err != nil {",
		"\t\tpanic(err)",
		"\t}",
		"\treturn result",
		"}",
	}}
	unit := relatedFile{path: "src/text/format_test.go", lines: []string{
		"package text",
		"",
		"func TestAlphaHandler(t *testing.T) {",
		"\talphaHandler(Request{}, Options{})",
		"}",
	}}
	sample := relatedFile{path: "examples/demo/main.go", lines: []string{
		"package main",
		"",
		"func demo() {",
		"\talphaHandler(Request{}, Options{})",
		"}",
	}}
	return relatedFixture{
		files: []relatedFile{cloneFamilyFile("src/text/format.go"), caller, unit, sample},
		symbols: []SymbolRecord{
			relatedFunction("alpha", "alphaHandler", "src/text/format.go", 3, 9),
			relatedFunction("beta", "betaHandler", "src/text/format.go", 11, 17),
			relatedFunction("tiny", "tiny", "src/text/format.go", 19, 21),
			relatedFunction("run", "runFormat", "src/text/caller.go", 3, 10),
			relatedFunction("unit", "TestAlphaHandler", "src/text/format_test.go", 3, 5),
			relatedFunction("sample", "demo", "examples/demo/main.go", 3, 5),
		},
		relations: []RelationRecord{
			{
				RecordType: "relation", FromID: "run", ToID: "alpha", Type: "CALLS", Confidence: 0.95,
				Evidence: []Evidence{{Kind: "call", FilePath: "src/text/caller.go", StartLine: 5}},
			},
			relatedEdge("CALLS", "unit", "alpha"),
			relatedEdge("CALLS", "sample", "alpha"),
		},
	}
}

// overrideFixture: Mesh.draw and Line.draw both override Object.draw (member-level edges).
func overrideFixture() relatedFixture {
	body := func(owner string) []string {
		lines := []string{fmt.Sprintf("class %s {", owner)}
		lines = append(lines, handlerBody("draw")...)
		lines = append(lines, "}")
		return lines
	}
	return relatedFixture{
		files: []relatedFile{
			{path: "src/render/mesh.go", lines: body("Mesh")},
			{path: "src/render/line.go", lines: body("Line")},
			{path: "src/render/object.go", lines: body("Object")},
		},
		symbols: []SymbolRecord{
			relatedClass("class:Mesh", "Mesh", "src/render/mesh.go", 1, 9),
			relatedClass("class:Line", "Line", "src/render/line.go", 1, 9),
			relatedClass("class:Object", "Object", "src/render/object.go", 1, 9),
			relatedMethod("mesh.draw", "draw", "Mesh.draw", "src/render/mesh.go", "class:Mesh", 2, 8),
			relatedMethod("line.draw", "draw", "Line.draw", "src/render/line.go", "class:Line", 2, 8),
			relatedMethod("object.draw", "draw", "Object.draw", "src/render/object.go", "class:Object", 2, 8),
		},
		relations: []RelationRecord{
			relatedEdge("OVERRIDES", "mesh.draw", "object.draw"),
			relatedEdge("OVERRIDES", "line.draw", "object.draw"),
		},
	}
}

// inheritFixture: Square and Circle both EXTENDS Shape, each with its own `area`. No member-level
// override edges, which is how several providers emit inheritance.
func inheritFixture() relatedFixture {
	body := func(owner string) []string {
		lines := []string{fmt.Sprintf("class %s {", owner)}
		lines = append(lines, handlerBody("area")...)
		lines = append(lines, "}")
		return lines
	}
	return relatedFixture{
		files: []relatedFile{
			{path: "src/shape/square.go", lines: body("Square")},
			{path: "src/shape/circle.go", lines: body("Circle")},
			{path: "src/shape/shape.go", lines: body("Shape")},
		},
		symbols: []SymbolRecord{
			relatedClass("class:Square", "Square", "src/shape/square.go", 1, 9),
			relatedClass("class:Circle", "Circle", "src/shape/circle.go", 1, 9),
			relatedClass("class:Shape", "Shape", "src/shape/shape.go", 1, 9),
			relatedMethod("square.area", "area", "Square.area", "src/shape/square.go", "class:Square", 2, 8),
			relatedMethod("circle.area", "area", "Circle.area", "src/shape/circle.go", "class:Circle", 2, 8),
			relatedMethod("shape.perimeter", "perimeter", "Shape.perimeter", "src/shape/shape.go", "class:Shape", 2, 8),
		},
		relations: []RelationRecord{
			relatedEdge("EXTENDS", "class:Square", "class:Shape"),
			relatedEdge("EXTENDS", "class:Circle", "class:Shape"),
		},
	}
}

// crowdedUnitFixture: one file whose top level holds more callables than a "rest of this unit"
// answer can be specific about.
func crowdedUnitFixture(memberCount int) relatedFixture {
	lines := []string{"package util", ""}
	symbols := make([]SymbolRecord, 0, memberCount)
	for index := 0; index < memberCount; index++ {
		start := len(lines) + 1
		lines = append(lines, fmt.Sprintf("func helper%d(value int) int {", index), "\treturn value", "}", "")
		symbols = append(symbols, relatedFunction(
			fmt.Sprintf("helper%d", index), fmt.Sprintf("helper%d", index), "src/util/util.go", start, start+2,
		))
	}
	return relatedFixture{
		files:   []relatedFile{{path: "src/util/util.go", lines: lines}},
		symbols: symbols,
	}
}

func relatedSiteKinds(sites []searchRelatedSite) []string {
	out := make([]string, 0, len(sites))
	for _, site := range sites {
		out = append(out, string(site.kind)+":"+site.symbol.QualifiedName)
	}
	return out
}

func selectFixtureSites(t *testing.T, fixture relatedFixture, anchorID, query string, results []SearchResult, limit int) []searchRelatedSite {
	t.Helper()
	byID, byFile := fixture.indexes()
	return selectSearchRelatedSites(
		results, []SymbolRecord{fixture.symbol(t, anchorID)}, buildSearchQuery(query),
		fixture.relations, byID, byFile, fixture.reader(), limit,
	)
}

// ---------------------------------------------------------------------------------------------
// selection
// ---------------------------------------------------------------------------------------------

func TestSelectSearchRelatedSitesRoutes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		fixture  relatedFixture
		anchor   string
		query    string
		results  []SearchResult
		limit    int
		want     []string
		notWant  []string
		wantNone bool
	}{
		{
			// The clone twin in the anchor's own file is the site the graph's repo-wide MinHash
			// misses on short bodies, and the caller comes back at its CALL SITE.
			name:    "near duplicate twin and caller",
			fixture: cloneFamilyFixture(),
			anchor:  "alpha",
			query:   "alpha handler drops the payload",
			results: []SearchResult{{Rank: 1, FilePath: "src/text/format.go", StartLine: 3, EndLine: 9, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 9, SymbolID: "alpha"}},
			limit:   4,
			want:    []string{"near-dupe:betaHandler", "caller:runFormat"},
			// A unit test and an example are not fix sites, so they never spend a slot.
			notWant: []string{"caller:TestAlphaHandler", "caller:demo", "near-dupe:tiny"},
		},
		{
			// Same fixture, one slot: the near-duplicate has the strongest claim on the change
			// (it needs the IDENTICAL edit), so it is drawn before the caller.
			name:    "near duplicate outranks caller for the last slot",
			fixture: cloneFamilyFixture(),
			anchor:  "alpha",
			query:   "alpha handler drops the payload",
			results: []SearchResult{{Rank: 1, FilePath: "src/text/format.go", StartLine: 3, EndLine: 9, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 9, SymbolID: "alpha"}},
			limit:   1,
			want:    []string{"near-dupe:betaHandler"},
			notWant: []string{"caller:runFormat"},
		},
		{
			// A candidate the payload already prints must not consume a slot.
			name:    "already surfaced twin frees its slot",
			fixture: cloneFamilyFixture(),
			anchor:  "alpha",
			query:   "alpha handler drops the payload",
			results: []SearchResult{
				{Rank: 1, FilePath: "src/text/format.go", StartLine: 3, EndLine: 9, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 9, SymbolID: "alpha"},
				{Rank: 2, FilePath: "src/text/format.go", StartLine: 11, EndLine: 17, FocusLine: 11, SnippetStartLine: 11, SnippetEndLine: 17, SymbolID: "beta"},
			},
			limit:   4,
			want:    []string{"caller:runFormat"},
			notWant: []string{"near-dupe:betaHandler"},
		},
		{
			// Member-level OVERRIDES: the other implementation of the same contract, and the
			// supertype member the anchor stands in for.
			name:    "sibling implementations via override edges",
			fixture: overrideFixture(),
			anchor:  "mesh.draw",
			query:   "draw skips the first vertex",
			results: []SearchResult{{Rank: 1, FilePath: "src/render/mesh.go", StartLine: 2, EndLine: 8, FocusLine: 2, SnippetStartLine: 2, SnippetEndLine: 8, SymbolID: "mesh.draw"}},
			limit:   4,
			want:    []string{"sibling:Line.draw", "sibling:Object.draw"},
		},
		{
			// Container-level inheritance only: the same-named member of the sibling type.
			name:    "sibling implementations via container inheritance",
			fixture: inheritFixture(),
			anchor:  "square.area",
			query:   "area is off by one",
			results: []SearchResult{{Rank: 1, FilePath: "src/shape/square.go", StartLine: 2, EndLine: 8, FocusLine: 2, SnippetStartLine: 2, SnippetEndLine: 8, SymbolID: "square.area"}},
			limit:   4,
			want:    []string{"sibling:Circle.area"},
			// A differently-named member of the supertype is not a sibling implementation.
			notWant: []string{"sibling:Shape.perimeter"},
		},
		{
			// A small unit's other members are a specific answer ("the rest of this file").
			name:    "co-members of a small unit",
			fixture: crowdedUnitFixture(3),
			anchor:  "helper0",
			query:   "helper returns the wrong value",
			results: []SearchResult{{Rank: 1, FilePath: "src/util/util.go", StartLine: 3, EndLine: 5, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 5, SymbolID: "helper0"}},
			limit:   4,
			want:    []string{"sibling:helper1", "sibling:helper2"},
		},
		{
			// A crowded unit's members are a lottery, so the route switches off entirely.
			name:     "co-members suppressed in a crowded unit",
			fixture:  crowdedUnitFixture(searchRelatedUnitMemberLimit + 2),
			anchor:   "helper0",
			query:    "helper returns the wrong value",
			results:  []SearchResult{{Rank: 1, FilePath: "src/util/util.go", StartLine: 3, EndLine: 5, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 5, SymbolID: "helper0"}},
			limit:    4,
			wantNone: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			sites := selectFixtureSites(t, testCase.fixture, testCase.anchor, testCase.query, testCase.results, testCase.limit)
			got := relatedSiteKinds(sites)
			if testCase.wantNone && len(got) != 0 {
				t.Fatalf("expected no related sites, got %v", got)
			}
			if len(got) > testCase.limit {
				t.Fatalf("block returned %d sites, limit %d: %v", len(got), testCase.limit, got)
			}
			for _, want := range testCase.want {
				if !containsRelatedString(got, want) {
					t.Fatalf("related sites %v missing %q", got, want)
				}
			}
			for _, reject := range testCase.notWant {
				if containsRelatedString(got, reject) {
					t.Fatalf("related sites %v must not contain %q", got, reject)
				}
			}
		})
	}
}

func containsRelatedString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestSelectSearchRelatedSitesReportsTheCallSite pins that a caller comes back at the line that
// calls the anchor, not at its own declaration: that line is what a contract change touches.
func TestSelectSearchRelatedSitesReportsTheCallSite(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	sites := selectFixtureSites(t, fixture, "alpha", "alpha handler drops the payload",
		[]SearchResult{
			{Rank: 1, FilePath: "src/text/format.go", StartLine: 3, EndLine: 9, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 9, SymbolID: "alpha"},
			{Rank: 2, FilePath: "src/text/format.go", StartLine: 11, EndLine: 17, FocusLine: 11, SnippetStartLine: 11, SnippetEndLine: 17, SymbolID: "beta"},
		}, 4)
	for _, site := range sites {
		if site.kind != searchRelatedCaller {
			continue
		}
		if site.line != 5 {
			t.Fatalf("caller reported line %d, want the call site 5", site.line)
		}
		return
	}
	t.Fatalf("no caller in %v", relatedSiteKinds(sites))
}

func TestSearchRelatedCallSiteLineFallsBackToTheDeclaration(t *testing.T) {
	t.Parallel()
	caller := relatedFunction("run", "runFormat", "src/text/caller.go", 3, 10)
	cases := []struct {
		name     string
		evidence []Evidence
		want     int
	}{
		{name: "no evidence", want: 3},
		{name: "evidence in the caller", evidence: []Evidence{{FilePath: "src/text/caller.go", StartLine: 5}}, want: 5},
		{name: "evidence in another file is ignored", evidence: []Evidence{{FilePath: "other.go", StartLine: 5}}, want: 3},
		{name: "evidence outside the caller span is ignored", evidence: []Evidence{{FilePath: "src/text/caller.go", StartLine: 99}}, want: 3},
		{name: "evidence without a line is ignored", evidence: []Evidence{{FilePath: "src/text/caller.go"}}, want: 3},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			relation := RelationRecord{FromID: "run", ToID: "alpha", Type: "CALLS", Evidence: testCase.evidence}
			if got := searchRelatedCallSiteLine(relation, caller); got != testCase.want {
				t.Fatalf("call site line = %d, want %d", got, testCase.want)
			}
		})
	}
}

// TestSelectSearchRelatedSitesUsesEveryHeadAnchor pins that the block is not computed from rank 1
// alone: the measurement that sized the complete-body head found rank 1 is the edit site only 35%
// of the time, so a block anchored only there describes the wrong neighbourhood on most searches.
func TestSelectSearchRelatedSitesUsesEveryHeadAnchor(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	byID, byFile := fixture.indexes()
	results := []SearchResult{
		{Rank: 1, FilePath: "src/text/caller.go", StartLine: 3, EndLine: 10, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 10, SymbolID: "run"},
		{Rank: 2, FilePath: "src/text/format.go", StartLine: 3, EndLine: 9, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 9, SymbolID: "alpha"},
	}
	anchors := searchRelatedAnchors(results, byID, byFile, searchEnclosureHeadRanks)
	if len(anchors) != 2 || anchors[0].ID != "run" || anchors[1].ID != "alpha" {
		t.Fatalf("anchors = %v, want [run alpha] in rank order", relatedSymbolIDs(anchors))
	}
	sites := selectSearchRelatedSites(
		results, anchors, buildSearchQuery("alpha handler drops the payload"),
		fixture.relations, byID, byFile, fixture.reader(), 4,
	)
	if got := relatedSiteKinds(sites); !containsRelatedString(got, "near-dupe:betaHandler") {
		t.Fatalf("related sites %v lost the rank-2 anchor's clone twin", got)
	}
}

func TestSearchRelatedAnchorsSkipsDocsAndRepeats(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	byID, byFile := fixture.indexes()
	results := []SearchResult{
		// A docs-and-fixtures hit is not a fix site, so its neighbourhood is not the change's.
		{Rank: 1, FilePath: "CHANGELOG.md", StartLine: 1, EndLine: 2, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 2, Section: searchSectionDocs},
		{Rank: 2, FilePath: "src/text/format.go", StartLine: 3, EndLine: 9, FocusLine: 4, SnippetStartLine: 3, SnippetEndLine: 9, SymbolID: "alpha"},
		// A second fragment of the same symbol must not anchor twice.
		{Rank: 3, FilePath: "src/text/format.go", StartLine: 5, EndLine: 7, FocusLine: 6, SnippetStartLine: 5, SnippetEndLine: 7, SymbolID: "alpha"},
		// A related-site entry is not an anchor either.
		{Rank: 4, FilePath: "src/text/caller.go", StartLine: 3, EndLine: 10, FocusLine: 3, SnippetStartLine: 3, SnippetEndLine: 3, SymbolID: "run", Section: searchSectionRelated},
	}
	anchors := searchRelatedAnchors(results, byID, byFile, searchEnclosureHeadRanks)
	if len(anchors) != 1 || anchors[0].ID != "alpha" {
		t.Fatalf("anchors = %v, want [alpha]", relatedSymbolIDs(anchors))
	}
}

func relatedSymbolIDs(symbols []SymbolRecord) []string {
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, symbol.ID)
	}
	return out
}

// TestInterleaveSearchRelatedSitesRotatesKinds pins that no single kind can monopolize the block
// and that a symbol is never listed twice.
func TestInterleaveSearchRelatedSitesRotatesKinds(t *testing.T) {
	t.Parallel()
	site := func(kind searchRelatedKind, id string) searchRelatedSite {
		return searchRelatedSite{kind: kind, symbol: SymbolRecord{ID: id, QualifiedName: id}}
	}
	dupes := []searchRelatedSite{site(searchRelatedNearDupe, "d1"), site(searchRelatedNearDupe, "d2"), site(searchRelatedNearDupe, "d3")}
	siblings := []searchRelatedSite{site(searchRelatedSibling, "s1"), site(searchRelatedSibling, "s2")}
	callers := []searchRelatedSite{site(searchRelatedCaller, "c1"), site(searchRelatedCaller, "d1")}

	cases := []struct {
		name  string
		limit int
		kinds [][]searchRelatedSite
		want  []string
	}{
		{
			name:  "one of each before a second of any",
			limit: 4,
			kinds: [][]searchRelatedSite{dupes, siblings, callers},
			want:  []string{"near-dupe:d1", "sibling:s1", "caller:c1", "near-dupe:d2"},
		},
		{
			name:  "exhausted kinds are skipped",
			limit: 5,
			kinds: [][]searchRelatedSite{dupes, siblings, nil},
			want:  []string{"near-dupe:d1", "sibling:s1", "near-dupe:d2", "sibling:s2", "near-dupe:d3"},
		},
		{
			name:  "a symbol is never listed twice",
			limit: 4,
			kinds: [][]searchRelatedSite{{site(searchRelatedNearDupe, "d1")}, nil, callers},
			want:  []string{"near-dupe:d1", "caller:c1"},
		},
		{
			name:  "zero limit yields nothing",
			limit: 0,
			kinds: [][]searchRelatedSite{dupes, siblings, callers},
			want:  nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := relatedSiteKinds(interleaveSearchRelatedSites(testCase.limit, testCase.kinds...))
			if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
				t.Fatalf("interleave = %v, want %v", got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// merge
// ---------------------------------------------------------------------------------------------

func mergeTestResults(count int, filler string) []SearchResult {
	results := make([]SearchResult, 0, count)
	for index := 0; index < count; index++ {
		results = append(results, SearchResult{
			Rank: index + 1, FilePath: fmt.Sprintf("src/pkg/file%d.go", index), StartLine: 1, EndLine: 40,
			FocusLine: 10, SnippetStartLine: 10, SnippetEndLine: 11, Signals: []string{"body"},
			Snippet: filler,
		})
	}
	return results
}

func mergeTestSite(id string, start int) searchRelatedSite {
	return searchRelatedSite{
		kind:   searchRelatedNearDupe,
		symbol: relatedFunction(id, id, "src/text/format.go", start, start+6),
		line:   start,
	}
}

func TestMergeSearchRelatedSitesNeverGrowsThePayload(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	read := fixture.reader()
	// Tail hits are fat and share a file, so displacing them funds the whole block.
	results := mergeTestResults(4, strings.Repeat("x", 400))
	results = append(results,
		SearchResult{Rank: 5, FilePath: "src/pkg/dup.go", StartLine: 1, EndLine: 40, FocusLine: 10, SnippetStartLine: 10, SnippetEndLine: 11, Signals: []string{"body"}, Snippet: strings.Repeat("y", 700)},
		SearchResult{Rank: 6, FilePath: "src/pkg/dup.go", StartLine: 50, EndLine: 90, FocusLine: 60, SnippetStartLine: 60, SnippetEndLine: 61, Signals: []string{"body"}, Snippet: strings.Repeat("z", 700)},
	)
	before := serializedSearchResultBytes(results)
	sites := []searchRelatedSite{mergeTestSite("alpha", 3), mergeTestSite("beta", 11)}
	merged, count := mergeSearchRelatedSites(results, sites, read, 0)
	if count == 0 {
		t.Fatalf("no related sites merged")
	}
	if after := serializedSearchResultBytes(merged); after > before {
		t.Fatalf("payload grew from %d to %d bytes", before, after)
	}
	for index, result := range merged {
		if result.Rank != index+1 {
			t.Fatalf("merged rank %d at index %d: ranks must stay 1..N", result.Rank, index)
		}
	}
	related := 0
	for _, result := range merged {
		if result.Section == searchSectionRelated {
			related++
			if result.Snippet == "" || result.SnippetStartLine != result.SnippetEndLine {
				t.Fatalf("related entry is not a one-line locator: %+v", result)
			}
			if result.SymbolID != "" || result.Language != "" {
				t.Fatalf("related entry carries fields a locator does not need: %+v", result)
			}
		}
	}
	if related != count {
		t.Fatalf("reported %d related sites, payload carries %d", count, related)
	}
}

// TestMergeSearchRelatedSitesRespectsTheHardBudget pins that --max-context-bytes still binds: a
// ranking already at or over the ceiling cannot buy a block, and the payload it returns is the
// untouched ranking rather than an over-budget one (which SearchResponse.Validate would reject).
func TestMergeSearchRelatedSitesRespectsTheHardBudget(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	results := mergeTestResults(6, strings.Repeat("x", 300))
	budget := serializedSearchResultBytes(results) / 2
	merged, count := mergeSearchRelatedSites(results, []searchRelatedSite{mergeTestSite("alpha", 3)}, fixture.reader(), budget)
	if count != 0 {
		t.Fatalf("merged %d related sites into a payload already over the ceiling", count)
	}
	if len(merged) != len(results) {
		t.Fatalf("merged payload has %d results, want the ranking's %d unchanged", len(merged), len(results))
	}
}

// TestMergeSearchRelatedSitesProtectsTheHead pins that the block is funded out of the tail only.
// searchEnclosureHeadRanks is where the snippet allocator already stops spending, because that is
// as deep as an agent reads before deciding.
func TestMergeSearchRelatedSitesProtectsTheHead(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	// Five results, all in one file, all fat: every one of them is displaceable by the file rule,
	// so only the head protection can stop the block from eating the ranking.
	results := make([]SearchResult, 0, 5)
	for index := 0; index < 5; index++ {
		results = append(results, SearchResult{
			Rank: index + 1, FilePath: "src/pkg/same.go", StartLine: index*50 + 1, EndLine: index*50 + 40,
			FocusLine: index*50 + 10, SnippetStartLine: index*50 + 10, SnippetEndLine: index*50 + 11,
			Signals: []string{"body"}, Snippet: strings.Repeat("x", 900),
		})
	}
	sites := []searchRelatedSite{mergeTestSite("alpha", 3), mergeTestSite("beta", 11)}
	merged, _ := mergeSearchRelatedSites(results, sites, fixture.reader(), 0)
	kept := 0
	for _, result := range merged {
		if result.Section == searchSectionPrimary {
			kept++
		}
	}
	if kept != len(results) {
		t.Fatalf("kept %d of %d head results: the head is never displaced", kept, len(results))
	}
}

// TestSearchRelatedDisplacementOrderKeepsTheLastMentionOfAFile pins the file-recall rule: what the
// tail is worth is the FILES it names, so a redundant fragment of an already-shown file is
// displaceable and the only mention of a file is not, at any price.
func TestSearchRelatedDisplacementOrderKeepsTheLastMentionOfAFile(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		{Rank: 1, FilePath: "a.go"}, {Rank: 2, FilePath: "b.go"}, {Rank: 3, FilePath: "c.go"},
		{Rank: 4, FilePath: "d.go"}, {Rank: 5, FilePath: "e.go"},
		{Rank: 6, FilePath: "a.go"}, // redundant: a.go is also rank 1
		{Rank: 7, FilePath: "f.go"}, // only mention
		{Rank: 8, FilePath: "a.go"}, // redundant
		{Rank: 9, FilePath: "g.go"}, // only mention
	}
	order := searchRelatedDisplacementOrder(results, searchEnclosureHeadRanks)
	want := []int{7, 5} // indices of rank 8 then rank 6, lowest-ranked redundant fragment first
	if len(order) != len(want) {
		t.Fatalf("displacement order = %v, want %v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("displacement order = %v, want %v", order, want)
		}
	}
}

func TestMergeSearchRelatedSitesKeepsFileRecall(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	// Ranks 6 and 8 are redundant fragments of the file at rank 1; ranks 7 and 9 are the only
	// mention of their files. Enough bytes are free in the redundant pair to buy the whole block.
	results := mergeTestResults(5, strings.Repeat("x", 120))
	add := func(rank int, path string, start int, size int) {
		results = append(results, SearchResult{
			Rank: rank, FilePath: path, StartLine: start, EndLine: start + 40, FocusLine: start + 5,
			SnippetStartLine: start + 5, SnippetEndLine: start + 6, Signals: []string{"body"},
			Snippet: strings.Repeat("y", size),
		})
	}
	add(6, "src/pkg/file0.go", 100, 900)
	add(7, "src/pkg/only-a.go", 1, 120)
	add(8, "src/pkg/file0.go", 200, 900)
	add(9, "src/pkg/only-b.go", 1, 120)

	merged, count := mergeSearchRelatedSites(results, []searchRelatedSite{mergeTestSite("alpha", 3), mergeTestSite("beta", 11)}, fixture.reader(), 0)
	if count == 0 {
		t.Fatalf("no related sites merged")
	}
	files := map[string]bool{}
	for _, result := range merged {
		files[result.FilePath] = true
	}
	for _, path := range []string{"src/pkg/only-a.go", "src/pkg/only-b.go"} {
		if !files[path] {
			t.Fatalf("merge dropped the only mention of %s", path)
		}
	}
}

func TestMergeSearchRelatedSitesNoOpWithoutSites(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	results := mergeTestResults(3, "snippet")
	merged, count := mergeSearchRelatedSites(results, nil, fixture.reader(), 0)
	if count != 0 || len(merged) != len(results) {
		t.Fatalf("merge with no sites changed the payload: %d sites, %d results", count, len(merged))
	}
}

// TestSearchRelatedBodySimilarityUsesTheGraphsOwnCloneBar pins that this estimator answers the
// same question SIMILAR_TO does, at the same threshold — only the estimator differs, because
// MinHash over shingles under-detects the short bodies that clone families are made of.
func TestSearchRelatedBodySimilarityUsesTheGraphsOwnCloneBar(t *testing.T) {
	t.Parallel()
	fixture := cloneFamilyFixture()
	lines := strings.Split(strings.Join(cloneFamilyFile("src/text/format.go").lines, "\n"), "\n")
	alpha := fixture.symbol(t, "alpha")
	tokens := searchRelatedBodyTokens(lines, alpha)
	if len(tokens) == 0 {
		t.Fatalf("anchor body produced no tokens")
	}
	score, similar := searchRelatedBodySimilarity(tokens, lines, fixture.symbol(t, "beta"))
	if !similar || score < similarThreshold {
		t.Fatalf("twin similarity = %.4f (similar=%v), want >= %.2f", score, similar, similarThreshold)
	}
	if _, similar := searchRelatedBodySimilarity(tokens, lines, fixture.symbol(t, "tiny")); similar {
		t.Fatalf("a body below the near-duplicate size floor must never count as a clone")
	}
}
