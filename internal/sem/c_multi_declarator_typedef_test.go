package sem

import (
	"sort"
	"strings"
	"testing"
)

// typeNamesIn returns the sorted names of every `type` symbol in a repository
// built from one C-family source file.
func typedefSymbolsFor(t *testing.T, path, source string) (types []string, all []string, snapshot ProviderSnapshot) {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, path, source)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range snapshot.Symbols {
		all = append(all, symbol.Kind+":"+symbol.Name)
		if symbol.Kind == "type" {
			types = append(types, symbol.Name)
		}
	}
	sort.Strings(types)
	sort.Strings(all)
	return types, all, snapshot
}

// A C typedef may bind several names in one declaration. `typedef struct {…}
// A, B;` is the ordinary way to give a type two names, and `typedef int I1,
// I2;` is the same shape without a struct.
//
// entityFromNode returns a single Entity, and the name it picked came from
// cFamilyTypedefNameRe, whose `$` anchor can only match the LAST declarator —
// so every name but one was silently dropped. `A` was not a symbol: search
// could not find it, and a parameter declared `A a` had no definition to
// resolve against. The function-pointer form failed in the other direction
// (the regex matches nothing, so the nodeName fallback kept the FIRST name and
// dropped the rest), which is why this pins both ends.
func TestCMultiDeclaratorTypedefEmitsEveryName(t *testing.T) {
	source := `typedef struct {int v;} A, B;
typedef int I1, I2;
typedef struct {int w;} *H1, H2[4];
typedef int (*F1)(int), (*F2)(int);
typedef struct Node {int v;} NodeAlias, *NodeP;
typedef enum {RED, GREEN} C1, C2;
`
	// C and C++ share this extraction path; both grammars put every declarator
	// on a `declarator` field, so both must behave identically.
	for _, path := range []string{"src/t.c", "src/t.cpp"} {
		t.Run(path, func(t *testing.T) {
			types, _, _ := typedefSymbolsFor(t, path, source)
			counts := map[string]int{}
			for _, name := range types {
				counts[name]++
			}
			for _, want := range []string{
				"A", "B", // anonymous struct, two plain declarators
				"I1", "I2", // no struct at all
				"H1", "H2", // pointer and array declarators
				"F1", "F2", // function-pointer declarators
				"NodeAlias", "NodeP", // tagged struct, plain + pointer
				"C1", "C2", // anonymous enum
			} {
				// Exactly once, not merely present: the primary declarator is
				// emitted by entityFromNode and must not be emitted a second
				// time as its own alias.
				if counts[want] != 1 {
					t.Errorf("typedef name %q produced %d type symbols, want exactly 1; got types %v", want, counts[want], types)
				}
			}
		})
	}
}

// The counterweight: a typedef that binds ONE name must still emit exactly one
// type symbol. A fix that emitted a symbol per declarator unconditionally, or
// that stopped deduplicating, would double-count these.
func TestCSingleDeclaratorTypedefEmitsExactlyOneName(t *testing.T) {
	source := `typedef unsigned long ULong;
typedef struct {int z;} Solo;
typedef struct Tagged {int q;} TaggedAlias;
typedef int (*Callback)(int);
`
	for _, path := range []string{"src/t.c", "src/t.cpp"} {
		t.Run(path, func(t *testing.T) {
			types, _, _ := typedefSymbolsFor(t, path, source)
			counts := map[string]int{}
			for _, name := range types {
				counts[name]++
			}
			for _, want := range []string{"ULong", "Solo", "TaggedAlias", "Callback"} {
				if counts[want] != 1 {
					t.Errorf("single-declarator typedef %q produced %d type symbols, want exactly 1 (types %v)", want, counts[want], types)
				}
			}
		})
	}
}

// The names are not just present, they are usable: a parameter declared with a
// dropped alias now resolves to a definition. This is the reason the missing
// symbols mattered — before the fix `use_a` had a parameter whose type was
// invisible to the graph, so no PARAM_TYPE edge could be built for it.
func TestCMultiDeclaratorTypedefAliasResolvesAsAParameterType(t *testing.T) {
	source := `typedef struct {int v;} A, B;

int use_a(A a) { return a.v; }

int use_b(B b) { return b.v; }
`
	_, _, snapshot := typedefSymbolsFor(t, "src/t.c", source)
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	seen := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "PARAM_TYPE" {
			continue
		}
		seen[byID[relation.FromID].Name+"->"+byID[relation.ToID].Name] = true
	}
	// B was always reachable; A is the one the defect dropped.
	for _, want := range []string{"use_a->A", "use_b->B"} {
		if !seen[want] {
			var got []string
			for edge := range seen {
				got = append(got, edge)
			}
			sort.Strings(got)
			t.Errorf("missing PARAM_TYPE %s; got %s", want, strings.Join(got, ", "))
		}
	}
}
