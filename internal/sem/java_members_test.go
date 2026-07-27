package sem

import (
	"sort"
	"testing"
)

// javaMembersSnapshot builds a snapshot of one Java file and indexes its symbols by qualified
// name so the member-shape tests below can assert on containment directly.
func javaMembersSnapshot(t *testing.T, path, source string) (ProviderSnapshot, map[string]SymbolRecord) {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, path, source)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byQualified := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		if s.Language == "Java" {
			byQualified[s.QualifiedName] = s
		}
	}
	return snapshot, byQualified
}

func javaSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A Java constant table is an anonymous class in a FIELD initializer (gson's
// TypeAdapters.BYTE, lucene's IntervalBuilder.NO_INTERVALS). The entity walker stops at a
// field declaration, so those methods had no symbol at all: nothing for the graph to locate,
// and a bare call to one of their names resolved to whatever same-named method the file
// happened to have elsewhere.
func TestJavaFieldInitializerAnonymousClassMembers(t *testing.T) {
	snapshot, byQualified := javaMembersSnapshot(t, "src/Adapters.java", `package p;

public final class Adapters {
  public static final Adapter BYTE =
      new Adapter() {
        @Override
        public String read(Reader in) {
          return normalize(in.next());
        }
      };

  private final Adapter instance =
      new Adapter() {
        @Override
        public String read(Reader in) {
          return "instance";
        }
      };

  static String normalize(String raw) {
    return raw;
  }
}

class Decoy {
  public String read(Reader in) {
    return "decoy";
  }
}
`)
	if _, ok := byQualified["Adapters.BYTE"]; !ok {
		t.Fatalf("field symbol missing: %v", javaSortedKeys(byQualified))
	}
	read, ok := byQualified["Adapters.read"]
	if !ok {
		t.Fatalf("anonymous-class method in a static field initializer not extracted: %v", javaSortedKeys(byQualified))
	}
	if read.Kind != "method" {
		t.Fatalf("anonymous-class member kind = %q, want method", read.Kind)
	}
	// Both anonymous classes declare read(); each must be its own node, so the static-field
	// one and the instance-field one cannot collapse into a single symbol.
	reads := 0
	for _, s := range snapshot.Symbols {
		if s.Language == "Java" && s.Name == "read" && s.FilePath == "src/Adapters.java" {
			reads++
		}
	}
	if reads != 3 { // the two anonymous-class reads plus Decoy.read
		t.Fatalf("read() symbols in file = %d, want 3 (two anonymous-class members + the decoy)", reads)
	}
	// The call the anonymous body makes must resolve to the file's own helper, and nothing may
	// resolve into the unrelated same-named Decoy.read.
	qualifiedByID := map[string]string{}
	for _, s := range snapshot.Symbols {
		qualifiedByID[s.ID] = s.QualifiedName
	}
	calls := map[string]bool{}
	fromDecoy := false
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		calls[qualifiedByID[r.FromID]+"->"+qualifiedByID[r.ToID]] = true
		if qualifiedByID[r.ToID] == "Decoy.read" {
			fromDecoy = true
		}
	}
	if !calls["Adapters.read->Adapters.normalize"] {
		t.Fatalf("anonymous-class member's own call not resolved: %v", javaSortedKeys(calls))
	}
	if fromDecoy {
		t.Fatalf("a call resolved to the unrelated same-named Decoy.read: %v", javaSortedKeys(calls))
	}
}

