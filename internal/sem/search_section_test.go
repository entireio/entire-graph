package sem

import "testing"

func TestSearchDocsSectionPathClassifiesNonCodeOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		path  string
		query string
		want  bool
	}{
		// Prose, serialized data and machine-written artifacts hold no program text.
		{name: "markdown doc", path: "docs/guide/routing.md", query: "routes drop trailing slash", want: true},
		{name: "changelog", path: "CHANGELOG.md", query: "merge loses fallback", want: true},
		{name: "html template", path: "website/templates/features/ExtensionMethod.html", query: "extension method transforms unrelated call", want: true},
		{name: "recorded fixture", path: "caddytest/integration/adapt/global_options_default_bind.txt", query: "default bind address ignored", want: true},
		{name: "config", path: "axum-extra/Cargo.toml", query: "merge loses fallback", want: true},
		{name: "lockfile", path: "yarn.lock", query: "dependency resolution", want: true},
		// Program text stays a candidate fix site even when the ranker tilts away from it:
		// a vendored copy and an example are still code, and a fix really can land in them.
		{name: "source", path: "src/routing/mod.rs", query: "merge loses fallback", want: false},
		{name: "vendored source", path: "vendor/github.com/x/y/router.go", query: "merge loses fallback", want: false},
		{name: "example source", path: "examples/basic/main.go", query: "merge loses fallback", want: false},
		{name: "python config script", path: "setup.py", query: "install fails", want: false},
		// The prior's own intent gate decides: a caller who asked for documentation gets it in
		// the primary list, because that is the question they asked.
		{name: "doc asked for", path: "docs/guide/routing.md", query: "the documentation for routing is wrong", want: false},
		{name: "config asked for", path: "axum-extra/Cargo.toml", query: "wrong manifest configuration", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchDocsSectionPath(buildSearchQuery(testCase.query), testCase.path); got != testCase.want {
				t.Fatalf("searchDocsSectionPath(%q) = %v, want %v", testCase.path, got, testCase.want)
			}
		})
	}
}

func TestAssignSearchSectionsLabelsNonCodeWithoutHidingIt(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		{Rank: 1, FilePath: "CHANGELOG.md"},
		{Rank: 2, FilePath: "src/routing/mod.rs"},
		{Rank: 3, FilePath: "config/app.yaml"},
		{Rank: 4, FilePath: "src/routing/path.rs"},
	}
	assigned := assignSearchSections(results, buildSearchQuery("merge loses the nested fallback route"))
	if len(assigned) != 4 {
		t.Fatalf("sectioning dropped results: %d of 4 survived", len(assigned))
	}
	want := []string{searchSectionDocs, searchSectionPrimary, searchSectionDocs, searchSectionPrimary}
	for index, expected := range want {
		if assigned[index].Section != expected {
			t.Fatalf("result %d section = %q, want %q", index+1, assigned[index].Section, expected)
		}
		if assigned[index].Rank != index+1 {
			t.Fatalf("result %d rank changed to %d: sectioning must not re-rank", index+1, assigned[index].Rank)
		}
	}
}

// TestAssignSearchSectionsKeepsNonCodeOnlyPayloadPrimary pins the guard that makes sectioning
// safe: a payload whose ONLY hits are non-code has no list of fix sites to protect, and those
// hits are the answer the caller gets. Measured instances exist where the useful rank-1 hit is a
// recorded fixture or a rule document, so labelling them away would render an empty page.
func TestAssignSearchSectionsKeepsNonCodeOnlyPayloadPrimary(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		{Rank: 1, FilePath: "caddytest/integration/adapt/global_options_default_bind.txt"},
		{Rank: 2, FilePath: "doc/rules/casing/constant_case.rst"},
	}
	assigned := assignSearchSections(results, buildSearchQuery("default bind address is ignored"))
	for index, result := range assigned {
		if result.Section != searchSectionPrimary {
			t.Fatalf("result %d section = %q, want primary when every hit is non-code", index+1, result.Section)
		}
	}
}

func TestRelatedSiteKindReadsTheSignal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result SearchResult
		want   string
	}{
		{
			name:   "near duplicate",
			result: SearchResult{Section: searchSectionRelated, Signals: []string{"related:near-dupe"}},
			want:   "near-dupe",
		},
		{
			name:   "caller",
			result: SearchResult{Section: searchSectionRelated, Signals: []string{"related:caller"}},
			want:   "caller",
		},
		{
			name:   "primary result is not a related site",
			result: SearchResult{Signals: []string{"related:caller"}},
			want:   "",
		},
		{
			name:   "related without a reason",
			result: SearchResult{Section: searchSectionRelated, Signals: []string{"body"}},
			want:   "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := RelatedSiteKind(testCase.result); got != testCase.want {
				t.Fatalf("RelatedSiteKind = %q, want %q", got, testCase.want)
			}
		})
	}
}
