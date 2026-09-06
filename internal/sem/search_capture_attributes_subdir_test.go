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

func TestCapturedPreselectionSubdirectoryRetainsAncestorPolicyDecision(t *testing.T) {
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
	git(t, root, "commit", "-m", "initial subdirectory policy")

	source, err := prepareSource(context.Background(), filepath.Join(root, "pkg"), ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32)
	if err != nil {
		source.close()
		t.Fatal(err)
	}
	if containsCapturedPath(first, "src/target.target") {
		source.close()
		t.Fatal("initial ancestor binary policy did not apply")
	}

	// The operation must keep the first captured ancestor policy even after the
	// mutable worktree policy changes.
	write(t, root, ".gitattributes", "pkg/**/*.target diff\n")
	repeated, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32)
	if err != nil {
		source.close()
		t.Fatal(err)
	}
	if containsCapturedPath(repeated, "src/target.target") {
		source.close()
		t.Fatal("repeated operation reread changed ancestor policy")
	}
	if err := source.close(); err != nil {
		t.Fatal(err)
	}

	fresh, err := prepareSource(context.Background(), filepath.Join(root, "pkg"), ProviderSnapshotOptions{
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
		t.Fatal("fresh operation did not observe changed ancestor policy")
	}
}

func TestCapturedPreselectionSubdirectoryUsesIndexedAncestorPolicy(t *testing.T) {
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
	git(t, root, "commit", "-m", "indexed subdirectory policy")
	if err := os.Remove(filepath.Join(root, ".gitattributes")); err != nil {
		t.Fatal(err)
	}

	source, err := prepareSource(context.Background(), filepath.Join(root, "pkg"), ProviderSnapshotOptions{
		NoNetwork: true, Worktree: true, ExtractionReuse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	matches, _, err := capturedPreselectionMatches(context.Background(), source, source.paths, []string{"needle"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if containsCapturedPath(matches, "src/target.target") {
		t.Fatal("indexed ancestor binary policy did not apply when worktree policy was absent")
	}
}
