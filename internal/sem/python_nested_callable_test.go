package sem

import "testing"

// A callable declared inside a Python method BODY is a local binding of that
// method, not a member of the enclosing class.
//
// `def helper(v)` inside `Widget.render()` inherited the class scope and was
// emitted as the method `Widget.helper` — a symbol naming a member the class
// does not have. Python has no way to reach it: the name is bound in the
// method's local frame when the method runs and is gone when it returns.
//
// It then shared a base compound-v1 ID with a real same-named member, so the
// disambiguation branch in entitySymbols fired for BOTH and the real method's
// published ID moved during an ordinary edit to an unrelated method body
// (reproduced on the released binary before this fix):
//
//	before: local/r:Python:widget.py:method:Widget.handler
//	after:  local/r:Python:widget.py:method:Widget.handler#sig:a70a679061a4eab3
//
// The rule pinned here matches the JS/TS one in TestJavaScriptNestedCallableIs
// NotAClassMember: a nested callable is qualified by the CALLABLE that declares
// it (`Widget.render.handler`, kind "function", Local), not by the type. The
// type scope is REPLACED, not cleared — clearing it would make `helper` inside
// `A.m` and `helper` inside `B.m` share one base ID, which is the same
// instability under a different spelling.
func TestPythonNestedCallableIsNotAClassMember(t *testing.T) {
	symbolsFor := func(t *testing.T, src string) []SymbolRecord {
		t.Helper()
		entities, _, status := TreeSitterParser{}.ParseWithStatus("widget.py", src)
		if status.ParseError {
			t.Fatalf("unexpected parse error: %s", status.Detail)
		}
		return entitySymbols("local/r", "widget.py", "Python", entities)
	}
	findOne := func(t *testing.T, symbols []SymbolRecord, name string) SymbolRecord {
		t.Helper()
		var found []SymbolRecord
		for _, symbol := range symbols {
			if symbol.Name == name {
				found = append(found, symbol)
			}
		}
		if len(found) != 1 {
			t.Fatalf("symbols named %s = %d, want 1; all = %s", name, len(found), symbolIDs(symbols))
		}
		return found[0]
	}

	const wantMemberID = "local/r:Python:widget.py:method:Widget.handler"

	// Control: with no nested callable the real class member owns the bare ID.
	const noNested = "class Widget:\n" +
		"    def render(self, a):\n" +
		"        return a\n" +
		"\n" +
		"    def handler(self, a):\n" +
		"        return a + 1\n"
	if got := findOne(t, symbolsFor(t, noNested), "handler"); got.ID != wantMemberID {
		t.Fatalf("baseline member = %s, want the bare ID %s", got.ID, wantMemberID)
	}

	for _, testCase := range []struct {
		name       string
		src        string
		wantNested SymbolRecord
	}{{
		// The headline case: a nested def whose name collides with a real
		// member of the same class. Both halves matter — the nested callable
		// must not be a member, AND the real member must keep its bare ID.
		name: "def colliding with a real member",
		src: "class Widget:\n" +
			"    def render(self, a):\n" +
			"        def handler(v):\n" +
			"            return v\n" +
			"        return handler(a)\n" +
			"\n" +
			"    def handler(self, a):\n" +
			"        return a + 1\n",
		wantNested: SymbolRecord{
			Kind: "function", Name: "handler", QualifiedName: "Widget.render.handler",
			ID: "local/r:Python:widget.py:function:Widget.render.handler",
		},
	}, {
		name: "async def",
		src: "class Widget:\n" +
			"    def render(self, a):\n" +
			"        async def handler(v):\n" +
			"            return v\n" +
			"        return handler\n" +
			"\n" +
			"    def handler(self, a):\n" +
			"        return a + 1\n",
		wantNested: SymbolRecord{
			Kind: "function", Name: "handler", QualifiedName: "Widget.render.handler",
			ID: "local/r:Python:widget.py:function:Widget.render.handler",
		},
	}, {
		// A decorated nested def arrives as decorated_definition wrapping the
		// function_definition; the walk reaches the inner node, so the same
		// rule must hold.
		name: "decorated def",
		src: "class Widget:\n" +
			"    def render(self, a):\n" +
			"        @staticmethod\n" +
			"        def handler(v):\n" +
			"            return v\n" +
			"        return handler(a)\n" +
			"\n" +
			"    def handler(self, a):\n" +
			"        return a + 1\n",
		wantNested: SymbolRecord{
			Kind: "function", Name: "handler", QualifiedName: "Widget.render.handler",
			ID: "local/r:Python:widget.py:function:Widget.render.handler",
		},
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			symbols := symbolsFor(t, testCase.src)

			// The real member's published ID must NOT have moved.
			member := SymbolRecord{}
			var nested []SymbolRecord
			for _, symbol := range symbols {
				if symbol.QualifiedName == "Widget.handler" {
					member = symbol
				}
				if symbol.QualifiedName == testCase.wantNested.QualifiedName {
					nested = append(nested, symbol)
				}
			}
			if member.ID != wantMemberID {
				t.Errorf("real member ID = %q, want the unmoved %q; all = %s",
					member.ID, wantMemberID, symbolIDs(symbols))
			}
			if len(nested) != 1 {
				t.Fatalf("symbols named %s = %d, want 1; all = %s",
					testCase.wantNested.QualifiedName, len(nested), symbolIDs(symbols))
			}
			got := nested[0]
			if got.Kind != testCase.wantNested.Kind {
				t.Errorf("nested callable kind = %q, want %q", got.Kind, testCase.wantNested.Kind)
			}
			if got.ID != testCase.wantNested.ID {
				t.Errorf("nested callable ID = %q, want %q", got.ID, testCase.wantNested.ID)
			}
			if !got.Local {
				t.Errorf("nested callable %s is not Local", got.ID)
			}
			// No phantom member of the class may survive anywhere.
			for _, symbol := range symbols {
				if symbol.QualifiedName == "Widget.handler" && symbol.Kind == "method" && symbol.ID != wantMemberID {
					t.Errorf("phantom class member still emitted: %s", symbol.ID)
				}
			}
		})
	}
}

