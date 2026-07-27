package sem

import (
	"strings"
	"testing"
)

// TestParameterListBounds pins the "last balanced parenthesis group" rule. Taking the FIRST
// parenthesis instead reported a Java annotation's argument, a Go receiver, or a decorator call as
// the member's parameters — so a no-argument getter came back as taking a String.
func TestParameterListBounds(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		signature string
		want      string
	}{
		{name: "no parentheses at all", signature: "class Outer", want: ""},
		{name: "plain no-arg method", signature: "public String getName()", want: "()"},
		{
			name:      "annotation before a no-arg getter",
			signature: `@JsonProperty("fn") public String getFnName()`,
			want:      "()",
		},
		{
			name:      "two annotations before a no-arg getter",
			signature: `@JsonProperty("ordering") @JsonInclude(Include.NON_NULL) public String getOrdering()`,
			want:      "()",
		},
		{name: "go receiver before the parameter list", signature: "func (o *Outer) Run() int", want: "()"},
		{name: "arguments after an annotation", signature: `@Override public int compare(Double lhs, Double rhs)`, want: "Double lhs, Double rhs"},
		{name: "generic argument containing a comma", signature: "public Object compute(Map<String, Object> values)", want: "Map<String, Object> values"},
		{name: "trailing return type", signature: "fun run(seed: Int): Int", want: "seed: Int"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			open, close, ok := parameterListBounds(testCase.signature)
			if testCase.want == "" {
				if ok {
					t.Fatalf("expected no parameter list, got %q", testCase.signature[open+1:close])
				}
				return
			}
			if !ok {
				t.Fatalf("no parameter list found in %q", testCase.signature)
			}
			got := testCase.signature[open+1 : close]
			if testCase.want == "()" {
				got = "(" + got + ")"
			}
			if got != testCase.want {
				t.Fatalf("parameter list = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestCompactParameterList pins the compaction: the tokens that IDENTIFY a member survive, the
// rest does not, and a list too wide to identify anything degrades to an arity.
func TestCompactParameterList(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		signature string
		want      string
	}{
		{name: "no signature", signature: "", want: ""},
		{name: "no arguments", signature: "public String getName()", want: "()"},
		{name: "java type and name", signature: "private static boolean preserveFieldOrderInCacheKey(Ops op)", want: "(Ops)"},
		{name: "generic type collapses to its head", signature: "public Object compute(Map<String, Object> values)", want: "(Map)"},
		{name: "two generic parameters", signature: "void put(List<Ops> ops, Map<String, Ops> lookup)", want: "(List,Map)"},
		{name: "typescript annotated names", signature: "tick(seed: number, label: string): void", want: "(number,string)"},
		{name: "python untyped names", signature: "def assist(self, seed)", want: "(self,seed)"},
		{name: "defaults are dropped", signature: "def assist(self, seed=1)", want: "(self,seed)"},
		{name: "qualified type keeps its last component", signature: "void handle(com.example.Event e)", want: "(Event)"},
		{name: "pointer receivers lose their sigil", signature: "void write(char *buffer)", want: "(char)"},
		{
			name:      "four short types still fit",
			signature: "public ArithmeticPostAggregator(String name, String fnName, List<PostAggregator> fields, String ordering)",
			want:      "(String,String,List,String)",
		},
		{
			name:      "a list too wide to identify anything degrades to an arity",
			signature: "void configure(AggregatorFactory factory, ColumnInspector inspector, CacheKeyBuilder builder, PostAggregator parent)",
			want:      "(4 args)",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := compactParameterList(testCase.signature); got != testCase.want {
				t.Fatalf("compactParameterList(%q) = %q, want %q", testCase.signature, got, testCase.want)
			}
		})
	}
}

// TestDeclaredFieldType pins the by-elimination type extraction, which has to cope with parsers
// that emit `name Type` and parsers that emit `private final Type name`.
func TestDeclaredFieldType(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		symbol SymbolRecord
		want   string
	}{
		{name: "no signature", symbol: SymbolRecord{Name: "op"}, want: ""},
		{name: "name then type", symbol: SymbolRecord{Name: "op", Signature: "op Ops"}, want: "Ops"},
		{name: "generic is reduced to its head", symbol: SymbolRecord{Name: "fields", Signature: "fields List<PostAggregator>"}, want: "List"},
		{name: "modifiers then type then name", symbol: SymbolRecord{Name: "op", Signature: "private final Ops op;"}, want: "Ops"},
		{name: "annotated type name", symbol: SymbolRecord{Name: "seed", Signature: "seed: number"}, want: "number"},
		{name: "initializer only", symbol: SymbolRecord{Name: "seed", Signature: "seed = 1"}, want: ""},
		{name: "keywords alone are not a type", symbol: SymbolRecord{Name: "seed", Signature: "public static final seed"}, want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := declaredFieldType(testCase.symbol); got != testCase.want {
				t.Fatalf("declaredFieldType(%q) = %q, want %q", testCase.symbol.Signature, got, testCase.want)
			}
		})
	}
}

