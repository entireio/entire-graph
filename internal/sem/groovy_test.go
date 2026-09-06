package sem

import (
	"strings"
	"testing"
)

func groovyEntityIndex(t *testing.T, entities []Entity) map[string]Entity {
	t.Helper()
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
		seen[entity.Kind+":"+entity.Name] = entity
	}
	return seen
}

func TestGroovyEntitiesTypesAndMembers(t *testing.T) {
	entities, status := groovyEntities(`package org.example

import java.util.concurrent.Callable

@CompileStatic
class Transpiler extends BaseVisitor implements Callable, Runnable {
  public static final int MAX_DEPTH = 12
  private Writer writer
  String scriptName = 'script.groovy'
  def captured, pending = []
  Map<String, List<Integer>> index = [:]

  Transpiler(Writer writer) {
    this.writer = writer
  }

  TranspileResult compileScript(String script, int compilePhase, ClassLoader classLoader = null) {
    def local = script.trim()
    return new TranspileResult(local)
  }

  @Override
  void visitMethod(MethodNode node) {
    visitParameters(node.parameters)
  }

  private static <T> T tryAll(Callable<T> first, Closure second) {
    first.call()
  }

  def "feature methods can have quoted names"() {
    expect:
    true
  }
}

interface Visitor {
  void visit(Node node)
  String describe(Node node) throws IOException
}

trait Nameable {
  String name

  String displayName() {
    name.capitalize()
  }
}

enum Phase {
  CONVERSION, CANONICALIZATION,
  OUTPUT(9)

  final int number = 0

  int number() {
    number
  }
}

@interface Marker {
  String value()
}
`)
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	seen := groovyEntityIndex(t, entities)
	wantKinds := map[string]string{
		"Transpiler":                                       "class",
		"Transpiler.MAX_DEPTH":                             "field",
		"Transpiler.writer":                                "field",
		"Transpiler.scriptName":                            "field",
		"Transpiler.captured":                              "field",
		"Transpiler.pending":                               "field",
		"Transpiler.index":                                 "field",
		"Transpiler.compileScript":                         "method",
		"Transpiler.visitMethod":                           "method",
		"Transpiler.tryAll":                                "method",
		"Transpiler.feature methods can have quoted names": "method",
		"Visitor":                                          "interface",
		"Visitor.visit":                                    "method",
		"Visitor.describe":                                 "method",
		"Nameable":                                         "trait",
		"Nameable.name":                                    "field",
		"Nameable.displayName":                             "method",
		"Phase":                                            "enum",
		"Phase.CONVERSION":                                 "field",
		"Phase.CANONICALIZATION":                           "field",
		"Phase.OUTPUT":                                     "field",
		"field:Phase.number":                               "field",
		"method:Phase.number":                              "method",
		"Marker":                                           "interface",
		"Marker.value":                                     "method",
	}
	for name, kind := range wantKinds {
		entity, ok := seen[name]
		if !ok {
			t.Errorf("missing entity %q in %#v", name, names(entities))
			continue
		}
		if entity.Kind != kind {
			t.Errorf("%s kind = %q, want %q", name, entity.Kind, kind)
		}
	}
	// The constructor mirrors the Java path: not a symbol.
	if _, ok := seen["Transpiler.Transpiler"]; ok {
		t.Errorf("constructor should not be emitted as a symbol")
	}
	// Local variables inside method bodies never become symbols.
	for _, forbidden := range []string{"local", "Transpiler.local"} {
		if _, ok := seen[forbidden]; ok {
			t.Errorf("method-local %q leaked as a symbol", forbidden)
		}
	}
	// Container scoping: the class spans its members.
	class := seen["Transpiler"]
	method := seen["Transpiler.visitMethod"]
	if method.StartLine <= class.StartLine || method.EndLine > class.EndLine {
		t.Errorf("visitMethod (%d-%d) not inside Transpiler (%d-%d)",
			method.StartLine, method.EndLine, class.StartLine, class.EndLine)
	}
	if !strings.Contains(class.Signature, "extends BaseVisitor") ||
		!strings.Contains(class.Signature, "implements Callable, Runnable") {
		t.Errorf("class signature lost inheritance clause: %q", class.Signature)
	}
	edges := supertypesFromSignature("Groovy", class.Signature)
	var relations []string
	for _, edge := range edges {
		relations = append(relations, edge.Relation+":"+edge.Super)
	}
	joined := strings.Join(relations, " ")
	for _, want := range []string{"EXTENDS:BaseVisitor", "IMPLEMENTS:Callable", "IMPLEMENTS:Runnable"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing supertype edge %s in %v", want, relations)
		}
	}
}

