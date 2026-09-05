package sem

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestRepoIgnoreSampleBoundsTreeSuppliedPaths is the review finding.
//
// maxRepoExclusionRuleBytes bounds the pattern text and reasons that Path needs
// no bound of its own because "Path and Source are filesystem paths". The
// worktree walk makes that true. The committed-tree listing does not: those
// paths are names read out of a Git tree object, and nothing requires a tree
// entry's name to be one any filesystem could hold. The ledger retained them
// verbatim, ten at a time, into a report that every JSON response carries whole
// and an NDJSON stream carries twice.
func TestRepoIgnoreSampleBoundsTreeSuppliedPaths(t *testing.T) {
	t.Parallel()
	ledger := &repoIgnoreLedger{}
	for i := range maxRepoExclusionSample {
		ledger.note(RepoExclusion{
			Path:   fmt.Sprintf("hidden/%s%02d.go", strings.Repeat("a", 200_000), i),
			Source: graphIgnoreFileName,
			Rule:   "hidden/",
		})
	}
	report := ledger.report()
	if report == nil {
		t.Fatal("fixture is wrong: nothing disclosed")
	}
	if report.Files != maxRepoExclusionSample {
		t.Errorf("Files = %d, want %d: bounding what the sample NAMES must not change what it COUNTS",
			report.Files, maxRepoExclusionSample)
	}
	total := 0
	for _, exclusion := range report.Sample {
		if len(exclusion.Path) > maxRepoExclusionPathBytes+len("...") {
			t.Errorf("sample path is %d bytes, want at most %d: a Git tree entry's name is not bounded by"+
				" any filesystem, so a committed path reaches every JSON response at whatever length the"+
				" repository chose", len(exclusion.Path), maxRepoExclusionPathBytes+len("..."))
		}
		total += len(exclusion.Path)
	}
	if total > maxRepoExclusionSamplePathBytes {
		t.Errorf("sample paths total %d bytes, want at most %d: the disclosure's size must be a property"+
			" of this file, not of the tree being searched", total, maxRepoExclusionSamplePathBytes)
	}
	if !report.SampleTruncated {
		t.Error("names were withheld and SampleTruncated does not say so")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 8192 {
		t.Errorf("the serialized report is %d bytes for %d excluded paths", len(encoded), report.Files)
	}
}

// TestSearchBoundsCommittedTreePathsInTheDisclosure is the same finding end to
// end, through the listing that actually produces such a path.
//
// `git update-index --add --cacheinfo` writes a 200,000-byte path into the index
// and `git write-tree` commits it without any file of that name ever existing;
// `git ls-tree` hands it back to the HEAD listing, which is where
// filterIgnoredPaths fills the ledger. Measured before the bound: a two-file
// repository disclosed 2,000,734 serialized bytes and a 200,057-byte warning.
func TestSearchBoundsCommittedTreePathsInTheDisclosure(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	initRepo(t, repo)
	write(t, repo, graphIgnoreFileName, "hidden/\n")
	write(t, repo, "visible/auth.go", "package visible\n\n"+
		"// ValidateToken checks the bearer token presented on a request.\n"+
		"func ValidateToken(token string) bool { return len(token) == 64 }\n")
	blob := gitInput(t, repo, "package hidden\n", "hash-object", "-w", "--stdin")
	var index strings.Builder
	for i := range 6 {
		fmt.Fprintf(&index, "100644 %s\thidden/%s%02d.go\n", blob, strings.Repeat("a", 60_000), i)
	}
	gitInput(t, repo, index.String(), "update-index", "--add", "--index-info")
	git(t, repo, "add", "visible/auth.go", graphIgnoreFileName)
	git(t, repo, "commit", "-q", "-m", "seed")

	response, err := SearchRepository(t.Context(), repo, "test", "bearer token validation", SearchOptions{
		Profile: ProfileSyntaxOnly,
		TopK:    5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RepoIgnored == nil {
		t.Fatal("fixture is wrong: the committed tree hid nothing")
	}
	encoded, err := json.Marshal(response.RepoIgnored)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 8192 {
		t.Errorf("repo_ignored serializes to %d bytes from a repository holding two source files:"+
			" a committed tree sized the disclosure every JSON response carries", len(encoded))
	}
	warnings := withRepoIgnoreDisclosure(response.Warnings, response.RepoIgnored)
	if len(warnings) == 0 {
		t.Fatal("the disclosure warning is missing")
	}
	if len(warnings[0].Detail)+len(warnings[0].FilePath) > 4096 {
		t.Errorf("the %s warning carries %d bytes of detail and %d of file_path: the sample path it"+
			" quotes is repository-sized", repoIgnoreDisclosureCode,
			len(warnings[0].Detail), len(warnings[0].FilePath))
	}
}