// TestSearchContainerMemberFlags pins the flag derivation, including the whole-word modifier match
// that keeps `internalise()` from reading as `internal`.
func TestSearchContainerMemberFlags(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		symbol SymbolRecord
		want   []string
	}{
		{
			name:   "private nested enum reports both facts",
			symbol: SymbolRecord{Kind: "enum", Name: "Ops", Signature: "private enum Ops", Language: "Java"},
			want:   []string{"NESTED", "PRIVATE"},
		},
		{
			name:   "public nested class reports only nesting",
			symbol: SymbolRecord{Kind: "class", Name: "Ordering", Signature: "public enum Ordering", Language: "Java"},
			want:   []string{"NESTED"},
		},
		{
			name:   "private static method",
			symbol: SymbolRecord{Kind: "method", Name: "f", Signature: "private static boolean f(Ops op)", Language: "Java"},
			want:   []string{"PRIVATE", "static"},
		},
		{
			name:   "plain method has no flags",
			symbol: SymbolRecord{Kind: "method", Name: "compute", Signature: "public Object compute(Map values)", Language: "Java"},
			want:   nil,
		},
		{
			name:   "a modifier word inside an identifier is not a modifier",
			symbol: SymbolRecord{Kind: "method", Name: "internalise", Signature: "public void internalise()", Language: "Java"},
			want:   nil,
		},
		{
			name:   "python underscore convention",
			symbol: SymbolRecord{Kind: "method", Name: "_assist", Signature: "def _assist(self)", Language: "Python"},
			want:   []string{"PRIVATE"},
		},
		{
			name:   "go unexported convention",
			symbol: SymbolRecord{Kind: "function", Name: "factory", Signature: "func factory() int", Language: "Go"},
			want:   []string{"PRIVATE"},
		},
		{
			name:   "go exported name is not private",
			symbol: SymbolRecord{Kind: "function", Name: "Factory", Signature: "func Factory() int", Language: "Go"},
			want:   nil,
		},
		{
			name:   "at most two flags are kept",
			symbol: SymbolRecord{Kind: "class", Name: "Helper", Signature: "private abstract static class Helper", Language: "Java"},
			want:   []string{"NESTED", "PRIVATE"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := searchContainerMemberFlags(testCase.symbol)
			if strings.Join(got, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("flags = %v, want %v", got, testCase.want)
			}
		})
	}
}

