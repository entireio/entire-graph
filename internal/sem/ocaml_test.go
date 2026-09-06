package sem

import (
	"strings"
	"testing"
)

func TestStripOCamlCodeText(t *testing.T) {
	// Nested `(* ... *)` comments, string literals, `{|...|}` quoted strings,
	// and character literals must be masked so their contents never register
	// as call sites; primed identifiers and type variables share the quote
	// character and must survive; newlines survive so offsets keep line
	// context.
	in := "let run x' =\n" +
		"  (* commented_call x (* Nested.call y *) still commented *)\n" +
		"  Printf.sprintf \"fake_call %s (* not a comment\" x';\n" +
		"  let sep = ';' in\n" +
		"  let raw = {|Raw.call inside quoted string|} in\n" +
		"  real_call sep raw\n"
	out := stripOCamlCodeText(in)
	for _, gone := range []string{"commented_call", "Nested.call", "fake_call", "Raw.call"} {
		if strings.Contains(out, gone) {
			t.Fatalf("stripOCamlCodeText left %q in:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"Printf.sprintf", "x'", "real_call sep raw"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("stripOCamlCodeText dropped %q from:\n%s", kept, out)
		}
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Fatalf("stripOCamlCodeText changed line count:\n%s", out)
	}
	// A `'` after an identifier is part of the name, not a character literal:
	// masking it as one would swallow the code between two primed names.
	primes := "let f a' b = g a' (h b)"
	if got := stripOCamlCodeText(primes); got != primes {
		t.Fatalf("primed identifiers were masked: %q -> %q", primes, got)
	}
}

func TestOCamlCallSites(t *testing.T) {
	locals := map[string]bool{
		"expand_ordered_set_lang": true,
		"helper":                  true,
		"standard":                true,
		"set":                     true,
	}
	block := `let expand_and_eval_set t set ~standard =
  let standard =
    if Ordered_set_lang.Unexpanded.has_standard set then standard
    else Memo.return []
  in
  let* set = expand_ordered_set_lang set ~dir:t.dir in
  ignore { Loc.start = 1 };
  List.iter ~f:helper [ 1 ];
  Fmt.failwith @@ helper 3;
  set |> helper;
  Ordered_set_lang.eval set ~standard ~eq:String.equal ~parse:(fun ~loc:_ s -> s)
`
	sites := ocamlCallSites(block, locals, nil)
	got := map[ocamlCallSite]bool{}
	for _, site := range sites {
		got[site] = true
	}
	for _, want := range []ocamlCallSite{
		// Qualified applications, including a nested module path.
		{Path: "Ordered_set_lang.Unexpanded", Name: "has_standard"},
		{Path: "Ordered_set_lang", Name: "eval"},
		// Bare application of a same-file binding, argument on the same line.
		{Name: "expand_ordered_set_lang"},
		// `arg |> fn` pipeline and `fn @@ arg` right-application heads.
		{Name: "helper"},
		{Path: "Fmt", Name: "failwith"},
	} {
		if !got[want] {
			t.Fatalf("missing call site %+v in %+v", want, sites)
		}
	}
	// Not calls: `standard`/`set` are binders in `let` heads and trailing
	// arguments elsewhere (`eval set ~standard`); `Loc.start` is a record
	// field binding (`= 1` follows); `~f:helper` passes the function as a
	// labeled value rather than applying it; `String.equal` likewise sits
	// behind a label; `Memo.return` is applied but Memo is external — still a
	// site here (resolution is the caller's job), so only assert the noise.
	for _, bogus := range []ocamlCallSite{
		{Name: "standard"},
		{Name: "set"},
		{Path: "Loc", Name: "start"},
		{Path: "String", Name: "equal"},
	} {
		if got[bogus] {
			t.Fatalf("bogus call site %+v in %+v", bogus, sites)
		}
	}
}

// OCaml CALLS extraction (evidence: on ocaml/dune the focus function
// Expander.expand_and_eval_set had zero inbound/outbound CALLS). Qualified
// applications `Mod.fn args` resolve to the callable named `fn` in the file
// defining module `Mod` (module name = capitalized file basename by OCaml
// convention, or a nested `module Mod = struct` symbol); bare `fn args`
// applications resolve within the same file only. When both `mod.ml` and
// `mod.mli` define the name, the implementation wins.
func TestOCamlCallExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/expander.ml", `open Import

let expand_ordered_set_lang set ~dir = ignore (set, dir)

let expand_and_eval_set t set ~standard =
  let standard =
    if Ordered_set_lang.Unexpanded.has_standard set then standard
    else Memo.return []
  in
  (* Ordered_set_lang.eval commented_out ~standard *)
  let* set = expand_ordered_set_lang set ~dir:t.dir in
  Ordered_set_lang.eval set ~standard ~eq:String.equal ~parse:(fun ~loc:_ s -> s)
`)
	writeFile(t, repo, "src/ordered_set_lang.ml", `module Unexpanded = struct
  let has_standard t = ignore t
end

let eval t ~standard ~eq ~parse = ignore (t, standard, eq, parse)
`)
	writeFile(t, repo, "src/ordered_set_lang.mli", `module Unexpanded : sig
  val has_standard : t -> bool
end

val eval : t -> standard:string list -> eq:(string -> string -> bool) -> parse:(loc:int -> string -> string) -> string list
`)
	writeFile(t, repo, "src/ocaml_flags_db.ml", `let ocaml_flags_env ~expander flags =
  let f = "log: Expander.expand_and_eval_set inside a string" in
  ignore f;
  Expander.expand_and_eval_set expander flags ~standard:[]
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		to, ok := symbolsByID[r.ToID]
		if !ok || to.Language != "OCaml" {
			continue
		}
		from, ok := symbolsByID[r.FromID]
		if !ok {
			// The only non-symbol CALLS source is the file-level top-level
			// scan; OCaml's top level is declarations (opens, types, module
			// headers) that must not register as call sites.
			t.Fatalf("file-level CALLS edge into OCaml symbol %s should not exist", to.Name)
		}
		calls[edge{from.FilePath + ":" + from.Name, to.FilePath + ":" + to.Name}] = true
	}
	for _, want := range []edge{
		// Inbound: qualified application from another compilation unit.
		{"src/ocaml_flags_db.ml:ocaml_flags_env", "src/expander.ml:expand_and_eval_set"},
		// Outbound: bare application of a same-file binding.
		{"src/expander.ml:expand_and_eval_set", "src/expander.ml:expand_ordered_set_lang"},
		// Outbound: qualified application into another unit's top-level let.
		{"src/expander.ml:expand_and_eval_set", "src/ordered_set_lang.ml:eval"},
		// Outbound: nested module path `Ordered_set_lang.Unexpanded.has_standard`.
		{"src/expander.ml:expand_and_eval_set", "src/ordered_set_lang.ml:has_standard"},
	} {
		if !calls[want] {
			t.Fatalf("missing OCaml CALLS edge %v in %v", want, calls)
		}
	}
	for e := range calls {
		// The `.mli` restates eval/has_standard: with the `.ml` matched, the
		// interface must not receive duplicate edges, and being an interface
		// it must not emit any.
		if strings.Contains(e.to, ".mli:") || strings.HasPrefix(e.from, "src/ordered_set_lang.mli") {
			t.Fatalf("interface file participates in CALLS edge %v", e)
		}
		// The commented-out application and the call named inside a string
		// literal must not fabricate extra edges into ordered_set_lang.
		if e.from == "src/ocaml_flags_db.ml:ocaml_flags_env" && strings.Contains(e.to, "ordered_set_lang") {
			t.Fatalf("masked text fabricated OCaml CALLS edge %v", e)
		}
	}
}

