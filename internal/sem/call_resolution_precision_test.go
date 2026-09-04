package sem

import (
	"strings"
	"testing"
)

// runCallsFrom returns every CALLS relation whose from-symbol short name is the
// given name.
func runCallsFrom(snapshot ProviderSnapshot, from string) []RelationRecord {
	var out []RelationRecord
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && lastSegment(r.FromID) == from {
			out = append(out, r)
		}
	}
	return out
}

// A dotted call whose terminal name resolves to an in-repo class method
// (`from myapp import models; models.User.save()`) must NOT fabricate an
// external CALLS edge. The method is kind-gated out of the dotted local edge,
// but the call targets an in-repo member — the generic receiver path already
// emits the correct local edge, and the dotted external fallback must stay
// silent (precision > recall: a wrong external edge is worse than none).
func TestPythonDottedFromImportKindGatedMethodEmitsNoExternal(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "myapp/__init__.py", "")
	writeFile(t, repo, "myapp/models.py", `class User:
    @classmethod
    def save(cls):
        return 1
`)
	writeFile(t, repo, "consumer.py", `from myapp import models


def go():
    return models.User.save()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runCallsFrom(snapshot, "go") {
		if r.TargetKind == "external" || strings.HasPrefix(r.ToID, "external:symbol:") {
			t.Fatalf("dotted path fabricated external CALLS go -> %s for an in-repo method", r.ToID)
		}
	}
	// The correct in-repo edge from the generic receiver path must still exist.
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "go", "consumer.py", "save", "myapp/models.py") {
		t.Fatalf("missing in-repo CALLS go -> save @ myapp/models.py: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// The alias-import spelling `import myapp.models as models` composes the same
// `myapp.models.User` module qualifier as the from-import form, so it must emit
// the same result: no fabricated external edge, and the in-repo edge preserved.
func TestPythonDottedAliasKindGatedMethodEmitsNoExternal(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "myapp/__init__.py", "")
	writeFile(t, repo, "myapp/models.py", `class User:
    @classmethod
    def save(cls):
        return 1
`)
	writeFile(t, repo, "consumer.py", `import myapp.models as models


def go():
    return models.User.save()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runCallsFrom(snapshot, "go") {
		if r.TargetKind == "external" || strings.HasPrefix(r.ToID, "external:symbol:") {
			t.Fatalf("dotted path fabricated external CALLS go -> %s for an in-repo method", r.ToID)
		}
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "go", "consumer.py", "save", "myapp/models.py") {
		t.Fatalf("missing in-repo CALLS go -> save @ myapp/models.py: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A dotted call into a genuinely external package (`import requests;
// requests.sessions.session()`) has no in-repo symbol of the terminal name, so
// the external fallback must still fire. The suppression is scoped to in-repo
// targets only and must not silence real external edges.
func TestPythonDottedGenuineExternalStillEmitted(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", `import requests


def go():
    return requests.sessions.session()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range runCallsFrom(snapshot, "go") {
		if r.ToID == externalID("symbol", "requests.sessions.session") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing genuine external CALLS go -> requests.sessions.session: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

// A `from pkg import Name` binding where Name is a re-exported repo class is
// string-identical to a submodule import in importsByName, but `Name.attr.fn()`
// is class-attribute access, not a `pkg.attr` submodule path. The composer must
// recognise that Name is a repo class defined under pkg and compose nothing, so
// no CALLS edge is fabricated to an unrelated sibling module whose filename
// coincides with the attribute (myapp/settings.py here). Precision > recall: the
// true target (Config.settings being a Settings instance) is unresolved, and no
// edge is better than a wrong one.
func TestPythonDottedReExportedClassAttributeEmitsNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "myapp/__init__.py", "from myapp._impl import Config\n")
	writeFile(t, repo, "myapp/_impl.py", `class Settings:
    def load(self):
        return 1


class Config:
    settings = Settings()
`)
	writeFile(t, repo, "myapp/settings.py", `def load():
    return 2
`)
	writeFile(t, repo, "consumer.py", `from myapp import Config


def go():
    return Config.settings.load()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if edges := runCallsFrom(snapshot, "go"); len(edges) != 0 {
		t.Fatalf("re-exported class attribute access must emit no CALLS edge from go, got %#v", edges)
	}
}

// Repro B (the ubiquitous Flask-SQLAlchemy idiom): `from app import db` where
// app/__init__.py binds a module-level singleton `db = SQLAlchemy()`. `db` is
// not a submodule, so `db.session.query()` is instance-attribute access, not an
// `app.session` submodule path. The from-import form composes nothing (no
// app.db submodule), so no CALLS edge is fabricated — in particular no spurious
// external `app.session.query`. Precision > recall.
func TestPythonFromImportSingletonChainedCallEmitsNoEdge(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/__init__.py", "db = SQLAlchemy()\n")
	writeFile(t, repo, "c.py", `from app import db


def caller():
    return db.session.query()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if edges := runCallsFrom(snapshot, "caller"); len(edges) != 0 {
		t.Fatalf("from-imported module-level singleton chained call must emit no CALLS edge from caller, got %#v", edges)
	}
}

// Repro A: pkg/__init__.py binds a module-level singleton `service = Service()`.
// `from pkg import service` then `service.helper.fn()` is instance-attribute
// access on that singleton, NOT the pkg.helper submodule. With no pkg.service
// submodule the from-import form composes nothing, so the coincidentally-named
// sibling pkg/helper.py must NOT receive a fabricated local edge.
func TestPythonFromImportSingletonDoesNotResolveToSiblingModule(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pkg/__init__.py", "from pkg.impl import Service\nservice = Service()\n")
	writeFile(t, repo, "pkg/impl.py", `class Service:
    pass
`)
	writeFile(t, repo, "pkg/helper.py", `def fn():
    return 1
`)
	writeFile(t, repo, "c.py", `from pkg import service


def caller():
    return service.helper.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if edges := runCallsFrom(snapshot, "caller"); len(edges) != 0 {
		t.Fatalf("from-imported singleton attribute call must emit no CALLS edge from caller, got %#v", edges)
	}
}

// `import x.y as x` binds the local `x` to module x.y — an alias rename that
// happens to shadow its own leading segment. `x.z.fn()` therefore means
// x.y.z.fn and must resolve to x/y/z.py, never the sibling x/z.py that a
// leading-segment reading would name, even though both define fn. Exactly one
// edge, to x/y/z.py.
func TestPythonSelfShadowAliasRenameResolvesToRenamedModule(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "x/__init__.py", "")
	writeFile(t, repo, "x/z.py", `def fn():
    return 1
`)
	writeFile(t, repo, "x/y/__init__.py", "")
	writeFile(t, repo, "x/y/z.py", `def fn():
    return 2
`)
	writeFile(t, repo, "consumer.py", `import x.y as x


def go():
    return x.z.fn()
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var targets []string
	for _, r := range runCallsFrom(snapshot, "go") {
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "fn" {
			targets = append(targets, to.FilePath)
		}
	}
	if len(targets) != 1 || targets[0] != "x/y/z.py" {
		t.Fatalf("`import x.y as x` + x.z.fn() targets = %#v, want exactly [x/y/z.py]", targets)
	}
}

// A reassignment of an already-typed local nested inside a fluent chain's
// method-call arguments (`$u->set($w = get())`) must still invalidate that
// local: $w is reassigned away from its Foo ctor type, so the later single-hop
// `$w->render` must NOT emit a type_inferred edge to Foo::render. The chain's
// consumed span protects the typer's own `$v =` lead but excludes the
// argument-paren interior where the foreign write lives. Positive control: the
// same chain with a write-free argument keeps every type, so `$w->render` still
// resolves.
func TestPerlNestedChainArgWriteDropsInferredType(t *testing.T) {
	fooBar := map[string]string{
		"lib/Foo.pm": `package Foo;

sub new {
  return bless {}, shift;
}

sub render {
  return 1;
}
`,
		"lib/Bar.pm": `package Bar;

sub new {
  return bless {}, shift;
}

sub set {
  return shift;
}

sub go {
  return shift;
}
`,
	}

	// Negative: the nested `$w = get()` invalidates $w, so no type_inferred edge.
	nested := t.TempDir()
	writeFile(t, nested, "lib/App.pm", `package App;

sub run {
  my $w = Foo->new;
  my $u = Bar->new;
  my $v = $u->set($w = get())->go;
  $w->render;
}
`)
	for p, c := range fooBar {
		writeFile(t, nested, p, c)
	}
	nestedSnap, err := BuildProviderSnapshotWithOptions(t.Context(), nested, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runCallsFrom(nestedSnap, "run") {
		if r.Resolution == "type_inferred" && lastSegment(r.ToID) == "render" {
			t.Fatalf("stale $w type fabricated a type_inferred CALLS run -> render: %#v", r)
		}
	}

	// Positive control: a write-free argument keeps $w typed Foo, so the
	// single-hop $w->render still resolves via the inferred package type.
	clean := t.TempDir()
	writeFile(t, clean, "lib/App.pm", `package App;

sub run {
  my $w = Foo->new;
  my $u = Bar->new;
  my $v = $u->set(cfg())->go;
  $w->render;
}
`)
	for p, c := range fooBar {
		writeFile(t, clean, p, c)
	}
	cleanSnap, err := BuildProviderSnapshotWithOptions(t.Context(), clean, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range runCallsFrom(cleanSnap, "run") {
		if r.Resolution == "type_inferred" && lastSegment(r.ToID) == "render" {
			found = true
		}
	}
	if !found {
		t.Fatalf("write-free chain argument must keep $w typed: expected type_inferred CALLS run -> render, got %#v", relationsOfType(cleanSnap.Relations, "CALLS"))
	}
}

// A Perl-inferred receiver type (`my $x = Foo->new`) must never reach the
// language-agnostic generic type-inference loop. When Foo.pm has no `render`
// sub but a TypeScript `class Foo { render() }` exists, the Perl receiver must
// NOT resolve cross-language to the TS method: the Perl-only type map is
// consumed solely by the language-gated Perl resolver, so no edge is emitted.
func TestPerlInferredTypeDoesNotLeakToCrossLanguageClass(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  $x->render;
}
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new {
  return bless {}, shift;
}
`)
	writeFile(t, repo, "web/foo.ts", `export class Foo {
  render(): number {
    return 1;
  }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	for _, r := range runCallsFrom(snapshot, "run") {
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "render" {
			t.Fatalf("Perl inferred type leaked into a cross-language edge: run -> render @ %s (%s)", to.FilePath, to.Language)
		}
	}
}

// When both a Perl `Foo::go` and a TypeScript `Foo.go` exist, a Perl receiver
// typed `Foo` must resolve to exactly ONE edge — the Perl one. The generic loop
// must never see the Perl-inferred type and double-emit a second edge to the TS
// class.
func TestPerlInferredTypePrefersPerlOverCollidingClass(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lib/App.pm", `package App;

sub run {
  my $x = Foo->new;
  $x->go;
}
`)
	writeFile(t, repo, "lib/Foo.pm", `package Foo;

sub new {
  return bless {}, shift;
}

sub go {
  return 1;
}
`)
	writeFile(t, repo, "web/foo.ts", `export class Foo {
  go(): number {
    return 1;
  }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	var goEdges []RelationRecord
	for _, r := range runCallsFrom(snapshot, "run") {
		if to, ok := symbolsByID[r.ToID]; ok && to.Name == "go" {
			goEdges = append(goEdges, r)
		}
	}
	if len(goEdges) != 1 {
		t.Fatalf("expected exactly one CALLS run -> go edge, got %d: %#v", len(goEdges), goEdges)
	}
	if to := symbolsByID[goEdges[0].ToID]; to.Language != "Perl" || to.FilePath != "lib/Foo.pm" {
		t.Fatalf("run -> go resolved to the wrong target: %#v", to)
	}
}

// pythonFFICallEdges builds a repo whose only `compute` lives in C, so the
// name-only tier cannot rescue the edge: an import binding either survives
// Python scope analysis and resolves across the FFI boundary, or the edge is
// gone entirely.
func pythonFFICallEdges(t *testing.T, app string) []RelationRecord {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "frobnicate.c", `int compute(int value) {
	return value + 1;
}
`)
	writeFile(t, repo, "app.py", app)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var out []RelationRecord
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && strings.HasSuffix(r.ToID, "frobnicate.c:function:compute") {
			out = append(out, r)
		}
	}
	return out
}

// assertOneImportResolvedComputeEdge demands the exact edge an unshadowed
// imported call in a plain function body produces, so a signature call is held
// to the body oracle rather than to a weaker surviving tier.
func assertOneImportResolvedComputeEdge(t *testing.T, edges []RelationRecord, fromSuffix string) {
	t.Helper()
	if len(edges) != 1 {
		t.Fatalf("want exactly one CALLS edge to the imported C `compute`, got %d: %#v", len(edges), edges)
	}
	if !strings.HasSuffix(edges[0].FromID, fromSuffix) {
		t.Fatalf("edge came from %q, want a symbol ending %q", edges[0].FromID, fromSuffix)
	}
	if edges[0].Resolution != "import_resolved" {
		t.Fatalf("want the import tier to bind the call, got resolution %q: %#v", edges[0].Resolution, edges[0])
	}
}

// Python evaluates decorators, default arguments and annotations where the
// `def` statement runs -- in the ENCLOSING scope -- not inside the call frame.
// The scope walker descended only into the function body, so a call in the
// signature was recorded in no scope at all. That is not a harmless omission:
// the call scan still attributes it to the function whose signature line holds
// it, and a `complete` scope view that reports no modules for the name makes
// importsWithName DELETE the file-level import binding. The imported call then
// loses its resolution tier outright. Decorators already worked -- tree-sitter
// hangs them off `decorated_definition`, outside the function node -- so this
// covers the signature itself, and keeps the enclosing scope authoritative so a
// shadowed name still fails closed.
func TestPythonSignatureCallsResolveThroughEnclosingImports(t *testing.T) {
	t.Run("body call is the oracle", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return compute(1)
`), "app.py:function:plain")
	})

	t.Run("default argument", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def defaulted(value=compute()):
    return value
`), "app.py:function:defaulted")
	})

	t.Run("parameter annotation", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def annotated(value: compute()):
    return value
`), "app.py:function:annotated")
	})

	t.Run("return annotation", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def returned() -> compute():
    return 1
`), "app.py:function:returned")
	})

	t.Run("method default reads the class body scope chain", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

class Holder:
    def method(self, value=compute()):
        return value
`), "app.py:method:Holder.method")
	})

	t.Run("decorator was already enclosing-scoped", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

@compute()
def decorated():
    return 1
`), "file:app.py")
	})

	t.Run("a parameter never shadows its own default", func(t *testing.T) {
		// The default is evaluated before the parameter exists, so `compute`
		// here is the module-level import, not the parameter beside it.
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def shadowing(compute=compute()):
    return compute
`), "app.py:function:shadowing")
	})

	t.Run("an enclosing local still fails closed", func(t *testing.T) {
		if edges := pythonFFICallEdges(t, `from frobnicate import compute

def outer():
    compute = 1
    def inner(value=compute()):
        return value
    return inner
`); len(edges) != 0 {
			t.Fatalf("the enclosing function rebound `compute`, so its nested default must not reach the import: %#v", edges)
		}
	})
}

// `from mod import (compute,)` splits on commas into `(compute` and `)`, so the
// grouping parentheses were parsed as part of the bound name. The scope walker
// then bound a name no call can ever match, reported the scope as complete, and
// importsWithName deleted the real `compute` binding -- taking the resolved
// CALLS edge with it. The multi-line form is the one Python code actually
// writes, and the line-oriented import scanners cannot see past its first line
// at all, so the AST-derived scope binding is the only thing that restores it.
func TestPythonParenthesizedFromImportBindsTheImportedMember(t *testing.T) {
	t.Run("single line", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import (compute,)

def plain():
    return compute(1)
`), "app.py:function:plain")
	})

	t.Run("multi line", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import (
    compute,
)

def plain():
    return compute(1)
`), "app.py:function:plain")
	})

	t.Run("parenthesised alias resolves to the member it renames", func(t *testing.T) {
		edges := pythonFFICallEdges(t, `from frobnicate import (compute as c,)

def plain():
    return c(1)
`)
		assertOneImportResolvedComputeEdge(t, edges, "app.py:function:plain")
	})
}

// assertNoComputeEdge is the negative fence beside
// assertOneImportResolvedComputeEdge: the name really is rebound at the call
// site, so the imported `compute` must not be reached. Keeping both halves on
// every construct is what stops a relaxation from turning into blanket
// permission.
func assertNoComputeEdge(t *testing.T, edges []RelationRecord, why string) {
	t.Helper()
	if len(edges) != 0 {
		t.Fatalf("%s, so no CALLS edge to the imported C `compute` may exist, got %d: %#v", why, len(edges), edges)
	}
}

// tree-sitter gives `except_clause` no fields at all: its exception expression,
// its `as` target and its handler block are plain named children. The walker
// asked it for a "body" field, got nothing, and returned -- so nothing inside a
// try statement's handler was ever recorded as a call of the enclosing scope.
// The function-wide local pass then asked the same clause for a "name" field,
// got its last named child (the handler BLOCK) instead, and recursed into it,
// declaring every name written anywhere in the handler a local of the whole
// function. Both halves delete edges: the second one deletes them for calls in
// the `try` body too, which the walker had recorded correctly.
func TestPythonExceptClauseIsOrdinaryCodeOfItsScope(t *testing.T) {
	t.Run("exception expression", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    try:
        pass
    except compute():
        pass
`), "app.py:function:plain")
	})

	t.Run("handler body", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    try:
        pass
    except ValueError:
        return compute(1)
`), "app.py:function:plain")
	})

	t.Run("a handler must not make the try body's call a local", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    try:
        return compute(1)
    except ValueError:
        return compute(2)
`), "app.py:function:plain")
	})

	t.Run("an `as` target still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    try:
        pass
    except ValueError as compute:
        return compute(1)
`), "the handler rebound `compute` to the caught exception")
	})

	t.Run("except* binds its `as` target too", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    try:
        pass
    except* ValueError as compute:
        return compute(1)
`), "an exception-group handler rebinds `compute` exactly like a plain one")
	})
}