// arithmeticFixture reproduces the shape the container map exists for: a class whose fix site is a
// PRIVATE NESTED enum two lines past the end of the snippet a member-level hit returns.
func arithmeticFixture() []SymbolRecord {
	class := SymbolRecord{
		ID: "c", Kind: "class", Name: "Agg", QualifiedName: "Agg",
		FilePath: "Agg.java", StartLine: 10, EndLine: 100, Signature: "public class Agg", Language: "Java",
	}
	member := func(id, kind, name, signature string, start, end int) SymbolRecord {
		return SymbolRecord{
			ID: id, Kind: kind, Name: name, QualifiedName: "Agg." + name, ContainerID: "c",
			FilePath: "Agg.java", StartLine: start, EndLine: end, Signature: signature, Language: "Java",
		}
	}
	return []SymbolRecord{
		class,
		member("f1", "field", "op", "op Ops", 12, 12),
		member("f2", "field", "name", "name String", 13, 13),
		member("m1", "method", "Agg", "public Agg(String name)", 15, 20),
		member("m2", "method", "Agg", "public Agg(String name, String fn)", 21, 28),
		member("m3", "method", "compute", "public Object compute(Map<String, Object> values)", 30, 44),
		member("m4", "method", "preserve", "private static boolean preserve(Ops op)", 46, 58),
		{
			ID: "e1", Kind: "enum", Name: "Ops", QualifiedName: "Ops", ContainerID: "c",
			FilePath: "Agg.java", StartLine: 60, EndLine: 90, Signature: "private enum Ops", Language: "Java",
		},
		{
			ID: "e1a", Kind: "method", Name: "lookup", QualifiedName: "Ops.lookup", ContainerID: "e1",
			FilePath: "Agg.java", StartLine: 80, EndLine: 84, Signature: "static Ops lookup(String fn)", Language: "Java",
		},
		{
			ID: "e1b", Kind: "method", Name: "getFns", QualifiedName: "Ops.getFns", ContainerID: "e1",
			FilePath: "Agg.java", StartLine: 86, EndLine: 89, Signature: "static Set<String> getFns()", Language: "Java",
		},
		member("m5", "method", "equals", "public boolean equals(Object o)", 92, 99),
	}
}

func fixtureContentOfLines(count int) string {
	return strings.Repeat("x\n", count)
}

func fixtureReader(path, content string) contentReader {
	return func(want string) (string, bool) {
		if want != path {
			return "", false
		}
		return content, true
	}
}

