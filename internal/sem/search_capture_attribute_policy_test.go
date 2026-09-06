package sem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

func TestCapturedPreselectionBindsAttributePolicyAndDriverDecisions(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "diff.custom.binary", "true")
	write(t, repo, ".gitattributes", "*.target diff=custom\n")
	write(t, repo, "src/target.target", "needle\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "captured attribute policy")

	source, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	first, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if containsCapturedPath(first, "src/target.target") {
		t.Fatal("custom binary driver leaked target into first captured selection")
	}
	firstManifest, err := source.finishCapture(source.paths)
	if err != nil {
		t.Fatal(err)
	}
	if firstManifest == nil || firstManifest.ID == "" {
		t.Fatal("first operation did not record captured input identity")
	}

	// Changing Git configuration after the first decision must not affect a
	// repeated helper call in the same operation.
	git(t, repo, "config", "diff.custom.binary", "false")
	repeated, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if containsCapturedPath(repeated, "src/target.target") {
		t.Fatal("repeated helper reread mutable custom-driver configuration")
	}

	// A fresh operation observes the changed policy and must produce a new
	// operation identity as well as a different selected path.
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.target diff\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	selected, _, err := capturedPreselectionMatches(context.Background(), fresh, fresh.paths, []string{"needle"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !containsCapturedPath(selected, "src/target.target") {
		t.Fatal("fresh operation did not observe changed text attribute")
	}
	freshManifest, err := fresh.finishCapture(fresh.paths)
	if err != nil {
		t.Fatal(err)
	}
	if freshManifest == nil || freshManifest.ID == firstManifest.ID {
		t.Fatal("effective captured attribute policy did not change operation identity")
	}
}

func TestCapturedPreselectionRejectsOversizedAttributePolicy(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	attribute := strings.Repeat("*.target diff\n", 128)
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte(attribute), 0600); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "src/target.target", "needle\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "oversized captured attribute policy")
	source, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true, MaxParseBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if _, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32); err == nil || !strings.Contains(err.Error(), "attribute policy exceeds the captured read limit") {
		t.Fatalf("oversized attribute policy error = %v", err)
	}
	if _, ok := source.oversize(".gitattributes"); !ok {
		t.Fatal("oversized attribute policy was not retained as an explicit oversize observation")
	}
}

func containsCapturedPath(matches []gitutil.GrepMatch, path string) bool {
	for _, match := range matches {
		if match.Path == path {
			return true
		}
	}
	return false
}
