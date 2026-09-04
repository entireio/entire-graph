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
	// exhaustive marks a probe rich enough to reach every relation the language
	// declares, so its expectation is checked as full equality. A probe that is
	// not exhaustive is checked only over genericRelationTypes: the language
	// also declares framework-shaped relations (a GraphQL schema, a tRPC
	// router, an async call site, a resolved supertype) that no single
	// self-contained file can produce, and inventing one here would pin the
	// framework detector rather than the language's extractor.
	exhaustive bool
}{
	// A type hint in the signature is Clojure's only route to USES_TYPE; the
	// bare `[p]` parameter of an idiomatic accessor carries no type at all.
	"Clojure": {path: "src/fixture/core.clj", source: `(ns fixture.core)

(defrecord Point [x y])

(defn add [a b] (+ a b))

(defn make-point [x y] (Point. x y))

(defn point-sum [^Point p] (add (:x p) (:y p)))
`, exhaustive: true},
	"Dart": {path: "lib/flow.dart", source: `class Point {
  int x = 0;
  int y = 0;
  Point(this.x, this.y);
}

int add(int a, int b) { return a + b; }

int total(int a, int b) { return add(a, b); }

Point make(int a, int b) { return Point(a, b); }

int usePoint(Point p) { return add(p.x, p.y); }

Future<int> later(int a) async { return await add(a, 1); }
`, exhaustive: true},
	"Elixir": {path: "lib/point.ex", source: `defmodule Fixture.Point do
  def add(a, b) do
    a + b
  end

  def sum(x, y) do
    add(x, y)
  end
end
`, exhaustive: true},
	// Erlang reaches the signature passes through a record pattern in the head
	// and the flow pass through a forwarded argument, and it needs both in one
	// file: a fixture with only one of them under-reports the language.
	"Erlang": {path: "src/flow.erl", source: `-module(flow).
-export([add/2, total/2, sum/1]).

-record(point, {x = 0, y = 0}).

add(A, B) -> A + B.

total(A, B) -> add(A, B).

sum(#point{x = X, y = Y}) -> add(X, Y).
`, exhaustive: true},
	"F#": {path: "src/Flow.fs", source: `module Fixture

type Point = { X: int; Y: int }

let add a b = a + b

let sum (p: Point) = add p.X p.Y
`, exhaustive: true},
	"Haskell": {path: "src/Flow.hs", source: `module Fixture where

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
`, exhaustive: true},
	"Julia": {path: "src/flow.jl", source: `struct Point
    x::Int
    y::Int
end

function add(a, b)
    return a + b
end

function pointsum(p::Point)
    return add(p.x, p.y)
end
`, exhaustive: true},
	"Lua": {path: "src/flow.lua", source: `local function add(a, b)
  return a + b
end

local function total(x, y)
  return add(x, y)
end

return total
`, exhaustive: true},
	// OCaml annotates parameters and returns positionally, which the
	// signature-type pass reads like any other annotation.
	"OCaml": {path: "src/flow.ml", source: `type point = { px : int; py : int }

let add (a : int) (b : int) : int = a + b

let total (a : int) (b : int) : int = add a b

let make (a : int) (b : int) : point = { px = a; py = b }

let point_sum (p : point) : int = add p.px p.py
`, exhaustive: true},
	// The probe carries a local `@interface` and C-style declarations that name
	// it, because that -- not Objective-C method syntax -- is what reaches the
	// signature-type pass. Without a type of its own the probe could only ever
	// emit DATA_FLOWS, which is exactly how the declaration came to under-report
	// this language: the fixture proved nothing about the three relations the
	// shared C extraction path already produces.
	"Objective-C": {path: "src/Flow.m", source: `#import <Foundation/Foundation.h>

@interface Point : NSObject {
    NSInteger x;
    NSInteger y;
}
@end

static NSInteger addValues(NSInteger a, NSInteger b) {
    return a + b;
}

static NSInteger total(NSInteger x, NSInteger y) {
    return addValues(x, y);
}

static NSInteger movePoint(Point *p, NSInteger dx) {
    return addValues(dx, dx);
}

static Point *makePoint(NSInteger a) {
    return nil;
}
`, exhaustive: true},
	"Perl": {path: "lib/flow.pl", source: `use strict;
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
`, exhaustive: true},
	// R declares no type symbols -- `setClass` produces none -- so USES_TYPE is
	// unreachable here however the signature is written, and only DATA_FLOWS is
	// declared for it.
	"R": {path: "R/flow.R", source: `add <- function(a, b) {
  a + b
}

make_point <- function(x, y) {
  structure(list(x = x, y = y), class = "point")
}

point_sum <- function(p) {
  add(p$x, p$y)
}
`, exhaustive: true},
	// A SQL function body forwards its arguments into another function, and the
	// call pass already resolves it.
	"SQL": {path: "db/flow.sql", source: `CREATE FUNCTION add_nums(a integer, b integer) RETURNS integer AS $$
BEGIN
  RETURN a + b;
END;
$$ LANGUAGE plpgsql;

CREATE FUNCTION total_nums(a integer, b integer) RETURNS integer AS $$
BEGIN
  RETURN add_nums(a, b);
END;
$$ LANGUAGE plpgsql;
`, exhaustive: true},
	// -- languages below were added when the declaration/emission equality was
	// extended from the thirteen probed above to every language the provider
	// runs call extraction for. Each carries the same five constructs so the
	// generic passes are all reachable from one file: a type declaration, a
	// parameter annotated with it, a return annotated with it, a field read, a
	// field write, and an argument forwarded into another call.

	// Bash and Zsh parse and yield callables, but their function headers carry
	// no parameter list at all, so no signature exists for a type to be read
	// out of and no generic relation is reachable. The probes pin that empty
	// expectation: a future grammar change that starts emitting one has to
	// declare it.
	"Bash": {path: "src/flow.sh", source: `#!/usr/bin/env bash
add() {
  echo $(( $1 + $2 ))
}

total() {
  add "$1" "$2"
}
`, exhaustive: true},
	"Zsh": {path: "src/flow.zsh", source: `#!/usr/bin/env zsh
add() {
  echo $(( $1 + $2 ))
}

total() {
  add "$1" "$2"
}
`, exhaustive: true},

	"C": {path: "src/flow.c", source: `struct Point {
  int x;
  int y;
};

int add(int a, int b) { return a + b; }

int total(int a, int b) { return add(a, b); }

int point_sum(struct Point p) { return add(p.x, p.y); }

struct Point make(int a, int b) {
  struct Point q;
  q.x = a;
  q.y = b;
  return q;
}
`, exhaustive: true},
	// C++ shares C's extraction path; the probe is kept separate so a divergence
	// between the two grammars shows up as a C++ failure rather than silence.
	"C++": {path: "src/flow.cpp", source: `struct Point {
  int x;
  int y;
};

int add(int a, int b) { return a + b; }

int total(int a, int b) { return add(a, b); }

int pointSum(Point p) { return add(p.x, p.y); }

int* update(Point p, int x) {
  p.x = x;
  return &p.y;
}

Point make(int a, int b) {
  Point q;
  q.x = a;
  q.y = b;
  return q;
}
`, exhaustive: true},

	// ClojureScript reads .cljs with the same grammar Clojure reads .clj with,
	// so it reaches USES_TYPE through the same `^Point` hint -- but it was not a
	// key of ooRelationSupport at all, so `capabilities --json` advertised only
	// the structural relations for it.
	"ClojureScript": {path: "src/fixture/core.cljs", source: `(ns fixture.core)

(defrecord Point [x y])

(defn add [a b] (+ a b))

(defn make-point [x y] (Point. x y))

(defn point-sum [^Point p] (add (:x p) (:y p)))
`, exhaustive: true},

	"C#": {path: "src/Flow.cs", source: `namespace Fixture {
  public class Point { public int X; public int Y; }

  public class Flow {
    public int Add(int a, int b) { return a + b; }
    public int Total(int a, int b) { return Add(a, b); }
    public int PointSum(Point p) { return Add(p.X, p.Y); }
    public Point Make(int a, int b) { var q = new Point(); q.X = a; q.Y = b; return q; }
  }
}
`},
	"Go": {path: "flow.go", source: `package fixture

type Point struct {
	X int
	Y int
}

func Add(a, b int) int { return a + b }

func Total(a, b int) int { return Add(a, b) }

func PointSum(p Point) int { return Add(p.X, p.Y) }

func Make(a, b int) Point {
	q := Point{}
	q.X = a
	q.Y = b
	return q
}
`},
	"Groovy": {path: "src/flow.groovy", source: `class Point {
  int x
  int y
}

class Flow {
  int add(int a, int b) { return a + b }
  int total(int a, int b) { return add(a, b) }
  int pointSum(Point p) { return add(p.x, p.y) }
  Point make(int a, int b) { Point q = new Point(); q.x = a; q.y = b; return q }
}
`, exhaustive: true},
	"Java": {path: "src/Flow.java", source: `public class Flow {
  static class Point { int x; int y; }

  int add(int a, int b) { return a + b; }

  int total(int a, int b) { return add(a, b); }

  int pointSum(Point p) { return add(p.x, p.y); }

  Point make(int a, int b) { Point q = new Point(); q.x = a; q.y = b; return q; }
}
`},
	// JavaScript has no type annotations, but a default argument that constructs
	// a local class puts the type name in the signature, which is exactly what
	// the USES_TYPE pass reads -- so the language reaches a relation its
	// declaration did not list.
	"JavaScript": {path: "src/flow.js", source: `class Point {
  constructor(x, y) {
    this.x = x;
    this.y = y;
  }
}

function add(a, b) { return a + b; }

function total(a, b) { return add(a, b); }

function pointSum(p = new Point(0, 0)) { return add(p.x, p.y); }

function make(a, b) {
  const q = new Point(a, b);
  q.x = a;
  return q;
}
`},
	"Kotlin": {path: "src/Flow.kt", source: `class Point(var x: Int, var y: Int)

fun add(a: Int, b: Int): Int { return a + b }

fun total(a: Int, b: Int): Int { return add(a, b) }

fun pointSum(p: Point): Int { return add(p.x, p.y) }

fun make(a: Int, b: Int): Point {
    val q = Point(a, b)
    q.x = a
    return q
}
`, exhaustive: true},
	"PHP": {path: "src/Flow.php", source: `<?php
class Point {
  public int $x = 0;
  public int $y = 0;
}

function add(int $a, int $b): int { return $a + $b; }

function total(int $a, int $b): int { return add($a, $b); }

function pointSum(Point $p): int { return add($p->x, $p->y); }

function make(int $a, int $b): Point { $q = new Point(); $q->x = $a; return $q; }
`},
	"Python": {path: "src/flow.py", source: `class Point:
    def __init__(self, x: int, y: int) -> None:
        self.x = x
        self.y = y


def add(a: int, b: int) -> int:
    return a + b


def total(a: int, b: int) -> int:
    return add(a, b)


def point_sum(p: Point) -> int:
    return add(p.x, p.y)


def make(a: int, b: int) -> Point:
    q = Point(a, b)
    q.x = a
    return q
`},
	// Ruby is annotation-free like JavaScript and reaches USES_TYPE the same
	// way, through a default argument that names a local class. INHERITS comes
	// from the `include` in the class body -- Ruby's header form (`class A < B`)
	// is not parsed, which is why the declaration lists INHERITS and not
	// EXTENDS.
	"Ruby": {path: "lib/flow.rb", source: `module Summable
  def describe
    "summable"
  end
end

class Point
  attr_accessor :x, :y

  def initialize(x, y)
    @x = x
    @y = y
  end
end

class Flow
  include Summable

  def add(a, b)
    a + b
  end

  def total(a, b)
    add(a, b)
  end

  def point_sum(p = Point.new(0, 0))
    add(p.x, p.y)
  end
end
`, exhaustive: true},
	"Rust": {path: "src/flow.rs", source: `pub struct Point {
    pub x: i32,
    pub y: i32,
}

pub fn add(a: i32, b: i32) -> i32 { a + b }

pub fn total(a: i32, b: i32) -> i32 { add(a, b) }

pub fn point_sum(p: Point) -> i32 { add(p.x, p.y) }

pub fn make(a: i32, b: i32) -> Point {
    let mut q = Point { x: 0, y: 0 };
    q.x = a;
    q.y = b;
    q
}
`},
	"Scala": {path: "src/Flow.scala", source: `class Point(var x: Int, var y: Int)

object Flow {
  def add(a: Int, b: Int): Int = a + b
  def total(a: Int, b: Int): Int = add(a, b)
  def pointSum(p: Point): Int = add(p.x, p.y)
  def make(a: Int, b: Int): Point = { val q = new Point(a, b); q.x = a; q }
}
`, exhaustive: true},
	// Swift resolves BOTH the write and the read through a local constructor
	// binding: `let q = Point()` types q, so `q.x = a` and `return q.x` are each
	// attributed. Only the read of a PARAMETER's field is not -- `_ p: Point` is
	// not a shape parameterVarTypes recognises -- which is the asymmetry the
	// probe records. The earlier fixture wrote through the binding but read only
	// through a parameter, so READS_FIELD looked unreachable when it was merely
	// unexercised.
	"Swift": {path: "Sources/Flow.swift", source: `class Point {
    var x: Int = 0
    var y: Int = 0
}

func add(_ a: Int, _ b: Int) -> Int { return a + b }

func total(_ a: Int, _ b: Int) -> Int { return add(a, b) }

func pointSum(_ p: Point) -> Int { return add(p.x, p.y) }

func make(_ a: Int, _ b: Int) -> Point {
    let q = Point()
    q.x = a
    return q
}

func readPoint() -> Int {
    let q = Point()
    return q.x
}
`, exhaustive: true},
	"TypeScript": {path: "src/flow.ts", source: `export class Point {
  x: number = 0;
  y: number = 0;
}

export function add(a: number, b: number): number { return a + b; }

export function total(a: number, b: number): number { return add(a, b); }

export function pointSum(p: Point): number { return add(p.x, p.y); }

export function make(a: number, b: number): Point {
  const q = new Point();
  q.x = a;
  return q;
}
`},
	// Zig extracts the struct but not its fields, so no field symbol exists for
	// a READS_FIELD/WRITES_FIELD edge to land on however the body is written.
	"Zig": {path: "src/flow.zig", source: `const Point = struct {
    x: i32,
    y: i32,
};

pub fn add(a: i32, b: i32) i32 { return a + b; }

pub fn total(a: i32, b: i32) i32 { return add(a, b); }

pub fn pointSum(p: Point) i32 { return add(p.x, p.y); }

pub fn make(a: i32, b: i32) Point {
    var q = Point{ .x = 0, .y = 0 };
    q.x = a;
    q.y = b;
    return q;
}
`, exhaustive: true},
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

// genericRelationTypes are the relations produced by passes that run over every
// extracted symbol with no per-language gate: usesTypeRelations,
// signatureTypeRelations, fieldAccessRelations and the data-flow pass are each
// reached from buildRelations under a `spec.emits(...)` check on the PROFILE
// alone. CONFIGURES and RESOURCE_DEPENDS_ON, by contrast, are filtered through
// recordsByRelationSupport, so for those the declaration gates the emission and
// the two cannot drift apart.
//
// That asymmetry is what makes this set the one worth pinning per language: a
// language reaches these relations by having the right symbol shapes, never by
// being listed anywhere, so ooRelationSupport can fall out of step with what
// the provider actually emits without any code path noticing.
//
// ACCESSES is deliberately excluded. Its only trigger is the `&` in
// fieldAccessRe, which is address-of in C, C++, C#, Go and Rust but plain
// bitwise-and in Java, Kotlin and TypeScript, where `return m &p.x;` is
// classified as an address-of and yields an ACCESSES edge. Pinning it per
// language would enshrine that misclassification as a capability.
var genericRelationTypes = map[string]bool{
	"USES_TYPE":    true,
	"PARAM_TYPE":   true,
	"RETURNS_TYPE": true,
	"READS_FIELD":  true,
	"WRITES_FIELD": true,
	"DATA_FLOWS":   true,
}

// callExtractionLanguages is every language the provider runs the generic
// relation passes for, derived from the same predicate buildRelations consults
// rather than from a second hand-maintained list.
func callExtractionLanguages() []string {
	set := map[string]bool{}
	for _, spec := range supportedLanguageSpecs() {
		if supportsCallExtraction(spec) {
			set[spec.language] = true
		}
	}
	languages := make([]string, 0, len(set))
	for language := range set {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return languages
}

// TestCapabilityRelationProbesCoverEveryCallExtractionLanguage fails when a
// language reaches the generic relation passes with no probe pinning what it
// emits. Without it the equality check below is only as good as whoever
// remembered to add an entry, which is the same weakness --
// coverage-by-coincidence -- that let the declarations drift in the first
// place.
func TestCapabilityRelationProbesCoverEveryCallExtractionLanguage(t *testing.T) {
	t.Parallel()

	var missing []string
	for _, language := range callExtractionLanguages() {
		if _, ok := capabilityRelationProbes[language]; !ok {
			missing = append(missing, language)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("no isolated relation probe for %v; every language supportsCallExtraction admits reaches the generic type, field and data-flow passes, so its declaration has to be pinned against extraction", missing)
	}

	for language := range capabilityRelationProbes {
		if len(ooRelationSupport[language]) == 0 {
			continue
		}
		found := false
		for _, candidate := range callExtractionLanguages() {
			if candidate == language {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s has a call-extraction probe but supportsCallExtraction does not admit it", language)
		}
	}
}

// TestCapabilityRelationDeclarationsMatchIsolatedExtraction asserts, per
// language, that ooRelationSupport agrees with what that language's extractor
// emits over a single-language fixture.
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
//
// The comparison is over genericRelationTypes for every language, and over the
// full non-structural set for the probes marked exhaustive. Restricting the
// rest is not a loosening of the invariant so much as an admission of what a
// self-contained file can prove: a language that also declares HANDLES_GRAPHQL
// or ASYNC_CALLS needs a framework or a runtime construct to reach it, and the
// golden fixtures already carry those.
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

			// A cross-language edge is the failure mode this test exists to
			// avoid measuring: USES_TYPE resolves a signature identifier
			// against every type symbol in the repository by short name, so an
			// edge landing outside the probe's language would credit the
			// language with a capability it does not have.
			for _, relation := range snapshot.Relations {
				if languageByID[relation.FromID] != language {
					continue
				}
				if to, ok := languageByID[relation.ToID]; ok && to != language {
					t.Fatalf("%s emitted %s into %s; the probe repository is no longer single-language", language, relation.Type, to)
				}
			}

			keep := func(relation string) bool { return genericRelationTypes[relation] }
			scope := "generic"
			if probe.exhaustive {
				keep = func(string) bool { return true }
				scope = "non-structural"
			}

			got := make([]string, 0, len(emitted))
			for relation := range emitted {
				if keep(relation) {
					got = append(got, relation)
				}
			}
			sort.Strings(got)

			want := make([]string, 0, len(ooRelationSupport[language]))
			for _, relation := range ooRelationSupport[language] {
				if keep(relation) {
					want = append(want, relation)
				}
			}
			sort.Strings(want)

			if len(got) != len(want) {
				t.Fatalf("%s declares %v but emits %v (%s relations)", language, want, got, scope)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s declares %v but emits %v (%s relations)", language, want, got, scope)
				}
			}
		})
	}
}

