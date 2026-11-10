package sem

import (
	"sort"
	"testing"
)

// Objective-C's grammar is a C superset, so a typedef parses to the same
// `type_definition` node C uses. entityFromNode's naming branch for that node
// was gated to C and C++ only, so Objective-C fell through to nodeName —
// which finds no `name` field on a typedef and descends to the first
// identifier in pre-order. That identifier is always part of the type being
// aliased, never the alias, so every Objective-C typedef was published under
// a name that belongs to something else:
//
//	typedef struct {int v;} A, B;          -> type:v          (a field)
//	typedef struct Node {int q;} NodeAlias; -> type:Node      (the struct tag)
//	typedef enum {RED, GREEN} Color;        -> type:RED       (an enum constant)
//	typedef NSInteger MyInt;                -> type:NSInteger (a framework type)
//
// Both directions are wrong. The declared name is not a symbol, so searching
// for it finds nothing and a declaration using it has no definition to resolve
// against; and the name that IS emitted is a phantom definition pointing at
// the typedef line — for `MyInt` the graph claimed this file defines
// `NSInteger`, a type it only mentions.
//
// C parses the identical source correctly, so the fix is parity: the C naming
// rule covers Objective-C. This asserts name-by-name rather than on the whole
// symbol set, so it stays orthogonal to the anonymous-specifier and
// multi-declarator work on the same node.
func TestObjCTypedefIsNamedByItsAliasNotItsSource(t *testing.T) {
	source := `typedef struct {int v;} A, B;
typedef struct {int w;} P;
typedef struct Node {int q;} NodeAlias;
typedef enum {RED, GREEN} Color;
typedef NSInteger MyInt;
typedef int I1;
typedef unsigned long long ULL;
typedef void (^Handler)(int);
typedef int (*FnPtr)(int);
`
	// The declared names the typedefs above bind. `A` is deliberately absent:
	// a multi-declarator typedef keeping only its last name is a separate
	// defect on this node, shared with C.
	wantTypes := []string{"B", "P", "NodeAlias", "Color", "MyInt", "I1", "ULL", "Handler", "FnPtr"}
	// Names that appear in the source but name the aliased type, not the
	// alias. None may be emitted as a `type`.
	wantAbsentTypes := []string{"v", "w", "q", "Node", "RED", "GREEN", "NSInteger"}

	// C is the control: it already reports this source correctly, and
	// Objective-C must not diverge from it.
	for _, path := range []string{"src/typedefs.m", "src/typedefs.c"} {
		t.Run(path, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, path, source)
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
			if err != nil {
				t.Fatal(err)
			}
			types := map[string]bool{}
			var all []string
			for _, symbol := range snapshot.Symbols {
				all = append(all, symbol.Kind+":"+symbol.Name)
				if symbol.Kind == "type" {
					types[symbol.Name] = true
				}
			}
			sort.Strings(all)
			for _, name := range wantTypes {
				if !types[name] {
					t.Errorf("typedef %q is not a type symbol; got %#v", name, all)
				}
			}
			for _, name := range wantAbsentTypes {
				if types[name] {
					t.Errorf("%q is part of an aliased type, not a typedef name, but was emitted as a type; got %#v", name, all)
				}
			}
		})
	}
}