// `with_item` has no alias field either -- the alias hangs off a nested
// `as_pattern` -- so the walker took the item's last named child, which is that
// whole `as_pattern`, VALUE expression included. The function-wide local pass
// recursed into it and declared the names of the context expression locals of
// the function, so `with compute() as handle:` reported its own import shadowed.
func TestPythonWithItemBindsOnlyItsAlias(t *testing.T) {
	t.Run("the context expression is not a binding", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    with compute() as handle:
        return handle
`), "app.py:function:plain")
	})

	t.Run("the alias still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    with ctx() as compute:
        return compute(1)
`), "the with statement rebound `compute`")
	})

	t.Run("a destructured alias still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    with ctx() as (compute, other):
        return compute(1)
`), "the with statement rebound `compute` through a tuple target")
	})
}

// Python evaluates a comprehension's OUTERMOST iterable where the comprehension
// is written and passes the result into the comprehension's own frame; every
// later iterable runs inside that frame. The walker evaluated all of them
// inside it, so `[x for x in compute() for compute in ys]` -- which real Python
// runs happily -- reported its import shadowed by a target bound afterwards.
// The interleaved-filter case is the opposite and must stay suppressed: CPython
// raises UnboundLocalError on `[x for x in xs if compute() for compute in ys]`,
// so that `compute` is genuinely the comprehension's own local, not the import.
func TestPythonComprehensionOutermostIterableReadsTheEnclosingScope(t *testing.T) {
	t.Run("outermost iterable", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return [x for x in compute() for compute in ys]
`), "app.py:function:plain")
	})

	t.Run("a later iterable still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return [y for compute in ys for y in compute()]
`), "the second iterable runs inside the comprehension frame, where `compute` is a target")
	})

	t.Run("an interleaved filter still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return [x for x in xs if compute() for compute in ys]
`), "CPython raises UnboundLocalError here, so `compute` is the comprehension's own local")
	})
}

