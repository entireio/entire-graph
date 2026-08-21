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
