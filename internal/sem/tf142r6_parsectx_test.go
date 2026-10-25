package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round six. The three findings were three instances of one shape: a stage
// reached AFTER the budget check, holding a parse context of its own that the
// caller's budget could not reach. The fix is structural rather than per-site:
// every tree-sitter parse in this package now derives its context from the
// caller, and this file is the invariant that keeps it that way for stages
// nobody has written yet. It is the only round-six test that compiles against
// the pre-fix tree, so it is also the runtime-failing evidence for the class.

// Unbudgeted parsing has one explicit entry point. Three legacy cache helpers
// also accept omitted contexts; all production paths supply the caller context.
// They never parse source. The allowlist is scoped to each function so another
// background context in the same file cannot silently pass this guard.
// TestCheckoutScopeLookupHonorsCallerCancellation and
// TestRecordsCacheLoadHonorsCallerCancellation verify the cache call paths.
var unbudgetedParseAllowlist = map[string]string{
	"parser.go.ParseWithStatus":                "return TreeSitterParser{}.ParseWithStatusCtx(context.Background(), path, content)",
	"search_cache.go.cacheCheckoutScope":       "ctx := context.Background()",
	"search_cache.go.loadCachedSearchSnapshot": "ctx := context.Background()",
	// Same entry point as the line above, after main factored the sidecar read
	// out of loadCachedSearchSnapshot: both take `contexts ...context.Context`
	// and fall back to Background only when the caller supplied none. A
	// caller's context is forwarded through loadCachedSearchSnapshot and
	// reaches loadSearchStoreForEntry intact, so the fallback is the
	// no-context default, not a parse stage escaping the caller's context.
	"search_cache.go.readSearchSnapshotPreferringStore": "ctx := context.Background()",
	// The records key body moved from providerRecordsKeyForSchema into
	// providerRecordsKeyForVersions (which also takes the cache version, so a
	// test can address an earlier build's entry). Same no-context fallback.
	"records_cache.go.providerRecordsKeyForVersions": "ctx := context.Background()",
}

// TestTF142R6NoUnbudgetedParseContext closes the CLASS the three round-six
// findings belong to. Threading the predicate into one more producer or one
// more parser only closes the site that was reported; this fails the build for
// any future stage that starts a fresh root context instead of taking the
// caller's, which is the mechanism behind every one of them.
func TestTF142R6NoUnbudgetedParseContext(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, name, body, 0)
		if err != nil {
			t.Fatal(err)
		}
		owner := func(line int) string {
			for _, decl := range parsed.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fset.Position(fn.Pos()).Line <= line && fset.Position(fn.End()).Line >= line {
					return name + "." + fn.Name.Name
				}
			}
			return name
		}
		scanned++
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(trimmed, "context.Background()") {
				continue
			}
			if unbudgetedParseAllowlist[owner(i+1)] == trimmed {
				continue
			}
			t.Errorf("%s:%d starts a context the caller's cancellation cannot reach: %s\n"+
				"take the caller's context instead; if this really is an unbudgeted entry point, add it to unbudgetedParseAllowlist with a reason",
				name, i+1, trimmed)
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d package files; the guard is not looking at the package", scanned)
	}
}
