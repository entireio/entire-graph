package sem

import (
	"context"
	"testing"
)

// TestAnalyzeGitRangeDependentsExcludeUnindexedCallers pins the count to the
// graph. Once the diff stopped naming ignored files, counting their references
// left a number nothing in the output accounted for.
func TestAnalyzeGitRangeDependentsExcludeUnindexedCallers(t *testing.T) {
	repo := t.TempDir()
	initDiffPolicyRepo(t, repo)
	write(t, repo, ".graphignore", "vendored/\n")
	write(t, repo, "keep/k.go", "package keep\n\nfunc Keep() int { return 1 }\n")
	write(t, repo, "other.go", "package other\n\nimport \"x/keep\"\n\nfunc Real() int { return keep.Keep() }\n")
	write(t, repo, "vendored/callers.go", "package vendored\n\nimport \"x/keep\"\n\nfunc A() int { return keep.Keep() }\nfunc B() int { return keep.Keep() }\nfunc C() int { return keep.Keep() }\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "keep/k.go", "package keep\n\nfunc Keep() int { return 2 }\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "edit the callee")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := onlyFile(t, result)
	if len(file.Changes) != 1 {
		t.Fatalf("changes = %#v, want one", file.Changes)
	}
	// other.go is the only caller the graph holds; the three in vendored/ are in
	// no snapshot and so are not dependents the graph can show.
	if got := file.Changes[0].DependentsCount; got != 1 {
		t.Fatalf("dependents = %d, want 1 indexed caller", got)
	}
}

// TestAnalyzeGitRangeDependentsIncludeReincludedVendorCallers is the
// over-rejection guard for the same filter, and the reason the head policy is
// resolved separately for this scan.
//
// The changed-file probe may skip loading a tree's vendored-tree rules when
// nothing vendorable changed. The dependents scan asks about the whole head
// tree, where a re-included vendor path can still appear, so reusing that
// probe's policy judged a real caller by empty rules and dropped it.
func TestAnalyzeGitRangeDependentsIncludeReincludedVendorCallers(t *testing.T) {
	repo := t.TempDir()
	initDiffPolicyRepo(t, repo)
	write(t, repo, ".gitignore", "vendor/*\n!vendor/rootpkg/\n!vendor/rootpkg/**\n")
	write(t, repo, "keep/k.go", "package keep\n\nfunc Keep() int { return 1 }\n")
	write(t, repo, "other.go", "package other\n\nimport \"x/keep\"\n\nfunc Real() int { return keep.Keep() }\n")
	write(t, repo, "vendor/rootpkg/caller.go", "package rootpkg\n\nimport \"x/keep\"\n\nfunc Vendored() int { return keep.Keep() }\n")
	git(t, repo, "add", "-A", "-f")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	// Only an ordinary file changes, so the changed-file probe finds nothing
	// vendorable and skips the rule load. The caller below still exists.
	write(t, repo, "keep/k.go", "package keep\n\nfunc Keep() int { return 2 }\n")
	git(t, repo, "add", "-A", "-f")
	git(t, repo, "commit", "-m", "edit the callee")
	head := rev(t, repo, "HEAD")

	if got := snapshotSymbolNames(t, repo, "vendor/rootpkg/caller.go"); len(got) == 0 {
		t.Fatal("fixture is wrong: the snapshot must index the re-included vendor caller")
	}

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := onlyFile(t, result)
	if len(file.Changes) != 1 {
		t.Fatalf("changes = %#v, want one", file.Changes)
	}
	if got := file.Changes[0].DependentsCount; got != 2 {
		t.Fatalf("dependents = %d, want both indexed callers including the re-included vendor one", got)
	}
}