// The import scanners that record WHAT a local name is bound to are line
// oriented, so a parenthesised list spanning lines showed them only
// `from mod import (` -- an item list of one bare paren. No member name was
// recorded behind the alias, and the resolver, which needs the member name
// because the workspace index is keyed by it, had nothing to resolve. The AST
// scope walker meanwhile saw the import and reported the call unshadowed, so
// the edge was dropped rather than merely left to a weaker tier.
func TestPythonMultiLineFromImportAliasResolvesToItsMember(t *testing.T) {
	t.Run("multi-line alias matches the single-line oracle", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import (
    compute as c,
)

def plain():
    return c(1)
`), "app.py:function:plain")
	})

	t.Run("comments inside the list are not part of a name", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import (  # noqa
    compute as c,  # keep
)

def plain():
    return c(1)
`), "app.py:function:plain")
	})

	t.Run("an ambiguous alias still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import (
    compute as c,
)
from other import (
    helper as c,
)

def plain():
    return c(1)
`), "`c` names two different members, so which one it renames is unknown")
	})

	t.Run("a parenthesised expression is not an import list", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `values = (
    1,
)
from frobnicate import compute

def plain():
    return compute(1)
`), "app.py:function:plain")
	})
}

// The import scanner joins a parenthesised from-import list only when it is a
// real from-import whose list closes, and consumes nothing otherwise -- so a
// stray paren cannot hide the imports written below it.
func TestPythonMultiLineImportScannerKeepsLaterImportsVisible(t *testing.T) {
	t.Run("a closed list records the member behind its alias", func(t *testing.T) {
		bindings := importedPythonImportBindings("from mod import (\n    compute as c,\n)\n")
		info, ok := bindings["c"]
		if !ok || info.bindsModule || len(info.members) != 1 || info.members[0] != "compute" {
			t.Fatalf("bindings[\"c\"] = %#v (present %v), want the single member `compute`", info, ok)
		}
	})

	t.Run("an unclosed list hides nothing below it", func(t *testing.T) {
		bindings := importedPythonImportBindings("from broken import (\n    thing,\nfrom mod import helper\n")
		info, ok := bindings["helper"]
		if !ok || len(info.members) != 1 || info.members[0] != "helper" {
			t.Fatalf("bindings[\"helper\"] = %#v (present %v), want the import below the unclosed list still recorded", info, ok)
		}
	})

	t.Run("a parenthesised expression is not an import list", func(t *testing.T) {
		names := importedPythonNames("values = (\n    1,\n)\nfrom mod import helper\n")
		if got := names["helper"]; len(got) != 1 || got[0] != "mod" {
			t.Fatalf("names[\"helper\"] = %#v, want [mod]", got)
		}
	})
}

// pythonScopeCallable is one hand-built record for pythonScopeModules. The AST
// scope walker matches a `def` to the smallest function/method symbol whose
// byte range contains it and reads nothing else from the record, so a header
// substring and the rest of the file is the whole fixture.
func pythonScopeCallable(t *testing.T, src, id, kind, header string) SymbolRecord {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("header %q is not in the fixture", header)
	}
	record := SymbolRecord{ID: id, Kind: kind, Language: "Python", FilePath: "app.py"}
	record.sourceStartByte = start
	record.sourceEndByte = len(src)
	return record
}

// pythonScopeModules is the modules one bare call name reaches from one owner
// in the AST scope view, which is where a definition-name binding is actually
// observable. `resolveCallTargets` DECLINES a name this view reports as bound
// before any tier runs, so a name wrongly reported bound deletes an edge --
// but a definition also defines a same-named symbol, and that symbol wins the
// same-file tier (or a CONSTRUCTS relation) before imports are consulted, so
// the emitted relations cannot distinguish the two answers. The view can.
func pythonScopeModules(t *testing.T, src string, symbols []SymbolRecord, owner SymbolRecord, name string) []string {
	t.Helper()
	scopes := newPythonBareImportScopes(src, symbols)
	if !scopes.complete {
		t.Fatalf("scope analysis did not complete for:\n%s", src)
	}
	return scopes.importModules(owner, name)
}

var pythonScopeModule = SymbolRecord{Kind: "file", ID: "file:app.py", FilePath: "app.py"}

// Python binds a `def`/`class` name only once the whole statement has run: its
// decorators, bases, default arguments and annotations are evaluated FIRST and
// still read the enclosing binding. CPython is the oracle --
//
//	$ python3 -c 'def compute(): return "OUTER"
//	def compute(v=compute()): return v
//	print(compute())'                                  -> OUTER
//	$ python3 -c 'def compute(v=compute()): return v'   -> NameError: name 'compute' is not defined
//
// -- and it says the same for a class: a base expression, a class-body
// statement and a method's signature all run before the class name exists,
// while a method BODY runs after it and really does see the class. The walker
// bound the name at the `def`/`class` line instead, so every eagerly evaluated
// part of the statement was told the name was already rebound; for an imported
// name that is the fail-closed direction, which deletes an edge.
func TestPythonDefinitionNameBindsOnlyAfterItsOwnSignature(t *testing.T) {
	t.Run("a default argument reaches the shadowed enclosing name", func(t *testing.T) {
		src := `from frobnicate import compute

