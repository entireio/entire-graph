package sem

import (
	"sort"
	"testing"
)

// capabilityRelationProbes pins one single-language source file per language
// whose relation declaration was derived empirically rather than from the
// golden fixtures.
//
// Each source deliberately carries the two constructs the generic passes key
// on: a signature a type name can be read out of, and a parameter forwarded
// into a call. That is what makes the expected set below reachable from this
// one file, with no dependency on which other fixtures happen to exist.
//
// Single-language repositories are load-bearing, not incidental. USES_TYPE
// resolves a signature identifier against every type symbol in the repository
// by short name, so a polyglot fixture can hand one language an edge that
// points at another language's type: an `-record(point, ...)` in an Erlang
// file is enough to make an unrelated R or Clojure function look like it uses
// a type. Probing one language at a time keeps each expectation a property of
// that language's extractor.
var capabilityRelationProbes = map[string]struct {
	path   string
	source string
}{
	// A type hint in the signature is Clojure's only route to USES_TYPE; the
	// bare `[p]` parameter of an idiomatic accessor carries no type at all.
	"Clojure": {"src/fixture/core.clj", `(ns fixture.core)

(defrecord Point [x y])

(defn add [a b] (+ a b))

(defn make-point [x y] (Point. x y))

(defn point-sum [^Point p] (add (:x p) (:y p)))
`},
	"Dart": {"lib/flow.dart", `class Point {
  int x = 0;
  int y = 0;
  Point(this.x, this.y);
}

int add(int a, int b) { return a + b; }

int total(int a, int b) { return add(a, b); }

Point make(int a, int b) { return Point(a, b); }

int usePoint(Point p) { return add(p.x, p.y); }

Future<int> later(int a) async { return await add(a, 1); }
`},
	"Elixir": {"lib/point.ex", `defmodule Fixture.Point do
  def add(a, b) do
    a + b
  end

  def sum(x, y) do
    add(x, y)
  end
end
`},
	// Erlang reaches the signature passes through a record pattern in the head
	// and the flow pass through a forwarded argument, and it needs both in one
	// file: a fixture with only one of them under-reports the language.
	"Erlang": {"src/flow.erl", `-module(flow).
-export([add/2, total/2, sum/1]).

-record(point, {x = 0, y = 0}).

add(A, B) -> A + B.

total(A, B) -> add(A, B).

sum(#point{x = X, y = Y}) -> add(X, Y).
`},
	"F#": {"src/Flow.fs", `module Fixture

type Point = { X: int; Y: int }

let add a b = a + b

let sum (p: Point) = add p.X p.Y
`},
	"Haskell": {"src/Flow.hs", `module Fixture where

data Point = Point
  { px :: Int
  , py :: Int
  }

add :: Int -> Int -> Int
add a b = a + b

pointSum :: Point -> Int
pointSum p = add (px p) (py p)

main :: IO ()
main = print (pointSum (Point 1 2))
`},
	"Julia": {"src/flow.jl", `struct Point
    x::Int
    y::Int
end

function add(a, b)
    return a + b
end

function pointsum(p::Point)
    return add(p.x, p.y)
end
`},
	"Lua": {"src/flow.lua", `local function add(a, b)
  return a + b
end

local function total(x, y)
  return add(x, y)
end

return total
`},
	// OCaml annotates parameters and returns positionally, which the
	// signature-type pass reads like any other annotation.
	"OCaml": {"src/flow.ml", `type point = { px : int; py : int }

let add (a : int) (b : int) : int = a + b

let total (a : int) (b : int) : int = add a b

let make (a : int) (b : int) : point = { px = a; py = b }

let point_sum (p : point) : int = add p.px p.py
`},
	"Objective-C": {"src/Flow.m", `#import <Foundation/Foundation.h>

static NSInteger addValues(NSInteger a, NSInteger b) {
    return a + b;
}

static NSInteger total(NSInteger x, NSInteger y) {
    return addValues(x, y);
}
`},
	"Perl": {"lib/flow.pl", `use strict;
use warnings;

sub add {
    my ($a, $b) = @_;
    return $a + $b;
}

sub total {
    my ($x, $y) = @_;
    return add($x, $y);
}

1;
`},
	// R declares no type symbols -- `setClass` produces none -- so USES_TYPE is
	// unreachable here however the signature is written, and only DATA_FLOWS is
	// declared for it.
	"R": {"R/flow.R", `add <- function(a, b) {
  a + b
}

make_point <- function(x, y) {
  structure(list(x = x, y = y), class = "point")
}

point_sum <- function(p) {
  add(p$x, p$y)
}
`},
	// A SQL function body forwards its arguments into another function, and the
	// call pass already resolves it.
	"SQL": {"db/flow.sql", `CREATE FUNCTION add_nums(a integer, b integer) RETURNS integer AS $$
BEGIN
  RETURN a + b;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION total_nums(a integer, b integer) RETURNS integer AS $$
BEGIN
  RETURN add_nums(a, b);
END;
$$ LANGUAGE plpgsql;
`},
}