func TestGroovyEntitiesScriptLevel(t *testing.T) {
	entities, status := groovyEntities(`#!/usr/bin/env groovy
apply plugin: 'groovy'

def greet(name) {
  println "hello ${name}"
}

String upper(String value) {
  value.toUpperCase()
}

def counter = 0
String label = 'x'

dependencies {
  implementation 'org.codehaus.groovy:groovy:3.0.9'
}

task integrationTest(type: Test) {
  description = 'runs the integration tests'
}

println greet('world')
`)
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	seen := groovyEntityIndex(t, entities)
	if entity := seen["greet"]; entity.Kind != "function" {
		t.Errorf("greet kind = %q, want function (%v)", entity.Kind, names(entities))
	}
	if entity := seen["upper"]; entity.Kind != "function" {
		t.Errorf("upper kind = %q, want function", entity.Kind)
	}
	if entity := seen["counter"]; entity.Kind != "variable" {
		t.Errorf("counter kind = %q, want variable", entity.Kind)
	}
	if entity := seen["label"]; entity.Kind != "variable" {
		t.Errorf("label kind = %q, want variable", entity.Kind)
	}
	for _, forbidden := range []string{"dependencies", "implementation", "integrationTest", "description", "plugin"} {
		if _, ok := seen[forbidden]; ok {
			t.Errorf("script/DSL statement %q leaked as a symbol", forbidden)
		}
	}
}

func TestGroovyEntitiesClosuresAreNotSymbols(t *testing.T) {
	entities, status := groovyEntities(`class Callbacks {
  def onEvent = { event ->
    handle(event)
  }

  static Closure factory = { int a, int b -> a + b }

  void handle(event) {
    def inner = { x -> x * 2 }
    [1, 2, 3].each { item ->
      inner(item)
    }
  }

  void after() { }
}
`)
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	seen := groovyEntityIndex(t, entities)
	if entity := seen["Callbacks.onEvent"]; entity.Kind != "field" {
		t.Errorf("onEvent kind = %q, want field (%v)", entity.Kind, names(entities))
	}
	if entity := seen["Callbacks.factory"]; entity.Kind != "field" {
		t.Errorf("factory kind = %q, want field", entity.Kind)
	}
	for _, forbidden := range []string{"inner", "Callbacks.inner", "item", "event", "x"} {
		if _, ok := seen[forbidden]; ok {
			t.Errorf("closure %q leaked as a symbol", forbidden)
		}
	}
	// Brace depth must survive the closures: after() is still a member.
	if entity := seen["Callbacks.after"]; entity.Kind != "method" {
		t.Errorf("after kind = %q, want method — closure braces broke scoping (%v)", entity.Kind, names(entities))
	}
}

func TestGroovyEntitiesStringsDoNotConfuseStructure(t *testing.T) {
	entities, status := groovyEntities(`class Strings {
  String gstring = "prefix ${values.collect { "${it.name} {" }.join(', ')} suffix"
  String triple = """
    class Fake {
      void bogus() { }
    }
  """
  String single = '''another { fake } body'''
  def pattern = /class Slashy \{ def x/
  def dollar = $/multi
    line ${"nested"} $$ body/$
  int ratio = total / count

  void real() {
    def sql = """
      SELECT count(*) FROM users WHERE name = '${name}'
    """
  }
}
`)
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	seen := groovyEntityIndex(t, entities)
	for _, want := range []string{"Strings", "Strings.gstring", "Strings.triple", "Strings.single", "Strings.pattern", "Strings.dollar", "Strings.ratio", "Strings.real"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("missing entity %q in %v", want, names(entities))
		}
	}
	for _, forbidden := range []string{"Fake", "Slashy", "Fake.bogus", "bogus"} {
		if _, ok := seen[forbidden]; ok {
			t.Errorf("string content %q leaked as a symbol", forbidden)
		}
	}
	if entity := seen["Strings"]; entity.EndLine < seen["Strings.real"].EndLine {
		t.Errorf("class end (%d) precedes member end (%d): string masking broke depth", entity.EndLine, seen["Strings.real"].EndLine)
	}
}