// Java constructors had no node case at all, so a constructor was not a symbol: unsearchable,
// container-less, and the graph reported the whole enclosing class as the tightest symbol
// around a constructor fix site.
func TestJavaConstructorsAreSymbols(t *testing.T) {
	snapshot, byQualified := javaMembersSnapshot(t, "src/Monitor.java", `package p;

public class Monitor {
  private final String name;

  public Monitor() {
    this.name = "default";
  }

  public Monitor(String name) {
    this.name = name;
  }

  public String describe() {
    return name;
  }
}
`)
	ctor, ok := byQualified["Monitor.Monitor"]
	if !ok {
		t.Fatalf("constructor not extracted: %v", javaSortedKeys(byQualified))
	}
	if ctor.Kind != "method" {
		t.Fatalf("constructor kind = %q, want method", ctor.Kind)
	}
	// Both overloads must survive as distinct nodes, each contained by the enclosing class.
	ids := map[string]bool{}
	for _, s := range snapshot.Symbols {
		if s.QualifiedName != "Monitor.Monitor" {
			continue
		}
		ids[s.ID] = true
		if s.ContainerID == "" || lastSegment(s.ContainerID) != "Monitor" {
			t.Fatalf("constructor container = %q, want the Monitor class", s.ContainerID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("distinct constructor symbols = %d, want 2 (one per overload)", len(ids))
	}
}

// A Java record had no type symbol (the node case was gated to C#) and a Java enum did not
// scope its children, so record accessors and enum members emitted bare, container-less names
// that collide across files and that container-based resolution cannot reach.
func TestJavaRecordAndEnumMemberContainment(t *testing.T) {
	_, byQualified := javaMembersSnapshot(t, "src/Shapes.java", `package p;

public record Point(int x, int y) {
  public int sum() {
    return x + y;
  }
}

enum Mode {
  FAST {
    @Override
    public int weight() {
      return 1;
    }
  },
  SLOW;

  public int weight() {
    return 10;
  }
}
`)
	point, ok := byQualified["Point"]
	if !ok || point.Kind != "class" {
		t.Fatalf("record type symbol missing or wrong kind: %#v / %v", point, javaSortedKeys(byQualified))
	}
	if _, ok := byQualified["Point.sum"]; !ok {
		t.Fatalf("record member not scoped to the record: %v", javaSortedKeys(byQualified))
	}
	if _, ok := byQualified["Mode.weight"]; !ok {
		t.Fatalf("enum member not scoped to the enum: %v", javaSortedKeys(byQualified))
	}
	// A constant WITH a body is a singleton subclass and scopes its own override; a constant
	// without a body carries no code of its own and stays unemitted so a large constant table
	// does not flood the snapshot.
	fast, ok := byQualified["Mode.FAST"]
	if !ok {
		t.Fatalf("enum constant with a class body not extracted: %v", javaSortedKeys(byQualified))
	}
	if fast.ContainerID == "" || lastSegment(fast.ContainerID) != "Mode" {
		t.Fatalf("enum constant container = %q, want the Mode enum", fast.ContainerID)
	}
	if _, ok := byQualified["Mode.FAST.weight"]; !ok {
		t.Fatalf("constant-specific override not scoped to its constant: %v", javaSortedKeys(byQualified))
	}
	if _, ok := byQualified["Mode.SLOW"]; ok {
		t.Fatalf("bodyless enum constant should not be a symbol: %v", javaSortedKeys(byQualified))
	}
}

// A Java `@interface` is a type, but it is a bodyless marker whose name is the noun the issue
// prose repeats, so emitting it takes the head of the ranking from the handler that implements
// it. Measured over 43 Java instances: rank 1 taken from the fix site on 6, gained on none.
func TestJavaAnnotationTypeIsNotExtracted(t *testing.T) {
	_, byQualified := javaMembersSnapshot(t, "src/NonNull.java", `package p;

public @interface NonNull {
  String value() default "";
}
`)
	if _, ok := byQualified["NonNull"]; ok {
		t.Fatalf("annotation type should not be a symbol: %v", javaSortedKeys(byQualified))
	}
}

// A constructor is named after its own type, so an exact match on it is a type-name match that
// the type itself already earns as a candidate in the same file. Paying the bonus twice for one
// name is the whole margin by which constructors outranked the methods a fix lands in — a method
// whose own name is a single word cannot earn the bonus at all inside a multi-word query — so a
// constructor earns it zero times there. Asked for by name alone, it is the answer again.
func TestExactSymbolBonusDoesNotPayTheTypeNameTwice(t *testing.T) {
	ctor := SymbolRecord{Kind: "method", Name: "GsonBuilder", QualifiedName: "GsonBuilder.GsonBuilder"}
	method := SymbolRecord{Kind: "method", Name: "registerTypeAdapter", QualifiedName: "GsonBuilder.registerTypeAdapter"}
	class := SymbolRecord{Kind: "class", Name: "GsonBuilder", QualifiedName: "GsonBuilder"}
	nested := SymbolRecord{Kind: "method", Name: "FAST", QualifiedName: "Mode.FAST.FAST"}
	// A method that merely shares a name with some OTHER type keeps the callable bonus.
	unrelated := SymbolRecord{Kind: "method", Name: "GsonBuilder", QualifiedName: "Factory.GsonBuilder"}

	multi := buildSearchQuery("gson builder should throw for json element")
	if got := exactSymbolBonus(multi, ctor); got != 0 {
		t.Fatalf("constructor bonus on a multi-word query = %v, want 0 (the type earns it)", got)
	}
	if got := exactSymbolBonus(multi, class); got != 6 {
		t.Fatalf("class bonus = %v, want 6", got)
	}
	if got := exactSymbolBonus(multi, method); got != 14 {
		t.Fatalf("method bonus = %v, want 14 (callable tier)", got)
	}
	if got := exactSymbolBonus(multi, unrelated); got != 14 {
		t.Fatalf("method sharing a name with another type = %v, want 14 (callable tier)", got)
	}
	if got := exactSymbolBonus(multi, nested); got != 0 {
		t.Fatalf("nested constructor-shaped member = %v, want 0", got)
	}
	// Asking for the name on its own still means the caller wants that thing.
	single := buildSearchQuery("gsonbuilder")
	if got := exactSymbolBonus(single, ctor); got != 14 {
		t.Fatalf("constructor bonus on a single-word query = %v, want 14", got)
	}
}

func TestNamedAfterOwnContainer(t *testing.T) {
	for _, tc := range []struct {
		qualified string
		want      bool
	}{
		{"GsonBuilder.GsonBuilder", true},
		{"Mode.FAST.FAST", true},
		{"GsonBuilder.registerTypeAdapter", false},
		{"Factory.GsonBuilder", false},
		{"GsonBuilder", false},
		{"", false},
		{".", false},
	} {
		if got := namedAfterOwnContainer(SymbolRecord{QualifiedName: tc.qualified}); got != tc.want {
			t.Fatalf("namedAfterOwnContainer(%q) = %v, want %v", tc.qualified, got, tc.want)
		}
	}
}
