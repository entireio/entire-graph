package sem

import (
	"strings"
	"testing"
)

// Constructs from entirehq/entire.io that the vendored TypeScript/TSX grammars
// reject; each is rewritten position-preservingly before parsing.

func requireNoTSParseError(t *testing.T, path, src string) []Entity {
	t.Helper()
	entities, _, status := TreeSitterParser{}.ParseWithStatus(path, src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	return entities
}

// `export type * from "./x"` (TS 5.0) is not in the grammar; the `type`
// keyword is blanked so it parses as a plain star re-export.
func TestTypeScriptExportTypeStarParses(t *testing.T) {
	requireNoTSParseError(t, "types.ts", "// Auto-generated database schema types.\nexport type * from \"./schema\"\n")
}

// Import-type references without `typeof` — `import("./types").Foo["k"]` in a
// type position — are rejected by the grammar (entire.io github.ts, shiki
// module declarations).
func TestTypeScriptBareImportTypeParses(t *testing.T) {
	entities := requireNoTSParseError(t, "github.ts",
		"function mapStatus(status: string): import(\"./types\").GitSourceFileStat[\"status\"] {\n"+
			"  return \"added\"\n"+
			"}\n")
	found := false
	for _, e := range entities {
		if e.Name == "mapStatus" {
			found = true
		}
	}
	if !found {
		t.Errorf("mapStatus missing: %+v", entities)
	}
	requireNoTSParseError(t, "shiki-modules.d.ts",
		"declare module \"@shikijs/langs/typescript\" {\n"+
			"  const lang: import(\"shiki\").LanguageRegistration[]\n"+
			"  export default lang\n"+
			"}\n")
}

// `unique` is the grammar's `unique symbol` keyword; after `<` it derails an
// ordinary identifier use (`i < unique.length`, entire.io checkpoints.ts).
func TestTypeScriptUniqueIdentifierAfterLessThanParses(t *testing.T) {
	requireNoTSParseError(t, "checkpoints.ts",
		"const unique = [...new Set([1])]\n"+
			"for (let i = 0; i < unique.length; i += 2) {\n"+
			"  console.log(i)\n"+
			"}\n")
}

// The mask must not touch a real `unique symbol` type operator.
func TestTypeScriptUniqueSymbolTypeUnmasked(t *testing.T) {
	src := "declare const s: unique symbol\ntype M = Map<unique symbol, string>\n"
	if got := maskTypeScriptCommonSyntax(src); got != src {
		t.Errorf("unique symbol should be untouched:\n%s", got)
	}
	requireNoTSParseError(t, "sym.ts", src)
}

// .tsx received no masking at all, so `typeof import(...)` inside generic call
// arguments — the standard vi.mock/importOriginal pattern — swallowed entire
// test files (entire.io frontend *.test.tsx).
func TestTSXTypeofImportGenericCallParses(t *testing.T) {
	src := "import { vi } from \"vitest\"\n" +
		"vi.mock(\"@tanstack/react-router\", async (importOriginal) => {\n" +
		"  const actual = await importOriginal<typeof import(\"@tanstack/react-router\")>()\n" +
		"  return {\n" +
		"    ...actual,\n" +
		"    RouterProvider: () => <div data-testid=\"router-provider\" />,\n" +
		"  }\n" +
		"})\n" +
		"const helper = {\n" +
		"  ...(await importOriginal<\n" +
		"    typeof import(\"@/hooks/useQuery\")\n" +
		"  >()),\n" +
		"  useQuery: (...args: unknown[]) => mock(...args),\n" +
		"}\n" +
		"const actual2 = await vi.importActual<typeof import(\"@/api\")>(\n" +
		"  \"@/api\",\n" +
		")\n"
	requireNoTSParseError(t, "AppRouter.test.tsx", src)
}

// A runtime dynamic import may take an arbitrary expression. The type-import
// mask must not stop at a nested call's first `)` and leave the outer `)` as a
// syntax error (`typeof import(resolveSpecifier())` used to become `any )`).
func TestTSXRuntimeTypeofDynamicImportWithNestedCallUnmasked(t *testing.T) {
	src := "export function moduleKind() {\n" +
		"  return typeof import(resolveSpecifier())\n" +
		"}\n"
	if got := maskTypeScriptCommonSyntax(src); got != src {
		t.Fatalf("runtime dynamic import should be untouched:\n%s", got)
	}
	entities := requireNoTSParseError(t, "runtime.tsx", src)
	found := false
	for _, e := range entities {
		if e.Name == "moduleKind" {
			found = true
		}
	}
	if !found {
		t.Errorf("moduleKind missing: %+v", entities)
	}
}

// Newline-separated type members whose names start with `in` (`in_x:`) close
// the enclosing body early; the keyword-property mask now covers `in_*` names
// and applies to .tsx (entire.io SystemStatus.tsx).
func TestTSXInPrefixedPropertyParses(t *testing.T) {
	entities := requireNoTSParseError(t, "SystemStatus.tsx",
		"interface StatusSummary {\n"+
			"  ongoing_incidents: unknown[]\n"+
			"  in_progress_maintenances: unknown[]\n"+
			"}\n"+
			"export function SystemStatus() {\n"+
			"  return <div />\n"+
			"}\n")
	found := false
	for _, e := range entities {
		if e.Name == "SystemStatus" {
			found = true
		}
		if strings.HasPrefix(e.Name, "ii") {
			t.Errorf("masked identifier leaked into entity name: %+v", e)
		}
	}
	if !found {
		t.Errorf("SystemStatus missing: %+v", entities)
	}
}

// Every rewrite must be byte-length-preserving: entity offsets are computed on
// the masked source but sliced from the original.
func TestTypeScriptMasksPreserveLength(t *testing.T) {
	for _, src := range []string{
		"export type * from \"./schema\"\n",
		"const x: import(\"./t\").Foo[\"k\"] = load()\n",
		"const n = 1 < unique.length\n",
		"const a = await importOriginal<typeof import(\"@x/y\")>()\n",
		"type T = typeof import(\n  \"@x/y\"\n)\n",
	} {
		if got := maskTypeScriptCommonSyntax(src); len(got) != len(src) {
			t.Errorf("length drift %d -> %d for %q", len(src), len(got), src)
		} else if strings.Count(got, "\n") != strings.Count(src, "\n") {
			t.Errorf("line-count drift for %q:\n%s", src, got)
		}
		if got := maskTSXUnsupportedSyntax(src); len(got) != len(src) {
			t.Errorf("tsx length drift %d -> %d for %q", len(src), len(got), src)
		}
	}
}

// A TypeScript OVERLOAD SET is several bodyless `function_signature`
// declarations followed by one implementation. tree-sitter emits a distinct
// node type for the bodyless form, and it used to be gated to Dart, so an
// overloaded function collapsed to the single implementation symbol: vue's
// packages/runtime-core/src/helpers/renderList.ts (five overloads on lines
// 10-53, implementation on 54) exposed exactly one symbol spanning 54-107, and
// the declared parameter types and generics — which live only in the overloads
// — were unindexed. Every declaration must get its own symbol over its own
// lines, and the spans must not overlap.
func TestTypeScriptOverloadSignaturesEachEmitSymbol(t *testing.T) {
	src := "/** v-for string */\n" + // 1
		"export function renderList(\n" + // 2
		"  source: string,\n" + // 3
		"  renderItem: (value: string, index: number) => VNodeChild,\n" + // 4
		"): VNodeChild[]\n" + // 5
		"\n" + // 6
		"/** v-for iterable */\n" + // 7
		"export function renderList<T>(\n" + // 8
		"  source: Iterable<T>,\n" + // 9
		"  renderItem: (value: T, index: number) => VNodeChild,\n" + // 10
		"): VNodeChild[]\n" + // 11
		"\n" + // 12
		"/** implementation */\n" + // 13
		"export function renderList(\n" + // 14
		"  source: any,\n" + // 15
		"  renderItem: (...args: any[]) => VNodeChild,\n" + // 16
		"): VNodeChild[] {\n" + // 17
		"  return []\n" + // 18
		"}\n" // 19

	var got [][2]int
	for _, entity := range requireNoTSParseError(t, "renderList.ts", src) {
		if entity.Name != "renderList" {
			t.Fatalf("unexpected entity %+v", entity)
		}
		if entity.Kind != "function" {
			t.Errorf("kind = %q, want function: %+v", entity.Kind, entity)
		}
		got = append(got, [2]int{entity.StartLine, entity.EndLine})
	}
	want := [][2]int{{2, 5}, {8, 11}, {14, 19}}
	if len(got) != len(want) {
		t.Fatalf("renderList symbols = %d %v, want %d declarations %v", len(got), got, len(want), want)
	}
	for i, span := range want {
		if got[i] != span {
			t.Errorf("declaration %d span = %v, want %v", i, got[i], span)
		}
	}
	// Overlapping spans would mean a signature was merged into the
	// implementation instead of standing on its own lines.
	for i := 1; i < len(got); i++ {
		if got[i][0] <= got[i-1][1] {
			t.Errorf("spans overlap: %v then %v", got[i-1], got[i])
		}
	}

	// Same file, same qualified name, same kind: the compound-v1 ID scheme must
	// still hand out one distinct, stable ID per overload, or `def`/`neighbors`
	// on an overload set collapses back to a single target.
	// See TestNeighborsExactSymbolIDDisambiguatesSameFileOverloads in
	// internal/cli for the consumer side of this contract.
	entities, _, _ := TreeSitterParser{}.ParseWithStatus("renderList.ts", src)
	ids := map[string]bool{}
	for _, symbol := range entitySymbols("gh/vuejs/core", "src/renderList.ts", "TypeScript", entities) {
		if ids[symbol.ID] {
			t.Errorf("duplicate symbol ID %q across overloads", symbol.ID)
		}
		ids[symbol.ID] = true
	}
	if len(ids) != len(want) {
		t.Errorf("distinct overload IDs = %d, want %d", len(ids), len(want))
	}
}

// The same bodyless shape covers ambient declarations (`declare function`,
// including inside `declare namespace`/`.d.ts` files) and overloaded class
// methods and constructors (`method_signature` in a class_body). Interface and
// type-literal members share the node type but stay unextracted on purpose:
// they declare a contract rather than code, and emitting them lets a call bind
// to a bodyless member (see TestTypeScriptNamespaceCallSkipsParameterReceiverRoots).
func TestTypeScriptBodylessDeclarationCoverage(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
		src  string
		want []string
	}{{
		name: "ambient function",
		path: "globals.d.ts",
		src:  "declare function amb(a: string): void\ndeclare namespace N {\n  function inner(b: number): void\n}\n",
		want: []string{"function amb", "function inner"},
	}, {
		name: "class method and constructor overloads",
		path: "worker.ts",
		src: "class Worker {\n" +
			"  constructor(a: string)\n" +
			"  constructor(a: any) {}\n" +
			"  run(a: string): void\n" +
			"  run(a: number): void\n" +
			"  run(a: any): void {}\n" +
			"}\n",
		want: []string{
			"class Worker", "method Worker.constructor", "method Worker.constructor",
			"method Worker.run", "method Worker.run", "method Worker.run",
		},
	}, {
		name: "interface and type-literal members stay out",
		path: "contract.ts",
		src:  "interface Client {\n  parse(): void\n  parse(raw: string): void\n}\ntype Handler = { handle(a: string): void }\n",
		want: []string{"interface Client", "type Handler"},
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			var got []string
			for _, entity := range requireNoTSParseError(t, testCase.path, testCase.src) {
				got = append(got, entity.Kind+" "+entity.Name)
			}
			if strings.Join(got, ", ") != strings.Join(testCase.want, ", ") {
				t.Errorf("entities = [%s], want [%s]", strings.Join(got, ", "), strings.Join(testCase.want, ", "))
			}
		})
	}
}

