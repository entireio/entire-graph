package sem

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sharedCacheFixture commits a small multi-language repository with two
// commits, so co-change history, containers, imports, externals and a
// partial failure all reach the snapshot.
func sharedCacheFixture(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "ledger/ledger.go", `package ledger

import "strings"

// Ledger frobnicates entries.
type Ledger struct{ entries []string }

// Add appends a normalized entry.
func (l *Ledger) Add(entry string) { l.entries = append(l.entries, normalize(entry)) }

func normalize(entry string) string { return strings.TrimSpace(entry) }
`)
	write(t, repo, "ledger/audit.go", `package ledger

// Audit checks every ledger entry.
func Audit(l *Ledger) int {
	l.Add("audit")
	return len(l.entries)
}
`)
	write(t, repo, "web/client.ts", `import { format } from "./format";

export class Client {
  render(entry: string): string { return format(entry); }
}
`)
	write(t, repo, "web/format.ts", `export function format(entry: string): string { return entry.trim(); }
`)
	write(t, repo, "ledger/flow.go", `package ledger

func alpha(a int) int {
	return bravo(a)
}

func bravo(b int) int {
	value := alpha(b)
	return value
}
`)
	write(t, repo, "assets/bundle.js", "var a=1;"+strings.Repeat("a=a+1;", 4000)+"\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "initial")
	write(t, repo, "ledger/ledger.go", `package ledger

import "strings"

// Ledger frobnicates entries.
type Ledger struct{ entries []string }

// Add appends a normalized entry.
func (l *Ledger) Add(entry string) { l.entries = append(l.entries, normalize(entry)) }

func normalize(entry string) string { return strings.ToLower(strings.TrimSpace(entry)) }
`)
	write(t, repo, "ledger/audit.go", `package ledger

// Audit checks every ledger entry.
func Audit(l *Ledger) int {
	l.Add("audit")
	return len(l.entries) + 0
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "co-change")
}

// TestSearchCacheIsSharedAcrossRemotelessClonesUnderTheirOwnNamespace pins the
// local/<basename> half of the path-free key: two remote-less clones of one
// tree under DIFFERENT directory names carry different repo keys, so every
// symbol and file ID differs, yet the second is served the first's entry and
// what it is served is exactly what a cold build in the second would produce.
// That equality is the audit of rebindCachedSearchSnapshot: any ID-bearing field
// it failed to rewrite, or any absolute path it failed to restamp, breaks it.
func TestSearchCacheIsSharedAcrossRemotelessClonesUnderTheirOwnNamespace(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "ledger-src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	sharedCacheFixture(t, source)
	clone := filepath.Join(parent, "ledger-clone")
	if out, err := exec.Command("git", "clone", "-q", source, clone).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	sourceKey, cloneKey := repoKey(t.Context(), source), repoKey(t.Context(), clone)
	if sourceKey == cloneKey || !strings.HasPrefix(sourceKey, "local/") || !strings.HasPrefix(cloneKey, "local/") {
		t.Fatalf("premise lost: want two different local/ keys, got %q and %q", sourceKey, cloneKey)
	}

	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	if _, hit, err := PreindexProviderSnapshot(t.Context(), source, "test-version", options, cacheDir); err != nil || hit {
		t.Fatalf("first preindex: hit=%v err=%v", hit, err)
	}
	served, hit, err := PreindexProviderSnapshot(t.Context(), clone, "test-version", options, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("a remote-less clone of the same tree under another name missed the shared entry")
	}
	cold, err := BuildProviderSnapshotWithOptions(t.Context(), clone, "test-version", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(cold.Relations) == 0 || len(cold.Externals) == 0 || len(cold.Header.PartialFailures) == 0 || len(cold.unmergedDataFlowEdges) == 0 {
		t.Fatalf("fixture too thin to audit the rebind: %d relations, %d externals, %d failures, %d unmerged flows",
			len(cold.Relations), len(cold.Externals), len(cold.Header.PartialFailures), len(cold.unmergedDataFlowEdges))
	}
	if !reflect.DeepEqual(served, cold) {
		t.Fatalf("shared entry differs from a cold build in the clone:\nserved header=%#v\ncold header=%#v\nfirst differing relation: %s",
			served.Header, cold.Header, firstRelationDifference(served.Relations, cold.Relations))
	}
}

func firstRelationDifference(left, right []RelationRecord) string {
	for index := 0; index < len(left) && index < len(right); index++ {
		if !reflect.DeepEqual(left[index], right[index]) {
			return relationKey(left[index]) + " vs " + relationKey(right[index])
		}
	}
	if len(left) != len(right) {
		return "lengths differ"
	}
	return "none"
}

// TestSearchCacheEntryCarriesNoCheckoutPathButTheHeader is the audit behind
// rebindCachedSearchSnapshot's claim that Header.RepoRoot is the only absolute
// path a search entry carries. Every complete and derived entry written for the
// fixture names the checkout exactly once, so restamping that one field is all
// a path-free key needs.
func TestSearchCacheEntryCarriesNoCheckoutPathButTheHeader(t *testing.T) {
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil {
		t.Fatal(err)
	}
	if _, err := SearchRepository(t.Context(), repo, "test-version", "Audit ledger entry", SearchOptions{
		Profile: ProfileFull, TopK: 5, MaxIndexedFiles: 2, CacheDir: cacheDir,
	}); err != nil {
		t.Fatal(err)
	}
	entries := 0
	if err := filepath.WalkDir(filepath.Join(cacheDir, "search"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".json.gz") {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		reader, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		entries++
		// The entry is JSON, so count the path's JSON spelling: a Windows
		// path's separators are escaped there.
		encoded, err := json.Marshal(repo)
		if err != nil {
			return err
		}
		if count := bytes.Count(raw, bytes.Trim(encoded, `"`)); count != 1 {
			t.Errorf("%s names the checkout %d times, want once (repo_root)", path, count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if entries < 2 {
		t.Fatalf("audited %d entries, want the complete entry and at least one derived view", entries)
	}
}
