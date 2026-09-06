//go:build !windows

package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// colonRepoKeyFixture creates a repository whose directory basename contains a
// ':', so repoKey derives `local/weird:name` and every symbol ID it stamps
// carries that colon in its first field.
func colonRepoKeyFixture(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "weird:name")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Skipf("filesystem rejects ':' in a directory name: %v", err)
	}
	writeFile(t, repo, "main.go", `package main

type Store interface{ Get(k string) string }

type Mem struct{ m map[string]string }

func (s *Mem) Get(k string) string { return s.m[k] }

func main() { var s Store = &Mem{}; _ = s.Get("x") }
`)
	writeFile(t, repo, "lib.rs", `use std::collections::HashMap;

pub struct Cache { map: HashMap<String, String> }
`)
	// A path segment may itself contain a ':', which injects a second colon into
	// the same ID from a different field.
	writeFile(t, repo, "od:d/mod.py", `class Cache:
    def lookup(self, k):
        return k
`)
	return repo
}

// TestSymbolIDParsersTolerateAColonInTheRepoKey pins the property that makes a
// ':' in a repo directory name harmless.
//
// A local repo key is `local/<basename>`, and a symbol ID is
// `repoKey:language:path:kind:qualifiedName` joined with ':'. Nothing escapes the
// key, so a directory named `weird:name` yields
// `local/weird:name:Go:main.go:method:Mem.Get` — six ':'-separated fields where
// the format names five. Every field after the key shifts for anyone who parses
// an ID by splitting on ':' and indexing positionally.
//
// No parser here does that, and the format already depends on it: a Rust import
// ships as `external:import:std::collections::HashMap`, whose value field holds
// three colons, and a file path may contain one too. Each parser either compares
// the whole ID, anchors on the LAST separator, or is gated to the `external:`
// namespace whose kind field can never hold a ':'. This test fails if a future
// positional `strings.Split(id, ":")[1]` replaces one of them, instead of letting
// it silently mis-attribute every symbol in such a repo.
func TestSymbolIDParsersTolerateAColonInTheRepoKey(t *testing.T) {
	repo := colonRepoKeyFixture(t)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}

	const wantKey = "local/weird:name"
	if snapshot.Header.RepoKey != wantKey {
		t.Fatalf("repo_key = %q, want %q", snapshot.Header.RepoKey, wantKey)
	}

	symbolsByID := map[string]SymbolRecord{}
	symbolsByShortName := map[string][]SymbolRecord{}
	var method SymbolRecord
	var colonPath SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if !strings.HasPrefix(symbol.ID, wantKey+":") {
			t.Fatalf("symbol ID %q is not stamped with repo key %q", symbol.ID, wantKey)
		}
		symbolsByID[symbol.ID] = symbol
		symbolsByShortName[symbol.Name] = append(symbolsByShortName[symbol.Name], symbol)
		if symbol.Language == "Go" && symbol.Kind == "method" && symbol.QualifiedName == "Mem.Get" {
			method = symbol
		}
		if strings.Contains(symbol.FilePath, ":") && symbol.Kind == "class" {
			colonPath = symbol
		}
	}
	if method.ID == "" {
		t.Fatalf("fixture did not produce the Go method symbol: %#v", snapshot.Symbols)
	}
	if colonPath.ID == "" {
		t.Fatalf("fixture did not produce a symbol under a ':'-containing path: %#v", snapshot.Symbols)
	}

	// The premise: a naive positional split reads the wrong field. Field 1 is the
	// language for a well-formed ID and is `name` here; the path-borne colon adds
	// yet another. If this ever stops holding, the guards below stop being needed
	// and this test should be re-derived rather than relaxed.
	if parts := strings.Split(method.ID, ":"); len(parts) < 6 || parts[1] == method.Language {
		t.Fatalf("expected a ':' in the repo key to shift the fields of %q, got %q", method.ID, parts)
	}
	if strings.Count(colonPath.ID, ":") < 6 {
		t.Fatalf("expected the path colon to add a field to %q", colonPath.ID)
	}

	// goMethodSymbolByID anchors on the LAST `:method:` and then re-checks the
	// whole ID, so the extra leading fields cannot move the marker.
	resolved, ok := goMethodSymbolByID(method.ID, symbolsByShortName)
	if !ok || resolved.ID != method.ID {
		t.Fatalf("goMethodSymbolByID(%q) = %q, %v; want the same record", method.ID, resolved.ID, ok)
	}

	// enclosingTypeShortName reads the container from the right.
	if method.ContainerID == "" {
		t.Fatalf("Go method %q has no container ID to parse", method.ID)
	}
	if got := enclosingTypeShortName(method); got != "Mem" {
		t.Fatalf("enclosingTypeShortName(%q) = %q, want %q", method.ContainerID, got, "Mem")
	}

	// externalParts is gated to the `external:` namespace, whose kind field never
	// holds a ':' — so cutting at the FIRST one keeps a value that does.
	var external ExternalRecord
	for _, record := range snapshot.Externals {
		if record.Kind == "import" && strings.Contains(record.Value, "::") {
			external = record
		}
	}
	if external.ID == "" {
		t.Fatalf("fixture did not produce a '::'-bearing external import: %#v", snapshot.Externals)
	}
	kind, value := externalParts(external.ID)
	if kind != external.Kind || value != external.Value {
		t.Fatalf("externalParts(%q) = %q, %q; want %q, %q", external.ID, kind, value, external.Kind, external.Value)
	}

	// Relation endpoints address symbols and files by whole ID, so every edge in
	// such a repo still resolves to a record.
	files := map[string]bool{}
	for _, file := range snapshot.Files {
		files[fileID(wantKey, file.Path)] = true
	}
	for _, relation := range snapshot.Relations {
		for _, endpoint := range []string{relation.FromID, relation.ToID} {
			if strings.HasPrefix(endpoint, "external:") || files[endpoint] {
				continue
			}
			if _, ok := symbolsByID[endpoint]; !ok {
				t.Fatalf("relation endpoint %q resolves to no symbol or file", endpoint)
			}
		}
	}
}