// An overload set is ONE call target, not an ambiguity.
//
// resolveImportedCallTargets downgrades a multi-candidate imported call to
// confidence 0.62 / resolution "name_only". Once every overload signature
// became a symbol, an ordinary `import { renderList }` + `renderList(...)`
// matched three candidates and took that path, and because the fast profile
// keeps only exact/package/import_resolved edges (shallowCallRelationRetained)
// it then dropped ALL of them — `impact --profile fast` answered "no callers"
// for a function with a caller, which is a false negative stated as fact.
//
// The set is one implementation plus its own bodyless declarations, so the call
// resolves to the implementation and keeps the confidence and reason it would
// have had before the signatures existed.
func TestTypeScriptOverloadedImportedCallResolvesToImplementation(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/renderList.ts",
		"export function renderList(source: number, fn: (i: number) => any): any[]\n"+
			"export function renderList(source: string, fn: (v: string) => any): any[]\n"+
			"export function renderList(source: any, fn: (...a: any[]) => any): any[] {\n"+
			"  return []\n"+
			"}\n")
	writeFile(t, repo, "src/caller.ts",
		"import { renderList } from './renderList'\n"+
			"\n"+
			"export function useList(items: string[]): any[] {\n"+
			"  return renderList(items, (v) => v)\n"+
			"}\n")

	for _, profile := range []Profile{ProfileFull, ProfileFast} {
		t.Run(string(profile), func(t *testing.T) {
			snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version",
				ProviderSnapshotOptions{Profile: profile})
			if err != nil {
				t.Fatal(err)
			}
			calls := runCallsFrom(snapshot, "useList")
			if len(calls) != 1 {
				t.Fatalf("useList CALLS = %d, want 1 edge to the implementation: %#v", len(calls), calls)
			}
			call := calls[0]
			if call.Resolution != "import_resolved" || call.Confidence < 0.86 {
				t.Errorf("call = %s conf %.2f (%s), want import_resolved at 0.86: %#v",
					call.Resolution, call.Confidence, call.Reason, call)
			}
			if strings.Contains(call.Reason, "ambiguous") {
				t.Errorf("overload set reported as ambiguity: %q", call.Reason)
			}
			// The target is the implementation — the declaration that has a body,
			// which is the one that keeps the bare compound-v1 ID.
			wantTarget := ""
			for _, symbol := range snapshot.Symbols {
				if symbol.FilePath == "src/renderList.ts" && symbol.Name == "renderList" && !symbol.bodyless {
					wantTarget = symbol.ID
				}
			}
			if wantTarget == "" {
				t.Fatal("no renderList implementation symbol in the snapshot")
			}
			if call.ToID != wantTarget {
				t.Errorf("call target = %s, want the implementation %s", call.ToID, wantTarget)
			}
		})
	}
}

