package sem

import (
	"strings"
	"testing"
)

// nearDuplicateBody is long enough to clear searchNearDuplicateMinChars and
// carries ~28 distinct tokens, so a one-word addition stays above the 0.9
// Jaccard threshold while a different sentence falls well below it.
const nearDuplicateBody = "The markdown loader normalizes carriage return and line feed sequences " +
	"before the parser tokenizes any front matter, headings, fenced code blocks, " +
	"tables, admonitions, or embedded JSX expressions in the document."

const nearDuplicateOtherBody = "Socket read timeouts propagate through the retry budget so a stalled " +
	"upstream connection releases its worker slot instead of pinning one until " +
	"the outer deadline expires."

func TestCollapseNearDuplicateCandidates(t *testing.T) {
	body := nearDuplicateBody
	variant := nearDuplicateBody + " Additionally."
	other := nearDuplicateOtherBody

	for _, test := range []struct {
		name     string
		in       []searchCandidate
		wantPath []string
		wantMore map[string]int
	}{
		{
			name: "versioned copies of one document collapse",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "website/versioned_docs/version-2.x/i18n/tut.mdx", Snippet: body}},
				{score: 20, result: SearchResult{FilePath: "website/versioned_docs/version-3.0.1/i18n/tut.mdx", Snippet: variant}},
				{score: 20, result: SearchResult{FilePath: "website/versioned_docs/version-3.1.1/i18n/tut.mdx", Snippet: body}},
			},
			wantPath: []string{"website/versioned_docs/version-2.x/i18n/tut.mdx"},
			wantMore: map[string]int{"website/versioned_docs/version-2.x/i18n/tut.mdx": 2},
		},
		{
			name: "identical vendored copy collapses into the authored file",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "src/errors.go", Snippet: body}},
				{score: 19, result: SearchResult{FilePath: "vendor/github.com/x/errors.go", Snippet: body}},
			},
			wantPath: []string{"src/errors.go"},
			wantMore: map[string]int{"src/errors.go": 1},
		},
		{
			name: "different content is kept",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "docs/v1/a.md", Snippet: body}},
				{score: 19, result: SearchResult{FilePath: "docs/v2/a.md", Snippet: other}},
			},
			wantPath: []string{"docs/v1/a.md", "docs/v2/a.md"},
		},
		{
			name: "different monorepo units are kept even when identical",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "packages/alpha/src/index.ts", Snippet: body}},
				{score: 19, result: SearchResult{FilePath: "packages/beta/src/index.ts", Snippet: body}},
			},
			wantPath: []string{"packages/alpha/src/index.ts", "packages/beta/src/index.ts"},
		},
		{
			name: "different basenames are kept even when identical",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "src/a.go", Snippet: body}},
				{score: 19, result: SearchResult{FilePath: "src/b.go", Snippet: body}},
			},
			wantPath: []string{"src/a.go", "src/b.go"},
		},
		{
			name: "two regions of the same file are kept",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "src/a.go", StartLine: 10, Snippet: body}},
				{score: 19, result: SearchResult{FilePath: "src/a.go", StartLine: 90, Snippet: body}},
			},
			wantPath: []string{"src/a.go", "src/a.go"},
		},
		{
			name: "short identical regions are never duplicates",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "a/v1/x.go", Snippet: "return nil"}},
				{score: 19, result: SearchResult{FilePath: "a/v2/x.go", Snippet: "return nil"}},
			},
			wantPath: []string{"a/v1/x.go", "a/v2/x.go"},
		},
		{
			name: "same path modulo version but genuinely different code is kept",
			in: []searchCandidate{
				{score: 20, result: SearchResult{FilePath: "client/v2/client.go", Snippet: body}},
				{score: 19, result: SearchResult{FilePath: "client/v3/client.go", Snippet: other}},
			},
			wantPath: []string{"client/v2/client.go", "client/v3/client.go"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := collapseNearDuplicateCandidates(test.in)
			if len(out) != len(test.wantPath) {
				t.Fatalf("got %d results, want %d: %#v", len(out), len(test.wantPath), out)
			}
			for index, candidate := range out {
				if candidate.result.FilePath != test.wantPath[index] {
					t.Fatalf("result %d = %q, want %q", index, candidate.result.FilePath, test.wantPath[index])
				}
				want := test.wantMore[candidate.result.FilePath]
				signal := ""
				for _, value := range candidate.result.Signals {
					if strings.HasSuffix(value, " similar") {
						signal = value
					}
				}
				if want == 0 {
					if signal != "" {
						t.Fatalf("result %d unexpectedly reports collapses: %q", index, signal)
					}
					continue
				}
				if got := "+" + itoa(want) + " similar"; signal != got {
					t.Fatalf("result %d signal = %q, want %q", index, signal, got)
				}
			}
		})
	}
}

func itoa(value int) string {
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

func TestVersionAgnosticSearchPath(t *testing.T) {
	for _, test := range []struct {
		path    string
		want    string
		flagged bool
	}{
		{"website/versioned_docs/version-2.x/i18n/tut.mdx", "website/versioned_docs/*/i18n/tut.mdx", true},
		{"website/versioned_docs/version-3.0.1/i18n/tut.mdx", "website/versioned_docs/*/i18n/tut.mdx", true},
		{"docs/v1/api.md", "docs/*/api.md", true},
		{"docs/latest/api.md", "docs/*/api.md", true},
		{"docs/2.4/api.md", "docs/*/api.md", true},
		{"src/policy.ts", "src/policy.ts", false},
		{"src/i18n/policy.ts", "src/i18n/policy.ts", false},
		{"src/next-gen/policy.ts", "src/next-gen/policy.ts", false},
	} {
		got, flagged := versionAgnosticSearchPath(test.path)
		if got != test.want || flagged != test.flagged {
			t.Fatalf("versionAgnosticSearchPath(%q) = (%q, %v), want (%q, %v)", test.path, got, flagged, test.want, test.flagged)
		}
	}
}

func TestSearchVersionPathSegment(t *testing.T) {
	for _, test := range []struct {
		segment string
		want    bool
	}{
		{"version-2.x", true},
		{"version_3", true},
		{"v2", true},
		{"3.0.1", true},
		{"2.x", true},
		{"release-1.2", true},
		{"latest", true},
		{"next", true},
		{"stable", true},
		{"version", false},
		{"x", false},
		{"i18n", false},
		{"vendor", false},
		{"src", false},
		{"next-gen", false},
	} {
		if got := searchVersionPathSegment(test.segment); got != test.want {
			t.Fatalf("searchVersionPathSegment(%q) = %v, want %v", test.segment, got, test.want)
		}
	}
}

func TestSearchTokenJaccard(t *testing.T) {
	left := searchDuplicateTokens(normalizeSearchDuplicateText("alpha beta gamma delta"))
	same := searchDuplicateTokens(normalizeSearchDuplicateText("ALPHA   beta\ngamma\tdelta"))
	if got := searchTokenJaccard(left, same); got != 1 {
		t.Fatalf("whitespace/case-only difference scored %v, want 1", got)
	}
	disjoint := searchDuplicateTokens(normalizeSearchDuplicateText("epsilon zeta eta theta"))
	if got := searchTokenJaccard(left, disjoint); got != 0 {
		t.Fatalf("disjoint token sets scored %v, want 0", got)
	}
	if got := searchTokenJaccard(left, nil); got != 0 {
		t.Fatalf("empty token set scored %v, want 0", got)
	}
}
