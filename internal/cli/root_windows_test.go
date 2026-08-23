//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveRepoRefusesUNCCeilingWithoutProbing(t *testing.T) {
	boundary := t.TempDir()
	repo := filepath.Join(boundary, "repo")
	child := filepath.Join(repo, "nested")
	for _, dir := range []string{filepath.Join(repo, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(child)
	t.Setenv("PATH", t.TempDir())
	// TEST-NET must remain inert text. Git would canonicalize every ceiling
	// entry and can therefore make an SMB connection before repository lookup.
	t.Setenv("GIT_CEILING_DIRECTORIES", `\\192.0.2.1\entire-graph\ceiling;`+boundary)

	type result struct {
		repo string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		got, err := resolveRepo(t.Context(), EntireEnv{}, "")
		done <- result{repo: got, err: err}
	}()
	select {
	case got := <-done:
		if got.err == nil || got.repo != "" {
			t.Fatalf("resolveRepo = (%q, %v), want (empty, error)", got.repo, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repository discovery did not return promptly; UNC ceiling must not be probed")
	}
}

func TestDiscoverImplicitCheckoutRootCanonicalizesJunctionCeiling(t *testing.T) {
	outer := t.TempDir()
	boundary := filepath.Join(outer, "discovery-boundary")
	child := filepath.Join(boundary, "nested", "child")
	for _, dir := range []string{filepath.Join(outer, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "boundary-alias")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, boundary).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v\n%s", err, output)
	}

	t.Run("before marker", func(t *testing.T) {
		t.Setenv("GIT_CEILING_DIRECTORIES", alias)
		if root, ok := discoverImplicitCheckoutRoot(child); ok {
			t.Fatalf("filesystem fallback discovered %q above junction ceiling %q", root, alias)
		}
	})

	t.Run("after marker", func(t *testing.T) {
		t.Setenv("GIT_CEILING_DIRECTORIES", string(os.PathListSeparator)+alias)
		root, ok := discoverImplicitCheckoutRoot(child)
		if !ok || !strings.EqualFold(root, outer) {
			t.Fatalf("filesystem fallback with non-canonicalized junction = (%q, %v), want (%q, true)", root, ok, outer)
		}
	})
}

func TestDiscoverImplicitCheckoutRootAcceptsGitWindowsAbsoluteCeilings(t *testing.T) {
	outer := t.TempDir()
	boundary := filepath.Join(outer, "discovery-boundary")
	child := filepath.Join(boundary, "nested", "child")
	for _, dir := range []string{filepath.Join(outer, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	volume := filepath.VolumeName(boundary)
	if volume == "" {
		t.Fatalf("temporary path %q has no Windows volume", boundary)
	}
	rest := strings.TrimLeft(boundary[len(volume):], `/\\`)
	rootRelative := string(filepath.Separator) + rest

	for name, ceiling := range map[string]string{
		"backslash rooted": rootRelative,
		"slash rooted":     filepath.ToSlash(rootRelative),
		"drive prefixed":   volume + rest,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)
			if root, ok := discoverImplicitCheckoutRoot(child); ok {
				t.Fatalf("filesystem fallback discovered %q above Git-for-Windows ceiling %q", root, ceiling)
			}
		})
	}
}

func TestDiscoverImplicitCheckoutRootRefusesAmbiguousCaseFoldedGitEntry(t *testing.T) {
	repo := t.TempDir()
	child := filepath.Join(repo, "nested", "child")
	for _, dir := range []string{filepath.Join(repo, ".GIT"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", t.TempDir())

	if root, ok := discoverImplicitCheckoutRoot(child); ok {
		t.Fatalf("filesystem fallback discovered %q through ambiguous case-folded Git entry", root)
	}
}

func TestDiscoverImplicitCheckoutRootRefusesUnrepresentableGitWindowsDrivePrefix(t *testing.T) {
	repo := t.TempDir()
	child := filepath.Join(repo, "nested", "child")
	for _, dir := range []string{filepath.Join(repo, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Git-for-Windows recognizes the multibyte code point before ':' as a DOS
	// drive prefix. Go cannot safely map that spelling to a native volume.
	t.Setenv("GIT_CEILING_DIRECTORIES", "ä:boundary")
	if root, ok := discoverImplicitCheckoutRoot(child); ok {
		t.Fatalf("filesystem fallback discovered %q with an unrepresentable Git drive prefix", root)
	}
}
