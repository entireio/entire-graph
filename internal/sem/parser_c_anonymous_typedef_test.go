package sem

import "testing"

func anonIndex(t *testing.T, path, content string) map[string]Entity {
	t.Helper()
	entities, _ := TreeSitterParser{}.Parse(path, content)
	out := make(map[string]Entity, len(entities))
	for _, entity := range entities {
		out[entity.Kind+":"+entity.Name] = entity
	}
	return out
}

func anonKeys(in map[string]Entity) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return out
}

// TestCAnonymousTypedefStructIsNotNamedAfterItsFirstField covers the most
// common way a C type is declared. `typedef struct {int x; int y;} Point;` has
// no tag: the type's only name is the typedef's, and the specifier itself is
// anonymous. nodeName has no name node to read, so it fell through to the first
// identifier in pre-order — the first FIELD — and emitted a struct definition
// called `x`. That symbol names nothing that exists: a search for the type
// finds a field name, and a search for `x` finds a struct.
func TestCAnonymousTypedefStructIsNotNamedAfterItsFirstField(t *testing.T) {
	t.Parallel()
	got := anonIndex(t, "point.c", `typedef struct {int x; int y;} Point;

typedef enum {RED, GREEN} Color;

typedef struct {int w;} *Handle;
`)
	// The typedef carries the type's real name and is unchanged.
	for _, want := range []string{"type:Point", "type:Color", "type:Handle"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, anonKeys(got))
		}
	}
	// No definition may be named after a member of the thing being defined.
	for _, unwanted := range []string{"struct:x", "struct:y", "enum:RED", "enum:GREEN", "struct:w"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("emitted %q, a definition named after a member: %#v", unwanted, anonKeys(got))
		}
	}
}

// TestCNamedStructDefinitionsSurviveTheAnonymousCheck keeps the check to
// specifiers that genuinely have no tag. A tagged definition names itself and
// must still be extracted, whether or not a typedef wraps it.
func TestCNamedStructDefinitionsSurviveTheAnonymousCheck(t *testing.T) {
	t.Parallel()
	got := anonIndex(t, "ledger.c", `struct Plain {int z;};

typedef struct Named {int a;} NamedAlias;

enum Mode {FAST, SLOW};

typedef enum Level {LOW, HIGH} LevelAlias;
`)
	for _, want := range []string{
		"struct:Plain",
		"struct:Named", "type:NamedAlias",
		"enum:Mode",
		"enum:Level", "type:LevelAlias",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, anonKeys(got))
		}
	}
}

// TestCPlusPlusAnonymousSpecifiersAreNotNamedAfterMembers covers the same shape
// in C++, where an anonymous struct or union nested in a class is idiomatic and
// would otherwise contribute a definition named after its first member.
func TestCPlusPlusAnonymousSpecifiersAreNotNamedAfterMembers(t *testing.T) {
	t.Parallel()
	got := anonIndex(t, "ledger.hpp", `typedef struct {int x;} Point;

struct Outer {
    struct {int nested;} anon;
};
`)
	if _, ok := got["type:Point"]; !ok {
		t.Errorf("missing type:Point: %#v", anonKeys(got))
	}
	if _, ok := got["struct:Outer"]; !ok {
		t.Errorf("missing struct:Outer: %#v", anonKeys(got))
	}
	for _, unwanted := range []string{"struct:x", "struct:nested", "struct:Outer.nested"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("emitted %q, a definition named after a member: %#v", unwanted, anonKeys(got))
		}
	}
}
