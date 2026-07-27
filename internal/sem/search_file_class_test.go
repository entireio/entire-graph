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
