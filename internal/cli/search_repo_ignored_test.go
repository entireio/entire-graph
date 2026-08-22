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

// TestTextSearchUnreadableListIsBounded reproduces the "unbounded" half of the
// re-review finding: report.Unreadable, unlike Sources and Sample above it,
// was joined into the "LOWER BOUND" line with no cap of its own. The ledger
// samples up to maxRepoExclusionSample (10) unreadable directories, each an
// arbitrary repository-controlled path, so that single line could grow with
// the size of the broken subtree instead of the size of the answer.
func TestTextSearchUnreadableListIsBounded(t *testing.T) {
	response := repoIgnoredResponse(5, []sem.RepoExclusion{
		{Path: "vendor/keep.go", Source: ".graphignore", Rule: "vendor/"},
	})
	unreadable := make([]string, 0, 10)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		unreadable = append(unreadable, "vendor/"+name+"/broken")
	}
	response.RepoIgnored.CountIncomplete = true
	response.RepoIgnored.Unreadable = unreadable
	var out bytes.Buffer
	if err := writeTextSearch(&out, response); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if listed := strings.Count(text, "/broken"); listed != 3 {
		t.Fatalf("payload listed %d unreadable paths, want the same cap of 3 as Sources/Sample:\n%s", listed, text)
	}
	if !strings.Contains(text, "+7 more") {
		t.Fatalf("payload must count the omitted unreadable paths:\n%s", text)
	}
}

// TestTextSearchDisclosureIsChargedAgainstTheContextBudget reproduces the
// trail finding: the disclosure block was written into the text payload
// entirely outside response.Stats.ContextBudgetBytes, after the ranked
// results it labels had already been fit to that same ceiling. A caller with
// a tiny explicit ceiling still got the full disclosure block on top of it —
// a repository-controlled payload that competed with (or dwarfed) the actual
// answer for a budget the caller asked to bound.
func TestTextSearchDisclosureIsChargedAgainstTheContextBudget(t *testing.T) {
	response := repoIgnoredResponse(1, []sem.RepoExclusion{
		{Path: "internal/auth/auth.go", Source: ".graphignore", Rule: "internal/auth/auth.go"},
	})

	unbudgeted := response
	unbudgeted.Stats.ContextBudgetBytes = 0
	var withoutBudget bytes.Buffer
	if err := writeTextSearch(&withoutBudget, unbudgeted); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withoutBudget.String(), "EXCLUDED:") {
		t.Fatalf("an unset budget (0, historical behavior) must still print the disclosure:\n%s", withoutBudget.String())
	}

	tooSmall := response
	tooSmall.Stats.ContextBudgetBytes = 8
	var withTinyBudget bytes.Buffer
	if err := writeTextSearch(&withTinyBudget, tooSmall); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withTinyBudget.String(), "EXCLUDED:") {
		t.Fatalf("a ceiling smaller than the disclosure block itself must drop it rather than blow"+
			" past the ceiling before a single ranked result is printed:\n%s", withTinyBudget.String())
	}

	roomy := response
	roomy.Stats.ContextBudgetBytes = 65536
	var withRoomyBudget bytes.Buffer
	if err := writeTextSearch(&withRoomyBudget, roomy); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withRoomyBudget.String(), "EXCLUDED:") {
		t.Fatalf("a ceiling comfortably larger than the disclosure block must still show it:\n%s", withRoomyBudget.String())
	}
}