// declaresFieldRelation reports whether a language's entry declares either of
// the two relations fieldAccessRelations produces.
func declaresFieldRelation(language string) bool {
	for _, relation := range ooRelationSupport[language] {
		if relation == "READS_FIELD" || relation == "WRITES_FIELD" {
			return true
		}
	}
	return false
}

// TestCapabilityDeclaredFieldRelationsRequireAFieldSymbol asserts a language may
// not declare READS_FIELD or WRITES_FIELD unless its extractor produces a
// symbol of kind "field".
//
// This is the OVER-declaration direction, and it is the one the suite could not
// see. TestCapabilityMatrixCoversEmittedRelations fails only on a relation
// emitted WITHOUT a declaration, so a declaration with nothing behind it is
// invisible to it however many fixtures are added. That asymmetry is not
// harmless: an agent told by AGENTS.md to feature-detect against
// `capabilities --json` reads the relation, queries for it and gets nothing,
// which fails it the same way an under-declaration does.
//
// The equality check above does catch one -- but only because a probe body
// happens to contain a field access, so editing a probe to drop the access
// would silently drop the guard with it. The mechanism makes this check exact
// instead of incidental: fieldAccessRelations is the sole producer of both
// relations and resolves through fieldsByContainer, which is populated from
// symbol.Kind == "field" alone. With no field symbol there is nothing for the
// edge to land on however the body is written.
//
// Zig declared READS_FIELD on exactly that footing. tree-sitter-zig names a
// struct member `container_field`, a node type fieldEntities never admits -- it
// appears in no Go source in this repository -- so Zig extracts the struct and
// none of its members, and the relation was unreachable rather than merely
// unexercised.
func TestCapabilityDeclaredFieldRelationsRequireAFieldSymbol(t *testing.T) {
	t.Parallel()

	languages := make([]string, 0, len(capabilityRelationProbes))
	for language := range capabilityRelationProbes {
		if declaresFieldRelation(language) {
			languages = append(languages, language)
		}
	}
	sort.Strings(languages)
	// Without this the whole test would pass by selecting nothing if the two
	// relation names were ever renamed.
	if len(languages) == 0 {
		t.Fatal("no probed language declares READS_FIELD or WRITES_FIELD; this check would be vacuous")
	}

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

			total := 0
			fields := 0
			for _, symbol := range snapshot.Symbols {
				if symbol.Language != language {
					continue
				}
				total++
				if symbol.Kind == "field" {
					fields++
				}
			}
			if total == 0 {
				t.Fatalf("%s produced no symbols; %s no longer reaches the extractor", language, probe.path)
			}
			if fields == 0 {
				t.Fatalf("%s declares %v but extracts no symbol of kind %q from %s; fieldAccessRelations resolves only through fieldsByContainer, which is keyed on Kind == %q, so neither field relation is reachable for this language", language, ooRelationSupport[language], "field", probe.path, "field")
			}
		})
	}
}

