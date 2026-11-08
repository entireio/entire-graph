package sem

import "testing"

// Issue #259 — the sibling set of #199, for the four remaining grammars that
// emitted a callable declared inside a method BODY as a member of the enclosing
// type. The semantics differ per language and were established individually
// before any of them was widened; none of them makes `Widget.handler` reachable:
//
//   - Swift: a nested `func` is a LOCAL FUNCTION. It is visible only inside the
//     function that declares it, takes no `self`, and no expression names it.
//   - Kotlin: a nested `fun` is a LOCAL FUNCTION with the same rule.
//   - Rust: a nested `fn` is an ITEM scoped to the enclosing BLOCK. It cannot
//     take `self`, and `Widget::handler` resolves only to the impl's own
//     associated function.
//   - PHP: there is no nested function scope at all. Executing the enclosing
//     method declares the name in the CURRENT NAMESPACE, after which it is a
//     plain function; `Widget::handler()` is a fatal undefined-method call
//     either way. The enclosing callable is where it is written, which is the
//     only true containment statement available — the class is not one.
//
// Ruby is deliberately NOT here: its nested `def` really does add an instance
// method to the enclosing class, so the class qualification is correct. That
// verdict is pinned by TestNestedCallableScopeResetIsGatedByLanguage.
//
// As in Python, the phantom was not cosmetic. It shared a base compound-v1 ID
// with the real same-named member, so the signature-disambiguation branch fired
// for BOTH and moved the REAL member's published ID. Reproduced per language on
// the released binary, e.g. Kotlin:
//
//	before: method:Widget.handler#sig:996ba99efb0cd862  (the nested one)
//	        method:Widget.handler#sig:b1a795dc801504d8  (the real member)
//	after:  function:Widget.render.handler
//	        method:Widget.handler
func nestedCallableLanguageCases() []struct {
	language string
	path     string
	src      string
	memberID string
	nestedID string
} {
	return []struct {
		language string
		path     string
		src      string
		memberID string
		nestedID string
	}{{
		language: "Swift",
		path:     "widget.swift",
		src: "class Widget {\n" +
			"    func render(_ a: Int) -> Int {\n" +
			"        func handler(_ v: Int) -> Int {\n" +
			"            return v\n" +
			"        }\n" +
			"        return handler(a)\n" +
			"    }\n" +
			"\n" +
			"    func handler(_ a: Int) -> Int {\n" +
			"        return a + 1\n" +
			"    }\n" +
			"}\n",
		memberID: "local/r:Swift:widget.swift:method:Widget.handler",
		nestedID: "local/r:Swift:widget.swift:function:Widget.render.handler",
	}, {
		language: "Kotlin",
		path:     "widget.kt",
		src: "class Widget {\n" +
			"    fun render(a: Int): Int {\n" +
			"        fun handler(v: Int): Int {\n" +
			"            return v\n" +
			"        }\n" +
			"        return handler(a)\n" +
			"    }\n" +
			"\n" +
			"    fun handler(a: Int): Int {\n" +
			"        return a + 1\n" +
			"    }\n" +
			"}\n",
		memberID: "local/r:Kotlin:widget.kt:method:Widget.handler",
		nestedID: "local/r:Kotlin:widget.kt:function:Widget.render.handler",
	}, {
		language: "Rust",
		path:     "widget.rs",
		src: "pub struct Widget;\n" +
			"\n" +
			"impl Widget {\n" +
			"    pub fn render(&self, a: i32) -> i32 {\n" +
			"        fn handler(v: i32) -> i32 {\n" +
			"            v\n" +
			"        }\n" +
			"        handler(a)\n" +
			"    }\n" +
			"\n" +
			"    pub fn handler(&self, a: i32) -> i32 {\n" +
			"        a + 1\n" +
			"    }\n" +
			"}\n",
		memberID: "local/r:Rust:widget.rs:method:Widget.handler",
		nestedID: "local/r:Rust:widget.rs:function:Widget.render.handler",
	}, {
		language: "PHP",
		path:     "widget.php",
		src: "<?php\n" +
			"class Widget\n" +
			"{\n" +
			"    public function render($a)\n" +
			"    {\n" +
			"        function handler($v)\n" +
			"        {\n" +
			"            return $v;\n" +
			"        }\n" +
			"\n" +
			"        return handler($a);\n" +
			"    }\n" +
			"\n" +
			"    public function handler($a)\n" +
			"    {\n" +
			"        return $a + 1;\n" +
			"    }\n" +
			"}\n",
		memberID: "local/r:PHP:widget.php:method:Widget.handler",
		nestedID: "local/r:PHP:widget.php:function:Widget.render.handler",
	}}
}