// The overload collapse must never resolve a call the tool cannot actually
// resolve, so its guard is pinned directly: only a set that is one
// implementation plus its OWN bodyless declarations collapses. Everything else
// — two real definitions, candidates in different files or under different
// qualified names, a set with no implementation at all — stays ambiguous and
// takes the existing downgrade.
func TestBodylessOverloadImplementationRefusesGenuineAmbiguity(t *testing.T) {
	target := func(file, qualified, kind string, bodyless bool) resolvedCallTarget {
		return resolvedCallTarget{
			SymbolRecord: SymbolRecord{
				ID:       file + ":" + qualified + ":" + kind + map[bool]string{true: ":decl", false: ":def"}[bodyless],
				FilePath: file, QualifiedName: qualified, Name: qualified, Kind: kind,
				Language: "TypeScript", bodyless: bodyless,
			},
			Confidence: 0.86, Resolution: "import_resolved",
		}
	}
	for _, testCase := range []struct {
		name    string
		targets []resolvedCallTarget
		want    string // "" means: refuse to collapse
	}{{
		name: "overload set collapses to the implementation",
		targets: []resolvedCallTarget{
			target("a.ts", "renderList", "function", true),
			target("a.ts", "renderList", "function", true),
			target("a.ts", "renderList", "function", false),
		},
		want: "a.ts:renderList:function:def",
	}, {
		name: "two real definitions stay ambiguous",
		targets: []resolvedCallTarget{
			target("a.ts", "renderList", "function", false),
			target("a.ts", "renderList", "function", false),
		},
	}, {
		name: "declarations with no implementation stay ambiguous",
		targets: []resolvedCallTarget{
			target("a.d.ts", "amb", "function", true),
			target("a.d.ts", "amb", "function", true),
		},
	}, {
		name: "candidates in different files stay ambiguous",
		targets: []resolvedCallTarget{
			target("a.ts", "renderList", "function", true),
			target("b.ts", "renderList", "function", false),
		},
	}, {
		name: "candidates under different qualified names stay ambiguous",
		targets: []resolvedCallTarget{
			target("a.ts", "Worker.run", "method", true),
			target("a.ts", "Other.run", "method", false),
		},
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := bodylessOverloadImplementation(testCase.targets)
			if testCase.want == "" {
				if ok {
					t.Fatalf("collapsed to %s; must stay ambiguous", got.ID)
				}
				return
			}
			if !ok {
				t.Fatalf("refused to collapse; want %s", testCase.want)
			}
			if got.ID != testCase.want {
				t.Errorf("collapsed to %s, want %s", got.ID, testCase.want)
			}
			if got.Resolution != "import_resolved" || got.Confidence != 0.86 {
				t.Errorf("target = %s conf %.2f, want the original import_resolved/0.86",
					got.Resolution, got.Confidence)
			}
		})
	}
}