// TestCapabilityReportCarriesEveryDeclaredRelation asserts every entry of
// ooRelationSupport reaches `capabilities --json`.
//
// The map is not the contract surface. relationSupportByLanguage builds the
// report by walking the language SPECS and folding this map in per language, so
// a key naming a language no spec produces is dropped without a word: the
// declaration sits in the source looking authoritative while the report agents
// feature-detect against never carries it. A new key is exactly the shape that
// can go missing that way, and the tests above would not notice -- they read
// ooRelationSupport directly.
func TestCapabilityReportCarriesEveryDeclaredRelation(t *testing.T) {
	t.Parallel()

	report := Capabilities().RelationSupportByLanguage

	languages := make([]string, 0, len(ooRelationSupport))
	for language := range ooRelationSupport {
		languages = append(languages, language)
	}
	sort.Strings(languages)

	for _, language := range languages {
		reported, ok := report[language]
		if !ok {
			t.Errorf("ooRelationSupport declares %v for %s but capabilities --json reports no such language", ooRelationSupport[language], language)
			continue
		}
		have := make(map[string]bool, len(reported))
		for _, relation := range reported {
			have[relation] = true
		}
		for _, relation := range ooRelationSupport[language] {
			if !have[relation] {
				t.Errorf("ooRelationSupport declares %s for %s but capabilities --json reports only %v", relation, language, reported)
			}
		}
	}
}