def compute(value=compute()):
    return value
`
		fn := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the default runs before `def` rebinds `compute`, so it must reach the import; got %#v", got)
		}
	})

	t.Run("an annotation reaches the shadowed enclosing name", func(t *testing.T) {
		src := `from frobnicate import compute

def compute(value: compute()):
    return value
`
		fn := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the annotation runs before `def` rebinds `compute`, so it must reach the import; got %#v", got)
		}
	})

	t.Run("the body is deferred code and stays shadowed", func(t *testing.T) {
		src := `from frobnicate import compute

def compute():
    return compute()
`
		fn := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 0 {
			t.Fatalf("a recursive call runs after the name is bound, so it must NOT reach the import; got %#v", got)
		}
	})

	t.Run("a base expression reaches the shadowed enclosing name", func(t *testing.T) {
		src := `from frobnicate import compute

class compute(compute()):
    pass
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("bases run before `class` rebinds `compute`, so they must reach the import; got %#v", got)
		}
	})

	t.Run("a class-body statement reaches the shadowed enclosing name", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    value = compute()
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the class body runs before the class name is bound, so it must reach the import; got %#v", got)
		}
	})

	t.Run("a method signature in the class body reaches it too", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=compute()):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("a method default is evaluated with the class body, before the class name is bound; got %#v", got)
		}
	})

	t.Run("a method body is deferred code and stays shadowed", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self):
        return compute()
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 0 {
			t.Fatalf("a method body runs after the class exists, so `compute` is the class, not the import; got %#v", got)
		}
	})

	t.Run("a class-body call after a same-named method stays shadowed", func(t *testing.T) {
		src := `from frobnicate import compute

class Holder:
    def compute(self):
        return 1
    value = compute()
`
		method := pythonScopeCallable(t, src, "app.py:method:Holder.compute", "method", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("the class body bound `compute` above this line, so it must NOT reach the import; got %#v", got)
		}
	})

	t.Run("code after the statement stays shadowed", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    pass

value = compute()
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("the class is bound by this line, so it must NOT reach the import; got %#v", got)
		}
	})

	t.Run("an enclosing function local still fails closed", func(t *testing.T) {
		// CPython raises UnboundLocalError here: the nested `def` makes `compute`
		// a local of `outer`, unbound while the default is evaluated. It never
		// reaches the import, so neither may the view.
		src := `from frobnicate import compute

def outer():
    def compute(value=compute()):
        return value
    return compute
`
		outer := pythonScopeCallable(t, src, "app.py:function:outer", "function", "def outer")
		inner := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		inner.sourceEndByte = strings.Index(src, "    return compute\n")
		for _, owner := range []SymbolRecord{outer, inner} {
			if got := pythonScopeModules(t, src, []SymbolRecord{outer, inner}, owner, "compute"); len(got) != 0 {
				t.Fatalf("`compute` is an unbound local of `outer` there, so %q must not reach the import; got %#v", owner.ID, got)
			}
		}
	})
}

