package sem

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The store's whole contract is fidelity: substituting it for the gzipped JSON
// entry must be invisible. The pin round-trips one real snapshot through BOTH
// containers and requires the results to be deeply equal — covering every
// record stream, every side map the private symbol fields travel in, and the
// envelope scalars.
func TestSearchStoreRoundTripsSnapshot(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A corpus small enough to build in milliseconds but wide enough to
	// populate every table: nested (Local) and overload-style symbols, calls,
	// types, aliases via a tool registration, and a second language.
	write("main.go", "package sample\n\ntype Widget struct{ Name string }\n\nfunc NewWidget(name string) *Widget { return &Widget{Name: name} }\n\nfunc Render(w *Widget) string {\n\thelper := func() string { return w.Name }\n\treturn helper()\n}\n")
	write("util.py", "def fetch(url):\n    return url\n\ndef main():\n    v = fetch(\"x\")\n    return v\n")
	write("lib.ts", "export function load(path: string): string;\nexport function load(path: string, mode: string): string;\nexport function load(path: string, mode?: string): string { return path; }\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "store-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	cache := newCachedSearchSnapshot("store-test", "commit-a", "tree-a", ProviderSnapshotOptions{}, snapshot, t.Context())

	// Reference: the gz JSON container production serves today.
	entry, err := newCacheEntry(t.TempDir(), "search", "test-v1", validTestCacheKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(entry, cache); err != nil {
		t.Fatal(err)
	}
	fromJSON, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatal(err)
	}

	storePath := filepath.Join(t.TempDir(), "store.sqlite")
	if err := buildSearchStore(t.Context(), storePath, "digest-a", &cache); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSearchStore(t.Context(), storePath, "digest-b"); err == nil {
		t.Fatal("store served a snapshot for an entry digest it was not built against")
	}
	fromStore, err := loadSearchStore(t.Context(), storePath, "digest-a")
	if err != nil {
		t.Fatal(err)
	}

	// Direct DeepEqual, no normalization: the loader must reproduce the exact
	// nil-vs-empty shapes and slice orders the JSON path produces, or a real
	// fidelity regression could hide behind a test-side normalizer.
	if !reflect.DeepEqual(fromStore, fromJSON) {
		t.Fatal(diffSearchStoreCaches(t, fromStore, fromJSON))
	}
}

func diffSearchStoreCaches(t *testing.T, g, w cachedSearchSnapshot) string {
	t.Helper()
	report := "store round-trip differs from JSON round-trip:"
	check := func(name string, gv, wv any) {
		if !reflect.DeepEqual(gv, wv) {
			report += "\n  " + name
		}
	}
	check("envelope scalars", [8]any{g.CacheVersion, g.ProviderVersion, g.Commit, g.Tree, g.Profile, g.MaxParseBytes, g.DerivedFrom, g.RepoScope},
		[8]any{w.CacheVersion, w.ProviderVersion, w.Commit, w.Tree, w.Profile, w.MaxParseBytes, w.DerivedFrom, w.RepoScope})
	check("header", g.Snapshot.Header, w.Snapshot.Header)
	check("files", g.Snapshot.Files, w.Snapshot.Files)
	check("externals", g.Snapshot.Externals, w.Snapshot.Externals)
	check("symbols", g.Snapshot.Symbols, w.Snapshot.Symbols)
	check("relations", g.Snapshot.Relations, w.Snapshot.Relations)
	check("file lines", g.FileLines, w.FileLines)
	check("byte ranges", g.SymbolByteRanges, w.SymbolByteRanges)
	check("name lines", g.SymbolNameLines, w.SymbolNameLines)
	check("parameter names", g.SymbolParameterNames, w.SymbolParameterNames)
	check("parameter known ids", g.SymbolParameterNamesKnownIDs, w.SymbolParameterNamesKnownIDs)
	check("signature types", g.SymbolSignatureTypes, w.SymbolSignatureTypes)
	check("local ids", g.LocalSymbolIDs, w.LocalSymbolIDs)
	check("bodyless ids", g.BodylessSymbolIDs, w.BodylessSymbolIDs)
	check("unmerged edges", g.UnmergedDataFlowEdges, w.UnmergedDataFlowEdges)
	return report
}

func validTestCacheKey(t *testing.T) string {
	t.Helper()
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

// The context query is the delegation surface a downstream consumer calls
// instead of keeping its own graph copy. Pinned: task tokens rank the matching
// symbol first, the relation fan-out covers the returned symbols, symbols in
// test files land in tests (not symbols), and a stale store reads as
// ErrSearchStoreMissing so consumers degrade rather than error.
func TestSearchStoreContextServesTaskContext(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("widget.go", "package sample\n\ntype Widget struct{ Name string }\n\nfunc NewWidget(name string) *Widget { return &Widget{Name: name} }\n\nfunc RenderWidget(w *Widget) string {\n\treturn w.Name\n}\n")
	write("other.go", "package sample\n\nfunc Unrelated() int { return 1 }\n")
	write("widget_test.go", "package sample\n\nimport \"testing\"\n\nfunc TestRenderWidget(t *testing.T) {\n\tif RenderWidget(NewWidget(\"x\")) != \"x\" {\n\t\tt.Fatal(\"render\")\n\t}\n}\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "store-test", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	cache := newCachedSearchSnapshot("store-test", "commit-a", "tree-a", ProviderSnapshotOptions{}, snapshot, t.Context())
	storePath := filepath.Join(t.TempDir(), "store.sqlite")
	if err := buildSearchStore(t.Context(), storePath, "digest-a", &cache); err != nil {
		t.Fatal(err)
	}

	got, err := QuerySearchStoreContext(t.Context(), storePath, "digest-a", "render the widget name", 5, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Symbols) == 0 || got.Symbols[0].Name != "RenderWidget" {
		t.Fatalf("expected RenderWidget ranked first, got %+v", got.Symbols)
	}
	if got.RelationsTruncated {
		t.Fatalf("default relations limit truncated a %d-relation fan-out", len(got.Relations))
	}
	for _, symbol := range got.Symbols {
		if symbol.FilePath == "widget_test.go" {
			t.Fatalf("test-file symbol leaked into symbols: %+v", symbol)
		}
		if symbol.StartLine == 0 {
			t.Fatalf("symbol %s lost its line range", symbol.ID)
		}
	}
	returned := map[string]bool{}
	for _, symbol := range got.Symbols {
		returned[symbol.ID] = true
	}
	touchesReturned := false
	for _, relation := range got.Relations {
		if returned[relation.FromID] || returned[relation.ToID] {
			touchesReturned = true
			break
		}
	}
	if len(got.Relations) > 0 && !touchesReturned {
		t.Fatalf("relation fan-out does not touch returned symbols: %+v", got.Relations)
	}
	foundTest := false
	for _, test := range got.Tests {
		if test.Name == "TestRenderWidget" {
			foundTest = true
		}
	}
	if !foundTest {
		t.Fatalf("expected TestRenderWidget in tests, got %+v", got.Tests)
	}

	// A hub symbol's fan-out must not make the response unbounded: the cap
	// keeps the highest-confidence relations, deterministically, and says so.
	if len(got.Relations) < 2 {
		t.Fatalf("fixture no longer produces a multi-relation fan-out: %+v", got.Relations)
	}
	capped, err := QuerySearchStoreContext(t.Context(), storePath, "digest-a", "render the widget name", 5, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Relations) != 1 || !capped.RelationsTruncated {
		t.Fatalf("relations limit 1 returned %d relations (truncated=%v)", len(capped.Relations), capped.RelationsTruncated)
	}
	for _, relation := range got.Relations {
		if relation.Confidence > capped.Relations[0].Confidence {
			t.Fatalf("cap kept confidence %v while %v existed", capped.Relations[0].Confidence, relation.Confidence)
		}
	}

	// Stale store (wrong digest) must degrade, not error arbitrarily.
	if _, err := QuerySearchStoreContext(t.Context(), storePath, "digest-b", "render the widget name", 5, 3, 0); !errors.Is(err, ErrSearchStoreMissing) {
		t.Fatalf("stale store should read as ErrSearchStoreMissing, got %v", err)
	}
}

func TestSearchStoreReadProjectionMatchesCanonicalRecords(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package sample\nfunc Normalize(s string) string { return s }\nfunc Render(s string) string { return Normalize(s) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), dir, "projection", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	cache := newCachedSearchSnapshot("projection", "commit", "tree", ProviderSnapshotOptions{}, snapshot, t.Context())
	path := filepath.Join(t.TempDir(), "store.sqlite")
	if err = buildSearchStore(t.Context(), path, "digest", &cache); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+sqliteURLPath(path)+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, symbol := range snapshot.Symbols {
		var name, qualified, file, kind, signature, language, search string
		var start, end int
		err = db.QueryRow(`SELECT s.name,s.qualified_name,s.file_path,s.kind,s.signature,s.language,s.start_line,s.end_line,f.text FROM brain_symbols_v1 s JOIN brain_symbol_fts_v1 f ON f.id=s.id WHERE s.id=?`, symbol.ID).Scan(&name, &qualified, &file, &kind, &signature, &language, &start, &end, &search)
		if err != nil {
			t.Fatal(err)
		}
		if name != symbol.Name || qualified != symbol.QualifiedName || file != symbol.FilePath || kind != symbol.Kind || signature != symbol.Signature || language != symbol.Language || start != symbol.StartLine || end != symbol.EndLine {
			t.Fatalf("projection changed symbol %s", symbol.ID)
		}
		want := strings.Join([]string{symbol.Name, symbol.QualifiedName, symbol.Signature, symbol.FilePath, symbol.Kind, symbol.Language}, " ")
		if search != want {
			t.Fatalf("search projection %q != %q", search, want)
		}
	}
	if len(snapshot.Files) == 0 {
		t.Fatal("fixture has no file records")
	}
	for _, file := range snapshot.Files {
		var blob, contentHash, language string
		err = db.QueryRow("SELECT blob,content_hash,language FROM brain_files_v1 WHERE path=?", file.Path).Scan(&blob, &contentHash, &language)
		if err != nil {
			t.Fatal(err)
		}
		if file.Blob == "" {
			t.Fatalf("fixture file %q has no blob", file.Path)
		}
		if blob != file.Blob || contentHash != "git-blob:"+file.Blob || language != file.Language {
			t.Fatalf("file projection %q: blob=%q content_hash=%q language=%q", file.Path, blob, contentHash, language)
		}
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM brain_relations_v1").Scan(&count); err != nil || count != len(snapshot.Relations) {
		t.Fatalf("relations=%d err=%v", count, err)
	}
	if _, err = db.Exec("DELETE FROM symbols"); err == nil {
		t.Fatal("read-only open allowed mutation")
	}
}
