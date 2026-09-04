package sem

import (
	"reflect"
	"strings"
	"testing"
)

// Scala curries: `def f(a: Int)(b: Int, c: Int)` hangs TWO `parameters` clauses
// off one definition, both under the `parameters` field. Reading only the first
// (ChildByFieldName returns one node) reported {a} — and reported it as
// AST-authoritative, which suppresses the signature fallback, so every binding
// and every type in the later clauses was dropped with no warning.
func TestASTParameterNamesReadsEveryCurriedClause(t *testing.T) {
	const src = "object O {\n  def f(a: Int)(b: Int, c: Int): Int = sink(c)\n}\n"
	names, known := astParameterNamesFor(t, "a.scala", src)
	if !known {
		t.Fatal("Scala parameter list must be AST-confirmed")
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("curried clauses: got %v want %v", names, want)
	}
	params, returns, typesKnown := astSignatureTypeTextsFor(t, "a.scala", src)
	if !typesKnown {
		t.Fatal("Scala signature types must be AST-confirmed")
	}
	if want := "Int, Int, Int"; params != want {
		t.Fatalf("curried clause types: got %q want %q", params, want)
	}
	if returns != "Int" {
		t.Fatalf("return type: got %q want %q", returns, "Int")
	}
}

// A Go method spells its RECEIVER and its multi-value RESULT as `parameter_list`
// nodes as well, so taking every `parameter_list` child would fold both into the
// parameter list. The `parameters` field is what tells the three apart, and it
// stays authoritative wherever a grammar has one.
func TestASTParameterClausesKeepGoReceiverAndResultOut(t *testing.T) {
	const src = "package p\n\nfunc (c *Client) Do(in Input) (Output, error) { return sink(in) }\n"
	names, known := astParameterNamesFor(t, "a.go", src)
	if !known {
		t.Fatal("Go parameter list must be AST-confirmed")
	}
	// The receiver is a real input and is kept, but exactly once and not as a
	// second parameter clause.
	if want := []string{"c", "in"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("go method inputs: got %v want %v", names, want)
	}
	params, _, typesKnown := astSignatureTypeTextsFor(t, "a.go", src)
	if !typesKnown {
		t.Fatal("Go signature types must be AST-confirmed")
	}
	if params != "Input" {
		t.Fatalf("go parameter types: got %q want %q (receiver and result must stay out)", params, "Input")
	}
}

// Dart groups its optional parameters in one `optional_formal_parameters` entry
// and hangs each DEFAULT VALUE beside the formal parameter it belongs to. Read
// as a single entry the group binds no name of its own, so the last-identifier
// fallback took the trailing default expression: `[int b = other]` bound `other`
// — a module-level constant — and lost `b`, inventing one flow while dropping
// another. The named `{...}` form has the same shape.
func TestASTParameterNamesUnwrapsDartOptionalGroups(t *testing.T) {
	for _, testCase := range []struct {
		name string
		src  string
		want []string
	}{
		{"positional optional", "void f(int a, [int b = other]) { sink(b); }\n", []string{"a", "b"}},
		{"named optional", "void h({required int k, String s = \"x\"}) { sink(k); }\n", []string{"k", "s"}},
		{"only optional", "void g([User user = defaultUser]) { sink(user); }\n", []string{"user"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			names, known := astParameterNamesFor(t, "a.dart", testCase.src)
			if !known {
				t.Fatal("Dart parameter list must be AST-confirmed")
			}
			if !reflect.DeepEqual(names, testCase.want) {
				t.Fatalf("got %v want %v", names, testCase.want)
			}
		})
	}
}

// The same wrapper hid the declared TYPE: `void g([User user = defaultUser])`
// reported no parameter type at all while still marking the AST metadata
// authoritative, so PARAM_TYPE and the overload resolution that reads it were
// dropped rather than falling back to the signature split.
func TestASTSignatureTypesUnwrapsDartOptionalGroups(t *testing.T) {
	params, returns, known := astSignatureTypeTextsFor(t, "a.dart", "void g([User user = defaultUser]) { sink(user); }\n")
	if !known {
		t.Fatal("Dart signature types must be AST-confirmed")
	}
	if params != "User" {
		t.Fatalf("optional parameter type: got %q want %q", params, "User")
	}
	if returns != "void" {
		t.Fatalf("Dart writes its result BEFORE the parameter list: got %q want %q", returns, "void")
	}
}