// TestBuildSearchContainerMap covers what the block has to get right for the session it comes
// from: the file extent, the container identity, the member list with ranges, the marked hit, and
// the nested type reported as nested, private and non-empty.
func TestBuildSearchContainerMap(t *testing.T) {
	t.Parallel()
	symbols := arithmeticFixture()
	byID := map[string]SymbolRecord{}
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	byFile := map[string][]SymbolRecord{"Agg.java": symbols}
	read := fixtureReader("Agg.java", fixtureContentOfLines(101))

	for _, testCase := range []struct {
		name      string
		results   []SearchResult
		wantNil   bool
		wantName  string
		wantHit   string
		wantLines int
		check     func(t *testing.T, containerMap *SearchContainerMap)
	}{
		{
			name: "member hit maps the enclosing class and marks the member",
			results: []SearchResult{{
				Rank: 1, FilePath: "Agg.java", StartLine: 46, EndLine: 58, FocusLine: 50, SymbolID: "m4",
			}},
			wantName:  "Agg",
			wantHit:   "preserve",
			wantLines: 101,
			check: func(t *testing.T, containerMap *SearchContainerMap) {
				nested, ok := containerMapMember(containerMap, "Ops")
				if !ok {
					t.Fatal("the nested enum Ops is missing from the map")
				}
				if nested.StartLine != 60 || nested.EndLine != 90 {
					t.Fatalf("Ops range = %d-%d, want 60-90", nested.StartLine, nested.EndLine)
				}
				if nested.Kind != "enum" {
					t.Fatalf("Ops kind = %q, want enum", nested.Kind)
				}
				if strings.Join(nested.Flags, ",") != "NESTED,PRIVATE" {
					t.Fatalf("Ops flags = %v, want [NESTED PRIVATE]", nested.Flags)
				}
				if nested.Members != 2 {
					t.Fatalf("Ops member count = %d, want 2", nested.Members)
				}
				if want := []string{"op:Ops", "name:String"}; strings.Join(containerMap.Fields, ",") != strings.Join(want, ",") {
					t.Fatalf("fields = %v, want %v", containerMap.Fields, want)
				}
			},
		},
		{
			name: "adjacent overloads collapse into one row",
			results: []SearchResult{{
				Rank: 1, FilePath: "Agg.java", StartLine: 30, EndLine: 44, FocusLine: 33, SymbolID: "m3",
			}},
			wantName: "Agg",
			wantHit:  "compute",
			check: func(t *testing.T, containerMap *SearchContainerMap) {
				row, ok := containerMapMember(containerMap, "Agg")
				if !ok {
					t.Fatal("constructor row missing")
				}
				if row.Overloads != 2 {
					t.Fatalf("constructor overloads = %d, want 2", row.Overloads)
				}
				if row.StartLine != 15 || row.EndLine != 28 {
					t.Fatalf("constructor range = %d-%d, want 15-28", row.StartLine, row.EndLine)
				}
				if row.Params != "" {
					t.Fatalf("an overload set must not claim one member's parameters, got %q", row.Params)
				}
			},
		},
		{
			name: "a hit inside the nested enum maps the nested enum",
			results: []SearchResult{{
				Rank: 1, FilePath: "Agg.java", StartLine: 80, EndLine: 84, FocusLine: 81, SymbolID: "e1a",
			}},
			wantName: "Ops",
			wantHit:  "lookup",
		},
		{
			name: "a hit on the container itself maps that container and marks nothing",
			results: []SearchResult{{
				Rank: 1, FilePath: "Agg.java", StartLine: 10, EndLine: 30, FocusLine: 11, SymbolID: "c",
			}},
			wantName: "Agg",
			wantHit:  "",
		},
		{
			name: "a related-section hit is skipped in favour of the primary one",
			results: []SearchResult{
				{Rank: 1, FilePath: "Agg.java", StartLine: 80, EndLine: 84, FocusLine: 81, SymbolID: "e1a", Section: searchSectionRelated},
				{Rank: 2, FilePath: "Agg.java", StartLine: 46, EndLine: 58, FocusLine: 50, SymbolID: "m4"},
			},
			wantName: "Agg",
			wantHit:  "preserve",
			check: func(t *testing.T, containerMap *SearchContainerMap) {
				if containerMap.AnchorRank != 2 {
					t.Fatalf("anchor rank = %d, want 2", containerMap.AnchorRank)
				}
			},
		},
		{
			name:    "no results, no map",
			results: nil,
			wantNil: true,
		},
		{
			name: "unknown file, no map",
			results: []SearchResult{{
				Rank: 1, FilePath: "Other.java", StartLine: 1, EndLine: 2, FocusLine: 1,
			}},
			wantNil: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := buildSearchContainerMap(testCase.results, byID, byFile, read)
			if testCase.wantNil {
				if got != nil {
					t.Fatalf("expected no map, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a map, got none")
			}
			if got.Name != testCase.wantName {
				t.Fatalf("container = %q, want %q", got.Name, testCase.wantName)
			}
			if testCase.wantLines != 0 && got.FileLines != testCase.wantLines {
				t.Fatalf("file_lines = %d, want %d", got.FileLines, testCase.wantLines)
			}
			hit := ""
			for _, member := range got.Members {
				if member.Hit {
					if hit != "" {
						t.Fatalf("more than one member marked as the hit: %s and %s", hit, member.Name)
					}
					hit = member.Name
				}
			}
			if hit != testCase.wantHit {
				t.Fatalf("marked hit = %q, want %q", hit, testCase.wantHit)
			}
			if testCase.check != nil {
				testCase.check(t, got)
			}
		})
	}
}

// TestSearchContainerMapPrintsNoSource is the "print each region once" rule, enforced against the
// map instead of trusted: no line of the file may appear in the block.
func TestSearchContainerMapPrintsNoSource(t *testing.T) {
	t.Parallel()
	symbols := arithmeticFixture()
	byID := map[string]SymbolRecord{}
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	source := strings.Join([]string{
		"package demo;", "public class Agg {", "  private final Ops op = null;",
		"  private static boolean preserve(Ops op) { return true; }", "}",
	}, "\n")
	containerMap := buildSearchContainerMap(
		[]SearchResult{{Rank: 1, FilePath: "Agg.java", StartLine: 46, EndLine: 58, FocusLine: 50, SymbolID: "m4"}},
		byID, map[string][]SymbolRecord{"Agg.java": symbols}, fixtureReader("Agg.java", source),
	)
	if containerMap == nil {
		t.Fatal("expected a map")
	}
	rendered := string(RenderSearchContainerMap(containerMap, false))
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 12 {
			continue
		}
		if strings.Contains(rendered, trimmed) {
			t.Fatalf("the map reprinted a source line:\n%s\nin\n%s", trimmed, rendered)
		}
	}
}

