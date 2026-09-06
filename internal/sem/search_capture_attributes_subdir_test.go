package sem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedPreselectionAttributesSupportRepositorySubdirectory(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.name", "Entire Graph Test")
	git(t, root, "config", "user.email", "graph@example.com")
	git(t, root, "config", "diff.custom.binary", "true")
	if err := os.MkdirAll(filepath.Join(root, "pkg", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".gitattributes", "pkg/**/*.target diff=custom\n")
	write(t, root, "pkg/src/target.target", "needle\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "subdirectory captured attributes")

	repo := filepath.Join(root, "pkg")
	source, err := prepareSource(context.Background(), repo, ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	matches, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32)
	if err != nil {
		t.Fatalf("captured subdirectory preselection: %v", err)
	}
	if containsCapturedPath(matches, "src/target.target") {
		t.Fatal("root .gitattributes custom binary policy did not apply to subdirectory source")
	}
}
