package sem

import (
	"strings"
	"testing"
)

func TestParserIdentityRevisionsSnapshotAndCaches(t *testing.T) {
	header := leanHeader(sourceContext{}, "same-release", profileSpec{})
	if header.IdentityRevision != "4" {
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

// Issue #259: the same argument for Swift, Kotlin, Rust and PHP. The Python
// half shipped at revision 3, so a build carrying only that half wrote records
// under the revision-3 namespace with these four languages still phantom-laden.
// Landing this correction at revision 3 would serve those records back and the
// fix would be invisible for exactly the languages it covers, so it takes its
// own bump.
func TestRemainingNestedCallableCorrectionsAreRevisioned(t *testing.T) {
	for _, testCase := range []struct {
		language  string
		path      string
		src       string
		corrected string
		phantom   string
	}{{
		language:  "Swift",
		path:      "c.swift",
		src:       "class C {\n    func m() -> Int {\n        func helper(_ v: Int) -> Int {\n            return v\n        }\n        return helper(1)\n    }\n}\n",
		corrected: "local/example:Swift:c.swift:function:C.m.helper",
		phantom:   "local/example:Swift:c.swift:method:C.helper",
	}, {
		language:  "Kotlin",
		path:      "c.kt",
		src:       "class C {\n    fun m(): Int {\n        fun helper(v: Int): Int {\n            return v\n        }\n        return helper(1)\n    }\n}\n",
		corrected: "local/example:Kotlin:c.kt:function:C.m.helper",
		phantom:   "local/example:Kotlin:c.kt:method:C.helper",
	}, {
		language:  "Rust",
		path:      "c.rs",
		src:       "impl C {\n    pub fn m(&self) -> i32 {\n        fn helper(v: i32) -> i32 {\n            v\n        }\n        helper(1)\n    }\n}\n",
		corrected: "local/example:Rust:c.rs:function:C.m.helper",
		phantom:   "local/example:Rust:c.rs:method:C.helper",
	}, {
		language:  "PHP",
		path:      "c.php",
		src:       "<?php\nclass C\n{\n    public function m()\n    {\n        function helper($v)\n        {\n            return $v;\n        }\n\n        return helper(1);\n    }\n}\n",
		corrected: "local/example:PHP:c.php:function:C.m.helper",
		phantom:   "local/example:PHP:c.php:method:C.helper",
	}} {
		t.Run(testCase.language, func(t *testing.T) {
			entities, _, status := TreeSitterParser{}.ParseWithStatus(testCase.path, testCase.src)
			if status.ParseError {
				t.Fatalf("unexpected parse error: %s", status.Detail)
			}
			symbols := entitySymbols("local/example", testCase.path, testCase.language, entities)
			corrected := false
			for _, symbol := range symbols {
				if symbol.ID == testCase.corrected {
					corrected = true
				}
				if symbol.ID == testCase.phantom {
					t.Errorf("phantom class member still emitted: %s", symbol.ID)
				}
			}
			if !corrected {
				t.Fatalf("corrected nested callable missing; symbols = %s", symbolIDs(symbols))
			}
		})
	}
	if IdentityRevision == "3" {
		t.Fatal("the Swift/Kotlin/Rust/PHP nested-callable corrections must invalidate revision 3")
	}
}