// TestCapSearchContainerMembers pins the truncation priority: the hit survives, nested containers
// survive (they are the landmarks the map exists to surface), and what is left is chosen by
// distance from the hit — with the survivors still in source order and the loss reported.
func TestCapSearchContainerMembers(t *testing.T) {
	t.Parallel()
	rows := []SearchContainerMember{
		{Name: "a", StartLine: 1, EndLine: 2},
		{Name: "nested1", Kind: "enum", StartLine: 4, EndLine: 8, Members: 3},
		{Name: "b", StartLine: 10, EndLine: 12},
		{Name: "nested2", Kind: "class", StartLine: 20, EndLine: 30, Members: 4},
		{Name: "c", StartLine: 40, EndLine: 42},
		{Name: "hit", StartLine: 50, EndLine: 55, Hit: true},
		{Name: "d", StartLine: 60, EndLine: 62},
	}
	all := []string{"a", "nested1", "b", "nested2", "c", "hit", "d"}
	for _, testCase := range []struct {
		name        string
		limit       int
		want        []string
		wantOmitted int
	}{
		{name: "no cap", limit: 0, want: all},
		{name: "cap above the row count", limit: 10, want: all},
		{
			// The single most important row is the one the agent is standing on, so it must
			// outrank even a nested container.
			name: "the hit outranks every other row", limit: 1, want: []string{"hit"}, wantOmitted: 6,
		},
		{name: "then the nested containers", limit: 3, want: []string{"nested1", "nested2", "hit"}, wantOmitted: 4},
		{name: "then the nearest neighbours", limit: 5, want: []string{"nested1", "nested2", "c", "hit", "d"}, wantOmitted: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, omitted := capSearchContainerMembers(rows, testCase.limit)
			names := make([]string, 0, len(got))
			for _, row := range got {
				names = append(names, row.Name)
			}
			if strings.Join(names, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("kept %v, want %v", names, testCase.want)
			}
			if omitted != testCase.wantOmitted {
				t.Fatalf("omitted = %d, want %d", omitted, testCase.wantOmitted)
			}
			for index := 1; index < len(got); index++ {
				if got[index-1].StartLine > got[index].StartLine {
					t.Fatalf("survivors are not in source order: %v", names)
				}
			}
		})
	}
}

// TestFitSearchContainerMap pins the degradation ladder and its floor: parameter lists go before
// flags, flags before rows, and the block is dropped entirely rather than exceeding its budget.
func TestFitSearchContainerMap(t *testing.T) {
	t.Parallel()
	base := func() *SearchContainerMap {
		return &SearchContainerMap{
			FilePath: "Agg.java", FileLines: 101, Kind: "class", Name: "Agg", StartLine: 10, EndLine: 100,
			Members: []SearchContainerMember{
				{Name: "compute", StartLine: 30, EndLine: 44, Params: "(Map)"},
				{Name: "preserve", StartLine: 46, EndLine: 58, Params: "(Ops)", Flags: []string{"PRIVATE", "static"}, Hit: true},
				{Name: "Ops", Kind: "enum", StartLine: 60, EndLine: 90, Flags: []string{"NESTED", "PRIVATE"}, Members: 2},
				{Name: "equals", StartLine: 92, EndLine: 99, Params: "(Object)"},
			},
		}
	}
	full := searchContainerMapCost(base())
	for _, testCase := range []struct {
		name         string
		budget       int
		wantNil      bool
		wantParams   bool
		wantFlags    bool
		wantMembers  int
		wantTruncNot bool
	}{
		{name: "budget clears the full block", budget: full, wantParams: true, wantFlags: true, wantMembers: 4},
		{name: "params go first", budget: full - 1, wantParams: false, wantFlags: true, wantMembers: 4},
		{name: "a budget of one byte drops the block", budget: 1, wantNil: true},
		{name: "a zero budget disables fitting", budget: 0, wantParams: true, wantFlags: true, wantMembers: 4},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := fitSearchContainerMap(base(), testCase.budget)
			if testCase.wantNil {
				if got != nil {
					t.Fatalf("expected the block to be dropped, got %d bytes", searchContainerMapCost(got))
				}
				return
			}
			if got == nil {
				t.Fatal("expected a map")
			}
			if testCase.budget > 0 && searchContainerMapCost(got) > testCase.budget {
				t.Fatalf("fitted map costs %d bytes, over the %d budget", searchContainerMapCost(got), testCase.budget)
			}
			if len(got.Members) != testCase.wantMembers {
				t.Fatalf("members = %d, want %d", len(got.Members), testCase.wantMembers)
			}
			hasParams, hasFlags := false, false
			for _, member := range got.Members {
				hasParams = hasParams || member.Params != ""
				hasFlags = hasFlags || len(member.Flags) > 0
			}
			if hasParams != testCase.wantParams {
				t.Fatalf("params present = %v, want %v", hasParams, testCase.wantParams)
			}
			if hasFlags != testCase.wantFlags {
				t.Fatalf("flags present = %v, want %v", hasFlags, testCase.wantFlags)
			}
			// Whatever survives, the two facts a range read is sized from must survive with it.
			if got.FileLines != 101 {
				t.Fatalf("file extent lost: %d", got.FileLines)
			}
			for _, member := range got.Members {
				if member.StartLine == 0 || member.EndLine == 0 {
					t.Fatalf("member %s lost its range", member.Name)
				}
			}
		})
	}
}

