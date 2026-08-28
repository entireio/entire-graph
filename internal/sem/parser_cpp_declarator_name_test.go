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
