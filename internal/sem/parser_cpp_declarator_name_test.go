package sem

import "testing"

// C++ function extraction used to read a definition's name off the first name
// node in pre-order, which is the RETURN TYPE whenever the return type is a
// named type rather than a builtin keyword. `std::string Config::name() const`
// became a symbol called `string`, and every other std::string-returning
// function in the repository collapsed onto that same name — destroying name
// search, symbol identity (the golden fixture needed `#sig:` suffixes to keep
// two functions apart) and call resolution along with it. The name now comes
// from the `declarator` field, as it already did for C and Objective-C.

func parseCPlusPlus(t *testing.T, src string) map[string]string {
	t.Helper()
	entities, lang, status := TreeSitterParser{}.ParseWithStatus("sample.cpp", src)
	if lang != "C++" {
		t.Fatalf("expected C++, got %s", lang)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	kinds := map[string]string{}
	for _, entity := range entities {
		kinds[entity.Name] = entity.Kind
	}
	return kinds
}

func TestCPlusPlusFunctionNamesComeFromDeclarator(t *testing.T) {
	src := `#include <string>
#include <vector>

namespace ns { struct Thing { int v; }; }

struct MyType { int v; };

MyType makeThing() { return MyType{}; }

std::string toText(int a) { return ""; }

std::vector<int> *ptrFn(int a) { return nullptr; }

const MyType &refFn() { static MyType m; return m; }

ns::Thing Holder::build() { return ns::Thing{}; }

int plain(int a) { return a; }

template <typename T>
T identity(T x) { return x; }
`
	kinds := parseCPlusPlus(t, src)
	for _, name := range []string{"makeThing", "toText", "ptrFn", "refFn", "build", "plain", "identity"} {
		if kinds[name] != "function" {
			t.Fatalf("C++ function %q not extracted as a function: %#v", name, kinds)
		}
	}
	// The return types must stay types, never be mistaken for the callables.
	if kinds["MyType"] != "struct" {
		t.Fatalf("return type MyType should remain the struct symbol: %#v", kinds)
	}
	for _, leaked := range []string{"string", "vector", "T"} {
		if _, ok := kinds[leaked]; ok {
			t.Fatalf("return type %q leaked in as a symbol name: %#v", leaked, kinds)
		}
	}
}

func TestCPlusPlusMemberNamesComeFromDeclarator(t *testing.T) {
	src := `#include <string>

struct MyType { int v; };

class Holder {
public:
  MyType get() const { return MyType{}; }
  std::string label() const { return ""; }
  int size() const { return 1; }
};

template <typename T>
class Box {
public:
  T unwrap() const { return T{}; }
};
`
	kinds := parseCPlusPlus(t, src)
	for _, name := range []string{"Holder.get", "Holder.label", "Holder.size", "Box.unwrap"} {
		if kinds[name] != "method" {
			t.Fatalf("C++ member %q not extracted as a method: %#v", name, kinds)
		}
	}
	for _, leaked := range []string{"Holder.MyType", "Holder.string", "Box.T"} {
		if _, ok := kinds[leaked]; ok {
			t.Fatalf("return type leaked into member name %q: %#v", leaked, kinds)
		}
	}
}

// Names that were already correct before the declarator fix must stay correct:
// out-of-line definitions, constructors and destructors all name themselves
// through declarator shapes the walk has to keep handling.
func TestCPlusPlusQualifiedAndSpecialNamesPreserved(t *testing.T) {
	src := `class Widget {
public:
  Widget();
  ~Widget();
};

Widget::Widget() {}

Widget::~Widget() {}

int Widget::helper(int a) { return a; }

void Widget::reset() {}
`
	kinds := parseCPlusPlus(t, src)
	for _, name := range []string{"helper", "reset", "Widget"} {
		if _, ok := kinds[name]; !ok {
			t.Fatalf("C++ symbol %q missing: %#v", name, kinds)
		}
	}
}

// An overload declares its name with `operator_name` (`operator new`) or, for a
// conversion, `operator_cast` (`operator const char *`, canonicalised to
// `operator const char*`) — neither of which contains an identifier node. A declarator walk that does not model them drops
// out to the pre-order identifier search, which steps past the operator into
// the parameter list, so `void *operator new(size_t n)` was named `n` and
// `void operator delete(void *p, size_t n)` was named `p`.
//
// A reference return type is the same class of miss: tree-sitter-cpp hangs the
// declarator a `reference_declarator` wraps off an unnamed child rather than a
// `declarator` field, so the field walk stops at the `&`. For a member function
// the fallback then skips the method's own `field_identifier` and returns the
// first parameter instead — `Cell &at(int i)` was named `i`.
//
// (Note: `operator==`, `operator+`, `operator[]`, `operator()` and `operator<<`
// never reach this walk. maskCPlusPlusOperatorCall rewrites `operator<op>(` to
// `op(` before the source is parsed, so tree-sitter sees an ordinary function
// named `op` and every such overload in a repository collapses onto that one
// name. That is a separate pre-existing defect in the C++ pre-parse mask, not
// in the declarator walk, and is unchanged by this test.)
func TestCPlusPlusOperatorNamesComeFromDeclarator(t *testing.T) {
	src := `#include <cstddef>

struct Cell { int v; };

class Grid {
public:
  int &at(int i) { return v; }
  const Cell &cell(int r) const { return c; }
  Cell *ptr(int i) { return &c; }
  void *operator new(size_t n) { return nullptr; }
  void operator delete(void *p, size_t n) {}
  void *operator new[](size_t n) { return nullptr; }
  operator const char *() const { return ""; }
  int v;
  Cell c;
};

void *operator new(size_t sz) { return nullptr; }
`
	kinds := parseCPlusPlus(t, src)
	for name, want := range map[string]string{
		"Grid.at":                   "method",
		"Grid.cell":                 "method",
		"Grid.ptr":                  "method",
		"Grid.operator new":         "method",
		"Grid.operator delete":      "method",
		"Grid.operator new[]":       "method",
		"Grid.operator const char*": "method",
		"operator new":              "function",
	} {
		if kinds[name] != want {
			t.Fatalf("C++ %q not extracted as a %s: %#v", name, want, kinds)
		}
	}
	// Parameter names must never become the callable's name.
	for _, leaked := range []string{"Grid.i", "Grid.r", "Grid.n", "Grid.p", "n", "sz"} {
		if _, ok := kinds[leaked]; ok {
			t.Fatalf("parameter name %q leaked in as a symbol name: %#v", leaked, kinds)
		}
	}
}

// TestCPlusPlusConversionOperatorKeepsAParenthesizedTarget pins the cut point for a
// conversion operator's name.
//
// A conversion operator is named for the type it converts to, and that type may contain
// parentheses of its own -- `decltype(...)` and function pointers both do. Cutting the
// name at the first '(' truncated the type, so `operator decltype(Value::v)()` was named
// `operator decltype` and every conversion whose type merely BEGAN the same way collapsed
// onto one name and one symbol ID: the same identity destruction this file's original
// return-type bug caused. The cut is structural now -- the type is what precedes the
// operator's own parameter list.
func TestCPlusPlusConversionOperatorKeepsAParenthesizedTarget(t *testing.T) {
	src := `struct Value { int v; };

struct S {
  operator decltype(Value::v)() const { return 0; }
  operator const char*() const { return ""; }
  operator void(*)(int)() const { return nullptr; }
  operator void(*)(double)() const { return nullptr; }
};
`
	kinds := parseCPlusPlus(t, src)
	for _, name := range []string{
		"S.operator decltype(Value::v)",
		"S.operator const char*",
		"S.operator void(*)(int)",
		"S.operator void(*)(double)",
	} {
		if kinds[name] == "" {
			t.Fatalf("conversion operator %q not extracted; got %#v", name, kinds)
		}
	}
	// The truncated spelling is what collided the identities, so it must be gone.
	if _, truncated := kinds["S.operator decltype"]; truncated {
		t.Fatalf("name still cut at the type's own parenthesis: %#v", kinds)
	}
}