// Python separates a from-import's module from its list with the `import`
// KEYWORD. The whitespace around it is free, and a parenthesised list needs
// none at all. CPython is the oracle --
//
//	$ printf 'from native import\tcompute\nprint(compute())\n' > t.py; python3 t.py
//	NATIVE
//	$ printf 'from native import(compute)\nprint(compute())\n' > t.py; python3 t.py
//	NATIVE
//
// -- but the AST scope view split on a literal " import ", so it recorded no
// binding for either spelling. That is the deleting direction, not a harmless
// omission: the line scanner's regex writes the separator `\s+import\s+`, so it
// DID resolve the tab form, and a `complete` view reporting no modules for the
// name makes importsWithName drop that binding and the CALLS edge with it.
func TestPythonFromImportKeywordIsWhitespaceIndependent(t *testing.T) {
	t.Run("a single space is the oracle", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import compute\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("a tab after the keyword binds", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import\tcompute\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("tabs on both sides of the keyword bind", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from\tfrobnicate\timport\tcompute\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("a parenthesised list needs no space at all", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import(compute)\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("a module whose own name contains the keyword is not split", func(t *testing.T) {
		// Relaxing the separator must not turn `importlib` into a keyword: the
		// module is the whole name, so the binding still reads `importlib`.
		src := "from importlib import compute\n\ndef plain():\n    return compute(1)\n"
		fn := pythonScopeCallable(t, src, "app.py:function:plain", "function", "def plain")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 1 || got[0] != "importlib" {
			t.Fatalf("`compute` is imported from the whole module `importlib`; got %#v", got)
		}
	})

	t.Run("a star import still binds no name", func(t *testing.T) {
		// The negative fence on the relaxation: `from mod import*` names
		// nothing, so the view must invent no binding for a name it never saw.
		src := "from frobnicate import*\n\ndef plain():\n    return compute(1)\n"
		fn := pythonScopeCallable(t, src, "app.py:function:plain", "function", "def plain")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 0 {
			t.Fatalf("a star import binds no `compute`, so the view must report none; got %#v", got)
		}
	})

	t.Run("a rebound name still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import\tcompute\n\ndef plain():\n    compute = 1\n    return compute(1)\n"), "the function rebound `compute`")
	})
}

// Python evaluates a lambda's defaults where the `lambda` EXPRESSION runs -- in
// the enclosing scope -- not inside the call frame, exactly as it does for a
// `def`. CPython is the oracle --
//
//	$ python3 -c 'def compute(): return "OUTER-FN"
//	f = lambda compute=compute(): compute
//	print(f())'                                 -> OUTER-FN
//	$ python3 -c 'f = lambda a, b=a: b'         -> NameError: name 'a' is not defined
//
// -- so a default neither sees the parameters beside it nor belongs to the
// lambda's isolated scope. The walker descended only into the lambda body, so
// such a call was recorded in no scope at all. The call scan still credits it
// to the callable whose source holds the lambda, and a `complete` view that
// reports no modules for the name makes importsWithName DELETE the file-level
// import binding, so the imported call loses its resolution tier outright.
func TestPythonLambdaDefaultsResolveThroughEnclosingImports(t *testing.T) {
	t.Run("body call is the oracle", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return (lambda value: compute(value))()
`), "app.py:function:plain")
	})

	t.Run("default argument", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return (lambda value=compute(): value)()
`), "app.py:function:plain")
	})

	t.Run("a module-level lambda default", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

handler = lambda value=compute(): value
`), "file:app.py")
	})

	t.Run("a parameter never shadows its own default", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return (lambda compute=compute(): compute)()
`), "app.py:function:plain")
	})

	t.Run("the body still fails closed under its own parameter", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return (lambda compute: compute(1))()
`), "the lambda's parameter rebinds `compute` for its body")
	})

	t.Run("an enclosing local still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def outer():
    compute = 1
    return (lambda value=compute(): value)()
`), "the enclosing function rebound `compute` before the lambda is written")
	})

	t.Run("a comprehension target still fails closed", func(t *testing.T) {
		// The default reads the scope the lambda is WRITTEN in, which inside a
		// comprehension is the comprehension's own frame:
		//
		//	$ python3 -c 'def compute(): return "OUT"
		//	ys = [lambda: "INNER"]
		//	print([(lambda v=compute(): v)() for compute in ys])'   -> ['INNER']
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return [(lambda value=compute(): value)() for compute in ys]
`), "the comprehension binds `compute`, and its frame is where the lambda is written")
	})
}

// Python binds an assignment target only AFTER the value expression has been
// evaluated, so a call on the right-hand side still reads the binding the
// statement is about to replace. CPython is the oracle --
//
//	$ python3 -c 'import sys, types
//	m = types.ModuleType("native"); m.compute = lambda: "IMPORTED"; sys.modules["native"] = m
//	from native import compute
//	compute = compute()
//	print(compute)'                                  -> IMPORTED
//
// -- and the same holds for a `for` target, whose iterable is evaluated once
// before the target is ever bound. Recording the target at its OWN offset made
// the scope view report those calls as shadowed, and a `complete` view that
// reports no modules for a name makes importsWithName DELETE the file-level
// import binding, taking the resolved call edge with it. Inside a FUNCTION the
// very same source raises UnboundLocalError, because a name assigned anywhere in
// a function is local throughout it --
//
//	$ python3 -c '...same preamble...
//	def f(): compute = compute()
//	f()'   -> UnboundLocalError: cannot access local variable 'compute'
//
// -- so the function-wide `locals` set has to keep failing closed there.
func TestPythonAssignmentTargetBindsAfterItsValueExpression(t *testing.T) {
	t.Run("module-level self assignment", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

compute = compute()
`), "file:app.py")
	})

	t.Run("module-level augmented assignment", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

compute += compute()
`), "file:app.py")
	})

	t.Run("module-level for target", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

for compute in compute():
    pass
`), "file:app.py")
	})

	t.Run("the same shape in a function still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    compute = compute()
    return compute
`), "a name assigned anywhere in a function is local throughout it, so CPython raises UnboundLocalError")
	})

	t.Run("a function for target still fails closed", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    for compute in compute():
        pass
`), "a for target is a function-wide local, so CPython raises UnboundLocalError")
	})

	t.Run("a later module-level call still sees the rebinding", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

compute = 1
handler = compute()
`), "the module-level assignment has finished before the later call runs")
	})

	t.Run("a for body still sees its own loop target", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

for compute in items:
    handler = compute()
`), "the loop target is bound before the body runs")
	})
}

