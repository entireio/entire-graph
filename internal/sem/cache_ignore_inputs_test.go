package sem

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ignoreInputKeyer struct {
	name string
	key  func(ProviderSnapshotOptions) (string, error)
}

func ignoreInputKeyers(repo string) []ignoreInputKeyer {
	return []ignoreInputKeyer{
		{
			name: "search snapshot",
			key: func(options ProviderSnapshotOptions) (string, error) {
				return searchSnapshotKey(repo, "repo", "version", "tree", options)
			},
		},
		{
			name: "provider records",
			key: func(options ProviderSnapshotOptions) (string, error) {
				return providerRecordsKey(repo, "repo", "version", "tree", "snapshot", options)
			},
		},
	}
}

func TestCacheKeysRequireExplicitIgnoreInputs(t *testing.T) {
	repo := t.TempDir()
	inputs := []struct {
		name    string
		options ProviderSnapshotOptions
	}{
		{"ignore", ProviderSnapshotOptions{IgnoreFiles: []string{"missing.ignore"}}},
		{"include", ProviderSnapshotOptions{IncludeFiles: []string{"missing.include"}}},
	}
	for _, input := range inputs {
		for _, keyer := range ignoreInputKeyers(repo) {
			t.Run(input.name+"/"+keyer.name, func(t *testing.T) {
				_, err := keyer.key(input.options)
				if err == nil {
					t.Fatal("missing explicit input was treated as optional")
				}
				if !strings.Contains(err.Error(), "does not exist") {
					t.Fatalf("error = %v, want required-input wording", err)
				}
			})
		}
	}
}

func TestCacheKeysRejectOversizedIgnoreInputs(t *testing.T) {
	repo := t.TempDir()
	ignore := filepath.Join(repo, "oversized.ignore")
	if err := os.WriteFile(ignore, bytes.Repeat([]byte{'#'}, maxIgnoreFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{IgnoreFiles: []string{ignore}}
	for _, keyer := range ignoreInputKeyers(repo) {
		t.Run(keyer.name, func(t *testing.T) {
			_, err := keyer.key(options)
			if err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("oversized cache-key input error = %v, want size refusal", err)
			}
		})
	}
}

func TestCacheKeysDistinguishMissingGraphIgnoreFromMarkerContent(t *testing.T) {
	repo := t.TempDir()
	for _, keyer := range ignoreInputKeyers(repo) {
		t.Run(keyer.name, func(t *testing.T) {
			missing, err := keyer.key(ProviderSnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("missing"), 0o600); err != nil {
				t.Fatal(err)
			}
			present, err := keyer.key(ProviderSnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if missing == present {
				t.Fatal("absent .graphignore collided with a present file containing the old missing marker")
			}
			if err := os.Remove(filepath.Join(repo, graphIgnoreFileName)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCacheKeysFrameRawIgnoreContent(t *testing.T) {
	repo := t.TempDir()
	first := filepath.Join(repo, "first.ignore")
	second := filepath.Join(repo, "second.ignore")
	if err := os.WriteFile(second, []byte("right"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Under NUL-delimited, untyped framing these two inputs serialize identically:
	// the one-file content can impersonate the delimiter, second path, delimiter,
	// and second content of the two-file form.
	crafted := append([]byte("left\x00"+filepath.Clean(second)+"\x00"), []byte("right")...)
	if err := os.WriteFile(first, crafted, 0o600); err != nil {
		t.Fatal(err)
	}
	oneFile := ProviderSnapshotOptions{IgnoreFiles: []string{first}}

	for _, keyer := range ignoreInputKeyers(repo) {
		t.Run(keyer.name, func(t *testing.T) {
			if err := os.WriteFile(first, crafted, 0o600); err != nil {
				t.Fatal(err)
			}
			oneKey, err := keyer.key(oneFile)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(first, []byte("left"), 0o600); err != nil {
				t.Fatal(err)
			}
			twoKey, err := keyer.key(ProviderSnapshotOptions{IgnoreFiles: []string{first, second}})
			if err != nil {
				t.Fatal(err)
			}
			if oneKey == twoKey {
				t.Fatal("raw NUL content crossed field boundaries in the cache key")
			}
		})
	}
}

func TestProviderRecordsKeyIncludesGraphIgnore(t *testing.T) {
	repo := t.TempDir()
	key := func() string {
		t.Helper()
		got, err := providerRecordsKey(repo, "repo", "version", "tree", "snapshot", ProviderSnapshotOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	absent := key()
	if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("vendor/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	present := key()
	if absent == present {
		t.Fatal("adding .graphignore did not invalidate provider records")
	}
	if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if edited := key(); edited == present {
		t.Fatal("editing .graphignore did not invalidate provider records")
	}
}

func TestSearchSnapshotKeyIncludesWorktreeInfoExcludeStateAndContent(t *testing.T) {
	repo := t.TempDir()
	key := func(options ProviderSnapshotOptions) string {
		t.Helper()
		got, err := searchSnapshotKey(repo, "repo", "version", "tree", options)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	worktreeOptions := ProviderSnapshotOptions{Worktree: true}
	missing := key(worktreeOptions)
	headMissing := key(ProviderSnapshotOptions{})
	infoDir := filepath.Join(repo, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(infoDir, "exclude")
	if err := os.WriteFile(exclude, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	presentEmpty := key(worktreeOptions)
	if presentEmpty == missing {
		t.Fatal("missing info/exclude collided with a present empty file")
	}
	if err := os.WriteFile(exclude, []byte("/dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	presentContent := key(worktreeOptions)
	if presentContent == presentEmpty {
		t.Fatal("editing info/exclude did not invalidate the worktree key")
	}
	if headPresent := key(ProviderSnapshotOptions{}); headPresent != headMissing {
		t.Fatal("committed-tree key changed for a worktree-only ignore input")
	}
}

func TestWorktreeSnapshotCacheInvalidatesForGitInfoExclude(t *testing.T) {
	repo := cacheTestRepo(t)
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull, NoNetwork: true}

	first, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("cold worktree cache reported a hit")
	}
	if !hasSymbolNamed(first.Symbols, "Alpha") {
		t.Fatal("cold snapshot did not include app.go")
	}

	exclude := gitInfoExcludePath(repo)
	if exclude == "" {
		t.Fatal("test repository did not resolve info/exclude")
	}
	if err := os.WriteFile(exclude, []byte("/app.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	InvalidateWorktreeCleanVerdicts()
	second, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("info/exclude edit reused the stale worktree snapshot")
	}
	if hasSymbolNamed(second.Symbols, "Alpha") {
		t.Fatal("snapshot retained a tracked file excluded by info/exclude")
	}

	InvalidateWorktreeCleanVerdicts()
	third, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", options, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("unchanged info/exclude did not reuse the rebuilt snapshot")
	}
	if hasSymbolNamed(third.Symbols, "Alpha") {
		t.Fatal("warm rebuilt snapshot restored the excluded file")
	}
}
