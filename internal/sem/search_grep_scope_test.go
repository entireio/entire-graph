package sem

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

func TestWarmCommittedSearchGrepsOnlyAdmittedFiles(t *testing.T) {
	for _, policy := range []string{"graphignore", "external-ignore", "include-override"} {
		t.Run(policy, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "maintenance.auto", "false")
			git(t, repo, "config", "user.name", "Entire Graph Test")
			git(t, repo, "config", "user.email", "graph@example.com")
			write(t, repo, "src/policy.ts", "export const durableCacheRefreshPolicy = 'eager'\n")
			write(t, repo, "src/consumer.ts", "export function applyRefreshPolicy() { install(durableCacheRefreshPolicy) }\n")
			write(t, repo, "generated/parser.ts", "export const ignored = 'durableCacheRefreshPolicy consumer'\n")
			write(t, repo, "generated/keep.ts", "export function keepRefreshPolicy() { return durableCacheRefreshPolicy }\n")
			options := SearchOptions{Profile: ProfileFull, TopK: 10, IndexAllFiles: true, CacheDir: t.TempDir()}
			if policy == "external-ignore" {
				path := filepath.Join(t.TempDir(), "ignore")
				if err := os.WriteFile(path, []byte("generated/\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				options.IgnoreFiles = []string{path}
			} else {
				write(t, repo, ".graphignore", "generated/\n")
			}
			if policy == "include-override" {
				path := filepath.Join(t.TempDir(), "include")
				if err := os.WriteFile(path, []byte("generated/keep.ts\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				options.IncludeFiles = []string{path}
			}
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "initial")
			if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
				Profile: ProfileFull, IgnoreFiles: options.IgnoreFiles, IncludeFiles: options.IncludeFiles,
			}, options.CacheDir); err != nil {
				t.Fatal(err)
			}
			const query = "durable cache refresh policy consumer"
			before, err := SearchRepository(t.Context(), repo, "test-version", query, options)
			if err != nil {
				t.Fatal(err)
			}
			foundIncluded := false
			for _, result := range before.Results {
				if result.FilePath == "generated/parser.ts" {
					t.Fatal("ignored file in results")
				}
				foundIncluded = foundIncluded || result.FilePath == "generated/keep.ts"
			}
			if foundIncluded != (policy == "include-override") {
				t.Fatalf("include policy not reflected in results: %#v", before.Results)
			}
			cmd := exec.Command("git", "rev-parse", "HEAD:generated/parser.ts")
			cmd.Dir = repo
			objectID, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			blob := strings.TrimSpace(string(objectID))
			blobPath := filepath.Join(repo, ".git", "objects", blob[:2], blob[2:])
			if err := os.Chmod(blobPath, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(blobPath); err != nil {
				t.Fatal(err)
			}
			if _, err := gitutil.GrepTreePaths(t.Context(), repo, "HEAD", []string{"durableCacheRefreshPolicy"}); err == nil {
				t.Fatal("whole-tree control did not fail on unavailable ignored blob")
			}
			after, err := SearchRepository(t.Context(), repo, "test-version", query, options)
			if err != nil {
				t.Fatal(err)
			}
			if !after.Stats.IndexCacheHit || after.Stats.PreselectionBackend != "git-tree-grep" || after.Stats.UsagePreselectionBackend != "git-tree-grep" {
				t.Fatalf("scoped scans fell back after encountering an excluded blob: %#v", after.Stats)
			}
			if !reflect.DeepEqual(before.Results, after.Results) {
				t.Fatalf("unavailable excluded blob changed results:\nbefore=%#v\nafter=%#v", before.Results, after.Results)
			}
		})
	}
}
