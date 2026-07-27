package sem

import (
	"strings"
	"testing"
)

// TestLexicalEntityParents pins the stack walk that resolves a lexical parent, including the
// cases that decide whether a containment level is real: equal ranges (not a nesting), siblings
// (no parent), and a grandchild whose nearest STRICT encloser sits under an equal-range record.
func TestLexicalEntityParents(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		entities []Entity
		want     []int
	}{
		{
			name:     "top level declaration has no parent",
			entities: []Entity{{Name: "A", StartLine: 1, EndLine: 10}},
			want:     []int{-1},
		},
		{
			name: "nested declaration takes the enclosing declaration",
			entities: []Entity{
				{Name: "Outer", StartLine: 1, EndLine: 20},
				{Name: "Ops", StartLine: 5, EndLine: 12},
			},
			want: []int{-1, 0},
		},
		{
			name: "grandchild takes the nearest encloser, not the outermost",
			entities: []Entity{
				{Name: "Outer", StartLine: 1, EndLine: 20},
				{Name: "Ops", StartLine: 5, EndLine: 12},
				{Name: "PLUS", StartLine: 6, EndLine: 8},
			},
			want: []int{-1, 0, 1},
		},
		{
			name: "siblings do not contain each other",
			entities: []Entity{
				{Name: "Outer", StartLine: 1, EndLine: 20},
				{Name: "a", StartLine: 2, EndLine: 4},
				{Name: "b", StartLine: 6, EndLine: 9},
			},
			want: []int{-1, 0, 0},
		},
		{
			name: "an equal range is not a nesting",
			entities: []Entity{
				{Name: "head", StartLine: 3, EndLine: 7},
				{Name: "body", StartLine: 3, EndLine: 7},
			},
			want: []int{-1, -1},
		},
		{
			name: "an equal-range record still shelters what is nested inside it",
			entities: []Entity{
				{Name: "Outer", StartLine: 1, EndLine: 9},
				{Name: "head", StartLine: 1, EndLine: 9},
				{Name: "inner", StartLine: 4, EndLine: 6},
			},
			want: []int{-1, -1, 1},
		},
		{
			name: "a following sibling closes the previous subtree",
			entities: []Entity{
				{Name: "Outer", StartLine: 1, EndLine: 30},
				{Name: "Ops", StartLine: 5, EndLine: 12},
				{Name: "PLUS", StartLine: 6, EndLine: 8},
				{Name: "after", StartLine: 20, EndLine: 25},
			},
			want: []int{-1, 0, 1, 0},
		},
		{
			name: "records with no line range are skipped",
			entities: []Entity{
				{Name: "Outer", StartLine: 1, EndLine: 9},
				{Name: "broken", StartLine: 0, EndLine: 0},
				{Name: "inner", StartLine: 3, EndLine: 5},
			},
			want: []int{-1, -1, 0},
		},
		{
			name: "an out-of-order record simply finds no parent",
			entities: []Entity{
				{Name: "inner", StartLine: 3, EndLine: 5},
				{Name: "Outer", StartLine: 1, EndLine: 9},
			},
			want: []int{-1, -1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := lexicalEntityParents(testCase.entities)
			if len(got) != len(testCase.want) {
				t.Fatalf("got %d parents, want %d", len(got), len(testCase.want))
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("entity %d (%s): got parent %d, want %d\nall: %v",
						index, testCase.entities[index].Name, got[index], testCase.want[index], got)
				}
			}
		})
	}
}

