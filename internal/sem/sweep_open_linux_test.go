//go:build linux

package sem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxSweepRootOpensAnOrdinaryNestedDirectory(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "ignored", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, resolvedRepo, err := newPathTraversalAnchor(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, testCase := range []struct {
		name         string
		wantMinSteps int
		wantMaxSteps int
		open         func(func() bool) (*os.File, error)
	}{
		{"openat2 or fallback", 1, 3, func(admit func() bool) (*os.File, error) {
			return root.Open(anchor, "ignored/nested", admit)
		}},
		{"mountinfo fallback", 2, 2, func(admit func() bool) (*os.File, error) {
			return root.openWithoutOpenat2(anchor, []string{"ignored", "nested"}, admit)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			steps := 0
			opened, err := testCase.open(func() bool {
				steps++
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			if steps < testCase.wantMinSteps || steps > testCase.wantMaxSteps {
				t.Fatalf("traversal steps = %d, want %d..%d", steps, testCase.wantMinSteps, testCase.wantMaxSteps)
			}
		})
	}
}

func TestLinuxSweepRootRefusesAMountBoundary(t *testing.T) {
	anchor, resolvedRoot, err := newPathTraversalAnchor("/", "/")
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, testCase := range []struct {
		name string
		open func(func() bool) (*os.File, error)
	}{
		{"openat2 or fallback", func(admit func() bool) (*os.File, error) {
			return root.Open(anchor, "proc", admit)
		}},
		{"mountinfo fallback", func(admit func() bool) (*os.File, error) {
			return root.openWithoutOpenat2(anchor, []string{"proc"}, admit)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			steps := 0
			opened, err := testCase.open(func() bool {
				steps++
				return true
			})
			if opened != nil {
				_ = opened.Close()
				t.Fatal("held sweep root crossed the /proc mount boundary")
			}
			if err == nil {
				t.Fatal("held sweep root returned no error at the /proc mount boundary")
			}
			if steps < 1 || steps > 2 {
				t.Fatalf("traversal steps = %d, want 1..2", steps)
			}
		})
	}
}

func TestLinuxSweepFallbackRefusesARenamedHeldRoot(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	anchor, resolvedRepo, err := newPathTraversalAnchor(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := os.Rename(repo, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("moved", repo); err != nil {
		t.Fatal(err)
	}
	opened, err := root.openWithoutOpenat2(anchor, []string{"ignored"}, func() bool { return true })
	if opened != nil {
		_ = opened.Close()
		t.Fatal("mountinfo fallback opened a directory after its held root was renamed")
	}
	if err == nil {
		t.Fatal("mountinfo fallback returned no error after its held root was renamed")
	}
}