// A `case` pattern CAPTURES the names it names, and a captured name is local for
// the WHOLE function like any other assignment. CPython is the oracle --
//
//	$ python3 -c '...import compute from a native module...
//	def f(v):
//	    r = compute()
//	    match v:
//	        case compute:
//	            pass
//	    return r
//	f(1)'   -> UnboundLocalError: cannot access local variable 'compute'
//
// -- so the function-wide binding pass has to see capture patterns, or a bare
// `compute()` elsewhere in the body is reported as an unshadowed import and can
// resolve across the FFI boundary. Only the capturing positions bind: a class
// pattern's class, a keyword pattern's keyword, a mapping key and a dotted value
// pattern are ordinary loads, and treating any of them as a binding would DELETE
// a real edge, so each keeps a fence here.
//
//	$ python3 -c 'src = """
//	def f(v):
//	    match v:
//	        case [a, *b]:
//	            pass
//	        case {"k": c, **rest}:
//	            pass
//	        case int() as d:
//	            pass
//	        case Point(x=e):
//	            pass
//	        case _:
//	            pass
//	"""
//	ns = {}; exec(compile(src, "<s>", "exec"), ns)
//	print(ns["f"].__code__.co_varnames)'   -> ('v', 'a', 'b', 'c', 'd', 'e', 'rest')
func TestPythonMatchCaseCapturesAreFunctionWideLocals(t *testing.T) {
	t.Run("a capture shadows a later call", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case compute:
            pass
    return compute()
`), "`case compute:` captures the name, making it a local of the whole function")
	})

	t.Run("a capture shadows an earlier call too", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    result = compute()
    match value:
        case compute:
            pass
    return result
`), "a captured name is local for the whole function body, before the match as well")
	})

	t.Run("an as-pattern alias captures", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case int() as compute:
            pass
    return compute()
`), "`as compute` captures the matched value")
	})

	t.Run("a sequence star captures", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case [first, *compute]:
            pass
    return compute()
`), "`*compute` captures the rest of the sequence")
	})

	t.Run("a mapping value captures", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case {"k": compute}:
            pass
    return compute()
`), "a mapping pattern's VALUE is a capture position")
	})

	t.Run("a mapping double star captures", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case {"k": first, **compute}:
            pass
    return compute()
`), "`**compute` captures the rest of the mapping")
	})

	t.Run("a class pattern's positional sub-pattern captures", func(t *testing.T) {
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case Point(compute):
            pass
    return compute()
`), "a class pattern's positional sub-pattern is a capture position")
	})

	t.Run("a class pattern's class name is a load, not a binding", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case Point(x=y):
            pass
    return compute()
`), "app.py:function:plain")
	})

	t.Run("a keyword pattern's keyword is not a binding", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case Point(compute=y):
            pass
    return compute()
`), "app.py:function:plain")
	})

	t.Run("a dotted value pattern is not a binding", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case Color.compute:
            pass
    return compute()
`), "app.py:function:plain")
	})

	t.Run("the wildcard binds nothing", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case _:
            pass
    return compute()
`), "app.py:function:plain")
	})

	t.Run("a mapping key is a load, not a binding", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain(value):
    match value:
        case {Color.compute: y}:
            pass
    return compute()
`), "app.py:function:plain")
	})
}