// TestNestedTypesGetLexicalContainer is the regression this change exists for: a nested
// declaration whose qualified name names no owner must still be a MEMBER of the declaration it
// sits in, in every language that writes one.
//
// Each case asserts the container of a nested declaration by the qualified name of its expected
// parent, so the test fails if the parent link is dropped, points at the file, or points at the
// wrong nesting level.
func TestNestedTypesGetLexicalContainer(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		path string
		// source is the fixture; want maps a nested symbol's qualified name to the qualified
		// name of the container it must be linked to.
		source string
		want   map[string]string
	}{
		{
			name: "java private nested enum, inner class and interface",
			path: "Outer.java",
			source: `public class Outer {
  private int seed;

  public int run() {
    return seed;
  }

  private enum Ops {
    PLUS("+");

    private final String fn;

    Ops(String fn) {
      this.fn = fn;
    }

    static Ops lookup(String fn) {
      return null;
    }
  }

  private static class Helper {
    void assist() {
    }
  }

  interface Shape {
    double area();
  }
}
`,
			want: map[string]string{
				"Ops":        "Outer",
				"Helper":     "Outer",
				"Shape":      "Outer",
				"Ops.lookup": "Ops",
			},
		},
		{
			name: "c# nested enum, class and struct",
			path: "Outer.cs",
			source: `namespace Demo
{
    public class Outer
    {
        public int Run()
        {
            return 1;
        }

        private enum Ops
        {
            Plus,
        }

        private class Helper
        {
            public void Assist() { }
        }

        public struct Pair
        {
            public int A;
        }
    }
}
`,
			want: map[string]string{
				"Ops":    "Outer",
				"Helper": "Outer",
				"Pair":   "Outer",
			},
		},
		{
			name: "kotlin nested enum class and inner class",
			path: "Outer.kt",
			source: `class Outer(val seed: Int) {
    fun run(): Int = seed

    private enum class Ops {
        PLUS,
    }

    private class Helper {
        fun assist(): Int = 2
    }
}
`,
			want: map[string]string{
				"Ops":    "Outer",
				"Helper": "Outer",
			},
		},
		{
			name: "python inner classes at two levels",
			path: "outer.py",
			source: `class Outer:
    def run(self):
        return 1

    class Inner:
        def assist(self):
            return 2

        class Deepest:
            def dig(self):
                return 3
`,
			want: map[string]string{
				"Inner":   "Outer",
				"Deepest": "Inner",
			},
		},
		{
			name: "python class declared inside a function",
			path: "factory.py",
			source: `def factory():
    class Local:
        def tick(self):
            return 1

    return Local()
`,
			want: map[string]string{"Local": "factory"},
		},
		{
			name: "ruby nested class and module",
			path: "outer.rb",
			source: `class Outer
  def run
    1
  end

  class Inner
    def assist
      2
    end
  end

  module Helpers
    def self.help
      3
    end
  end
end
`,
			want: map[string]string{
				"Inner":   "Outer",
				"Helpers": "Outer",
			},
		},
		{
			name: "go type declared inside a function",
			path: "outer.go",
			source: `package src

func Factory() int {
	type local struct {
		n int
	}
	v := local{n: 1}
	return v.n
}
`,
			want: map[string]string{"local": "Factory"},
		},
		{
			name: "rust item declared inside a function",
			path: "outer.rs",
			source: `pub fn factory() -> i32 {
    struct Local {
        n: i32,
    }
    let v = Local { n: 1 };
    v.n
}
`,
			want: map[string]string{"Local": "factory"},
		},
		{
			name: "typescript class in a function and object-literal method set",
			path: "outer.ts",
			source: `export function factory() {
  class Local {
    tick(): number {
      return 1;
    }
  }
  return new Local();
}

export const handlers = {
  onOpen(): void {},
  onClose(): void {},
};
`,
			want: map[string]string{
				"Local":   "factory",
				"onOpen":  "handlers",
				"onClose": "handlers",
			},
		},
		{
			name: "javascript class in a function and object-literal method set",
			path: "outer.js",
			source: `export function factory() {
  class Local {
    tick() {
      return 1;
    }
  }
  return new Local();
}

export const handlers = {
  onOpen() {},
  onClose() {},
};
`,
			want: map[string]string{
				"Local":   "factory",
				"onOpen":  "handlers",
				"onClose": "handlers",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, testCase.path, testCase.source)
			snapshot, err := BuildProviderSnapshotWithOptions(
				t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true},
			)
			if err != nil {
				t.Fatal(err)
			}
			byID := map[string]SymbolRecord{}
			for _, symbol := range snapshot.Symbols {
				byID[symbol.ID] = symbol
			}
			for qualified, wantContainer := range testCase.want {
				symbol, ok := findSymbolByQualifiedName(snapshot.Symbols, qualified)
				if !ok {
					t.Fatalf("%s was not extracted at all; symbols: %s", qualified, symbolNames(snapshot.Symbols))
				}
				if symbol.ContainerID == "" {
					t.Fatalf("%s has no container (orphaned nested declaration)", qualified)
				}
				container, ok := byID[symbol.ContainerID]
				if !ok {
					t.Fatalf("%s points at unknown container %q", qualified, symbol.ContainerID)
				}
				if container.QualifiedName != wantContainer {
					t.Fatalf("%s container = %q, want %q", qualified, container.QualifiedName, wantContainer)
				}
				if !containsRelation(snapshot.Relations, symbol.ContainerID, symbol.ID, "CONTAINS") {
					t.Fatalf("no CONTAINS relation from %s to %s", wantContainer, qualified)
				}
			}
		})
	}
}