func TestGroovyMaskedStringsNotScannedForCalls(t *testing.T) {
	block := `void run() {
  def sql = """
    exec procedureCall(1)
  """
  def re = ~/matchThing(x)/
  def g = "inline ${quoted(1)} and fake(2)"
  actual(3)
}`
	masked := maskGroovyLiteralsAndComments(block)
	callNames := callLikeIdentifiers(masked, "Groovy")
	if _, ok := callNames["actual"]; !ok {
		t.Fatalf("real call lost: %v", callNames)
	}
	for _, forbidden := range []string{"procedureCall", "matchThing", "fake", "quoted"} {
		if _, ok := callNames[forbidden]; ok {
			t.Errorf("string content %q scanned as a call", forbidden)
		}
	}
}

func TestGroovyCommandCallIdentifiers(t *testing.T) {
	block := maskGroovyLiteralsAndComments(`private String visitParameters(parameters) {
  boolean first = true
  visitModifiers(p.modifiers)
  visitType p.type
  print ' ' + p.name
  sleep 100
  handle this
  emit it
  logger log
  first = false
  Foo bar
}`)
	got := groovyCommandCallIdentifiers(block)
	for _, want := range []string{"visitType", "sleep", "handle", "emit"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing command call %q in %v", want, got)
		}
	}
	for _, forbidden := range []string{"boolean", "first", "logger", "Foo", "private", "print"} {
		if _, ok := got[forbidden]; ok {
			t.Errorf("false command call %q in %v", forbidden, got)
		}
	}
}

func TestGroovyKAndRAndAbstractShapes(t *testing.T) {
	entities, status := groovyEntities(`abstract class Shapes
{
  abstract double area()

  double scaled(double factor)
  {
    return area() * factor
  }
}
`)
	if status.ParseError {
		t.Fatalf("unexpected parse status: %#v", status)
	}
	seen := groovyEntityIndex(t, entities)
	if entity := seen["Shapes"]; entity.Kind != "class" || entity.EndLine < 8 {
		t.Errorf("Shapes = %#v, want class spanning to closing brace", entity)
	}
	if entity := seen["Shapes.area"]; entity.Kind != "method" {
		t.Errorf("abstract area kind = %q, want method (%v)", entity.Kind, names(entities))
	}
	if entity := seen["Shapes.scaled"]; entity.Kind != "method" || entity.EndLine <= entity.StartLine {
		t.Errorf("K&R scaled = %#v, want method with attached body", entity)
	}
}

func TestGroovyParseStatusReportsUnbalancedBraces(t *testing.T) {
	entities, status := groovyEntities(`class Broken {
  void fine() {
  }
`)
	if !status.ParseError {
		t.Fatalf("expected parse error for unbalanced braces, got %#v", status)
	}
	seen := groovyEntityIndex(t, entities)
	if _, ok := seen["Broken"]; !ok {
		t.Errorf("degraded parse should still extract confident entities: %v", names(entities))
	}
	if _, ok := seen["Broken.fine"]; !ok {
		t.Errorf("degraded parse should still extract members: %v", names(entities))
	}
}

func TestGroovyExtensionRouting(t *testing.T) {
	for _, path := range []string{"build.gradle", "Script.groovy", "helper.gvy"} {
		entities, language, status := TreeSitterParser{}.ParseWithStatus(path, "def helper() { }\n")
		if language != "Groovy" {
			t.Fatalf("%s language = %q, want Groovy", path, language)
		}
		if status.ParseError {
			t.Fatalf("%s parse status: %#v", path, status)
		}
		if len(entities) != 1 || entities[0].Name != "helper" || entities[0].Kind != "function" {
			t.Fatalf("%s entities = %#v", path, entities)
		}
	}
}

func names(entities []Entity) []string {
	out := make([]string, 0, len(entities))
	for _, entity := range entities {
		out = append(out, entity.Kind+":"+entity.Name)
	}
	return out
}