// The other direction: everything that legitimately IS a class member must
// still be emitted, with the container-qualified name resolution depends on.
// A fix that stops emitting phantom members by emitting fewer members is not a
// fix.
func TestPythonClassMembersSurviveTheNestedCallableFix(t *testing.T) {
	const src = "class Widget:\n" +
		"    CONSTANT = 1\n" +
		"\n" +
		"    def __init__(self):\n" +
		"        self.x = 1\n" +
		"\n" +
		"    def render(self, a):\n" +
		"        def helper(v):\n" +
		"            def deeper(w):\n" +
		"                return w\n" +
		"            return deeper(v)\n" +
		"        return helper(a)\n" +
		"\n" +
		"    @property\n" +
		"    def size(self):\n" +
		"        return 2\n" +
		"\n" +
		"    @staticmethod\n" +
		"    def build():\n" +
		"        return Widget()\n" +
		"\n" +
		"    class Inner:\n" +
		"        def f(self):\n" +
		"            return 3\n"

	entities, _, status := TreeSitterParser{}.ParseWithStatus("widget.py", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	symbols := entitySymbols("local/r", "widget.py", "Python", entities)

	byID := map[string]SymbolRecord{}
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	for _, want := range []struct{ id, qualified, kind string }{
		{"local/r:Python:widget.py:class:Widget", "Widget", "class"},
		{"local/r:Python:widget.py:method:Widget.__init__", "Widget.__init__", "method"},
		{"local/r:Python:widget.py:method:Widget.render", "Widget.render", "method"},
		{"local/r:Python:widget.py:method:Widget.size", "Widget.size", "method"},
		{"local/r:Python:widget.py:method:Widget.build", "Widget.build", "method"},
		// A nested class scopes its own members. Python nested classes are not
		// qualified by the outer class today (pre-existing behaviour, untouched
		// here); what matters is that `f` is a member of `Inner` and that this
		// fix did not move either ID.
		{"local/r:Python:widget.py:class:Inner", "Inner", "class"},
		{"local/r:Python:widget.py:method:Inner.f", "Inner.f", "method"},
		// The nested helper is still findable as itself, one level down too.
		{"local/r:Python:widget.py:function:Widget.render.helper", "Widget.render.helper", "function"},
		{"local/r:Python:widget.py:function:Widget.render.helper.deeper", "Widget.render.helper.deeper", "function"},
	} {
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
}

// A callable nested in a TOP-LEVEL function has no type scope to replace, so
// nothing about it changes. This pins that the fix moved only the IDs it had
// to — an unqualified nested def keeps the bare ID it has always had.
func TestPythonNestedCallableInTopLevelFunctionIsUnchanged(t *testing.T) {
	const src = "def top_level():\n" +
		"    def inner():\n" +
		"        return 1\n" +
		"    return inner()\n"

	entities, _, status := TreeSitterParser{}.ParseWithStatus("widget.py", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	symbols := entitySymbols("local/r", "widget.py", "Python", entities)

	var inner []SymbolRecord
	for _, symbol := range symbols {
		if symbol.Name == "inner" {
			inner = append(inner, symbol)
		}
	}
	if len(inner) != 1 {
		t.Fatalf("symbols named inner = %d, want 1; all = %s", len(inner), symbolIDs(symbols))
	}
	if want := "local/r:Python:widget.py:function:inner"; inner[0].ID != want {
		t.Errorf("callable nested in a top-level function = %s, want the unqualified %s",
			inner[0].ID, want)
	}
}

// Two classes each declaring a same-named helper inside a same-named method
// must stay distinct. This is why the type scope is REPLACED rather than
// cleared: clearing it would give both helpers one base ID, so adding the
// second class would move the first onto a `#sig:` ID — the instability the
// fix exists to remove, re-entering by the back door.
func TestPythonNestedCallableIDSurvivesAnUnrelatedClass(t *testing.T) {
	const classA = "class A:\n" +
		"    def m(self):\n" +
		"        def helper(v):\n" +
		"            return v\n" +
		"        return helper(1)\n"
	const classB = "class B:\n" +
		"    def m(self):\n" +
		"        def helper(v):\n" +
		"            return v\n" +
		"        return helper(1)\n"

	symbolsFor := func(t *testing.T, src string) []SymbolRecord {
		t.Helper()
		entities, _, status := TreeSitterParser{}.ParseWithStatus("widget.py", src)
		if status.ParseError {
			t.Fatalf("unexpected parse error: %s", status.Detail)
		}
		return entitySymbols("local/r", "widget.py", "Python", entities)
	}

	const wantA = "local/r:Python:widget.py:function:A.m.helper"
	only := symbolsFor(t, classA)
	foundOnly := false
	for _, symbol := range only {
		if symbol.ID == wantA {
			foundOnly = true
		}
	}
	if !foundOnly {
		t.Fatalf("helper in class A alone = %s, want %s", symbolIDs(only), wantA)
	}

	// Adding an unrelated class must not move A's helper.
	both := symbolsFor(t, classA+"\n"+classB)
	foundA, foundB := false, false
	for _, symbol := range both {
		switch symbol.ID {
		case wantA:
			foundA = true
		case "local/r:Python:widget.py:function:B.m.helper":
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("helpers after adding class B = %s, want both %s and B.m.helper at bare IDs",
			symbolIDs(both), wantA)
	}
}