// structuralRelationTypes are the relations mergeLanguageSupport adds to every
// language that can be parsed at all, so they are not part of the per-language
// declaration this test pins.
var structuralRelationTypes = map[string]bool{
	"CONTAINS":   true,
	"DEFINES":    true,
	"CALLS":      true,
	"CONSTRUCTS": true,
	"IMPORTS":    true,
}

// TestCapabilityRelationDeclarationsMatchIsolatedExtraction asserts, per
// language, that ooRelationSupport is exactly the set of non-structural
// relations that language's extractor emits over a single-language fixture.
//
// TestCapabilityMatrixCoversEmittedRelations checks only one direction --
// emitted implies declared -- over whichever relations the golden fixtures
// happen to contain. That is too weak to keep two independent changes to this
// map consistent: two branches can each set the same key to a different partial
// slice, both stay green against their own fixture, and whichever merges second
// silently discards the other's relations. `capabilities --json` then keeps
// under-reporting the language, which is the failure the declarations exist to
// prevent, because AGENTS.md tells agents to feature-detect against that report.
//
// Equality closes that. A narrowed entry fails because the fixture emits a
// relation the map no longer declares; a widened one fails because the map
// declares a relation the fixture cannot produce. Both directions are checked
// against extraction rather than against a second copy of the table, so the
// test cannot drift into agreeing with a wrong declaration.
func TestCapabilityRelationDeclarationsMatchIsolatedExtraction(t *testing.T) {
	t.Parallel()

	global := map[string]bool{}
	for _, relation := range Capabilities().HeuristicRelationTypes {
		global[relation] = true
	}

	languages := make([]string, 0, len(capabilityRelationProbes))
	for language := range capabilityRelationProbes {
		languages = append(languages, language)
	}
	sort.Strings(languages)

	for _, language := range languages {
		t.Run(language, func(t *testing.T) {
			t.Parallel()
			probe := capabilityRelationProbes[language]

			repo := t.TempDir()
			writeFile(t, repo, probe.path, probe.source)

			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
			if err != nil {
				t.Fatal(err)
			}

			languageByID := make(map[string]string, len(snapshot.Symbols))
			symbols := 0
			for _, symbol := range snapshot.Symbols {
				languageByID[symbol.ID] = symbol.Language
				if symbol.Language == language {
					symbols++
				}
			}
			// Without this the assertion would pass on an empty extraction, so
			// a parser regression would read as a clean run.
			if symbols == 0 {
				t.Fatalf("%s produced no symbols; %s no longer reaches the extractor", language, probe.path)
			}

			emitted := map[string]bool{}
			for _, relation := range snapshot.Relations {
				if languageByID[relation.FromID] != language {
					continue
				}
				if structuralRelationTypes[relation.Type] || global[relation.Type] {
					continue
				}
				emitted[relation.Type] = true
			}

			got := make([]string, 0, len(emitted))
			for relation := range emitted {
				got = append(got, relation)
			}
			sort.Strings(got)

			want := append([]string(nil), ooRelationSupport[language]...)
			sort.Strings(want)

			if len(got) != len(want) {
				t.Fatalf("%s declares %v but emits %v", language, want, got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s declares %v but emits %v", language, want, got)
				}
			}
		})
	}
}
