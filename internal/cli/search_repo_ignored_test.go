package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func repoIgnoredResponse(excluded int, sample []sem.RepoExclusion) sem.SearchResponse {
	return sem.SearchResponse{
		Results: []sem.SearchResult{{
			Rank: 1, FilePath: "internal/auth/auth_stub.go", StartLine: 1, EndLine: 4,
			FocusLine: 3, SnippetStartLine: 1, SnippetEndLine: 4,
			SymbolName: "ValidateTokenStub", Snippet: "func ValidateTokenStub(t string) bool { return t != \"\" }",
		}},
		RepoIgnored: &sem.RepoIgnoreReport{
			Files:   excluded,
			Sources: []sem.RepoIgnoreSource{{File: ".graphignore", Files: excluded}},
			Sample:  sample,
		},
		Stats: sem.SearchStats{RepoIgnoredFiles: excluded},
		Completeness: sem.CompletenessReport{Languages: map[string]sem.LanguageCompleteness{
			"Go": {Files: 2, Symbols: 2},
		}},
		Warnings: []sem.ProviderWarning{{
			Code:     "W_REPO_IGNORED_SOURCE",
			FilePath: "internal/auth/auth.go",
			Detail:   "1 file excluded by .graphignore",
		}},
	}
}

// TestAgentCoverageLineCountsRepoExclusions pins the specific sentence the
// finding was about. The coverage line is the agent payload's claim about how
// much of the repository the answer saw; before this change it reported the two
// files that survived a committed ignore rule and said nothing about the third.
func TestAgentCoverageLineCountsRepoExclusions(t *testing.T) {
	response := repoIgnoredResponse(1, []sem.RepoExclusion{
		{Path: "internal/auth/auth.go", Source: ".graphignore", Rule: "internal/auth/auth.go"},
	})
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 4096); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"1 excluded by repo ignore rules",
		"warning W_REPO_IGNORED_SOURCE: internal/auth/auth.go",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent payload omitted %q:\n%s", want, text)
		}
	}
}

// TestAgentPayloadWithoutExclusionsIsUnchanged keeps the disclosure free on the
// common path: a repository that excluded nothing must not pay a byte for it.
func TestAgentPayloadWithoutExclusionsIsUnchanged(t *testing.T) {
	response := repoIgnoredResponse(0, nil)
	response.RepoIgnored = nil
	response.Warnings = nil
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 4096); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "excluded by repo ignore rules") {
		t.Fatalf("payload mentioned exclusions that did not happen:\n%s", out.String())
	}
}

// TestTextSearchDisclosureIsBoundedAndLeads checks both halves of the rendering
// contract: the disclosure comes before the ranked hits (a reader who has already
// read the answer will not act on a footnote), and a repository that legitimately
// keeps dozens of vendored blobs out of the graph cannot turn every payload into
// a wall of paths.
func TestTextSearchDisclosureIsBoundedAndLeads(t *testing.T) {
	sample := make([]sem.RepoExclusion, 0, 10)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		sample = append(sample, sem.RepoExclusion{
			Path: "vendor/" + name + "/parser.c", Source: ".graphignore", Rule: "parser.c",
		})
	}
	response := repoIgnoredResponse(23, sample)
	var out bytes.Buffer
	if err := writeTextSearch(&out, response); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	header := strings.Index(text, "EXCLUDED: 23 files removed")
	if header != 0 {
		t.Fatalf("the disclosure must lead the payload, found at %d:\n%s", header, text)
	}
	if !strings.Contains(text, "... 20 more") {
		t.Fatalf("payload should name 3 paths and count the rest:\n%s", text)
	}
	if listed := strings.Count(text, "/parser.c"); listed != 3 {
		t.Fatalf("payload listed %d paths, want 3:\n%s", listed, text)
	}
}
