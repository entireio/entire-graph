package sem

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// Downstream preselection consumes only per-path, case-insensitive term presence.
// The captured matcher therefore has to agree with Git on distinct overlapping
// terms, the 32 matching-line budget, Unicode case folding, binary sniffing, and
// .gitattributes binary classification.
func TestCapturedPreselectionTermPresenceMatchesGitContract(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".gitattributes", "src/attribute.txt binary\n")
	var overlap strings.Builder
	for i := 0; i < 32; i++ {
		overlap.WriteString("Needle need\n")
	}
	overlap.WriteString("ThirtyThird\n")
	write(t, repo, "src/overlap.go", "package p\n"+overlap.String())
	write(t, repo, "src/unicode.go", "package p\n// é\n")
	write(t, repo, "src/binary.go", "package p\n// Needle\x00 binary\n")
	write(t, repo, "src/attribute.txt", "Needle in an attribute-classified binary file\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "captured matcher fixture")

	source, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	patterns := []string{"needle", "need", "thirtythird", "É"}
	captured, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, patterns, 32)
	if err != nil {
		t.Fatal(err)
	}
	gitMatches, err := gitutil.GrepIndexMatches(context.Background(), repo, patterns, 32)
	if err != nil {
		t.Fatal(err)
	}
	got := reduceTermPresence(captured, patterns)
	want := reduceTermPresence(gitMatches, patterns)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captured term presence differs from Git:\ncaptured=%#v\ngit=%#v", got, want)
	}
	if got["src/overlap.go"]["thirtythird"] {
		t.Fatal("33rd matching line escaped Git's per-file line budget")
	}
	if !got["src/unicode.go"]["é"] {
		t.Fatalf("Unicode case pair was not retained: %#v", got)
	}
	if got["src/binary.go"]["needle"] || got["src/attribute.txt"]["needle"] {
		t.Fatalf("binary content leaked into term presence: %#v", got)
	}
}

func reduceTermPresence(matches []gitutil.GrepMatch, patterns []string) map[string]map[string]bool {
	presence := map[string]map[string]bool{}
	for _, match := range matches {
		terms := presence[match.Path]
		if terms == nil {
			terms = map[string]bool{}
			presence[match.Path] = terms
		}
		text := strings.ToLower(match.Text)
		for _, pattern := range patterns {
			if strings.Contains(text, strings.ToLower(pattern)) {
				terms[strings.ToLower(pattern)] = true
			}
		}
	}
	paths := make([]string, 0, len(presence))
	for path := range presence {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	ordered := make(map[string]map[string]bool, len(paths))
	for _, path := range paths {
		ordered[path] = presence[path]
	}
	return ordered
}