// TestRenderSearchContainerMap pins the rendered shape, including that a truncated block says so.
func TestRenderSearchContainerMap(t *testing.T) {
	t.Parallel()
	containerMap := &SearchContainerMap{
		FilePath: "Agg.java", FileLines: 353, Kind: "class", Name: "Agg", StartLine: 49, EndLine: 353,
		AnchorRank: 1, Fields: []string{"op:Ops"}, FieldsOmitted: 3,
		Members: []SearchContainerMember{
			{Name: "Agg", StartLine: 60, EndLine: 91, Overloads: 2},
			{Name: "preserve", StartLine: 198, EndLine: 211, Params: "(Ops)", Flags: []string{"PRIVATE", "static"}, Hit: true},
			{Name: "Ops", Kind: "enum", StartLine: 213, EndLine: 282, Flags: []string{"NESTED", "PRIVATE"}, Members: 12},
		},
		MembersOmitted: 4,
	}
	for _, testCase := range []struct {
		name    string
		compact bool
		want    []string
		absent  []string
	}{
		{
			name: "full form",
			want: []string{
				"CONTAINER MAP Agg.java [353 lines] (class Agg, 49-353)",
				"fields: op:Ops, +3 more",
				"60-91 Agg x2",
				"* 198-211 preserve(Ops)  PRIVATE,static   <- rank 1",
				"213-282 enum Ops  NESTED,PRIVATE  +12 members",
				"+4 more members",
			},
		},
		{
			name:    "compact form keeps names and ranges",
			compact: true,
			want: []string{
				"CONTAINER MAP Agg.java [353 lines] (class Agg, 49-353)",
				"* 198-211 preserve   <- rank 1",
				"213-282 enum Ops",
			},
			absent: []string{"PRIVATE", "(Ops)", "+12 members"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := string(RenderSearchContainerMap(containerMap, testCase.compact))
			for _, want := range testCase.want {
				if !strings.Contains(got, want) {
					t.Fatalf("rendered map missing %q:\n%s", want, got)
				}
			}
			for _, absent := range testCase.absent {
				if strings.Contains(got, absent) {
					t.Fatalf("compact map should not contain %q:\n%s", absent, got)
				}
			}
		})
	}
	if RenderSearchContainerMap(nil, false) != nil {
		t.Fatal("a nil map must render nothing")
	}
	if RenderSearchContainerMap(&SearchContainerMap{FilePath: "x"}, false) != nil {
		t.Fatal("a memberless map must render nothing")
	}
}

