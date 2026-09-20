package sem

import (
	"strings"
	"testing"
)

func TestParserIdentityRevisionsSnapshotAndCaches(t *testing.T) {
	header := leanHeader(sourceContext{}, "same-release", profileSpec{})
	if header.IdentityRevision != "3" {
		t.Fatalf("identity=%q", header.IdentityRevision)
	}
	if !strings.HasSuffix(searchSnapshotCacheVersion, "-"+header.IdentityRevision) || !strings.HasSuffix(providerRecordsCacheVersion, "-"+header.IdentityRevision) {
		t.Fatal("parser identity does not invalidate cached snapshots")
	}
}

func TestDefaultExportIdentityCorrectionIsRevisioned(t *testing.T) {
	entities := javascriptDefaultExportEntities("helper.js", "export default classifier => classifier()\n")
	symbols := entitySymbols("local/example", "helper.js", "JavaScript", entities)
	if len(symbols) != 1 || symbols[0].ID != "local/example:JavaScript:helper.js:function:helper" {
		t.Fatalf("corrected default export symbols = %+v", symbols)
	}
	if IdentityRevision == "js-ts-callable-scope-1" {
		t.Fatal("default export identity correction must invalidate the previous parser revision")
	}
}

// Issue #199: qualifying a Python nested callable by the enclosing CALLABLE
// re-keys every Python nested-callable symbol, so serving it from a cache
// written by the previous revision would keep publishing the phantom
// `C.helper`. The cache key carries IdentityRevision and nothing else in it
// changes (the cache is keyed on the git TREE of the source, which an edit to
// the parser does not touch), so the bump is the entire invalidation mechanism.
func TestPythonNestedCallableCorrectionIsRevisioned(t *testing.T) {
	entities, _, status := TreeSitterParser{}.ParseWithStatus("c.py",
		"class C:\n    def m(self):\n        def helper(v):\n            return v\n        return helper(1)\n")
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	symbols := entitySymbols("local/example", "c.py", "Python", entities)
	corrected := false
	for _, symbol := range symbols {
		if symbol.ID == "local/example:Python:c.py:function:C.m.helper" {
			corrected = true
		}
		if symbol.ID == "local/example:Python:c.py:method:C.helper" {
			t.Errorf("phantom class member still emitted: %s", symbol.ID)
		}
	}
	if !corrected {
		t.Fatalf("corrected Python nested callable missing; symbols = %s", symbolIDs(symbols))
	}
	if IdentityRevision == "2" {
		t.Fatal("Python nested-callable correction must invalidate the previous parser revision")
	}
}