func TestOCamlOpenModuleAndCallableReferenceExtraction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "stdlib/camlinternalFormatBasics.ml", `let rec concat_fmt fmt rest =
  if fmt = rest then fmt else concat_fmt rest fmt
`)
	writeFile(t, repo, "stdlib/camlinternalFormat.ml", `open CamlinternalFormatBasics

let convert_int iconv n = iconv + n
let convert_int32 iconv n = iconv + n
let convert_int64 iconv n = iconv + n

let make_int_padding_precision _k _acc _rest _pad _prec trans iconv =
  trans iconv 1

let rec make_printf fmt =
  make_int_padding_precision () () fmt () () convert_int 0;
  make_int_padding_precision () () fmt () () convert_int32 0;
  make_int_padding_precision () () fmt () () convert_int64 0;
  concat_fmt fmt fmt
`)
	writeFile(t, repo, "stdlib/format.ml", `open CamlinternalFormatBasics
open CamlinternalFormat

let printf fmt =
  make_printf fmt

let eprintf fmt =
  make_printf fmt
`)
	writeFile(t, repo, "stdlib/printf.ml", `open CamlinternalFormat

let kbprintf fmt =
  make_printf fmt
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	type edge struct{ from, to string }
	calls := map[edge]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, fromOK := symbolsByID[r.FromID]
		to, toOK := symbolsByID[r.ToID]
		if !fromOK || !toOK || from.Language != "OCaml" || to.Language != "OCaml" {
			continue
		}
		calls[edge{from.FilePath + ":" + from.Name, to.FilePath + ":" + to.Name}] = true
	}
	for _, want := range []edge{
		{"stdlib/format.ml:printf", "stdlib/camlinternalFormat.ml:make_printf"},
		{"stdlib/format.ml:eprintf", "stdlib/camlinternalFormat.ml:make_printf"},
		{"stdlib/printf.ml:kbprintf", "stdlib/camlinternalFormat.ml:make_printf"},
		{"stdlib/camlinternalFormat.ml:make_printf", "stdlib/camlinternalFormatBasics.ml:concat_fmt"},
		{"stdlib/camlinternalFormat.ml:make_printf", "stdlib/camlinternalFormat.ml:convert_int"},
		{"stdlib/camlinternalFormat.ml:make_printf", "stdlib/camlinternalFormat.ml:convert_int32"},
		{"stdlib/camlinternalFormat.ml:make_printf", "stdlib/camlinternalFormat.ml:convert_int64"},
	} {
		if !calls[want] {
			t.Fatalf("missing OCaml open/callable-reference CALLS edge %v in %v", want, calls)
		}
	}
}
