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

func TestPythonDottedSameLanguageDataFlowRemainsAvailableWithoutGenericImport(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", `class AuthService:
    def validate(self, token):
        return bool(token)


def check_token(token):
    service = AuthService()
    return service.validate(token)
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "DATA_FLOWS" && strings.HasSuffix(relation.FromID, "function:check_token") && strings.HasSuffix(relation.ToID, "method:AuthService.validate") {
			return
		}
	}
	t.Fatalf("missing same-language dotted DATA_FLOWS check_token -> validate: %#v", relationsOfType(snapshot.Relations, "DATA_FLOWS"))
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

func assertNoComputeEdge(t *testing.T, edges []RelationRecord, why string) {
	t.Helper()
	if len(edges) != 0 {
		t.Fatalf("%s, so no CALLS edge to the imported C `compute` may exist, got %d: %#v", why, len(edges), edges)
	}
}

// tree-sitter gives `except_clause` no fields at all: its exception expression,
// its `as` target and its handler block are plain named children. The walker
// must visit all of them as ordinary code in the enclosing function scope.
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

// `with_item` has no alias field either: the alias hangs off a nested
// `as_pattern`, while the context expression remains ordinary executable code.
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

// pythonScopeCallable is a hand-built function/method record for the scope
// walker. Its byte range only needs to contain the corresponding definition.
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

func pythonScopeModules(t *testing.T, src string, symbols []SymbolRecord, owner SymbolRecord, name string) []string {
	t.Helper()
	scopes := newPythonBareImportScopes(src, symbols)
	if !scopes.complete {
		t.Fatalf("scope analysis did not complete for:\n%s", src)
	}
	return scopes.importModules(owner, name)
}

var pythonScopeModule = SymbolRecord{Kind: "file", ID: "file:app.py", FilePath: "app.py"}

