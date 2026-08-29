package sem

import "testing"

// TestFSharpJuxtapositionCallExtraction covers F#'s ordinary call syntax.
// `add ledger amount` — a function applied by writing its arguments beside it,
// with no dot and no parentheses — is how F# calls a function. The dotted
// scanners need a `.`, the pipeline scanner needs a `|>`, and the generic
// scanner needs `name(`, so plain application matched nothing: a module of
// functions calling each other produced no CALLS edge at all while
// `capabilities --json` advertises CALLS for F#.
func TestFSharpJuxtapositionCallExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Ledger.fs", `module Fixtures.Ledger

type Ledger = { Total: int }

let add (ledger: Ledger) (amount: int) : int =
    ledger.Total + amount

let scale (factor: int) (amount: int) : int =
    factor * amount

let describe (label: string) (amount: int) : string =
    label + string amount

let double (amount: int) : int =
    add { Total = 0 } amount * 2

let report (amount: int) : string =
    describe "total: " (scale 2 amount)
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{
		{"double", "add"},
		{"report", "describe"},
		{"report", "scale"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Fatalf("missing F# juxtaposition CALLS %s->%s: %#v", want[0], want[1], relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

// TestFSharpJuxtapositionScannerStaysNarrow pins the shapes that look like an
// application and are not one. Every entry names something that IS a known
// callable in the file, so only position and context can reject it — which is
// the whole difficulty of reading juxtaposition, and the reason #163 scanned
// only the unambiguous pipe position.
func TestFSharpJuxtapositionScannerStaysNarrow(t *testing.T) {
	t.Parallel()
	callables := map[string]bool{"add": true, "scale": true, "helper": true}
	for _, tc := range []struct {
		name  string
		block string
		want  []string
	}{
		{"plain application", "let f x = add x 1", []string{"add"}},
		{"applied to a record", "let f x = add { Total = 0 } x", []string{"add"}},
		{"applied to a parenthesised expression", "let f x = add (scale 2 x)", []string{"add", "scale"}},
		{"piped", "let f xs = xs |> add", []string{"add"}},
		{"backward pipe", "let f x = add <| x", []string{"add"}},
		// The binder in a `let` head is the name being DEFINED, not a call.
		{"binding head", "let add x y = x + y", nil},
		{"and-binding head", "let rec f x = g x\nand add y = y", nil},
		// A parameter that happens to share a callable's name is bound, not applied.
		{"parameter named after a callable", "let f add = add", nil},
		{"lambda parameter", "let f = fun add -> add", nil},
		// A trailing argument is not the head of the application.
		{"passed as a trailing argument", "let f xs = List.map helper add", nil},
		// A bare value reference applies nothing.
		{"value reference", "let f = add", nil},
		{"reference before an operator", "let f = add = scale", nil},
		// Member access is not a bare application.
		{"member access", "let f x = x.add y", nil},
		// A record field named after a callable is a field, not a call.
		{"record field", "let f = { add = 1 }", nil},
		// A name that is not a known callable in this file is not guessed at.
		{"unknown name", "let f x = unknown x 1", nil},
		// Literals and comments are masked before scanning.
		{"inside a string", "let f = \"add x 1\"", nil},
		{"inside a comment", "let f = 1 // add x 1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := fsharpJuxtapositionCallIdentifiers(tc.block, callables)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, name := range tc.want {
				if _, ok := got[name]; !ok {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