func nestedCallableSymbols(t *testing.T, path, language, src string) []SymbolRecord {
	t.Helper()
	entities, _, status := TreeSitterParser{}.ParseWithStatus(path, src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	return entitySymbols("local/r", path, language, entities)
}

// The headline case, per language: a nested callable whose name collides with a
// real member of the same type. Both halves matter — the nested callable must
// not be a member, AND the real member must keep its bare ID.
func TestNestedCallableIsNotATypeMember(t *testing.T) {
	for _, testCase := range nestedCallableLanguageCases() {
		t.Run(testCase.language, func(t *testing.T) {
			symbols := nestedCallableSymbols(t, testCase.path, testCase.language, testCase.src)

			var member, nested []SymbolRecord
			for _, symbol := range symbols {
				switch symbol.QualifiedName {
				case "Widget.handler":
					member = append(member, symbol)
				case "Widget.render.handler":
					nested = append(nested, symbol)
				}
			}
			if len(member) != 1 || member[0].ID != testCase.memberID || member[0].Kind != "method" {
				t.Fatalf("real member = %s, want exactly one %s of kind method; all = %s",
					symbolIDs(member), testCase.memberID, symbolIDs(symbols))
			}
			if len(nested) != 1 {
				t.Fatalf("symbols named Widget.render.handler = %d, want 1; all = %s",
					len(nested), symbolIDs(symbols))
			}
			if nested[0].ID != testCase.nestedID {
				t.Errorf("nested callable ID = %q, want %q", nested[0].ID, testCase.nestedID)
			}
			if nested[0].Kind != "function" {
				t.Errorf("nested callable kind = %q, want %q", nested[0].Kind, "function")
			}
			if !nested[0].Local {
				t.Errorf("nested callable %s is not Local", nested[0].ID)
			}
			// No phantom member of the type may survive anywhere, including on
			// a `#sig:` ID.
			for _, symbol := range symbols {
				if symbol.QualifiedName == "Widget.handler" && symbol.ID != testCase.memberID {
					t.Errorf("phantom type member still emitted: %s", symbol.ID)
				}
			}
		})
	}
}

// The other direction: everything that legitimately IS a member must still be
// emitted with the container-qualified name resolution depends on. A fix that
// stops emitting phantom members by emitting fewer members is not a fix.
func TestTypeMembersSurviveTheNestedCallableFix(t *testing.T) {
	for _, testCase := range []struct {
		language string
		path     string
		src      string
		want     []struct{ id, qualified, kind string }
	}{{
		language: "Swift",
		path:     "widget.swift",
		src: "class Widget {\n" +
			"    var size: Int = 0\n" +
			"\n" +
			"    init() {}\n" +
			"\n" +
			"    func render(_ a: Int) -> Int {\n" +
			"        func helper(_ v: Int) -> Int {\n" +
			"            func deeper(_ w: Int) -> Int {\n" +
			"                return w\n" +
			"            }\n" +
			"            return deeper(v)\n" +
			"        }\n" +
			"        return helper(a)\n" +
			"    }\n" +
			"\n" +
			"    static func build() -> Int {\n" +
			"        return 1\n" +
			"    }\n" +
			"}\n",
		want: []struct{ id, qualified, kind string }{
			{"local/r:Swift:widget.swift:class:Widget", "Widget", "class"},
			{"local/r:Swift:widget.swift:method:Widget.render", "Widget.render", "method"},
			{"local/r:Swift:widget.swift:method:Widget.build", "Widget.build", "method"},
			{"local/r:Swift:widget.swift:function:Widget.render.helper", "Widget.render.helper", "function"},
			{"local/r:Swift:widget.swift:function:Widget.render.helper.deeper", "Widget.render.helper.deeper", "function"},
		},
	}, {
		language: "Kotlin",
		path:     "widget.kt",
		src: "class Widget {\n" +
			"    val size: Int = 0\n" +
			"\n" +
			"    fun render(a: Int): Int {\n" +
			"        fun helper(v: Int): Int {\n" +
			"            fun deeper(w: Int): Int {\n" +
			"                return w\n" +
			"            }\n" +
			"            return deeper(v)\n" +
			"        }\n" +
			"        return helper(a)\n" +
			"    }\n" +
			"\n" +
			"    fun build(): Int {\n" +
			"        return 1\n" +
			"    }\n" +
			"}\n",
		want: []struct{ id, qualified, kind string }{
			{"local/r:Kotlin:widget.kt:class:Widget", "Widget", "class"},
			{"local/r:Kotlin:widget.kt:method:Widget.render", "Widget.render", "method"},
			{"local/r:Kotlin:widget.kt:method:Widget.build", "Widget.build", "method"},
			{"local/r:Kotlin:widget.kt:function:Widget.render.helper", "Widget.render.helper", "function"},
			{"local/r:Kotlin:widget.kt:function:Widget.render.helper.deeper", "Widget.render.helper.deeper", "function"},
		},
	}, {
		language: "Rust",
		path:     "widget.rs",
		src: "pub struct Widget {\n" +
			"    pub size: i32,\n" +
			"}\n" +
			"\n" +
			"impl Widget {\n" +
			"    pub fn render(&self, a: i32) -> i32 {\n" +
			"        fn helper(v: i32) -> i32 {\n" +
			"            fn deeper(w: i32) -> i32 {\n" +
			"                w\n" +
			"            }\n" +
			"            deeper(v)\n" +
			"        }\n" +
			"        helper(a)\n" +
			"    }\n" +
			"\n" +
			"    pub fn build() -> i32 {\n" +
			"        1\n" +
			"    }\n" +
			"}\n",
		want: []struct{ id, qualified, kind string }{
			{"local/r:Rust:widget.rs:struct:Widget", "Widget", "struct"},
			{"local/r:Rust:widget.rs:field:Widget.size", "Widget.size", "field"},
			{"local/r:Rust:widget.rs:method:Widget.render", "Widget.render", "method"},
			{"local/r:Rust:widget.rs:method:Widget.build", "Widget.build", "method"},
			{"local/r:Rust:widget.rs:function:Widget.render.helper", "Widget.render.helper", "function"},
			{"local/r:Rust:widget.rs:function:Widget.render.helper.deeper", "Widget.render.helper.deeper", "function"},
		},
	}, {
		language: "PHP",
		path:     "widget.php",
		src: "<?php\n" +
			"class Widget\n" +
			"{\n" +
			"    public $size = 0;\n" +
			"\n" +
			"    public function render($a)\n" +
			"    {\n" +
			"        function helper($v)\n" +
			"        {\n" +
			"            function deeper($w)\n" +
			"            {\n" +
			"                return $w;\n" +
			"            }\n" +
			"\n" +
			"            return deeper($v);\n" +
			"        }\n" +
			"\n" +
			"        return helper($a);\n" +
			"    }\n" +
			"\n" +
			"    public static function build()\n" +
			"    {\n" +
			"        return 1;\n" +
			"    }\n" +
			"}\n",
		want: []struct{ id, qualified, kind string }{
			{"local/r:PHP:widget.php:class:Widget", "Widget", "class"},
			// PHP property declarations emit no field symbol today
			// (pre-existing, untouched here), so `$size` is deliberately not
			// listed — asserting it would pin an unrelated gap as a
			// requirement of this fix.
			{"local/r:PHP:widget.php:method:Widget.render", "Widget.render", "method"},
			{"local/r:PHP:widget.php:method:Widget.build", "Widget.build", "method"},
			{"local/r:PHP:widget.php:function:Widget.render.helper", "Widget.render.helper", "function"},
			{"local/r:PHP:widget.php:function:Widget.render.helper.deeper", "Widget.render.helper.deeper", "function"},
		},
	}} {
		t.Run(testCase.language, func(t *testing.T) {
			symbols := nestedCallableSymbols(t, testCase.path, testCase.language, testCase.src)
			byID := map[string]SymbolRecord{}
			for _, symbol := range symbols {
				byID[symbol.ID] = symbol
			}
			for _, want := range testCase.want {
				got, ok := byID[want.id]
				if !ok {
					t.Errorf("missing symbol %s; all = %s", want.id, symbolIDs(symbols))
					continue
				}
				if got.QualifiedName != want.qualified || got.Kind != want.kind {
					t.Errorf("symbol %s = kind %q qualified %q, want kind %q qualified %q",
						want.id, got.Kind, got.QualifiedName, want.kind, want.qualified)
				}
			}
		})
	}
}

// Two types each declaring a same-named helper inside a same-named method must
// stay distinct. This is why the type scope is REPLACED rather than cleared:
// clearing it would give both helpers one base ID, so adding the second type
// would move the first onto a `#sig:` ID — the instability the fix exists to
// remove, re-entering by the back door.
func TestNestedCallableIDSurvivesAnUnrelatedType(t *testing.T) {
	for _, testCase := range []struct {
		language string
		path     string
		typeA    string
		typeB    string
		wantA    string
		wantB    string
	}{{
		language: "Swift",
		path:     "widget.swift",
		typeA: "class A {\n" +
			"    func m() -> Int {\n" +
			"        func helper(_ v: Int) -> Int {\n" +
			"            return v\n" +
			"        }\n" +
			"        return helper(1)\n" +
			"    }\n" +
			"}\n",
		typeB: "class B {\n" +
			"    func m() -> Int {\n" +
			"        func helper(_ v: Int) -> Int {\n" +
			"            return v\n" +
			"        }\n" +
			"        return helper(1)\n" +
			"    }\n" +
			"}\n",
		wantA: "local/r:Swift:widget.swift:function:A.m.helper",
		wantB: "local/r:Swift:widget.swift:function:B.m.helper",
	}, {
		language: "Kotlin",
		path:     "widget.kt",
		typeA: "class A {\n" +
			"    fun m(): Int {\n" +
			"        fun helper(v: Int): Int {\n" +
			"            return v\n" +
			"        }\n" +
			"        return helper(1)\n" +
			"    }\n" +
			"}\n",
		typeB: "class B {\n" +
			"    fun m(): Int {\n" +
			"        fun helper(v: Int): Int {\n" +
			"            return v\n" +
			"        }\n" +
			"        return helper(1)\n" +
			"    }\n" +
			"}\n",
		wantA: "local/r:Kotlin:widget.kt:function:A.m.helper",
		wantB: "local/r:Kotlin:widget.kt:function:B.m.helper",
	}, {
		language: "Rust",
		path:     "widget.rs",
		typeA: "impl A {\n" +
			"    pub fn m(&self) -> i32 {\n" +
			"        fn helper(v: i32) -> i32 {\n" +
			"            v\n" +
			"        }\n" +
			"        helper(1)\n" +
			"    }\n" +
			"}\n",
		typeB: "impl B {\n" +
			"    pub fn m(&self) -> i32 {\n" +
			"        fn helper(v: i32) -> i32 {\n" +
			"            v\n" +
			"        }\n" +
			"        helper(1)\n" +
			"    }\n" +
			"}\n",
		wantA: "local/r:Rust:widget.rs:function:A.m.helper",
		wantB: "local/r:Rust:widget.rs:function:B.m.helper",
	}, {
		// PHP would fatally redeclare `helper` if both methods ran, so the two
		// classes live in separate namespaces — which is how the shape occurs
		// in real PHP, and still exercises the two-types-one-file case.
		language: "PHP",
		path:     "widget.php",
		typeA: "<?php\n" +
			"namespace First;\n" +
			"class A\n" +
			"{\n" +
			"    public function m()\n" +
			"    {\n" +
			"        function helper($v)\n" +
			"        {\n" +
			"            return $v;\n" +
			"        }\n" +
			"\n" +
			"        return helper(1);\n" +
			"    }\n" +
			"}\n",
		typeB: "namespace Second;\n" +
			"class B\n" +
			"{\n" +
			"    public function m()\n" +
			"    {\n" +
			"        function helper($v)\n" +
			"        {\n" +
			"            return $v;\n" +
			"        }\n" +
			"\n" +
			"        return helper(1);\n" +
			"    }\n" +
			"}\n",
		wantA: "local/r:PHP:widget.php:function:A.m.helper",
		wantB: "local/r:PHP:widget.php:function:B.m.helper",
	}} {
		t.Run(testCase.language, func(t *testing.T) {
			only := nestedCallableSymbols(t, testCase.path, testCase.language, testCase.typeA)
			found := false
			for _, symbol := range only {
				if symbol.ID == testCase.wantA {
					found = true
				}
			}
			if !found {
				t.Fatalf("helper in type A alone = %s, want %s", symbolIDs(only), testCase.wantA)
			}

			// Adding an unrelated type must not move A's helper.
			both := nestedCallableSymbols(t, testCase.path, testCase.language, testCase.typeA+"\n"+testCase.typeB)
			foundA, foundB := false, false
			for _, symbol := range both {
				switch symbol.ID {
				case testCase.wantA:
					foundA = true
				case testCase.wantB:
					foundB = true
				}
			}
			if !foundA || !foundB {
				t.Errorf("helpers after adding type B = %s, want both %s and %s at bare IDs",
					symbolIDs(both), testCase.wantA, testCase.wantB)
			}
		})
	}
}

// A callable nested in a TOP-LEVEL function has no type scope to replace, so
// nothing about it changes. This pins that the fix moved only the IDs it had to
// — an unqualified nested callable keeps the bare ID it has always had, which
// was verified on the released binary before the change.
func TestNestedCallableInTopLevelFunctionIsUnchanged(t *testing.T) {
	for _, testCase := range []struct {
		language string
		path     string
		src      string
		wantID   string
	}{{
		language: "Swift",
		path:     "widget.swift",
		src: "func topLevel() -> Int {\n" +
			"    func inner() -> Int {\n" +
			"        return 1\n" +
			"    }\n" +
			"    return inner()\n" +
			"}\n",
		wantID: "local/r:Swift:widget.swift:function:inner",
	}, {
		language: "Kotlin",
		path:     "widget.kt",
		src: "fun topLevel(): Int {\n" +
			"    fun inner(): Int {\n" +
			"        return 1\n" +
			"    }\n" +
			"    return inner()\n" +
			"}\n",
		wantID: "local/r:Kotlin:widget.kt:function:inner",
	}, {
		language: "Rust",
		path:     "widget.rs",
		src: "pub fn top_level() -> i32 {\n" +
			"    fn inner() -> i32 {\n" +
			"        1\n" +
			"    }\n" +
			"    inner()\n" +
			"}\n",
		wantID: "local/r:Rust:widget.rs:function:inner",
	}, {
		language: "PHP",
		path:     "widget.php",
		src: "<?php\n" +
			"function topLevel()\n" +
			"{\n" +
			"    function inner()\n" +
			"    {\n" +
			"        return 1;\n" +
			"    }\n" +
			"\n" +
			"    return inner();\n" +
			"}\n",
		wantID: "local/r:PHP:widget.php:function:inner",
	}} {
		t.Run(testCase.language, func(t *testing.T) {
			symbols := nestedCallableSymbols(t, testCase.path, testCase.language, testCase.src)
			var inner []SymbolRecord
			for _, symbol := range symbols {
				if symbol.Name == "inner" {
					inner = append(inner, symbol)
				}
			}
			if len(inner) != 1 {
				t.Fatalf("symbols named inner = %d, want 1; all = %s", len(inner), symbolIDs(symbols))
			}
			if inner[0].ID != testCase.wantID {
				t.Errorf("callable nested in a top-level function = %s, want the unqualified %s",
					inner[0].ID, testCase.wantID)
			}
		})
	}
}

// A TYPE declared inside a method body scopes its own members again, so the
// re-anchoring must not leak past it: a method of that type is a member of it,
// not a lexical binding of whatever method the type happens to sit in. This is
// the guard that keeps the widened gate from demoting real members — the risk
// that made these four worth their own node-type sets rather than one shared
// one.
func TestNestedTypeMembersAreNotDemotedByTheScopeReset(t *testing.T) {
	for _, testCase := range []struct {
		language string
		path     string
		src      string
		wantID   string
	}{{
		language: "Swift",
		path:     "widget.swift",
		src: "class A {\n" +
			"    func m() {\n" +
			"        class C {\n" +
			"            func f() -> Int {\n" +
			"                return 1\n" +
			"            }\n" +
			"        }\n" +
			"    }\n" +
			"}\n",
		wantID: "local/r:Swift:widget.swift:method:C.f",
	}, {
		language: "Kotlin",
		path:     "widget.kt",
		src: "class A {\n" +
			"    fun m() {\n" +
			"        class C {\n" +
			"            fun f(): Int {\n" +
			"                return 1\n" +
			"            }\n" +
			"        }\n" +
			"    }\n" +
			"}\n",
		wantID: "local/r:Kotlin:widget.kt:method:C.f",
	}, {
		language: "Rust",
		path:     "widget.rs",
		src: "impl A {\n" +
			"    pub fn m(&self) {\n" +
			"        struct C;\n" +
			"        impl C {\n" +
			"            fn f(&self) -> i32 {\n" +
			"                1\n" +
			"            }\n" +
			"        }\n" +
			"    }\n" +
			"}\n",
		wantID: "local/r:Rust:widget.rs:method:C.f",
	}, {
		language: "PHP",
		path:     "widget.php",
		src: "<?php\n" +
			"class A\n" +
			"{\n" +
			"    public function m()\n" +
			"    {\n" +
			"        class C\n" +
			"        {\n" +
			"            public function f()\n" +
			"            {\n" +
			"                return 1;\n" +
			"            }\n" +
			"        }\n" +
			"    }\n" +
			"}\n",
		wantID: "local/r:PHP:widget.php:method:C.f",
	}} {
		t.Run(testCase.language, func(t *testing.T) {
			symbols := nestedCallableSymbols(t, testCase.path, testCase.language, testCase.src)
			var found []SymbolRecord
			for _, symbol := range symbols {
				if symbol.Name == "f" {
					found = append(found, symbol)
				}
			}
			if len(found) != 1 {
				t.Fatalf("symbols named f = %d, want 1; all = %s", len(found), symbolIDs(symbols))
			}
			if found[0].ID != testCase.wantID {
				t.Errorf("member of a type declared in a method body = %s, want %s",
					found[0].ID, testCase.wantID)
			}
		})
	}
}