func TestPythonDefinitionNameBindsOnlyAfterItsOwnSignature(t *testing.T) {
	t.Run("a default argument reaches the enclosing import", func(t *testing.T) {
		src := `from frobnicate import compute

def compute(value=compute()):
    return value
`
		fn := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the default runs before `def` rebinds `compute`; got %#v", got)
		}
	})

	t.Run("an annotation reaches the enclosing import", func(t *testing.T) {
		src := `from frobnicate import compute

def compute(value: compute()):
    return value
`
		fn := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the annotation runs before `def` rebinds `compute`; got %#v", got)
		}
	})

	t.Run("the body is deferred and stays shadowed", func(t *testing.T) {
		src := `from frobnicate import compute

def compute():
    return compute()
`
		fn := pythonScopeCallable(t, src, "app.py:function:compute", "function", "def compute")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 0 {
			t.Fatalf("a recursive body call runs after the name is bound; got %#v", got)
		}
	})

	t.Run("a class base reaches the enclosing import", func(t *testing.T) {
		src := `from frobnicate import compute

class compute(compute()):
    pass
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the base runs before `class` rebinds `compute`; got %#v", got)
		}
	})

	t.Run("a class-body statement reaches the enclosing import", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    value = compute()
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the class body runs before the class name is bound; got %#v", got)
		}
	})

	t.Run("a class-body lambda sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    thunk = lambda: compute()
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("a deferred lambda runs after the class exists; got %#v", got)
		}
	})

	t.Run("a class-body generator body sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    values = (compute() for _ in ())
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("a deferred generator body runs after the class exists; got %#v", got)
		}
	})

	t.Run("a class-body generator first iterable is eager", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    values = (value for value in compute())
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the generator's first iterable reaches the import; got %#v", got)
		}
	})

	t.Run("a generator nested in a class-body comprehension sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    values = [(compute() for _ in ()) for _ in range(1)]
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("a nested deferred generator runs after the class exists; got %#v", got)
		}
	})

	t.Run("a class-body list comprehension stays on the pre-class view", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    values = [compute() for _ in ()]
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("an immediate list comprehension reaches the import; got %#v", got)
		}
	})

	t.Run("a lambda nested in a class-body comprehension sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    values = [lambda: compute() for _ in range(1)]
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("a deferred lambda in a comprehension runs after the class exists; got %#v", got)
		}
	})

	t.Run("a lambda nested in a class-body comprehension keeps its local binding", func(t *testing.T) {
		src := `from frobnicate import compute

class Holder:
    values = [lambda: compute() for compute in factories]
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("the deferred lambda must capture the comprehension-local name; got %#v", got)
		}
	})

	t.Run("a walrus in a class base supersedes an imported same-name binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute((compute := list)):
    value = compute()
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("the walrus-bound class base name wins over the import; got %#v", got)
		}
	})

	t.Run("a method signature sees the pre-class scope", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=compute()):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("a method default runs before the class name is bound; got %#v", got)
		}
	})

	t.Run("a method-header list comprehension sees the pre-class scope", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=[compute() for _ in range(1)]):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("a method-header comprehension runs before the class name is bound; got %#v", got)
		}
	})

	t.Run("a lambda in a method-header comprehension sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=[lambda: compute() for _ in range(1)]):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 0 {
			t.Fatalf("the deferred lambda runs after the class exists; got %#v", got)
		}
	})

	t.Run("a generator in a method-header comprehension sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=[(compute() for _ in range(1)) for _ in range(1)]):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 0 {
			t.Fatalf("the deferred generator runs after the class exists; got %#v", got)
		}
	})

	t.Run("a method body sees the completed class binding", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self):
        return compute()
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 0 {
			t.Fatalf("a method body runs after the class exists; got %#v", got)
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
			t.Fatalf("the class body bound `compute` above this call; got %#v", got)
		}
	})

	t.Run("code after a class statement stays shadowed", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    pass

value = compute()
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 0 {
			t.Fatalf("the class is bound by the later call; got %#v", got)
		}
	})

	t.Run("an enclosing function local still fails closed", func(t *testing.T) {
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

// Lambda defaults, like function defaults, execute where the lambda expression
// is evaluated. They therefore use the eager enclosing scope, while the lambda
// body retains its own deferred scope and parameter bindings.
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
		assertNoComputeEdge(t, pythonFFICallEdges(t, `from frobnicate import compute

def plain():
    return [(lambda value=compute(): value)() for compute in ys]
`), "the comprehension binds `compute`, and its frame is where the lambda is written")
	})

	t.Run("a class-body lambda default sees the pre-class import", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    thunk = lambda value=compute(): value
`
		if got := pythonScopeModules(t, src, nil, pythonScopeModule, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the lambda default runs in the pre-class scope; got %#v", got)
		}
	})

	t.Run("a method-header lambda default sees the pre-class import", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=lambda default=compute(): default):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 1 || got[0] != "frobnicate" {
			t.Fatalf("the method-header lambda default runs before the class name is bound; got %#v", got)
		}
	})

	t.Run("a method-header lambda body sees the completed class", func(t *testing.T) {
		src := `from frobnicate import compute

class compute:
    def method(self, value=lambda: compute()):
        return value
`
		method := pythonScopeCallable(t, src, "app.py:method:compute.method", "method", "def method")
		if got := pythonScopeModules(t, src, []SymbolRecord{method}, method, "compute"); len(got) != 0 {
			t.Fatalf("the lambda body is deferred until the class name is bound; got %#v", got)
		}
	})
}

func TestPythonFromImportKeywordBoundaryForms(t *testing.T) {
	t.Run("relative imports may omit whitespace before keyword", func(t *testing.T) {
		for _, test := range []struct {
			source string
			module string
			item   string
		}{
			{source: "from .import x", module: ".", item: "x"},
			{source: "from ..import x", module: "..", item: "x"},
			{source: "from .import*", module: ".", item: "*"},
			{source: "from.import x", module: ".", item: "x"},
		} {
			statements := pythonFromImportStatements(test.source + "\n")
			if len(statements) != 1 || statements[0].module != test.module || len(statements[0].items) != 1 || statements[0].items[0] != test.item {
				t.Fatalf("pythonFromImportStatements(%q) = %#v, want module %q and item %q", test.source, statements, test.module, test.item)
			}
		}
	})

	t.Run("invalid keyword-like module forms remain rejected", func(t *testing.T) {
		for _, source := range []string{
			"from pkg.import x",
			"from .import.foo x",
			"frompkg import x",
			"from pkg importcompute",
		} {
			if statements := pythonFromImportStatements(source + "\n"); len(statements) != 0 {
				t.Fatalf("pythonFromImportStatements(%q) = %#v, want no statements", source, statements)
			}
		}
	})

	t.Run("tab-separated keyword remains resolved", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate\timport\tcompute\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("backslash continuation remains resolved", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import\\\n    compute\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("parenthesized list needs no space after keyword", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import(compute)\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("module name containing keyword is not split", func(t *testing.T) {
		src := "from importlib import compute\n\ndef plain():\n    return compute(1)\n"
		fn := pythonScopeCallable(t, src, "app.py:function:plain", "function", "def plain")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 1 || got[0] != "importlib" {
			t.Fatalf("got %#v, want importlib", got)
		}
	})

	t.Run("star import binds no name", func(t *testing.T) {
		src := "from frobnicate import*\n\ndef plain():\n    return compute(1)\n"
		fn := pythonScopeCallable(t, src, "app.py:function:plain", "function", "def plain")
		if got := pythonScopeModules(t, src, []SymbolRecord{fn}, fn, "compute"); len(got) != 0 {
			t.Fatalf("got %#v, want no binding", got)
		}
		imports := scanPythonImports("from mod import*\n")
		if len(imports) != 1 || imports[0] != "mod" {
			t.Fatalf("scanPythonImports got %#v, want module mod", imports)
		}
	})

	t.Run("multiline parenthesized list remains resolved", func(t *testing.T) {
		assertOneImportResolvedComputeEdge(t, pythonFFICallEdges(t, "from frobnicate import (\n    compute,\n)\n\ndef plain():\n    return compute(1)\n"), "app.py:function:plain")
	})

	t.Run("malformed list does not consume a later valid import", func(t *testing.T) {
		statements := pythonFromImportStatements("from broken import (compute\n\nfrom frobnicate import compute\n")
		if len(statements) != 1 || statements[0].module != "frobnicate" {
			t.Fatalf("got %#v, want the later valid import only", statements)
		}
	})
}

func TestPythonScopeDelayedBindingsAndMatchCaptures(t *testing.T) {
	for _, test := range []struct {
		name string
		src  string
		want bool
	}{
		{"assignment rhs", "from frobnicate import compute\ncompute = compute()\n", true},
		{"chained assignment rhs", "from frobnicate import compute\ncompute = other = compute()\n", true},
		{"destructured assignment rhs", "from frobnicate import compute\ncompute, other = compute(), 1\n", true},
		{"augmented assignment rhs", "from frobnicate import compute\ncompute += compute()\n", true},
		{"for iterable", "from frobnicate import compute\nfor compute in compute():\n    pass\n", true},
		{"valued annotation rhs", "from frobnicate import compute\ncompute: int = compute()\n", true},
		{"bare annotation keeps import", "from frobnicate import compute\ncompute: int\nhandler = compute()\n", true},
		{"class bare annotation keeps import", "from frobnicate import compute\nclass Holder:\n    compute: int\nhandler = compute()\n", true},
		{"function assignment remains local", "from frobnicate import compute\ndef plain():\n    compute = compute()\n", false},
		{"function bare annotation remains local", "from frobnicate import compute\ndef plain():\n    compute: int\n    return compute()\n", false},
		{"module case guard and body bind", "from frobnicate import compute\nmatch value:\n    case compute if compute():\n        pass\nhandler = compute()\n", false},
		{"class case guard and body bind", "from frobnicate import compute\nclass Holder:\n    match value:\n        case compute if compute():\n            pass\n    handler = compute()\n", false},
		{"function case capture remains local", "from frobnicate import compute\ndef plain(value):\n    match value:\n        case compute:\n            pass\n    return compute()\n", false},
		{"module walrus rhs", "from frobnicate import compute\nif (compute := compute()):\n    pass\n", true},
		{"module walrus later call", "from frobnicate import compute\nif (compute := 1):\n    pass\nhandler = compute()\n", false},
		{"function walrus remains local", "from frobnicate import compute\ndef plain():\n    if (compute := compute()):\n        return compute\n", false},
		{"comprehension walrus remains enclosing local", "from frobnicate import compute\ndef plain(values):\n    return [compute() for value in values if (compute := 1)]\n", false},
		{"lambda walrus remains lambda local", "from frobnicate import compute\nhandler = (lambda: (compute(), (compute := 1)))()\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			edges := pythonFFICallEdges(t, test.src)
			if test.want {
				assertOneImportResolvedComputeEdge(t, edges, "file:app.py")
			} else {
				assertNoComputeEdge(t, edges, "the binding must shadow the imported callable")
			}
		})
	}
}

func pythonRelationsFromApp(t *testing.T, app string, extra map[string]string, relationType, fromSuffix, targetSuffix string) []RelationRecord {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "frobnicate.c", "int compute(int value) { return value + 1; }\n")
	writeFile(t, repo, "app.py", app)
	for path, content := range extra {
		writeFile(t, repo, path, content)
	}
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var out []RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == relationType && strings.HasSuffix(relation.FromID, fromSuffix) && (targetSuffix == "" || strings.HasSuffix(relation.ToID, targetSuffix)) {
			out = append(out, relation)
		}
	}
	return out
}

func relationWithEndpoints(relations []RelationRecord, fromSuffix, targetSuffix string) (RelationRecord, bool) {
	for _, relation := range relations {
		if strings.HasSuffix(relation.FromID, fromSuffix) && strings.HasSuffix(relation.ToID, targetSuffix) {
			return relation, true
		}
	}
	return RelationRecord{}, false
}

func TestPythonScopedImportBindingExtendsAsyncAndDataFlow(t *testing.T) {
	for _, test := range []struct {
		name, relationType, source string
		extra                      map[string]string
		fromSuffix, targetSuffix   string
		want                       bool
	}{
		{
			name:         "direct async C target",
			relationType: "ASYNC_CALLS",
			source:       "from frobnicate import compute\nasync def plain():\n    return await compute(1)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "frobnicate.c:function:compute", want: true,
		},
		{
			name:         "direct data flow C target",
			relationType: "DATA_FLOWS",
			source:       "from frobnicate import compute\ndef plain(value):\n    return compute(value)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "frobnicate.c:function:compute", want: true,
		},
		{
			name:         "aliased data flow C target",
			relationType: "DATA_FLOWS",
			source:       "from frobnicate import compute as run\ndef plain(value):\n    return run(value)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "frobnicate.c:function:compute", want: true,
		},
		{
			name:         "aliased async import beats conflicting workspace name",
			relationType: "ASYNC_CALLS",
			source:       "from frobnicate import compute as run\nasync def plain():\n    return await run(1)\n",
			extra:        map[string]string{"other.py": "def run(value):\n    return value\n"},
			fromSuffix:   "app.py:function:plain", targetSuffix: "frobnicate.c:function:compute", want: true,
		},
		{
			name:         "same-language Python import wins over C",
			relationType: "ASYNC_CALLS",
			source:       "from helper import compute\nasync def plain():\n    return await compute(1)\n",
			extra:        map[string]string{"helper.py": "def compute(value):\n    return value\n"},
			fromSuffix:   "app.py:function:plain", targetSuffix: "helper.py:function:compute", want: true,
		},
		{
			name:         "local rebinding suppresses widening",
			relationType: "ASYNC_CALLS",
			source:       "from frobnicate import compute\nasync def plain():\n    compute = 1\n    return await compute(1)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "frobnicate.c:function:compute", want: false,
		},
		{
			name:         "local rebinding suppresses data-flow widening",
			relationType: "DATA_FLOWS",
			source:       "from frobnicate import compute\ndef plain(value):\n    compute = 1\n    return compute(value)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "frobnicate.c:function:compute", want: false,
		},
		{
			name:         "C and Ruby ambiguity does not widen async call",
			relationType: "ASYNC_CALLS",
			source:       "from frobnicate import compute\nasync def plain():\n    return await compute(1)\n",
			extra:        map[string]string{"frobnicate.rb": "def compute(value)\n  value\nend\n"},
			fromSuffix:   "app.py:function:plain", targetSuffix: "", want: false,
		},
		{
			name:         "C and Ruby ambiguity does not widen data flow",
			relationType: "DATA_FLOWS",
			source:       "from frobnicate import compute\ndef plain(value):\n    return compute(value)\n",
			extra:        map[string]string{"frobnicate.rb": "def compute(value)\n  value\nend\n"},
			fromSuffix:   "app.py:function:plain", targetSuffix: "", want: false,
		},
		{
			name:         "aliased C and Ruby ambiguity does not widen async call",
			relationType: "ASYNC_CALLS",
			source:       "from frobnicate import compute as run\nasync def plain():\n    return await run(1)\n",
			extra:        map[string]string{"frobnicate.rb": "def compute(value)\n  value\nend\n"},
			fromSuffix:   "app.py:function:plain", targetSuffix: "", want: false,
		},
		{
			name:         "aliased C and Ruby ambiguity does not widen data flow",
			relationType: "DATA_FLOWS",
			source:       "from frobnicate import compute as run\ndef plain(value):\n    return run(value)\n",
			extra:        map[string]string{"frobnicate.rb": "def compute(value)\n  value\nend\n"},
			fromSuffix:   "app.py:function:plain", targetSuffix: "", want: false,
		},
		{
			name:         "bare module alias does not widen async call",
			relationType: "ASYNC_CALLS",
			source:       "import frobnicate as m\nasync def plain():\n    return await m(1)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "", want: false,
		},
		{
			name:         "bare module alias does not widen data flow",
			relationType: "DATA_FLOWS",
			source:       "import frobnicate as m\ndef plain(value):\n    return m(value)\n",
			fromSuffix:   "app.py:function:plain", targetSuffix: "", want: false,
		},
		{
			name:         "class shadow suppresses async widening in method",
			relationType: "ASYNC_CALLS",
			source:       "from frobnicate import compute\nclass Holder:\n    async def plain(self):\n        class compute:\n            pass\n        return await compute()\n",
			fromSuffix:   "app.py:method:Holder.plain", targetSuffix: "frobnicate.c:function:compute", want: false,
		},
		{
			name:         "class shadow suppresses data-flow widening in method",
			relationType: "DATA_FLOWS",
			source:       "from frobnicate import compute\nclass Holder:\n    def plain(self, value):\n        class compute:\n            pass\n        return compute(value)\n",
			fromSuffix:   "app.py:method:Holder.plain", targetSuffix: "frobnicate.c:function:compute", want: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			edges := pythonRelationsFromApp(t, test.source, test.extra, test.relationType, test.fromSuffix, test.targetSuffix)
			if test.want && len(edges) != 1 {
				t.Fatalf("got %#v, want one %s edge", edges, test.relationType)
			}
			if !test.want && len(edges) != 0 {
				t.Fatalf("got %#v, want no %s edge", edges, test.relationType)
			}
		})
	}

	for _, test := range []struct {
		name, relationType, source string
	}{
		{
			name:         "function-local async alias does not leak to sibling",
			relationType: "ASYNC_CALLS",
			source:       "async def local():\n    from frobnicate import compute as c\n    return await c(1)\nasync def plain():\n    return await c(1)\n",
		},
		{
			name:         "function-local data-flow alias does not leak to sibling",
			relationType: "DATA_FLOWS",
			source:       "def local(value):\n    from frobnicate import compute as c\n    return c(value)\ndef plain(value):\n    return c(value)\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := pythonRelationsFromApp(t, test.source, nil, test.relationType, "app.py:function:local", "frobnicate.c:function:compute")
			if len(local) != 1 {
				t.Fatalf("got %#v, want one local %s edge", local, test.relationType)
			}
			sibling := pythonRelationsFromApp(t, test.source, nil, test.relationType, "app.py:function:plain", "")
			if len(sibling) != 0 {
				t.Fatalf("got %#v, want no sibling %s edge", sibling, test.relationType)
			}
		})
	}

	t.Run("aliased and unaliased data flows match", func(t *testing.T) {
		unaliased := pythonRelationsFromApp(t,
			"from frobnicate import compute\ndef plain(value):\n    return compute(value)\n",
			nil, "DATA_FLOWS", "", "")
		aliased := pythonRelationsFromApp(t,
			"from frobnicate import compute as c\ndef plain(value):\n    return c(value)\n",
			nil, "DATA_FLOWS", "", "")
		if len(unaliased) != 2 || len(aliased) != 2 {
			t.Fatalf("want exactly two flows in each fixture: unaliased=%#v aliased=%#v", unaliased, aliased)
		}
		for _, endpoints := range [][2]string{
			{"app.py:function:plain", "frobnicate.c:function:compute"},
			{"frobnicate.c:function:compute", "app.py:function:plain"},
		} {
			want, wantOK := relationWithEndpoints(unaliased, endpoints[0], endpoints[1])
			got, gotOK := relationWithEndpoints(aliased, endpoints[0], endpoints[1])
			if !wantOK || !gotOK {
				t.Fatalf("missing data-flow direction %s -> %s: unaliased=%#v aliased=%#v", endpoints[0], endpoints[1], unaliased, aliased)
			}
			if got.Resolution != want.Resolution || got.RelationScope != want.RelationScope || got.Confidence != want.Confidence || got.Reason != want.Reason {
				t.Fatalf("aliased flow %#v differs from unaliased flow %#v", got, want)
			}
		}
	})

	t.Run("commented aliased import preserves original member", func(t *testing.T) {
		source := "from frobnicate import compute as c  # comment\ndef plain(value):\n    return c(value)\nasync def asyncPlain():\n    return await c(1)\n"
		for _, test := range []struct {
			name, relationType, fromSuffix string
		}{
			{"calls", "CALLS", "app.py:function:plain"},
			{"async calls", "ASYNC_CALLS", "app.py:function:asyncPlain"},
			{"data flows", "DATA_FLOWS", "app.py:function:plain"},
		} {
			t.Run(test.name, func(t *testing.T) {
				edges := pythonRelationsFromApp(t, source, nil, test.relationType, test.fromSuffix, "frobnicate.c:function:compute")
				if len(edges) != 1 {
					t.Fatalf("got %#v, want one %s edge to imported compute", edges, test.relationType)
				}
			})
		}
	})
}
