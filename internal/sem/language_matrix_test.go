package sem

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// languageFixture pins what one semantic language must extract from a minimal
// fixture that contains a handful of unmistakable definitions: a function, a
// type, and a member. It exists because a zero-symbol language is invisible in
// aggregate counts — a grammar can keep loading, keep parsing without error,
// and keep being advertised in `capabilities --json` while producing nothing at
// all. Only a per-language assertion catches that.
type languageFixture struct {
	// dir is the fixture directory under testdata/langmatrix.
	dir string
	// symbols are entity records that must appear, written "kind:name".
	symbols []string
	// relations are relation types that must appear at least once.
	relations []string
	// callsGap documents a language whose capability report advertises CALLS
	// while this fixture's idiomatic call form produces none. It is an audited
	// defect list, not a licence: closing one of these fails this test until the
	// entry is removed, and adding a new one requires justifying it here.
	callsGap string
}

// languageMatrix covers every language in Capabilities().SemanticLanguages.
// TestLanguageMatrixCoversEverySemanticLanguage enforces that, so a language
// added to the registry cannot ship without a fixture, and a language whose
// extraction goes dark fails here instead of going unnoticed.
var languageMatrix = []languageFixture{
	{dir: "bash", symbols: []string{"function:ledger_add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS"}},
	{dir: "c", symbols: []string{"struct:Ledger", "function:ledger_add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS", "PARAM_TYPE", "USES_TYPE"}},
	{dir: "clojure", symbols: []string{"class:Ledger", "function:ledger-add", "function:ledger-double"}, relations: []string{"DEFINES", "CALLS"}},
	{dir: "clojurescript", symbols: []string{"class:Ledger", "function:ledger-add", "function:ledger-double"}, relations: []string{"DEFINES", "CALLS"}},
	{
		dir:     "cpp",
		symbols: []string{"class:Ledger", "function:LedgerDouble"},
		// A stack-allocated receiver (`Ledger ledger;` — C++'s default
		// construction syntax) is not type-inferred, so `ledger.Add(amount)`
		// resolves to nothing. `Ledger l = Ledger();` and `new Ledger()` both do
		// resolve, so this is a hole in declaration parsing, not in C++ receiver
		// resolution as a whole.
		relations: []string{"DEFINES", "CONTAINS", "USES_TYPE"},
		callsGap:  "a default-constructed C++ stack receiver (`Ledger ledger;`) carries no inferred type, so its method calls resolve to nothing",
	},
	{dir: "csharp", symbols: []string{"class:Ledger", "class:LedgerHelper", "field:Total", "method:Add", "method:Double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "cue", symbols: []string{"field:#Ledger", "field:#Add", "field:ledger"}, relations: []string{"DEFINES", "CONTAINS"}},
	{dir: "dart", symbols: []string{"class:Ledger", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "elixir", symbols: []string{"module:Ledger", "method:add", "method:double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS"}},
	{dir: "erlang", symbols: []string{"module:ledger", "struct:ledger", "function:add", "function:double"}, relations: []string{"DEFINES", "CALLS"}},
	{dir: "fsharp", symbols: []string{"module:Ledger", "type:Ledger", "function:add", "function:double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "PARAM_TYPE", "USES_TYPE"}},
	{dir: "go", symbols: []string{"type:Ledger", "field:Total", "method:Add", "function:LedgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "USES_TYPE", "READS_FIELD"}},
	{dir: "groovy", symbols: []string{"class:Ledger", "field:total", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "haskell", symbols: []string{"type:Ledger", "function:add", "function:double"}, relations: []string{"DEFINES", "CALLS", "PARAM_TYPE", "USES_TYPE"}},
	{dir: "hcl", symbols: []string{"block:region", "block:ledger", "block:ledger_bucket"}, relations: []string{"DEFINES", "CONFIGURES", "RESOURCE_DEPENDS_ON"}},
	{dir: "java", symbols: []string{"class:Ledger", "field:total", "method:add", "method:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "javascript", symbols: []string{"class:Ledger", "method:constructor", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{
		dir:     "julia",
		symbols: []string{"module:Ledgers", "struct:Ledger", "method:add", "method:double"},
		// Idiomatic Julia wraps a package in `module ... end`, which makes every
		// definition module-qualified and therefore emitted as a method. Bare
		// `add(...)` — Julia's only call syntax — is excluded from resolving to a
		// method, so a module-scoped package produces no same-file CALLS.
		relations: []string{"DEFINES", "CONTAINS", "CONSTRUCTS", "PARAM_TYPE", "USES_TYPE"},
		callsGap:  "a Julia definition inside `module ... end` is emitted as a method, and bare calls are barred from resolving to methods",
	},
	{dir: "kotlin", symbols: []string{"class:Ledger", "field:total", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "lua", symbols: []string{"function:new", "function:add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS"}},
	{
		dir:     "objc",
		symbols: []string{"class:Ledger", "method:add", "function:LedgerDouble"},
		// A message send to `self` resolves, but a send to any other receiver
		// does not: the local's declared type (`Ledger *ledger = ...`) is never
		// inferred, so `[ledger add:amount]` — the only way to call a method on
		// another object — binds to nothing.
		relations: []string{"DEFINES", "CONTAINS"},
		callsGap:  "an Objective-C local's declared type is not inferred, so a message send to anything but `self` resolves to nothing",
	},
	{dir: "ocaml", symbols: []string{"type:ledger", "module:Ledgers", "function:add", "function:double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "USES_TYPE"}},
	{dir: "perl", symbols: []string{"module:Ledger", "function:new", "function:add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS"}},
	{dir: "php", symbols: []string{"class:Ledger", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "protobuf", symbols: []string{"message:Ledger", "message:AddRequest", "service:LedgerService", "rpc:Add"}, relations: []string{"DEFINES", "CONTAINS", "HANDLES_GRPC"}},
	{dir: "python", symbols: []string{"class:Ledger", "method:__init__", "method:add", "function:ledger_double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "r", symbols: []string{"function:make_ledger", "function:ledger_add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS"}},
	{dir: "ruby", symbols: []string{"class:Ledger", "method:initialize", "method:add", "function:ledger_double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS"}},
	{dir: "rust", symbols: []string{"struct:Ledger", "field:total", "method:add", "function:ledger_double"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "READS_FIELD"}},
	{dir: "scala", symbols: []string{"class:Ledger", "class:LedgerApp", "method:add", "method:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS"}},
	{dir: "sql", symbols: []string{"table:ledger", "view:ledger_totals", "function:ledger_add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS"}},
	{dir: "swift", symbols: []string{"struct:Ledger", "field:total", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS", "IMPORTS"}},
	{dir: "typescript", symbols: []string{"class:Ledger", "field:total", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "CONSTRUCTS", "READS_FIELD"}},
	{dir: "yaml", symbols: []string{"section:service", "section:routes"}, relations: []string{"DEFINES"}},
	{dir: "zig", symbols: []string{"struct:Ledger", "method:add", "function:ledgerDouble"}, relations: []string{"DEFINES", "CONTAINS", "CALLS", "PARAM_TYPE", "USES_TYPE"}},
	{dir: "zsh", symbols: []string{"function:ledger_add", "function:ledger_double"}, relations: []string{"DEFINES", "CALLS"}},
}

// languageMatrixLanguages maps each fixture directory to the language label the
// provider must report for it. A fixture that silently reroutes to another
// language (or to inventory) would otherwise pass its symbol assertions while
// the language under test is not exercised at all.
var languageMatrixLanguages = map[string]string{
	"bash": "Bash", "c": "C", "clojure": "Clojure", "clojurescript": "ClojureScript",
	"cpp": "C++", "csharp": "C#", "cue": "CUE", "dart": "Dart", "elixir": "Elixir",
	"erlang": "Erlang", "fsharp": "F#", "go": "Go", "groovy": "Groovy", "haskell": "Haskell",
	"hcl": "HCL", "java": "Java", "javascript": "JavaScript", "julia": "Julia",
	"kotlin": "Kotlin", "lua": "Lua", "objc": "Objective-C", "ocaml": "OCaml",
	"perl": "Perl", "php": "PHP", "protobuf": "Protocol Buffers", "python": "Python",
	"r": "R", "ruby": "Ruby", "rust": "Rust", "scala": "Scala", "sql": "SQL",
	"swift": "Swift", "typescript": "TypeScript", "yaml": "YAML", "zig": "Zig",
	"zsh": "Zsh",
}

type languageMatrixResult struct {
	language  string
	symbols   map[string]bool
	relations map[string]bool
	symbolLen int
}

func runLanguageFixture(t *testing.T, dir string) languageMatrixResult {
	t.Helper()
	repo := t.TempDir()
	src := filepath.Join("testdata", "langmatrix", dir)
	files, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Fatalf("fixture %s has no files", dir)
	}
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(src, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, file.Name()), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := languageMatrixResult{symbols: map[string]bool{}, relations: map[string]bool{}}
	err = StreamSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true}, func(record any) error {
		switch rec := record.(type) {
		case SymbolRecord:
			out.symbols[rec.Kind+":"+rec.Name] = true
			out.symbolLen++
		case RelationRecord:
			out.relations[rec.Type] = true
		case FileRecord:
			if rec.Language != "" {
				out.language = rec.Language
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

// TestLanguageExtractionMatrix asserts that every semantic language extracts
// real symbols and real relations from a minimal fixture. The failure mode it
// guards against is a grammar that loads, parses without error, and yields
// nothing: aggregate symbol counts across a repository never reveal it, and
// `capabilities --json` keeps advertising the language regardless.
func TestLanguageExtractionMatrix(t *testing.T) {
	t.Parallel()
	for _, fixture := range languageMatrix {
		t.Run(fixture.dir, func(t *testing.T) {
			t.Parallel()
			got := runLanguageFixture(t, fixture.dir)

			if want := languageMatrixLanguages[fixture.dir]; got.language != want {
				t.Fatalf("fixture %s parsed as language %q, want %q", fixture.dir, got.language, want)
			}
			if got.symbolLen == 0 {
				t.Fatalf("fixture %s produced no symbols at all: the grammar loads but extracts nothing", fixture.dir)
			}
			for _, want := range fixture.symbols {
				if !got.symbols[want] {
					t.Errorf("fixture %s is missing symbol %q; got %v", fixture.dir, want, matrixSortedKeys(got.symbols))
				}
			}
			for _, want := range fixture.relations {
				if !got.relations[want] {
					t.Errorf("fixture %s is missing relation %q; got %v", fixture.dir, want, matrixSortedKeys(got.relations))
				}
			}
		})
	}
}

// TestLanguageMatrixCallsGapsAreExactlyAsAudited pins the languages that
// advertise CALLS in `capabilities --json` yet extract none from an idiomatic
// call site. Both directions are failures worth knowing about: a new entry
// means a language went dark, and a stale entry means a fix landed without the
// audited gap list being retired with it.
func TestLanguageMatrixCallsGapsAreExactlyAsAudited(t *testing.T) {
	t.Parallel()
	claimsCalls := map[string]bool{}
	for language, relations := range relationSupportByLanguage() {
		for _, relation := range relations {
			if relation == "CALLS" {
				claimsCalls[language] = true
			}
		}
	}
	for _, fixture := range languageMatrix {
		t.Run(fixture.dir, func(t *testing.T) {
			t.Parallel()
			language := languageMatrixLanguages[fixture.dir]
			if !claimsCalls[language] {
				if fixture.callsGap != "" {
					t.Fatalf("%s does not advertise CALLS, so it cannot have an audited CALLS gap", language)
				}
				return
			}
			got := runLanguageFixture(t, fixture.dir)
			switch {
			case got.relations["CALLS"] && fixture.callsGap != "":
				t.Fatalf("%s now extracts CALLS; remove its audited gap (%s) from languageMatrix", language, fixture.callsGap)
			case !got.relations["CALLS"] && fixture.callsGap == "":
				t.Fatalf("%s advertises CALLS but extracted none from its fixture; relations seen: %v", language, matrixSortedKeys(got.relations))
			}
		})
	}
}

// TestLanguageMatrixCoversEverySemanticLanguage keeps the fixture set and the
// parser registry in lockstep. Without it a language could be registered,
// counted in `capabilities --json`, documented as supported, and never once be
// shown to extract anything.
func TestLanguageMatrixCoversEverySemanticLanguage(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, fixture := range languageMatrix {
		language, ok := languageMatrixLanguages[fixture.dir]
		if !ok {
			t.Fatalf("fixture %q has no entry in languageMatrixLanguages", fixture.dir)
		}
		if covered[language] {
			t.Fatalf("language %q has more than one matrix fixture", language)
		}
		covered[language] = true
	}
	for _, language := range Capabilities().SemanticLanguages {
		if !covered[language] {
			t.Errorf("semantic language %q has no fixture in testdata/langmatrix; add one so it cannot go dark unnoticed", language)
		}
		delete(covered, language)
	}
	for language := range covered {
		t.Errorf("matrix fixture covers %q, which is not a semantic language", language)
	}
}

// TestDocumentedLanguageListsMatchTheRegistry reconciles both lists in
// docs/language-support.md with the parser registry. The two drifted apart in
// peregrine — Kotlin stayed documented as tier-1 support long after its
// extractor stopped producing symbols — and a docs list that is not checked is
// a list that will drift. The inventory-only list is checked too: it is the
// larger of the two, and the tier a language is documented under is what tells
// a reader whether call/type analysis is claimed for it at all.
func TestDocumentedLanguageListsMatchTheRegistry(t *testing.T) {
	t.Parallel()
	capabilities := Capabilities()
	for _, tier := range []struct {
		heading    string
		registered []string
	}{
		{"## Semantic Languages", capabilities.SemanticLanguages},
		{"## Inventory-Only Languages", capabilities.InventoryOnlyLanguages},
	} {
		documented := documentedLanguages(t, tier.heading)
		if len(documented) == 0 {
			t.Fatalf("docs/language-support.md has no %q list", tier.heading)
		}
		for _, language := range tier.registered {
			if !documented[language] {
				t.Errorf("language %q is registered under %q but undocumented", language, tier.heading)
			}
			delete(documented, language)
		}
		for language := range documented {
			t.Errorf("docs/language-support.md lists %q under %q, but it is not registered there", language, tier.heading)
		}
	}
}

func documentedLanguages(t *testing.T, heading string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "language-support.md"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.TrimSpace(line) == heading
			continue
		}
		if inSection && strings.HasPrefix(line, "- ") {
			out[strings.TrimSpace(strings.TrimPrefix(line, "- "))] = true
		}
	}
	return out
}

func matrixSortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