// TestSearchReportsContainerMapForTopHit is the end-to-end assertion, through a real index: the
// FIRST response for a member-level hit already carries the file extent, the enclosing container's
// member list, and the private nested enum's exact line range.
func TestSearchReportsContainerMapForTopHit(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "Agg.java", `package demo;

public class Agg
{
  private final Ops op;

  public Agg(String fn)
  {
    this.op = Ops.lookup(fn);
  }

  public double compute(double lhs, double rhs)
  {
    return op.compute(lhs, rhs);
  }

  private static boolean preserveFieldOrder(Ops op)
  {
    switch (op) {
      case PLUS:
        return false;
      default:
        return true;
    }
  }

  private enum Ops
  {
    PLUS("+"),
    MINUS("-");

    private final String fn;

    Ops(String fn)
    {
      this.fn = fn;
    }

    static Ops lookup(String fn)
    {
      return PLUS;
    }
  }
}
`)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"preserve field order in the cache key for the arithmetic operation",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	containerMap := response.ContainerMap
	if containerMap == nil {
		t.Fatalf("no container map in the response; results: %d", len(response.Results))
	}
	if containerMap.FileLines != 44 {
		t.Fatalf("file_lines = %d, want 44", containerMap.FileLines)
	}
	nested, ok := containerMapMember(containerMap, "Ops")
	if !ok {
		t.Fatalf("the private nested enum is absent from the map: %s", RenderSearchContainerMap(containerMap, false))
	}
	if nested.StartLine != 27 || nested.EndLine != 43 {
		t.Fatalf("Ops range = %d-%d, want 27-43", nested.StartLine, nested.EndLine)
	}
	if strings.Join(nested.Flags, ",") != "NESTED,PRIVATE" {
		t.Fatalf("Ops flags = %v, want [NESTED PRIVATE]", nested.Flags)
	}
	if response.Stats.ContainerMapBytes <= 0 {
		t.Fatal("container map cost is not reported in stats")
	}
	if cost := searchContainerMapCost(containerMap); cost > searchContainerMapMaxBytes {
		t.Fatalf("container map costs %d bytes, over the %d ceiling", cost, searchContainerMapMaxBytes)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestSearchContainerMapRespectsCeilingOnWideContainer is the guard that the block cannot grow
// into the cost it exists to prevent. A wide container is the case that would blow the budget, so
// the ceiling is asserted on one built by a real search rather than on a hand-made map.
func TestSearchContainerMapRespectsCeilingOnWideContainer(t *testing.T) {
	t.Parallel()
	var source strings.Builder
	source.WriteString("package demo;\n\npublic class WideAggregator\n{\n")
	for index := 0; index < 40; index++ {
		source.WriteString("  public AggregatorFactory decorateAggregatorNumber" + string(rune('a'+index%26)) +
			"(AggregatorFactory factory, ColumnInspector inspector)\n  {\n    return factory;\n  }\n\n")
	}
	source.WriteString("  private enum Ops\n  {\n    PLUS;\n  }\n}\n")
	repo := t.TempDir()
	writeFile(t, repo, "WideAggregator.java", source.String())

	response, err := SearchRepository(t.Context(), repo, "test-version",
		"decorate the aggregator factory with the column inspector",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	containerMap := response.ContainerMap
	if containerMap == nil {
		t.Fatalf("no container map for a wide container; results: %d", len(response.Results))
	}
	cost := searchContainerMapCost(containerMap)
	if cost > searchContainerMapMaxBytes {
		t.Fatalf("wide container map costs %d bytes, over the %d ceiling:\n%s",
			cost, searchContainerMapMaxBytes, RenderSearchContainerMap(containerMap, false))
	}
	// Truncation has to be visible, and the two facts a range read is sized from have to survive.
	if len(containerMap.Members) >= 41 {
		t.Fatalf("a 41-member container was not capped at all: %d rows", len(containerMap.Members))
	}
	if containerMap.MembersOmitted == 0 {
		t.Fatal("a truncated map must report how many members it dropped")
	}
	if containerMap.FileLines == 0 {
		t.Fatal("the file extent must survive truncation")
	}
}

func containerMapMember(containerMap *SearchContainerMap, name string) (SearchContainerMember, bool) {
	for _, member := range containerMap.Members {
		if member.Name == name {
			return member, true
		}
	}
	return SearchContainerMember{}, false
}
