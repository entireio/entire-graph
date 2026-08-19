package sem

import (
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

// unbudgetedParseAllowlist is the ONE place in this package allowed to root a
// parse at context.Background: the documented entry point for callers that are
// not under a wall-clock budget at all. Everything else must take the caller's
// context, or an expiring budget cannot reach the cgo parse it starts.
var unbudgetedParseAllowlist = map[string]string{
	"parser.go": "return TreeSitterParser{}.ParseWithStatusCtx(context.Background(), path, content)",
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
		scanned++
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(trimmed, "context.Background()") {
				continue
			}
			if unbudgetedParseAllowlist[name] == trimmed {
				continue
			}
			t.Errorf("%s:%d starts a context the caller's wall-clock budget cannot reach: %s\n"+
				"take the caller's context instead; if this really is an unbudgeted entry point, add it to unbudgetedParseAllowlist with a reason",
				name, i+1, trimmed)
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d package files; the guard is not looking at the package", scanned)
	}
}
