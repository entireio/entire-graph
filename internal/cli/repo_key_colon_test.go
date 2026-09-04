//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestColonInRepoDirectoryNameSurvivesEveryIDConsumer is the end-to-end half of
// the guard whose unit half lives in
// internal/sem.TestSymbolIDParsersTolerateAColonInTheRepoKey.
//
// A local repo key is `local/<basename>`, and a symbol ID is the fields joined
// with ':'. Neither the key nor the file path is escaped, so a directory named
// `weird:name` — or an ordinary directory holding a file at `od:d/mod.py` —
// emits IDs with more ':'-separated fields than the format names. Every command
// that takes an ID as a selector has to keep resolving it, because an ID is the
// one address that survives edits shifting line numbers.
//
// This drives the real commands rather than the parsers, so it also covers the
// selectors reached through the search/index caches.
func TestColonInRepoDirectoryNameSurvivesEveryIDConsumer(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "weird:name")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Skipf("filesystem rejects ':' in a directory name: %v", err)
	}
	write(t, repo, "main.go", `package main

type Store interface{ Get(k string) string }

type Mem struct{ m map[string]string }

func (s *Mem) Get(k string) string { return s.m[k] }

func main() { var s Store = &Mem{}; _ = s.Get("x") }
`)
	write(t, repo, "od:d/mod.py", `class Cache:
    def lookup(self, k):
        return k
`)

	run := func(t *testing.T, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()}, Stdout: &out}, args); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}

	symbols := run(t, "symbols", "--repo", repo, "--format", "ndjson")
	const wantKey = "local/weird:name"
	var methodID, colonPathID string
	for _, line := range strings.Split(strings.TrimSpace(symbols), "\n") {
		var record struct {
			RecordType    string `json:"record_type"`
			ID            string `json:"id"`
			RepoKey       string `json:"repo_key"`
			QualifiedName string `json:"qualified_name"`
			FilePath      string `json:"file_path"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid ndjson %q: %v", line, err)
		}
		if record.RepoKey != "" && record.RepoKey != wantKey {
			t.Fatalf("header repo_key = %q, want %q", record.RepoKey, wantKey)
		}
		if record.RecordType != "symbol" {
			continue
		}
		if !strings.HasPrefix(record.ID, wantKey+":") {
			t.Fatalf("symbol ID %q is not stamped with repo key %q", record.ID, wantKey)
		}
		if record.QualifiedName == "Mem.Get" {
			methodID = record.ID
		}
		if strings.Contains(record.FilePath, ":") && record.QualifiedName == "Cache" {
			colonPathID = record.ID
		}
	}
	if methodID == "" || colonPathID == "" {
		t.Fatalf("fixture produced method=%q colon-path=%q\n%s", methodID, colonPathID, symbols)
	}

	// Both IDs carry a colon the format's five fields do not account for: one
	// from the repo key, one more from the path.
	if strings.Count(methodID, ":") != 5 || strings.Count(colonPathID, ":") != 6 {
		t.Fatalf("expected 5 and 6 colons, got %q and %q", methodID, colonPathID)
	}

	for _, id := range []string{methodID, colonPathID} {
		// neighbors addresses a definition by exact ID.
		var neighbors struct {
			Query   string `json:"query"`
			Matches []struct {
				Symbol struct {
					ID string `json:"id"`
				} `json:"symbol"`
			} `json:"matches"`
		}
		output := run(t, "neighbors", "--repo", repo, "--symbol", id, "--relation", "CALLS", "--direction", "in")
		if err := json.Unmarshal([]byte(output), &neighbors); err != nil {
			t.Fatalf("neighbors json for %q: %v\n%s", id, err, output)
		}
		if len(neighbors.Matches) != 1 || neighbors.Matches[0].Symbol.ID != id {
			t.Fatalf("neighbors --symbol %q resolved to %#v", id, neighbors.Matches)
		}

		// edges filters relation endpoints by the same ID.
		edges := run(t, "edges", "--repo", repo, "--to", id, "--format", "ndjson")
		if !strings.Contains(edges, `"to_id":"`+id+`"`) {
			t.Fatalf("edges --to %q matched nothing:\n%s", id, edges)
		}
	}

	// def resolves an ID too, and must not mistake the trailing segment for a line.
	if out := run(t, "def", "--repo", repo, "--symbol", methodID); !strings.Contains(out, "Mem") {
		t.Fatalf("def --symbol %q did not resolve:\n%s", methodID, out)
	}
}

// TestIDSelectorParsersIgnoreExtraColons pins the three cli-side parsers that
// see a symbol ID, at the unit level, so a regression names the parser rather
// than surfacing as a command that stopped resolving.
func TestIDSelectorParsersIgnoreExtraColons(t *testing.T) {
	const methodID = "local/weird:name:Go:main.go:method:Mem.Get"
	const colonPathID = "local/weird:name:Python:od:d/mod.py:class:Cache"

	// idMatches compares the whole ID or anchors a trailing segment, so the
	// extra fields cannot shift a match.
	for _, tc := range []struct {
		id, selector string
		want         bool
	}{
		{methodID, methodID, true},
		{methodID, "method:Mem.Get", true},
		{methodID, "Mem.Get", true},
		{colonPathID, colonPathID, true},
		{colonPathID, "class:Cache", true},
		// The repo key's own second half must not select the symbol.
		{methodID, "name", false},
		{methodID, "local/weird", false},
	} {
		if got := idMatches(tc.id, tc.selector); got != tc.want {
			t.Fatalf("idMatches(%q, %q) = %v, want %v", tc.id, tc.selector, got, tc.want)
		}
	}

	// defExternalTypeName reads from the right, and external IDs never carry a
	// repo key.
	if got := defExternalTypeName("external:type:Foo"); got != "Foo" {
		t.Fatalf("defExternalTypeName = %q, want %q", got, "Foo")
	}

	// parseSymbolRef must leave a full ID alone: its `<file>:<line>` form only
	// triggers on a numeric tail that also names a file in the snapshot.
	paths := []string{"main.go", "od:d/mod.py"}
	for _, id := range []string{methodID, colonPathID} {
		if ref := parseSymbolRef(id, "", 0, "", "", paths); ref.Name != id || ref.File != "" || ref.Line != 0 {
			t.Fatalf("parseSymbolRef(%q) = %#v, want the ID kept whole", id, ref)
		}
	}
	// A ':'-containing path still addresses positionally, which is the case the
	// numeric-tail guard exists to allow.
	if ref := parseSymbolRef("od:d/mod.py:2", "", 0, "", "", paths); ref.Name != "" || ref.File != "od:d/mod.py" || ref.Line != 2 {
		t.Fatalf("parseSymbolRef(%q) = %#v, want file=od:d/mod.py line=2", "od:d/mod.py:2", ref)
	}
}