// TestContainmentReasonDistinguishesLexicalFromQualified pins that a consumer can tell an
// inferred containment from one the source spelled out.
func TestContainmentReasonDistinguishesLexicalFromQualified(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "Outer.java", `public class Outer {
  private enum Ops {
    PLUS;
  }

  void run() {
  }
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(
		t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := findSymbolByQualifiedName(snapshot.Symbols, "Ops")
	if !ok {
		t.Fatal("nested enum Ops was not extracted")
	}
	member, ok := findSymbolByQualifiedName(snapshot.Symbols, "Outer.run")
	if !ok {
		t.Fatal("method Outer.run was not extracted")
	}
	for _, testCase := range []struct {
		name   string
		symbol SymbolRecord
		want   string
	}{
		{name: "nested type is inferred from line ranges", symbol: nested, want: containmentReasonLexical},
		{name: "member is named by its qualified name", symbol: member, want: containmentReasonQualified},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := ""
			for _, relation := range snapshot.Relations {
				if relation.Type == "CONTAINS" && relation.ToID == testCase.symbol.ID {
					got = relation.Reason
				}
			}
			if got != testCase.want {
				t.Fatalf("CONTAINS reason for %s = %q, want %q", testCase.symbol.QualifiedName, got, testCase.want)
			}
		})
	}
}

// TestLexicalContainerScope pins both sides of the fallback's scope at the point where it is
// decided. It fires for a declaration whose qualified name names NO owner, and never for one
// that names an owner the file did not define — such an owner may legitimately live in another
// file (a Go receiver type, a C# partial class, a reopened Ruby class) and resolving it is the
// cross-file container resolver's job, not a line-range guess.
func TestLexicalContainerScope(t *testing.T) {
	t.Parallel()
	outer := Entity{Kind: "class", Name: "Outer", StartLine: 1, EndLine: 20, Signature: "class Outer"}
	for _, testCase := range []struct {
		name          string
		nested        Entity
		wantContained bool
	}{
		{
			name:          "unqualified nested declaration takes the encloser",
			nested:        Entity{Kind: "enum", Name: "Ops", StartLine: 5, EndLine: 9},
			wantContained: true,
		},
		{
			name:          "declaration naming an owner the file lacks stays unattached",
			nested:        Entity{Kind: "method", Name: "Missing.member", StartLine: 5, EndLine: 9},
			wantContained: false,
		},
		{
			name:          "declaration naming an owner the file HAS resolves by name",
			nested:        Entity{Kind: "method", Name: "Outer.member", StartLine: 5, EndLine: 9},
			wantContained: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			symbols := entitySymbols("local/x", "Outer.java", "Java", []Entity{outer, testCase.nested})
			if len(symbols) != 2 {
				t.Fatalf("got %d symbols, want 2", len(symbols))
			}
			contained := symbols[1].ContainerID == symbols[0].ID
			if contained != testCase.wantContained {
				t.Fatalf("%s container = %q (contained=%v), want contained=%v",
					testCase.nested.Name, symbols[1].ContainerID, contained, testCase.wantContained)
			}
		})
	}
}

func findSymbolByQualifiedName(symbols []SymbolRecord, qualified string) (SymbolRecord, bool) {
	for _, symbol := range symbols {
		if symbol.QualifiedName == qualified {
			return symbol, true
		}
	}
	return SymbolRecord{}, false
}

func containsRelation(relations []RelationRecord, fromID, toID, relationType string) bool {
	for _, relation := range relations {
		if relation.FromID == fromID && relation.ToID == toID && relation.Type == relationType {
			return true
		}
	}
	return false
}

func symbolNames(symbols []SymbolRecord) string {
	names := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		names = append(names, symbol.Kind+":"+symbol.QualifiedName)
	}
	return strings.Join(names, ", ")
}
