package sem

import (
	"strconv"
	"strings"
	"testing"
)

func TestSearchLiteralCandidatesExtractsTheThreeShapes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		line string
		want []string
	}{
		{name: "quoted enum value", line: `    QUOT("quotient"),`, want: []string{"quotient", "QUOT"}},
		{name: "screaming constant", line: `  public static final String CACHE_KEY = "k";`, want: []string{"CACHE_KEY"}},
		{name: "compound identifier", line: `func redirectFixedPath(w http.ResponseWriter) {`, want: []string{"redirectFixedPath", "ResponseWriter"}},
		{name: "snake identifier", line: `  read_bytes_limit_per_second = 0`, want: []string{"read_bytes_limit_per_second"}},
		{name: "single lowercase word is not a name", line: `  return bind`, want: nil},
		{name: "format punctuation is not a name", line: `  printf("%s: %d\n", a, b);`, want: nil},
		{name: "a quoted path is not a concept", line: `#![doc = include_str!("../docs/routing/merge.md")]`, want: []string{"include_str"}},
		{name: "trailing underscore is a visibility marker", line: `  private_ = 1;`, want: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := searchLiteralCandidates([]string{testCase.line})
			if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
				t.Fatalf("candidates = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestRankSearchLiteralCandidatesRequiresQueryOverlap(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		query      string
		candidates []string
		want       []string
	}{
		{
			name:       "a query word inside the literal qualifies",
			query:      "default bind address is ignored",
			candidates: []string{"default_bind", "unrelatedThing"},
			want:       []string{"default_bind"},
		},
		{
			name:       "exact equality with a query word ranks above containment",
			query:      "quotient operator missing",
			candidates: []string{"quotientOfTwo", "quotient"},
			want:       []string{"quotient", "quotientOfTwo"},
		},
		{
			name:       "a literal wider than the query word is still keyed on that word",
			query:      "bitcount subcommand range",
			candidates: []string{"BITCOUNT"},
			want:       []string{"BITCOUNT"},
		},
		{
			// Containment must hold in one direction only. `binding` is not inside `bind`, so the
			// query term's posting list would not be a superset of the literal's occurrences and the
			// repository-wide total could not be trusted.
			name:       "the query word must be INSIDE the literal, not around it",
			query:      "binding is ignored",
			candidates: []string{"bind"},
			want:       nil,
		},
		{
			name:       "a multi-word literal needs two query words",
			query:      "unknown operator crash",
			candidates: []string{"unknown post aggregator"},
			want:       nil,
		},
		{
			name:       "two query words license a multi-word literal",
			query:      "unknown aggregator in the registry",
			candidates: []string{"unknown aggregator type"},
			want:       []string{"unknown aggregator type"},
		},
		{
			name:       "short query words cannot license anything",
			query:      "the box is red",
			candidates: []string{"redBoxThing"},
			want:       nil,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := rankSearchLiteralCandidates(testCase.candidates, buildSearchQuery(testCase.query))
			if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
				t.Fatalf("ranked = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestSearchLiteralRoleSeparatesDefinitionFromUse(t *testing.T) {
	t.Parallel()
	symbols := []SymbolRecord{
		{ID: "enum", Kind: "enum", Name: "Ops", QualifiedName: "Ops", StartLine: 2, EndLine: 20},
		{ID: "method", Kind: "method", Name: "compute", QualifiedName: "Ops.compute", StartLine: 10, EndLine: 18},
	}
	for _, testCase := range []struct {
		name           string
		line           int
		indented       bool
		wantSymbol     string
		wantRole       string
		wantClassified bool
	}{
		{
			name: "inside the enum but outside any body", line: 4,
			wantSymbol: "Ops", wantRole: SearchLiteralRoleEdit, wantClassified: true,
		},
		{
			name: "inside a callable body", line: 12,
			wantSymbol: "Ops.compute", wantRole: SearchLiteralRoleConsumer, wantClassified: true,
		},
		{
			name: "an unindented line outside every symbol is a top-level declaration", line: 1,
			wantRole: SearchLiteralRoleEdit, wantClassified: true,
		},
		{
			// The careful case: indented and covered by nothing means the parser missed whatever
			// encloses it, so no role can be claimed.
			name: "an indented line outside every symbol cannot be classified", line: 1, indented: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			name, role, classified := searchLiteralRole(symbols,
				searchNeedleHit{line: testCase.line, indented: testCase.indented})
			if classified != testCase.wantClassified {
				t.Fatalf("classified = %v, want %v", classified, testCase.wantClassified)
			}
			if name != testCase.wantSymbol || role != testCase.wantRole {
				t.Fatalf("role(%d) = %q/%q, want %q/%q", testCase.line, name, role,
					testCase.wantSymbol, testCase.wantRole)
			}
		})
	}
}

func TestSearchLiteralHitAlreadyPrintedSkipsWhatTheReaderCanSee(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		{FilePath: "a.go", SnippetStartLine: 10, SnippetEndLine: 20},
		{FilePath: "b.go", SnippetStartLine: 0, SnippetEndLine: 0},
	}
	for _, testCase := range []struct {
		name string
		hit  searchNeedleHit
		want bool
	}{
		{name: "inside a printed snippet", hit: searchNeedleHit{filePath: "a.go", line: 12}, want: true},
		{name: "same file, outside the snippet", hit: searchNeedleHit{filePath: "a.go", line: 40}},
		{name: "another file", hit: searchNeedleHit{filePath: "c.go", line: 12}},
		{name: "a result that printed nothing hides nothing", hit: searchNeedleHit{filePath: "b.go", line: 1}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchLiteralHitAlreadyPrinted(results, testCase.hit); got != testCase.want {
				t.Fatalf("alreadyPrinted = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestClassifySearchLiteralHitsAnnotatesAndDeduplicates(t *testing.T) {
	t.Parallel()
	symbolsByFile := map[string][]SymbolRecord{
		"Ops.java": {
			{ID: "enum", Kind: "enum", Name: "Ops", QualifiedName: "Ops", StartLine: 3, EndLine: 14},
		},
		"AvgAggregator.java": {
			{ID: "class", Kind: "class", Name: "AvgAggregator", QualifiedName: "AvgAggregator",
				StartLine: 3, EndLine: 40},
			{ID: "render", Kind: "method", Name: "render", QualifiedName: "AvgAggregator.render",
				StartLine: 5, EndLine: 8},
		},
	}
	// The payload has already printed AvgAggregator.java:5-8.
	results := []SearchResult{
		{FilePath: "AvgAggregator.java", SnippetStartLine: 5, SnippetEndLine: 8},
	}
	scan := searchNeedleScan{
		occurrences: 9, files: 4, complete: true,
		hits: []searchNeedleHit{
			{filePath: "Ops.java", line: 6, indented: true},
			{filePath: "AvgAggregator.java", line: 7, indented: true},
			{filePath: "AvgAggregator.java", line: 30, indented: true},
			{filePath: "docs/aggregations.md", line: 39},
			{filePath: "generated/table.bin.go", line: 12, indented: true},
			{filePath: "registry.go", line: 3},
		},
	}
	cluster := classifySearchLiteralHits("quotient", scan, results, symbolsByFile)
	if cluster == nil {
		t.Fatal("expected a cluster")
	}
	// The repository totals are the scan's, never the sample's.
	if cluster.HitsTotal != 9 || cluster.FilesTotal != 4 {
		t.Fatalf("totals = %d/%d, want 9/4", cluster.HitsTotal, cluster.FilesTotal)
	}
	got := map[string]string{}
	for _, hit := range cluster.Hits {
		got[hit.FilePath] = hit.Role + ":" + hit.Symbol
	}
	want := map[string]string{
		// Inside the enum but outside any body: a declaration position.
		"Ops.java": SearchLiteralRoleEdit + ":Ops",
		// Line 7 is inside a printed snippet and is not repeated; line 30 is inside the class but
		// outside every method, so it is a declaration position too.
		"AvgAggregator.java":   SearchLiteralRoleEdit + ":AvgAggregator",
		"docs/aggregations.md": SearchLiteralRoleDoc + ":",
		// Unindented, program text, no symbol covers it: a top-level declaration.
		"registry.go": SearchLiteralRoleEdit + ":",
	}
	for path, expected := range want {
		if got[path] != expected {
			t.Fatalf("hit(%s) = %q, want %q (cluster: %s)", path, got[path], expected,
				RenderSearchLiteralCluster(cluster))
		}
	}
	// Indented, no symbol for the file: no role can be claimed, so the FILE is counted and not listed.
	if _, listed := got["generated/table.bin.go"]; listed {
		t.Fatalf("an unclassifiable site was listed: %s", RenderSearchLiteralCluster(cluster))
	}
	if cluster.Unclassified != 1 {
		t.Fatalf("unclassified = %d, want 1", cluster.Unclassified)
	}
}

func TestSearchLiteralClusterListsTheSitesThePayloadDidNotPrintEndToEnd(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "Ops.java", `package demo;

public enum Ops
{
  PLUS("plus"),
  QUOT("quotient");
}
`)
	// More files name the concept than the ranking has room for, which is the situation the block
	// exists for: the sites past the last rank are exactly the ones an agent would grep to find.
	for index := 0; index < 14; index++ {
		writeFile(t, repo, "handlers/Handler"+strconv.Itoa(index)+".java", `package demo;

public class Handler`+strconv.Itoa(index)+`
{
  public String render()
  {
    return "quotient";
  }
}
`)
	}
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"the quotient post-aggregator is missing from the registry",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cluster := response.LiteralCluster
	if cluster == nil {
		t.Fatalf("no literal cluster; results: %d", len(response.Results))
	}
	if cluster.Literal != "quotient" {
		t.Fatalf("literal = %q, want quotient", cluster.Literal)
	}
	if cluster.HitsTotal != 15 || cluster.FilesTotal != 15 {
		t.Fatalf("totals = %d occurrences in %d files, want 15/15", cluster.HitsTotal, cluster.FilesTotal)
	}
	if len(cluster.Hits) == 0 {
		t.Fatalf("no listed sites: %s", RenderSearchLiteralCluster(cluster))
	}
	printed := map[string]bool{}
	for _, result := range response.Results {
		if result.SnippetStartLine > 0 {
			printed[result.FilePath] = true
		}
	}
	for _, hit := range cluster.Hits {
		if hit.Role != SearchLiteralRoleConsumer && hit.Role != SearchLiteralRoleEdit {
			t.Fatalf("unexpected role %q for %s", hit.Role, hit.FilePath)
		}
		if hit.Symbol == "" && hit.Role == SearchLiteralRoleConsumer {
			t.Fatalf("a CONSUMER hit must name the body that uses the literal: %+v", hit)
		}
	}
	if response.Stats.LiteralClusterBytes != searchLiteralClusterCost(cluster) {
		t.Fatalf("accounted %d bytes, cost %d", response.Stats.LiteralClusterBytes,
			searchLiteralClusterCost(cluster))
	}
	if searchLiteralClusterCost(cluster) > searchLiteralClusterMaxBytes {
		t.Fatalf("cost %d exceeds the cap %d", searchLiteralClusterCost(cluster), searchLiteralClusterMaxBytes)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchLiteralClusterIsAbsentWhenEverySiteIsAlreadyPrinted(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// Three files, every one of them ranked and printed in full. There is nothing the block could add
	// that the reader is not already looking at, so its bytes would buy nothing.
	writeFile(t, repo, "Ops.java", `package demo;

public enum Ops
{
  QUOT("quotient");
}
`)
	writeFile(t, repo, "AvgAggregator.java", `package demo;

public class AvgAggregator
{
  public String render()
  {
    return "quotient";
  }
}
`)
	writeFile(t, repo, "docs/aggregations.md", "The `quotient` post-aggregator divides.\n")
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"the quotient post-aggregator is missing from the registry",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if response.LiteralCluster != nil {
		t.Fatalf("the block repeated source the payload already printed: %s",
			RenderSearchLiteralCluster(response.LiteralCluster))
	}
}

// searchLiteralMagnetFixture builds a corpus where one literal occurs in `files` files, all of them
// outside anything the payload printed, plus the anchor the literal is mined from.
func searchLiteralMagnetFixture(files int) (
	[]SearchResult, searchQuery, map[string][]SymbolRecord, *searchNeedleIndex,
) {
	sources := map[string]string{
		"anchor.go": "package demo\n\nfunc build() string {\n\treturn helperName()\n}\n",
	}
	symbolsByFile := map[string][]SymbolRecord{
		"anchor.go": {{ID: "build", Kind: "function", Name: "build", QualifiedName: "build",
			FilePath: "anchor.go", StartLine: 3, EndLine: 5}},
	}
	corpus := []string{"anchor.go"}
	for index := 0; index < files; index++ {
		path := "pkg" + strconv.Itoa(index) + "/thing.go"
		sources[path] = "package p\n\nfunc call() string {\n\treturn helperName()\n}\n"
		symbolsByFile[path] = []SymbolRecord{{ID: path, Kind: "function", Name: "call",
			QualifiedName: "call", FilePath: path, StartLine: 3, EndLine: 5}}
		corpus = append(corpus, path)
	}
	results := []SearchResult{{
		Rank: 1, FilePath: "anchor.go", StartLine: 3, EndLine: 5, FocusLine: 4,
		SnippetStartLine: 3, SnippetEndLine: 5,
	}}
	index := &searchNeedleIndex{
		read: func(path string) (string, bool) {
			content, ok := sources[path]
			return content, ok
		},
		corpus: corpus,
	}
	return results, buildSearchQuery("helperName is wrong"), symbolsByFile, index
}

func TestSearchLiteralClusterRefusesALexicalMagnet(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		files int
		want  bool
	}{
		{
			// A name in a handful of files is a concept name, and the block is worth its bytes.
			name: "a concept name is in few files", files: 6, want: true,
		},
		{
			// The same name spread past the cap is a lexical magnet, and there is nothing distinctive
			// to report — so there must be no block, however relevant the word looks.
			name: "a magnet is in many files", files: searchLiteralMaxFiles + 2, want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			results, q, symbolsByFile, index := searchLiteralMagnetFixture(testCase.files)
			cluster := buildSearchLiteralCluster(results, q, symbolsByFile, index,
				searchLiteralClusterMaxBytes)
			if (cluster != nil) != testCase.want {
				t.Fatalf("cluster present = %v, want %v (%s)", cluster != nil, testCase.want,
					RenderSearchLiteralCluster(cluster))
			}
			if cluster != nil && cluster.FilesTotal != testCase.files+1 {
				t.Fatalf("files_total = %d, want %d", cluster.FilesTotal, testCase.files+1)
			}
		})
	}
}

func TestFitSearchLiteralClusterShrinksTheListAndKeepsTheTotal(t *testing.T) {
	t.Parallel()
	cluster := &SearchLiteralCluster{Literal: "quotient", HitsTotal: 40, FilesTotal: 9}
	for index := 0; index < 6; index++ {
		cluster.Hits = append(cluster.Hits, SearchLiteralHit{
			FilePath: "a/very/long/path/that/eats/the/budget/File" + strconv.Itoa(index) + ".java",
			Line:     index + 1,
			Symbol:   "com.example.package.name.Class.methodNumber" + strconv.Itoa(index),
			Role:     SearchLiteralRoleConsumer,
		})
	}
	fitted := fitSearchLiteralCluster(cluster, searchLiteralClusterMaxBytes)
	if fitted == nil {
		t.Fatal("the block must shrink rather than vanish while two hits still fit")
	}
	if len(fitted.Hits) >= 6 {
		t.Fatalf("hits = %d, expected the list to shrink", len(fitted.Hits))
	}
	if fitted.HitsTotal != 40 || fitted.FilesTotal != 9 {
		t.Fatalf("truncation rewrote the repository totals: %d/%d", fitted.HitsTotal, fitted.FilesTotal)
	}
	if searchLiteralClusterCost(fitted) > searchLiteralClusterMaxBytes {
		t.Fatalf("cost %d exceeds the cap %d", searchLiteralClusterCost(fitted), searchLiteralClusterMaxBytes)
	}
}

func TestRenderSearchLiteralClusterStatesTheRepositoryTotal(t *testing.T) {
	t.Parallel()
	rendered := string(RenderSearchLiteralCluster(&SearchLiteralCluster{
		Literal:      "BITCOUNT",
		HitsTotal:    35,
		FilesTotal:   5,
		Unclassified: 2,
		Hits: []SearchLiteralHit{
			{FilePath: "src/bitops.c", Line: 794, Symbol: "bitcountCommand", Role: SearchLiteralRoleConsumer},
		},
	}))
	for _, want := range []string{"BITCOUNT", "35 in 5 files repo-wide", "2 unparsed", "src/bitops.c:794", "CONSUMER"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered block missing %q:\n%s", want, rendered)
		}
	}
}
