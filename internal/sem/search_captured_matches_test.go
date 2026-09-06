package sem

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// The captured matcher must preserve the Git preselection contract while reading
// operation-owned bytes: case-insensitive fixed strings, overlapping patterns,
// Unicode text, per-file line limits, binary detection, and .gitattributes binary
// classification all have to agree with Git's bounded matcher.
func TestCapturedPreselectionMatchesGitContract(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".gitattributes", "src/attribute.txt binary\n")
	write(t, repo, "src/overlap.go", "package p\n// Needle needle NEEDLE\n// Needle again\n// Needle third\n")
	write(t, repo, "src/unicode.go", "package p\n// wÉird WÉIRD weird\n")
	write(t, repo, "src/binary.go", "package p\n// Needle\x00 binary\n")
	write(t, repo, "src/attribute.txt", "Needle in an attribute-classified binary file\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "captured matcher fixture")

	options := ProviderSnapshotOptions{NoNetwork: true, Worktree: true, ExtractionReuse: true}
	source, err := prepareSource(context.Background(), repo, options)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	patterns := []string{"needle", "wÉird", "NEEDLE"}
	captured, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, patterns, 2)
	if err != nil {
		t.Fatal(err)
	}
	gitMatches, err := gitutil.GrepIndexMatches(context.Background(), repo, patterns, 2)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(captured, func(i, j int) bool {
		if captured[i].Path != captured[j].Path {
			return captured[i].Path < captured[j].Path
		}
		return captured[i].Text < captured[j].Text
	})
	sort.Slice(gitMatches, func(i, j int) bool {
		if gitMatches[i].Path != gitMatches[j].Path {
			return gitMatches[i].Path < gitMatches[j].Path
		}
		return gitMatches[i].Text < gitMatches[j].Text
	})
	if !reflect.DeepEqual(captured, gitMatches) {
		t.Fatalf("captured matcher differs from Git:\ncaptured=%#v\ngit=%#v", captured, gitMatches)
	}
	for _, match := range captured {
		if match.Path == "src/binary.go" || match.Path == "src/attribute.txt" {
			t.Fatalf("binary path leaked into captured matches: %#v", match)
		}
	}
}
