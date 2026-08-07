package sem

import (
	"strings"
	"testing"
)

func TestClassifySearchFile(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		want searchFileClass
	}{
		{"plain source", "src/policy.ts", searchFileClassSource},
		{"nested source", "packages/docusaurus-utils/src/markdownUtils.ts", searchFileClassSource},
		{"source named like a doc dir sibling", "internal/docparser/parse.go", searchFileClassSource},
		{"header", "include/fmt/ranges.h", searchFileClassSource},

		{"config yaml is data, not documentation", ".github/workflows/test.yml", searchFileClassData},
		{"package manifest", "axum-extra/Cargo.toml", searchFileClassData},
		{"command schema", "src/commands/bitcount.json", searchFileClassData},
		{"maven pom", "pom.xml", searchFileClassData},
		{"ini config", "setup.cfg", searchFileClassData},
		// Executable program text stays source even when it configures something: a fix
		// really can live in a config script, unlike in a serialized table.
		{"config written in code is source", "packages/app/vite.config.ts", searchFileClassSource},
		{"gradle build script is source", "build.gradle", searchFileClassSource},

		{"markdown anywhere", "src/policy.md", searchFileClassDoc},
		{"mdx anywhere", "src/policy.mdx", searchFileClassDoc},
		{"restructured text", "api/reference.rst", searchFileClassDoc},
		{"asciidoc", "notes/design.adoc", searchFileClassDoc},
		{"plain text", "notes/design.txt", searchFileClassDoc},
		{"docs tree", "docs/guide/intro.html", searchFileClassDoc},
		{"versioned docs tree", "website/versioned_docs/version-2.x/i18n/i18n-tutorial.mdx", searchFileClassDoc},
		{"website tree", "website/src/pages/showcase.tsx", searchFileClassDoc},
		{"changelog", "CHANGELOG.md", searchFileClassDoc},
		{"readme", "packages/core/README", searchFileClassDoc},
		{"man page", "man/rg.1", searchFileClassDoc},
		{"license", "LICENSE", searchFileClassDoc},

		{"go vendor", "vendor/github.com/pkg/errors/errors.go", searchFileClassVendored},
		{"node modules", "node_modules/lodash/lodash.js", searchFileClassVendored},
		{"third party", "third_party/zlib/deflate.c", searchFileClassVendored},
		{"python site packages", ".venv/lib/site-packages/attr/_make.py", searchFileClassVendored},
		{"vendored markdown is vendored, not doc", "vendor/foo/README.md", searchFileClassVendored},

		{"dist bundle", "dist/index.js", searchFileClassGenerated},
		{"generated tree", "internal/generated/schema.go", searchFileClassGenerated},
		{"amalgamation", "single_include/nlohmann/json.hpp", searchFileClassGenerated},
		{"lock file", "package-lock.json", searchFileClassGenerated},
		{"cargo lock", "Cargo.lock", searchFileClassGenerated},

		{"examples", "examples/basic/main.go", searchFileClassExample},
		{"samples", "samples/Reader/simple.php", searchFileClassExample},
		{"demo", "demo/app/index.js", searchFileClassExample},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifySearchFile(test.path); got != test.want {
				t.Fatalf("classifySearchFile(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestSearchFileClassPriorDemotesNonSourceForCodeQueries(t *testing.T) {
	query := buildSearchQuery("CRLF line ending normalization in markdown processing")
	for _, test := range []struct {
		name string
		path string
		want float64
	}{
		{"source untouched", "packages/docusaurus-utils/src/markdownUtils.ts", 1},
		{"doc halved", "website/versioned_docs/version-2.x/i18n/i18n-tutorial.mdx", searchNonSourceClassPrior},
		{"vendored halved", "vendor/github.com/pkg/errors/errors.go", searchNonSourceClassPrior},
		{"generated halved", "dist/bundle.js", searchNonSourceClassPrior},
		{"data halved", "src/commands/bitcount.json", searchNonSourceClassPrior},
		{"manifest halved", "axum-extra/Cargo.toml", searchNonSourceClassPrior},
		{"example softened", "examples/basic/main.go", searchSecondaryClassPrior},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := searchFileClassPrior(query, test.path); got != test.want {
				t.Fatalf("searchFileClassPrior(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestSearchFileClassPriorRespectsExplicitClassIntent(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		path  string
	}{
		{"documentation task", "fix the documentation for CRLF handling", "docs/crlf.md"},
		{"readme task", "update the readme install steps", "README.md"},
		{"changelog task", "add a changelog entry", "CHANGELOG.md"},
		{"vendor task", "refresh the vendored dependency", "vendor/x/y.go"},
		{"generated task", "regenerate the dist bundle", "dist/bundle.js"},
		{"example task", "fix the example program", "examples/basic/main.go"},
		{"config task", "the yaml config parses the wrong timeout", "deploy/values.yaml"},
		{"schema task", "the command schema declares the wrong arity", "src/commands/bitcount.json"},
		{"dependency task", "bump the dependency in the manifest", "axum-extra/Cargo.toml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := searchFileClassPrior(buildSearchQuery(test.query), test.path)
			if got != 1 {
				t.Fatalf("searchFileClassPrior(%q, %q) = %v, want 1 (query asked for that class)", test.query, test.path, got)
			}
		})
	}
}

func TestApplySearchFileClassPriorRanksSourceOverProse(t *testing.T) {
	query := buildSearchQuery("CRLF line ending normalization in markdown processing")
	candidates := []searchCandidate{
		{score: 22.85, result: SearchResult{FilePath: "website/versioned_docs/version-2.x/i18n/i18n-tutorial.mdx", Signals: []string{"body"}}},
		{score: 11.99, result: SearchResult{FilePath: "packages/docusaurus-utils/src/markdownUtils.ts", Signals: []string{"body"}}},
	}
	applySearchFileClassPrior(candidates, query)
	if candidates[0].score >= candidates[1].score {
		t.Fatalf("documentation still outranks source: doc=%v source=%v", candidates[0].score, candidates[1].score)
	}
	if !containsString(candidates[0].result.Signals, "doc-prior") {
		t.Fatalf("demotion not disclosed: %#v", candidates[0].result.Signals)
	}
	if containsString(candidates[1].result.Signals, "doc-prior") {
		t.Fatalf("source wrongly marked: %#v", candidates[1].result.Signals)
	}
}

func TestApplySearchFileClassPriorKeepsDocWhenItIsTheOnlyMatch(t *testing.T) {
	query := buildSearchQuery("CRLF line ending normalization in markdown processing")
	candidates := []searchCandidate{
		{score: 22.85, result: SearchResult{FilePath: "docs/crlf.md"}},
		{score: 9.0, result: SearchResult{FilePath: "docs/other.md"}},
	}
	applySearchFileClassPrior(candidates, query)
	if candidates[0].score <= 0 {
		t.Fatalf("documentation was filtered out rather than demoted: %v", candidates[0].score)
	}
	if candidates[0].score <= candidates[1].score {
		t.Fatalf("relative order within a class changed: %v vs %v", candidates[0].score, candidates[1].score)
	}
}

func TestApplySearchFileClassPriorLeavesNegativeScoresAlone(t *testing.T) {
	query := buildSearchQuery("CRLF line ending normalization in markdown processing")
	candidates := []searchCandidate{
		{score: -4, result: SearchResult{FilePath: "docs/crlf.md"}},
	}
	applySearchFileClassPrior(candidates, query)
	if candidates[0].score != -4 {
		t.Fatalf("negative score was scaled (which would UNDO a demotion): %v", candidates[0].score)
	}
}

func TestSearchDocumentationArtifactPathCoversDocSiteTrees(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{"/website/versioned_docs/version-2.x/i18n/i18n-tutorial.mdx", true},
		{"/website/docs/guides/markdown.mdx", true},
		{"/docs/api.md", true},
		{"/changelog.md", true},
		{"/man/rg.1", true},
		{"/src/policy.ts", false},
		{"/packages/docusaurus-utils/src/markdownutils.ts", false},
	} {
		if got := searchDocumentationArtifactPath(test.path); got != test.want {
			t.Fatalf("searchDocumentationArtifactPath(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestSearchRepositoryRanksSourceOverVersionedDocumentation(t *testing.T) {
	repo := t.TempDir()
	prose := strings.Repeat("The loader normalizes CRLF line endings before markdown parsing.\n", 6)
	write(t, repo, "website/versioned_docs/version-2.x/guide/crlf.md", prose)
	write(t, repo, "website/versioned_docs/version-3.0.1/guide/crlf.md", prose)
	write(t, repo, "website/docs/guide/crlf.md", prose)
	write(t, repo, "packages/utils/src/markdownLoader.go", `package utils

// normalize strips carriage returns.
func normalizeCrlf(markdown string) string {
	return markdown
}
`)

	response, err := SearchRepository(t.Context(), repo, "test", "normalize CRLF line endings before markdown parsing", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 {
		t.Fatal("search returned no results")
	}
	if response.Results[0].FilePath != "packages/utils/src/markdownLoader.go" {
		t.Fatalf("first result = %q, want the source file", response.Results[0].FilePath)
	}
	docHits := 0
	for _, result := range response.Results {
		if strings.HasSuffix(result.FilePath, "crlf.md") {
			docHits++
		}
	}
	if docHits > 1 {
		t.Fatalf("versioned copies of one document were not collapsed: %d hits", docHits)
	}
}

// A `.d.ts` / `.pyi` mirror declares an API's shape and contains no executable statement, so it can
// never be where a behavioural defect is fixed — but it names the very option an issue is about, in
// one line, which is exactly what a body match rewards. Measured on axios#4731: `index.d.ts:340
// maxRedirects?: number;` scored 66.2 and beat the gold fix site `dispatchHttpRequest` at 61.3.
func TestDeclarationMirrorsAreDemotedBelowImplementation(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"index.d.ts", "index.d.cts", "index.d.mts", "types/index.d.tsx", "pkg/stubs/thing.pyi",
	} {
		if got := classifySearchFile(path); got != searchFileClassDeclaration {
			t.Fatalf("classifySearchFile(%q) = %q, want %q", path, got, searchFileClassDeclaration)
		}
		if prior := searchFileClassPrior(buildSearchQuery("maxBodyLength enforced inconsistently"), path); prior != searchSecondaryClassPrior {
			t.Fatalf("prior(%q) = %v, want %v", path, prior, searchSecondaryClassPrior)
		}
	}
	// Ordinary TypeScript is program text and must NOT be demoted.
	for _, path := range []string{"lib/adapters/http.ts", "src/index.ts", "app/main.tsx", "mod.py"} {
		if prior := searchFileClassPrior(buildSearchQuery("maxBodyLength enforced inconsistently"), path); prior != 1 {
			t.Fatalf("prior(%q) = %v, want 1 (executable program text)", path, prior)
		}
	}
}

// Asking about the declared surface switches the prior off: then the mirror IS the fix site.
func TestDeclarationPriorOffWhenQueryAsksForTypes(t *testing.T) {
	t.Parallel()
	for _, q := range []string{
		"the exported type definitions are wrong",
		"add a declaration for the new option",
		"update the typings",
	} {
		if prior := searchFileClassPrior(buildSearchQuery(q), "index.d.ts"); prior != 1 {
			t.Fatalf("query %q: prior = %v, want 1", q, prior)
		}
	}
}

// A fuzz target or benchmark mentions the API under test as densely as the implementation does, so it
// competes on lexical grounds and sometimes wins. Measured on uutils/coreutils: a signal-handling query
// returned `fuzz/fuzz_targets/fuzz_env.rs` at ranks 1 AND 2, the derived verify command became
// `cd fuzz && cargo test` — a separate workspace — and the session ran 40 -> 60 turns.
func TestClassifySearchFileHarnessTrees(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"fuzz/fuzz_targets/fuzz_env.rs",
		"benches/parser.rs",
		"benchmark/throughput_test.go",
		"src/fuzzing/corpus_gen.py",
		// note: fuzz/Cargo.toml is classified "data" by extension first, and data carries a
		// STRONGER demotion (0.5) than harness (0.75), so that ordering is correct as it stands.
	} {
		if got := classifySearchFile(path); got != searchFileClassHarness {
			t.Errorf("classifySearchFile(%q) = %q, want %q", path, got, searchFileClassHarness)
		}
	}
	// The implementation itself must not be swept up by a substring.
	for _, path := range []string{
		"src/uu/env/env.rs",
		"internal/benchmarking.go",
		"src/fuzzy_match.rs",
	} {
		if got := classifySearchFile(path); got == searchFileClassHarness {
			t.Errorf("classifySearchFile(%q) = harness, want implementation", path)
		}
	}
}

// The harness prior is a demotion, not a filter, and it switches off when the query asks for it —
// the same contract every other class prior honours.
func TestSearchFileClassPriorHarness(t *testing.T) {
	t.Parallel()
	plain := buildSearchQuery("env ignore signal handling")
	got := searchFileClassPrior(plain, "fuzz/fuzz_targets/fuzz_env.rs")
	if got != searchSecondaryClassPrior {
		t.Fatalf("harness prior = %v, want %v (demoted, not filtered)", got, searchSecondaryClassPrior)
	}
	asked := buildSearchQuery("the fuzz target crashes on empty input")
	if got := searchFileClassPrior(asked, "fuzz/fuzz_targets/fuzz_env.rs"); got != 1 {
		t.Fatalf("harness prior with fuzz intent = %v, want 1 (switched off)", got)
	}
	if got := searchFileClassPrior(plain, "src/uu/env/env.rs"); got != 1 {
		t.Fatalf("implementation prior = %v, want 1", got)
	}
}

// A one-line declaration that only NAMES a type is a reference, not a fix site. Measured on
// apache/druid-14092: four such lines took the two highest scores (78.32) with the real fix site at
// rank 3; the agent read the list as an edit set and patched 5 files across 4 Maven modules, 31 -> 48
// turns. The exclusions matter as much as the rule — a constant WITH a value, and a multi-line
// constant list, are both things fixes legitimately change.
func TestSearchReferenceDeclaration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		res  SearchResult
		want bool
	}{
		{"uninitialised field names a type", SearchResult{
			Kind: "field", SymbolStartLine: 40, SymbolEndLine: 40,
			Snippet: "  private final DruidLeaderClient druidLeaderClient;"}, true},
		{"declaration with a trailing comment", SearchResult{
			Kind: "field", SymbolStartLine: 7, SymbolEndLine: 7,
			Snippet: "  private Foo foo; // set by the injector"}, true},
		{"constant WITH a value is a fix site", SearchResult{
			Kind: "constant", SymbolStartLine: 12, SymbolEndLine: 12,
			Snippet: "  static final int MAX_RETRIES = 5;"}, false},
		{"multi-line constant list is a fix site", SearchResult{
			Kind: "field", SymbolStartLine: 82, SymbolEndLine: 96,
			Snippet: "  public static final List<String> NONNULL_ANNOTATIONS = Arrays.asList(\n    \"javax.annotation.Nonnull\","}, false},
		{"a function is never a reference declaration", SearchResult{
			Kind: "function", SymbolStartLine: 3, SymbolEndLine: 3,
			Snippet: "func f() {}"}, false},
		{"falls back to result bounds when symbol bounds are unset", SearchResult{
			Kind: "variable", StartLine: 9, EndLine: 9, Snippet: "var client HTTPClient"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := searchReferenceDeclaration(tc.res); got != tc.want {
				t.Fatalf("searchReferenceDeclaration(%q) = %v, want %v", tc.res.Snippet, got, tc.want)
			}
		})
	}
}

// A callable with no body is a contract, not an implementation. Measured on laravel/framework-46234
// (+308.7% usd on Sonnet): the payload ranked the INTERFACE declaration `UrlGenerator.previous` and
// never surfaced the implementation the agent had to edit. redis-10068 shows the C form, a bare
// prototype line in a header.
func TestSearchReferenceDeclarationBodylessCallables(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		res  SearchResult
		want bool
	}{
		{"php interface method", SearchResult{Kind: "method", SymbolStartLine: 20, SymbolEndLine: 20,
			Snippet: "    public function previous($fallback = false);"}, true},
		{"c header prototype", SearchResult{Kind: "function", SymbolStartLine: 3328, SymbolEndLine: 3328,
			Snippet: "void streamTrim(stream *s, streamAddTrimArgs *args);"}, true},
		{"go interface method", SearchResult{Kind: "method", SymbolStartLine: 12, SymbolEndLine: 12,
			Snippet: "\tPrevious() string"}, true},
		{"rust trait method", SearchResult{Kind: "method", SymbolStartLine: 7, SymbolEndLine: 7,
			Snippet: "    fn previous(&self) -> Option<String>;"}, true},
		{"real implementation keeps full weight", SearchResult{Kind: "method", SymbolStartLine: 203, SymbolEndLine: 226,
			Snippet: "public function previous($fallback = false)\n{\n    $referrer = $this->request->headers->get('referer');"}, false},
		{"one-line go func with a body", SearchResult{Kind: "function", SymbolStartLine: 5, SymbolEndLine: 5,
			Snippet: "func f() int { return 1 }"}, false},
		{"field rule still applies", SearchResult{Kind: "field", SymbolStartLine: 40, SymbolEndLine: 40,
			Snippet: "  private final DruidLeaderClient druidLeaderClient;"}, true},
		{"constant with a value is a fix site", SearchResult{Kind: "constant", SymbolStartLine: 12, SymbolEndLine: 12,
			Snippet: "  static final int MAX_RETRIES = 5;"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := searchReferenceDeclaration(tc.res); got != tc.want {
				t.Fatalf("searchReferenceDeclaration(%q) = %v, want %v", tc.res.Snippet, got, tc.want)
			}
		})
	}
}

// The first version of the body-less rule accepted any single-line method snippet, or one ending in
// `)`, and so demoted real implementations whose snippet was short or truncated. On
// apache/lucene-12022 that pushed the correct geo hits out of the payload on BOTH models. A
// declaration must now be recognised on positive evidence only.
func TestSearchReferenceDeclarationDoesNotCatchTruncatedImplementations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		res  SearchResult
		want bool
	}{
		{"complete one-line interface method", SearchResult{Kind: "method",
			SymbolStartLine: 20, SymbolEndLine: 20, SnippetStartLine: 20, SnippetEndLine: 20,
			Snippet: "    public function previous($fallback = false);"}, true},
		{"snippet is a WINDOW into a long method", SearchResult{Kind: "method",
			SymbolStartLine: 189, SymbolEndLine: 247, SnippetStartLine: 189, SnippetEndLine: 190,
			Snippet: "  public boolean withinTriangle(double ax, double ay, boolean ab)"}, false},
		{"multi-line symbol is never a declaration", SearchResult{Kind: "method",
			SymbolStartLine: 171, SymbolEndLine: 217, Snippet: "  Circle2D.within("}, false},
		{"truncated signature ending in paren", SearchResult{Kind: "function",
			SymbolStartLine: 100, SymbolEndLine: 140, SnippetStartLine: 100, SnippetEndLine: 101,
			Snippet: "static void tessellate(List<Triangle> out)"}, false},
		{"c prototype, whole symbol, no block", SearchResult{Kind: "function",
			SymbolStartLine: 3328, SymbolEndLine: 3328, SnippetStartLine: 3328, SnippetEndLine: 3328,
			Snippet: "void streamTrim(stream *s, streamAddTrimArgs *args);"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := searchReferenceDeclaration(tc.res); got != tc.want {
				t.Fatalf("searchReferenceDeclaration(%q) = %v, want %v", tc.res.Snippet, got, tc.want)
			}
		})
	}
}
