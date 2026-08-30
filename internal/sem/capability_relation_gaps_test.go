package sem

import (
	"sort"
	"strings"
	"testing"
)

// TestCapabilityMatrixDeclaresDartErlangOCamlAndSQLRelations is a companion to
// TestCapabilityMatrixCoversEmittedRelations that does not depend on a golden
// fixture happening to contain the right construct.
//
// That guard walks goldenFixtures, so it only ever sees the relation families
// those fixtures exercise. None of them carried an annotated signature or a
// forwarded argument in Dart, Erlang, OCaml or SQL, so four languages emitted
// nine relation families that `capabilities --json` said they could not
// produce, and nothing tripped. AGENTS.md tells agents to feature-detect
// against that report before trusting a language, so an under-declaration
// silently steers them off relation families the provider does emit.
//
// This test builds the constructs directly instead of hoping for them: each
// source below carries an annotated signature and forwards a parameter into a
// call, which is what the generic type and data-flow passes key on.
//
// The direction of the check matches the golden guard: emitted-but-undeclared
// is the failure. A declared-but-unseen relation is fine, because absence from
// one small file is not evidence a language cannot produce it.
func TestCapabilityMatrixDeclaresDartErlangOCamlAndSQLRelations(t *testing.T) {
	sources := map[string]string{
		// Dart: annotated params and return, an async call, and both a
		// return-value flow and a forwarded argument.
		"lib/flow.dart": `class Point {
  int x = 0;
  int y = 0;
  Point(this.x, this.y);
}

int add(int a, int b) { return a + b; }

int total(int a, int b) { return add(a, b); }

Point make(int a, int b) { return Point(a, b); }

int usePoint(Point p) { return add(p.x, p.y); }

Future<int> later(int a) async { return await add(a, 1); }
`,
		// Erlang has no return keyword; the flow pass still sees the parameter
		// forwarded into the callee's argument list.
		"src/flow.erl": `-module(flow).
-export([add/2, total/2]).

add(A, B) -> A + B.

total(A, B) -> add(A, B).
`,
		// OCaml annotates parameters and returns positionally (`(a : int)`),
		// which the signature-type pass reads like any other annotation.
		"src/flow.ml": `type point = { px : int; py : int }

let add (a : int) (b : int) : int = a + b

let total (a : int) (b : int) : int = add a b

let make (a : int) (b : int) : point = { px = a; py = b }

let point_sum (p : point) : int = add p.px p.py
`,
		// A SQL function body forwards its arguments into another function the
		// same way, and the call pass already resolves it.
		"db/flow.sql": `CREATE FUNCTION add_nums(a integer, b integer) RETURNS integer AS $$
BEGIN
  RETURN a + b;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION total_nums(a integer, b integer) RETURNS integer AS $$
BEGIN
  RETURN add_nums(a, b);
END;
$$ LANGUAGE plpgsql;
`,
	}

	repo := t.TempDir()
	for path, source := range sources {
		writeFile(t, repo, path, source)
	}

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	capabilities := Capabilities()
	global := map[string]bool{}
	for _, relation := range capabilities.HeuristicRelationTypes {
		global[relation] = true
	}
	declared := map[string]map[string]bool{}
	for language, relations := range capabilities.RelationSupportByLanguage {
		set := make(map[string]bool, len(relations))
		for _, relation := range relations {
			set[relation] = true
		}
		declared[language] = set
	}

	// Every language must have produced symbols, so the check cannot pass by
	// extracting nothing at all — a parser regression would otherwise read as
	// a clean run.
	languages := []string{"Dart", "Erlang", "OCaml", "SQL"}
	symbolCount := map[string]int{}
	for _, symbol := range snapshot.Symbols {
		symbolCount[symbol.Language]++
	}
	for _, language := range languages {
		if symbolCount[language] == 0 {
			t.Fatalf("%s produced no symbols; the fixture no longer exercises the passes this test guards", language)
		}
	}

	// ...and each must have produced relations beyond the structural ones, or
	// the fixture has stopped reaching the type and flow passes.
	interesting := map[string]bool{}
	violations := map[string][]string{}
	for _, relation := range snapshot.Relations {
		// from_id is repoKey:language:path:kind:name for symbols and
		// repoKey:file:path for files; only the former carries a language.
		parts := strings.Split(relation.FromID, ":")
		if len(parts) < 3 || parts[1] == "file" {
			continue
		}
		language := parts[1]
		switch relation.Type {
		case "CONTAINS", "DEFINES", "CALLS", "CONSTRUCTS", "IMPORTS":
		default:
			interesting[language] = true
		}
		if global[relation.Type] || declared[language][relation.Type] {
			continue
		}
		if !contains(violations[language], relation.Type) {
			violations[language] = append(violations[language], relation.Type)
		}
	}
	for _, language := range languages {
		if !interesting[language] {
			t.Errorf("%s emitted no type or data-flow relation; the fixture no longer exercises the passes this test guards", language)
		}
	}

	for _, language := range languages {
		relations := violations[language]
		sort.Strings(relations)
		for _, relation := range relations {
			t.Errorf("%s emits %s but capabilities declares neither it per-language nor as a global heuristic", language, relation)
		}
	}
}
