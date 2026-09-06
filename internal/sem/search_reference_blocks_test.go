package sem

import (
	"strings"
	"testing"
)

// searchReferenceBlockRepo is a repository shaped so that every reference block has something to say:
// a class with fields and methods, a build manifest, and a mirror test.
func searchReferenceBlockRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "pom.xml", "<project/>\n")
	writeFile(t, repo, "src/main/java/demo/Interval.java", `package demo;

public class Interval
{
  private final long start;
  private final long end;

  public Interval(long start, long end)
  {
    this.start = start;
    this.end = end;
  }

  public long span()
  {
    return end - start;
  }

  public Interval widen(long amount)
  {
    return new Interval(start - amount, end + amount);
  }
}
`)
	writeFile(t, repo, "src/test/java/demo/IntervalTest.java", `package demo;

import org.junit.Test;

public class IntervalTest
{
  @Test
  public void widenGrowsBothEnds()
  {
    Interval widened = new Interval(2, 4).widen(1);
    assertEquals(4, widened.span());
  }
}
`)
	return repo
}

func TestSearchOmitsTheReferenceBlocksByDefault(t *testing.T) {
	t.Parallel()
	repo := searchReferenceBlockRepo(t)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"widen should grow the interval at both ends",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if response.ContainerMap != nil {
		t.Fatalf("the container map is off by default: %s", RenderSearchContainerMap(response.ContainerMap, false))
	}
	if len(response.SignatureTypes) != 0 {
		t.Fatalf("the signature-type block is off by default: %d entries", len(response.SignatureTypes))
	}
	if len(response.TypeCard) != 0 {
		t.Fatalf("the declaration card is off by default: %d entries", len(response.TypeCard))
	}
	for name, bytes := range map[string]int{
		"container_map_bytes":  response.Stats.ContainerMapBytes,
		"signature_type_bytes": response.Stats.SignatureTypeBytes,
		"type_card_bytes":      response.Stats.TypeCardBytes,
	} {
		if bytes != 0 {
			t.Fatalf("%s = %d, want 0 when the block is off", name, bytes)
		}
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchReturnsEachReferenceBlockWhenAskedFor(t *testing.T) {
	t.Parallel()
	repo := searchReferenceBlockRepo(t)
	query := "widen should grow the interval at both ends"
	for _, testCase := range []struct {
		name    string
		options SearchOptions
		check   func(*testing.T, SearchResponse)
	}{
		{
			name:    "container map",
			options: SearchOptions{IncludeContainerMap: true},
			check: func(t *testing.T, response SearchResponse) {
				if response.ContainerMap == nil {
					t.Fatal("no container map when it was asked for")
				}
				if response.Stats.ContainerMapBytes == 0 {
					t.Fatal("the map was returned without being charged for")
				}
				if len(response.TypeCard) != 0 || len(response.SignatureTypes) != 0 {
					t.Fatal("asking for one block must not turn the others on")
				}
			},
		},
		{
			name:    "declaration card",
			options: SearchOptions{IncludeTypeCard: true},
			check: func(t *testing.T, response SearchResponse) {
				if len(response.TypeCard) == 0 {
					t.Fatal("no declaration card when it was asked for")
				}
				if response.ContainerMap != nil {
					t.Fatal("asking for one block must not turn the others on")
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			options := testCase.options
			options.Worktree = true
			options.Profile = ProfileFull
			options.CacheDir = t.TempDir()
			response, err := SearchRepository(t.Context(), repo, "test-version", query, options)
			if err != nil {
				t.Fatal(err)
			}
			testCase.check(t, response)
			if err := response.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchKeepsTheTestPathWhenTheReferenceBlocksAreOff(t *testing.T) {
	t.Parallel()
	repo := searchReferenceBlockRepo(t)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"widen should grow the interval at both ends",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// The test the payload identifies is not a reference block, and the verify command is derived from
	// its path, so gating the reference blocks off must not take it away.
	if response.VerifyCommand == nil {
		t.Fatalf("no verify command; results: %d", len(response.Results))
	}
	if !strings.Contains(response.VerifyCommand.Targets, "IntervalTest.java") {
		t.Fatalf("verify targets %q, want the mirror test", response.VerifyCommand.Targets)
	}
	if !strings.Contains(response.VerifyCommand.Command, "-Dtest=IntervalTest") {
		t.Fatalf("verify command %q does not narrow to the test class", response.VerifyCommand.Command)
	}
}

func TestBuildSearchContractContextGatesOnlyTheCard(t *testing.T) {
	t.Parallel()
	symbols := []SymbolRecord{
		{ID: "widen", Kind: "method", Name: "widen", QualifiedName: "Interval.widen",
			FilePath: "src/main/java/demo/Interval.java", StartLine: 20, EndLine: 23,
			Signature: "public Interval widen(long amount)", Language: "Java"},
		{ID: "test", Kind: "method", Name: "widenGrowsBothEnds", QualifiedName: "IntervalTest.widenGrowsBothEnds",
			FilePath: "src/test/java/demo/IntervalTest.java", StartLine: 7, EndLine: 11,
			Signature: "public void widenGrowsBothEnds()", Language: "Java"},
	}
	symbolsByFile := map[string][]SymbolRecord{}
	symbolsByID := map[string]SymbolRecord{}
	for _, symbol := range symbols {
		symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
		symbolsByID[symbol.ID] = symbol
	}
	files := map[string]string{
		"src/main/java/demo/Interval.java": strings.Repeat("\n", 19) +
			"  public Interval widen(long amount)\n  {\n    return this;\n  }\n",
		"src/test/java/demo/IntervalTest.java": strings.Repeat("\n", 6) +
			"  public void widenGrowsBothEnds()\n  {\n    assertEquals(4, new Interval().widen(1).span());\n  }\n",
	}
	read := func(path string) (string, bool) {
		content, ok := files[path]
		return content, ok
	}
	results := []SearchResult{{
		Rank: 1, FilePath: "src/main/java/demo/Interval.java", SymbolID: "widen",
		StartLine: 20, EndLine: 23, FocusLine: 20, SnippetStartLine: 20, SnippetEndLine: 23,
	}}
	for _, includeCard := range []bool{false, true} {
		context := buildSearchContractContext(results, []SymbolRecord{symbols[0]},
			buildSearchQuery("widen should grow the interval at both ends"),
			nil, symbolsByID, symbolsByFile, read, includeCard)
		if context.test == nil {
			t.Fatalf("includeCard=%v dropped the covering test", includeCard)
		}
		if !includeCard && len(context.card) != 0 {
			t.Fatalf("the card must be absent when it is gated off: %v", context.card)
		}
	}
}

func TestValidateRejectsMisaccountedAgentAskedBlocks(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		response SearchResponse
	}{
		{
			name: "literal cluster charged wrongly",
			response: SearchResponse{
				Results: []SearchResult{},
				LiteralCluster: &SearchLiteralCluster{Literal: "quotient", HitsTotal: 2, FilesTotal: 2,
					Hits: []SearchLiteralHit{{FilePath: "a.java", Line: 1, Role: SearchLiteralRoleEdit}}},
				Stats: SearchStats{LiteralClusterBytes: 1, ContextBlockBytes: 1},
			},
		},
		{
			name: "verify command charged wrongly",
			response: SearchResponse{
				Results:       []SearchResult{},
				VerifyCommand: &SearchVerifyCommand{Command: "go test ./x", Targets: "x", DerivedFrom: "go.mod"},
				Stats:         SearchStats{VerifyCommandBytes: 1, ContextBlockBytes: 1},
			},
		},
		{
			name: "closed set charged wrongly",
			response: SearchResponse{
				Results: []SearchResult{},
				ClosedSet: &SearchClosedSet{Type: "Ops", Kind: "enum", Variants: 3, Warning: "w",
					Sites: []SearchClosedSetSite{{FilePath: "a.java", Line: 1, Arms: 3}}},
				Stats: SearchStats{ClosedSetBytes: 1, ContextBlockBytes: 1},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := testCase.response
			response.Stats.ResultBytes = serializedSearchResultBytes(response.Results)
			if err := response.Validate(); err == nil {
				t.Fatal("a block whose reported cost is wrong must not validate")
			}
		})
	}
}