// A Kotlin extension function leaves its RECEIVER type among the declaration's
// unfielded children, ahead of the name and the parameter list, exactly where
// the unfielded return type also sits. First-match therefore reported the
// receiver: `fun Receiver.load(): Result` emitted RETURNS_TYPE -> Receiver and
// lost Result on every extension function in the repository.
func TestASTSignatureTypesSkipsKotlinExtensionReceiver(t *testing.T) {
	for _, testCase := range []struct {
		name string
		src  string
		want string
	}{
		{"extension", "fun Receiver.load(): Result { return r }\n", "Result"},
		{"plain", "fun plain(a: Int): Result { return r }\n", "Result"},
		{"generic extension", "fun List<Item>.first(): Item { return x }\n", "Item"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, returns, known := astSignatureTypeTextsFor(t, "a.kt", testCase.src)
			if !known {
				t.Fatal("Kotlin signature types must be AST-confirmed")
			}
			if returns != testCase.want {
				t.Fatalf("return type: got %q want %q", returns, testCase.want)
			}
		})
	}
}

// Ruby binds a block parameter as `block` but forwards it as `sink(&block)`, and
// the flow detector compares argument text for EQUALITY. Carrying only the bare
// name meant `def f(&block)` contributed no DATA_FLOWS edge while the identical
// `def g(*args)` did.
func TestASTParameterNamesCarriesRubyBlockSpelling(t *testing.T) {
	names, known := astParameterNamesFor(t, "a.rb", "def f(&block)\n  sink(&block)\nend\n")
	if !known {
		t.Fatal("Ruby parameter list must be AST-confirmed")
	}
	if want := []string{"block", "&block"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("block parameter spellings: got %v want %v", names, want)
	}
}

// End to end: the block-forwarding flow the spelling exists to match. `g` is the
// control — it always produced its edge — so a failure here is about `f` alone.
func TestRubyBlockForwardingEmitsDataFlow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "a.rb", "def sink(b)\n  b\nend\n\ndef f(&block)\n  sink(&block)\nend\n\ndef g(*args)\n  sink(*args)\nend\n")
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	flows := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "DATA_FLOWS" {
			continue
		}
		for _, evidence := range relation.Evidence {
			flows[evidence.Detail] = true
		}
	}
	if !flows["*args -> sink()"] {
		t.Fatalf("control splat flow missing; got %v", flows)
	}
	if !flows["&block -> sink()"] {
		t.Fatalf("block-forwarding DATA_FLOWS edge missing; got %v", flows)
	}
}

// cleanParameterName rejected every rune that is neither a letter, a digit nor
// `_`. A combining mark is neither, but it is part of the letter it follows:
// `café` in NFD is `caf` + `e` + U+0301, which Python and Rust both accept as one
// identifier and which macOS filesystems hand back routinely. The parameter was
// dropped, and because the list stayed AST-authoritative the signature fallback
// never ran, so every flow through it disappeared silently.
func TestCleanParameterNameKeepsCombiningMarks(t *testing.T) {
	const decomposed = "café"
	if got := cleanParameterName(decomposed); got != decomposed {
		t.Errorf("decomposed identifier was rejected (got %q)", got)
	}
	if got := cleanParameterName("naïve"); got != "naïve" {
		t.Errorf("decomposed diaeresis identifier was rejected (got %q)", got)
	}
	// A leading combining mark cannot start an identifier in any supported
	// language, so it is still refused rather than silently accepted.
	if got := cleanParameterName("́abc"); got != "" {
		t.Errorf("leading combining mark was accepted as %q", got)
	}
	// And the parser keeps it end to end, in both a type-first and a
	// type-annotated grammar.
	for _, testCase := range []struct{ path, src string }{
		{"a.py", "def f(" + decomposed + ", plain):\n    return sink(" + decomposed + ")\n"},
		{"a.go", "package p\n\nfunc f(" + decomposed + " string, plain int) { sink(" + decomposed + ") }\n"},
	} {
		names, known := astParameterNamesFor(t, testCase.path, testCase.src)
		if !known {
			t.Fatalf("%s: parameter list must be AST-confirmed", testCase.path)
		}
		if !reflect.DeepEqual(names, []string{decomposed, "plain"}) {
			t.Fatalf("%s: got %v want [%q plain]", testCase.path, names, decomposed)
		}
		if strings.Contains(strings.Join(names, ","), "cafe,") {
			t.Fatalf("%s: combining mark was stripped from the name: %v", testCase.path, names)
		}
	}
}
