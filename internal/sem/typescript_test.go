package sem

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTypeScriptEntityParameterNamesComeFromFormalParameters(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("src/run.ts", `class Runner {
  run(value: Map<A, B>, other: Client): (B: Client) => void { return null as any; }
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	for _, entity := range entities {
		if entity.Name != "Runner.run" && entity.Name != "run" {
			continue
		}
		if len(entity.parameterNames) != 2 || entity.parameterNames[0] != "value" || entity.parameterNames[1] != "other" {
			t.Fatalf("parameter names = %#v, want [value other] and no type or return-function identifiers", entity.parameterNames)
		}
		return
	}
	t.Fatalf("run entity missing: %#v", entities)
}

func TestTypeScriptAbstractBaseMethodNeighborhood(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/types.ts", `
type input<T> = { data: T };

abstract class Base {
  abstract _parse(value: input<string>): void;

  _parseSync(value: input<string>): void {
    this._parse(value);
  }
}

class Child extends Base {
  _parse(value: input<string>): void {
    const option = this;
    option._parseSync(value);
  }
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Base._parseSync", "Base._parse") {
		t.Fatalf("abstract declaration call target missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", "Child._parse", "Base._parseSync", "name_only") {
		t.Fatalf("polymorphic receiver call to inherited unique method missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	for _, relation := range relationsOfType(snapshot.Relations, "CONSTRUCTS") {
		if lastSegment(relation.FromID) == "_parseSync" && lastSegment(relation.ToID) == "input" {
			t.Fatalf("callable argument fabricated a type construction: %#v", relation)
		}
	}
}

func TestTypeScriptUniqueInheritedFallbackSkipsImportedReceiverType(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/external.ts", `export interface External { helper(): void }`)
	writeFile(t, repo, "src/use.ts", `
import type { External } from "./external";

class Base {
  helper(): void {}
}

class Worker extends Base {
  run(client: External): void {
    client.helper();
  }
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "Worker.run", "Base.helper") {
		t.Fatalf("imported receiver type fabricated inherited-method edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptUniqueInheritedFallbackSkipsLocalInterfaceReceiverType(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/use.ts", `interface External {
  helper(): void;
}

class Base {
  helper(): void {}
}

class Worker extends Base {
  run(client: External): void {
    client.helper();
  }
}
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "Worker.run", "Base.helper") {
		t.Fatalf("local interface receiver type fabricated inherited-method edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptResolvedImportDoesNotMatchGeneratedSuffixMirror(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/helper.ts", `export function resolve() {}`)
	writeFile(t, repo, "generated/src/helper.ts", `export function resolve() {}`)
	writeFile(t, repo, "src/use.ts", `
import { resolve } from "./helper";
export function run() { resolve(); }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	var targets []SymbolRecord
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" && lastSegment(relation.FromID) == "run" && lastSegment(relation.ToID) == "resolve" {
			targets = append(targets, byID[relation.ToID])
		}
	}
	if len(targets) != 1 || targets[0].FilePath != "src/helper.ts" {
		t.Fatalf("relative import targets = %#v, want only src/helper.ts", targets)
	}
}

func TestTypeScriptObjectMethodDoesNotResolveAsWorkspaceBareFunction(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/configure.ts", `export function configure() {
  const references = new Set<unknown>();
  references.add({});
}
`)
	writeFile(t, repo, "examples/counter.ts", `export function add() {}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "configure", "add") {
		t.Fatalf("object method call fabricated a workspace bare-function edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptSameFileNamespaceCallResolvesExactQualifiedTarget(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `export function run() {
  B.parse();
}

namespace A {
  export function parse() {}
}

namespace B {
  export function parse() {}
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var aParse, bParse SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if symbol.Name != "parse" {
			continue
		}
		switch symbol.StartLine {
		case 6:
			aParse = symbol
		case 10:
			bParse = symbol
		}
	}
	if aParse.ID == "" || bParse.ID == "" {
		t.Fatalf("namespace declarations missing from symbol inventory: %#v", snapshot.Symbols)
	}
	var calls []RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" || lastSegment(relation.FromID) != "run" {
			continue
		}
		if relation.ToID == aParse.ID || relation.ToID == bParse.ID {
			calls = append(calls, relation)
		}
	}
	if len(calls) != 1 || calls[0].ToID != bParse.ID || calls[0].Resolution != "exact" {
		t.Fatalf("namespace call targets = %#v, want exact B.parse target %q and not A.parse target %q", calls, bParse.ID, aParse.ID)
	}
}

func TestTypeScriptRelativeNestedNamespaceCallResolves(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace A {
  export namespace B {
    export function parse() {}
  }

  export function run() {
    B.parse();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var runID, parseID string
	for _, symbol := range snapshot.Symbols {
		switch symbol.Name {
		case "run":
			runID = symbol.ID
		case "parse":
			parseID = symbol.ID
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" && relation.FromID == runID && relation.ToID == parseID && relation.Resolution == "exact" {
			return
		}
	}
	t.Fatalf("relative nested namespace call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
}

func TestTypeScriptSameLineNestedNamespaceCallResolvesExactTarget(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace A { export function parse(value: string) {} export namespace B { export function parse(value: number) {} } } export function run() { A.B.parse(1); }`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	var outerParse, nestedParse SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if symbol.Name == "run" {
			runID = symbol.ID
		}
		if symbol.Name == "parse" {
			switch {
			case strings.Contains(symbol.Signature, "value: string"):
				outerParse = symbol
			case strings.Contains(symbol.Signature, "value: number"):
				nestedParse = symbol
			}
		}
	}
	if outerParse.ID == "" || nestedParse.ID == "" {
		t.Fatalf("parse symbols = %#v, want outer A.parse and nested A.B.parse", snapshot.Symbols)
	}
	var calls []RelationRecord
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" {
			calls = append(calls, relation)
		}
	}
	if len(calls) != 1 || calls[0].FromID != runID || calls[0].ToID != nestedParse.ID || calls[0].Resolution != "exact" {
		t.Fatalf("same-line nested namespace calls = %#v, want exact nested target %q and not outer target %q", calls, nestedParse.ID, outerParse.ID)
	}
}

func TestTypeScriptNamespaceCallSkipsParameterReceiverRoots(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace B {
  export function parse() {}
}

interface Client {
  parse(): void;
}

export function runParam(B: Client) {
  B.parse();
}

export function runOptional(B?: Client) {
  B.parse();
}

export function runRest(...B: Client[]) {
  B.parse();
}

export function runDefault(B: Client = {} as Client) {
  B.parse();
}

class Worker {
  constructor(private B: Client) {
    B.parse();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var parseID string
	runIDs := map[string]bool{}
	for _, symbol := range snapshot.Symbols {
		if symbol.Name == "parse" {
			parseID = symbol.ID
		}
		if symbol.Name == "runParam" || symbol.Name == "runOptional" || symbol.Name == "runRest" || symbol.Name == "runDefault" || symbol.Name == "constructor" {
			runIDs[symbol.ID] = true
		}
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CALLS" && runIDs[relation.FromID] && relation.ToID == parseID {
			t.Fatalf("shadowed receiver fabricated namespace edge: %#v", relation)
		}
	}
}

func TestTypeScriptNestedLocalShadowKeepsValidOuterNamespaceCall(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace B {
  export function parse() {}
}

interface Client { parse(): void }

export function run(client: Client) {
  B.parse();
  {
    const B = client;
    B.parse();
  }
}

export function runEnclosing(client: Client) {
  const B = client;
  {
    B.parse();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
		t.Fatalf("outer namespace call was suppressed by nested local binding: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "runEnclosing", "parse") {
		t.Fatalf("enclosing local binding did not shadow descendant-block call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNestedCallablesInheritOuterShadows(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace B { export function parse() {} }
interface Client { parse(): void }
export function outerParam(B: Client) {
  function nestedParam() { B.parse(); }
  const nestedArrow = () => B.parse();
}
export function outerConst(client: Client) {
  const B = client;
  function nestedConst() { B.parse(); }
}
export function siblingScopes(client: Client) {
  { const B = client; B.parse(); }
  { B.parse(); }
}
export function functionScopedVar(client: Client) {
  { var B = client; }
  B.parse();
}
export function destructuredObject(obj: {B: Client}) {
  const {B} = obj;
  B.parse();
}
export function destructuredArray(tuple: [Client]) {
  const [B] = tuple;
  B.parse();
}
export function objectParameter({B}: {B: Client}) { B.parse(); }
export function arrayParameter([B]: [Client]) { B.parse(); }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	shadowed := map[string]bool{
		"nestedParam": true, "nestedArrow": true, "nestedConst": true,
		"functionScopedVar": true, "destructuredObject": true, "destructuredArray": true,
		"objectParameter": true, "arrayParameter": true,
	}
	foundSibling := false
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	for _, relation := range relationsOfType(snapshot.Relations, "CALLS") {
		from, to := byID[relation.FromID], byID[relation.ToID]
		if to.Name != "parse" {
			continue
		}
		if shadowed[from.Name] {
			t.Fatalf("captured/local binding fabricated namespace call from %s: %#v", from.Name, relation)
		}
		if from.Name == "siblingScopes" {
			foundSibling = true
		}
	}
	if !foundSibling {
		t.Fatalf("sibling lexical binding suppressed valid namespace call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptParameterNamespaceShadowMatchesFastAndFullProfiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace B { export function parse() {} }
interface Client { parse(): void }
export function run(B: Client) { B.parse(); }
`)
	for _, profile := range []Profile{ProfileFull, ProfileFast} {
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true, Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		if hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
			t.Fatalf("profile %s fabricated parameter-shadowed namespace call: %#v", profile, relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

func TestTypeScriptDeclarationMergeKeepsNamespaceCalls(t *testing.T) {
	for _, declaration := range []string{"function B() {}", "class B {}", "enum B { Value }"} {
		t.Run(strings.Fields(declaration)[0], func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, "src/parser.ts", declaration+`
namespace B { export function parse() {} }
interface Client { parse(): void }
export function run() { B.parse(); }
export function local(client: Client) { { const B = client; B.parse(); } }
`)
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
			if err != nil {
				t.Fatal(err)
			}
			if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
				t.Fatalf("%s merge suppressed namespace call: %#v", declaration, relationsOfType(snapshot.Relations, "CALLS"))
			}
			if hasRelationByLastSegment(snapshot.Relations, "CALLS", "local", "parse") {
				t.Fatalf("local shadow bypassed by %s merge: %#v", declaration, relationsOfType(snapshot.Relations, "CALLS"))
			}
		})
	}
	t.Run("dotted namespace does not merge short name", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "src/parser.ts", `class B {}
namespace A.B { export function parse() {} }
export function run() { B.parse(); }
`)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		if hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
			t.Fatalf("top-level B incorrectly merged with dotted A.B namespace: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
	})
}

func TestTypeScriptCrossFileNamespaceMergeCallResolves(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/a.ts", `namespace A {
  export function run() {
    A.helper();
  }
}
`)
	writeFile(t, repo, "src/b.ts", `namespace A {
  export function helper() {}
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "run", "helper") {
		t.Fatalf("cross-file namespace merge call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptGenericReceiverKeepsInheritedMethodFallback(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/worker.ts", `class Base {
  helper(): void {}
}

class Worker extends Base {
  run<T extends Base>(item: T): void {
    item.helper();
  }
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "Worker.run", "Base.helper") {
		t.Fatalf("generic receiver suppressed inheritance-chain fallback: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptRegexLiteralBraceDoesNotCorruptBindingScopes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `function B() {}
export function clean(value: string) {
  return value.replace(/\{/g, '');
}
namespace B { export function parse() {} }
export function run() { B.parse(); }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
		t.Fatalf("regex-literal brace corrupted namespace-merge scope: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNestedDestructuringShadowsNamespaceCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace Utils { export function parse() {} }
namespace First { export function parse() {} }
export function nestedObject(pkg: any) {
  const { config: { Utils } } = pkg;
  Utils.parse();
}
export function nestedArray(rows: any) {
  const [[First]] = rows;
  First.parse();
}
export function control() {
  Utils.parse();
  First.parse();
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "nestedObject", "parse") {
		t.Fatalf("nested object destructuring did not shadow namespace call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "nestedArray", "parse") {
		t.Fatalf("nested array destructuring did not shadow namespace call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "control", "parse") {
		t.Fatalf("unshadowed namespace calls missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptLoopHeadBindingShadowConfinedToLoop(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace Item { export function parse() {} }
export function run(items: unknown[]) {
  for (const Item of items) {
    Item.parse();
  }
  Item.parse();
}
export function onlyLoop(items: unknown[]) {
  for (const Item of items) {
    Item.parse();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
		t.Fatalf("loop-head binding shadowed the whole function: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "onlyLoop", "parse") {
		t.Fatalf("loop-body namespace call escaped its loop-head shadow: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptSameLineToolWordsDoNotCreateSyntheticTool(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/tools.ts", `export function tool() {} export function execute() {}`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range snapshot.Symbols {
		if symbol.Kind == "tool" {
			t.Fatalf("same-line sibling words fabricated tool symbol: %#v", symbol)
		}
	}
}

func TestTypeScriptSameLineGraphQLResolversKeepExactNamespaceCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/resolvers.ts", `namespace A { export function parse() {} } namespace B { export function parse() {} } export const resolvers = { Query: { left: () => A.parse(), right: () => B.parse() } };`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	got := map[string]string{}
	for _, relation := range relationsOfType(snapshot.Relations, "CALLS") {
		from, to := byID[relation.FromID], byID[relation.ToID]
		if from.Kind == "graphql_resolver" && to.Name == "parse" {
			got[from.QualifiedName] = jsNamespaceBySymbolID("namespace A { export function parse() {} } namespace B { export function parse() {} } export const resolvers = { Query: { left: () => A.parse(), right: () => B.parse() } };", snapshot.Symbols, jsNamespaceScopes("namespace A { export function parse() {} } namespace B { export function parse() {} } export const resolvers = { Query: { left: () => A.parse(), right: () => B.parse() } };"))[to.ID]
		}
	}
	if got["Query.left"] != "A" || got["Query.right"] != "B" {
		t.Fatalf("resolver namespace calls = %#v, want left->A and right->B; calls=%#v", got, relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptSecondaryVariableDeclaratorNamespaceCallResolves(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace A.B { export function parse() {} }
export const first = () => 0, run = () => A.B.parse();
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "run", "parse") {
		t.Fatalf("secondary variable declarator namespace call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationByLastSegment(snapshot.Relations, "CALLS", "first", "parse") {
		t.Fatalf("secondary declarator call contaminated its same-line sibling: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptBareCallDoesNotResolveToInventoryDocument(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/use.ts", `export function useItems() {
  return list();
}
`)
	writeFile(t, repo, "templates/list.html", `<ul></ul>
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "useItems", "list") {
		t.Fatalf("bare call resolved to a non-callable inventory symbol: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptReceiverTypesFromLocalsAndInjectProperties(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/url_handling_strategy.ts", `export abstract class UrlHandlingStrategy {
  abstract merge(newUrlPart: UrlTree, rawUrl: UrlTree): UrlTree;
}

export class DefaultUrlHandlingStrategy implements UrlHandlingStrategy {
  merge(newUrlPart: UrlTree, rawUrl: UrlTree): UrlTree {
    return newUrlPart;
  }
}

export class UrlTree {}
`)
	writeFile(t, repo, "src/router.ts", `import {DefaultUrlHandlingStrategy, UrlHandlingStrategy, UrlTree} from './url_handling_strategy';

declare function inject<T>(token: unknown): T;

export class Router {
  private readonly urlHandlingStrategy = inject(UrlHandlingStrategy);

  navigateByUrl(url: string | UrlTree): Promise<boolean> {
    const tree = url instanceof UrlTree ? url : this.parseUrl(url);
    const merged = this.urlHandlingStrategy.merge(tree, tree);
    return this.scheduleNavigation(merged);
  }

  parseUrl(url: string): UrlTree {
    return new UrlTree();
  }

  private scheduleNavigation(url: UrlTree): Promise<boolean> {
    return Promise.resolve(true);
  }
}

new DefaultUrlHandlingStrategy();
`)
	writeFile(t, repo, "src/router_link.ts", `import {Router} from './router';

declare function inject<T>(token: unknown): T;

export class RouterLink {
  private readonly router = inject(Router);

  onClick() {
    this.router.navigateByUrl('/home');
  }
}
`)
	writeFile(t, repo, "src/upgrade.ts", `import {Router} from './router';

export interface UpgradeModule {
  injector: { get<T>(token: unknown): T };
}

export function setUpLocationSync(ngUpgrade: UpgradeModule) {
  const router: Router = ngUpgrade.injector.get(Router);
  router.navigateByUrl('/legacy');
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}
	for _, want := range []string{
		"RouterLink.onClick->Router.navigateByUrl",
		"setUpLocationSync->Router.navigateByUrl",
		"Router.navigateByUrl->DefaultUrlHandlingStrategy.merge",
	} {
		if _, ok := calls[want]; !ok {
			t.Fatalf("missing TypeScript receiver call %s in %#v", want, calls)
		}
	}
	if got := calls["Router.navigateByUrl->DefaultUrlHandlingStrategy.merge"].Reason; got != "interface-typed receiver call resolved to the unique TypeScript implementation" {
		t.Fatalf("merge resolved for the wrong reason %q", got)
	}
}

func TestTSXClosingTagsDoNotSwallowNamespaceCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/row.tsx", `namespace Utils {
  export function format(value: number): string { return String(value); }
}
export function Row(props: { value: number }) {
  return (
    <table>
      <tbody>
        <tr>
          <td>{Utils.format(props.value)}</td>
        </tr>
      </tbody>
    </table>
  );
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "Row", "format") {
		t.Fatalf("JSX closing tags swallowed the namespace call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptQuoteAndBacktickInRegexKeepNamespaceCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `export function clean(value: string) {
  return value.replace(/'/g, "").replace(/`+"`"+`+/g, "");
}
namespace Utils { export function parse() {} }
export function run() { Utils.parse(); }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "run", "parse") {
		t.Fatalf("quote inside regex literal blanked the namespace declaration: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptRegexBraceInStatementPositionKeepsScopes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `export function check(flag: boolean, value: string) {
  if (flag) /\{+/.test(value);
  while (flag) /}/.test(value) ? flag = false : flag = false;
  return value;
}
namespace Utils { export function parse() {} }
export function run() { Utils.parse(); }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "run", "parse") {
		t.Fatalf("brace inside regex literal corrupted scope tracking: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNamespaceBodyStatementCallResolvesRelativeReceiver(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace A {
  export namespace B {
    export function f() {}
  }
  B.f();
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "src/parser.ts", "f") {
		t.Fatalf("statement-position call inside namespace body lost its CALLS edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptForOfDestructuredHeadShadowsNamespaceCall(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace Utils { export function helper() {} }
export function run(items: unknown[]) {
  for (const {Utils} of items) {
    Utils.helper();
  }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "run", "helper") {
		t.Fatalf("for-of destructured head binding did not shadow the namespace call: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptDestructuringDefaultComparisonKeepsLaterBindings(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace Utils { export function parse() {} }
export function run(opts: any, x: number) {
  const { a = x < 2, Utils } = opts;
  Utils.parse();
}
export function control() {
  Utils.parse();
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "run", "parse") {
		t.Fatalf("bare comparison in destructuring default dropped the later binding: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "control", "parse") {
		t.Fatalf("unshadowed namespace call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptGenericFunctionTypeClauseKeepsParameterShadow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace Utils { export function parse() {} }
export function run<T extends (x: number) => void>(Utils: T) {
  Utils.parse();
}
export function control() {
  Utils.parse();
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "run", "parse") {
		t.Fatalf("generic clause with function type broke the parameter shadow: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "control", "parse") {
		t.Fatalf("unshadowed namespace call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptSameLineDeclarationAndCallKeepsTopLevelCallEdge(t *testing.T) {
	t.Run("call after declaration", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "src/parser.ts", `export function helper() {}
export function f() {} helper();
`)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "src/parser.ts", "helper") {
			t.Fatalf("top-level call sharing a line with a declaration lost its CALLS edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
		if hasRelationByLastSegment(snapshot.Relations, "CALLS", "f", "helper") {
			t.Fatalf("same-line top-level call was attributed to the declaration sharing its line: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
	})
	t.Run("declaration after call", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "src/parser.ts", `export function helper() {}
helper(); export function g() {}
`)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "src/parser.ts", "helper") {
			t.Fatalf("top-level call preceding a same-line declaration lost its CALLS edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
		if hasRelationByLastSegment(snapshot.Relations, "CALLS", "g", "helper") {
			t.Fatalf("same-line top-level call was attributed to the declaration sharing its line: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
	})
}

func TestTypeScriptMergedClassNamespaceStaticCallResolvesToClassMethod(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/merged.ts", `export class A {
  static create() {}
}

export namespace A {
  export const version = 1;
}

export function run() {
  A.create();
}
`)
	writeFile(t, repo, "src/unrelated.ts", `export function create() {}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "run", "src/merged.ts", "create", "src/merged.ts") {
		t.Fatalf("merged class+namespace static call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolNameAndFile(snapshot, "CALLS", "run", "src/merged.ts", "create", "src/unrelated.ts") {
		t.Fatalf("merged class+namespace call fabricated a workspace-wide bare-name edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptMergedDeclarationMissingMemberResolvesCrossFileNamespaceMember(t *testing.T) {
	// The merged same-file class lacks the called member; the member lives in
	// the same namespace reopened in another file. The merged declaration must
	// not swallow that edge — but the fallback stays confined to the
	// receiver's namespace, so an unrelated same-named workspace function
	// never steals it.
	mergedSource := `export class A {
  static create() {}
}
export namespace A {
  export const marker = 1;
}
export function caller() {
  A.g();
}
`
	t.Run("cross-file namespace member wins", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "src/a.ts", mergedSource)
		writeFile(t, repo, "src/b.ts", `export namespace A {
  export function g() {}
}
`)
		writeFile(t, repo, "src/unrelated.ts", `export function g() {}
`)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		if !hasRelationBySymbolNameAndFile(snapshot, "CALLS", "caller", "src/a.ts", "g", "src/b.ts") {
			t.Fatalf("merged declaration suppressed the edge to the namespace member reopened in another file: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
		if hasRelationBySymbolNameAndFile(snapshot, "CALLS", "caller", "src/a.ts", "g", "src/unrelated.ts") {
			t.Fatalf("merged-receiver fallback resolved to a symbol unrelated to the namespace: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
	})
	t.Run("no namespace member anywhere stays unresolved", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "src/a.ts", mergedSource)
		writeFile(t, repo, "src/unrelated.ts", `export function g() {}
`)
		snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
		if err != nil {
			t.Fatal(err)
		}
		if hasRelationBySymbolNameAndFile(snapshot, "CALLS", "caller", "src/a.ts", "g", "src/unrelated.ts") {
			t.Fatalf("merged-receiver fallback fabricated a bare-name workspace edge: %#v", relationsOfType(snapshot.Relations, "CALLS"))
		}
	})
}

func TestTypeScriptDottedNamespaceSiblingBeatsFileBindingShadow(t *testing.T) {
	// `namespace A.B` records no scope named exactly "A", but it implicitly
	// declares A over the same range: inside sibling `namespace A.X`, bare B
	// resolves to A.B, beating the file-level `function B` binding. At top
	// level (outside A) the function binding is the only B in scope.
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `function B() { return 1 }
namespace A.B {
  export function f() {}
}
namespace A.X {
  export function g() {
    B.f();
  }
}
export function h() {
  B.f();
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "g", "f") {
		t.Fatalf("file-level binding wrongly shadowed the sibling dotted namespace: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "h", "f") {
		t.Fatalf("top-level call escaped the file-level binding shadow: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptBindingBetweenOuterAndInnerAnchorScopesShadows(t *testing.T) {
	// The prefix anchor must be the OUTERMOST enclosing scope extending the
	// prefix: a binding declared between the outer and inner scope starts is
	// lexically nearer than the prefix-namespace member and wins (tsc executes
	// the local const). An inner anchor would wrongly exempt it.
	repo := t.TempDir()
	writeFile(t, repo, "src/anchor.ts", `namespace A.Sub {
  export function target() {}
}
namespace A.B {
  const Sub = { target() {} };
  export namespace C {
    export function caller() {
      Sub.target();
    }
  }
}
`)
	writeFile(t, repo, "src/anchorexact.ts", `namespace N.Sub2 {
  export function target2() {}
}
namespace N {
  const Sub2 = { target2() {} };
  export namespace M {
    export function caller2() {
      Sub2.target2();
    }
  }
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "caller", "target") {
		t.Fatalf("binding between dotted anchor scopes failed to shadow: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "caller2", "target2") {
		t.Fatalf("binding between exact-name anchor scopes failed to shadow: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNearerNamespaceBeatsOuterBindingShadow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `function A() {}
namespace B {
  namespace A {
    export function f() {}
  }
  export function g() {
    A.f();
  }
}
interface Client { f(): void }
namespace C {
  namespace A2 {
    export function f2() {}
  }
  export function h(A2: Client) {
    A2.f2();
  }
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "g", "f") {
		t.Fatalf("outer file-wide binding shadowed the nearer nested namespace: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "h", "f2") {
		t.Fatalf("genuinely inner parameter binding no longer shadows the namespace: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNamedExpressionSelfNameShadowsNamespace(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/utils.ts", `namespace Utils {
  export function helper() {}
}
const x = (function Utils() {
  Utils.helper();
})();
const y = class Utils {
  run() {
    Utils.helper();
  }
};
export function control() {
  Utils.helper();
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	for _, relation := range relationsOfType(snapshot.Relations, "CALLS") {
		if byID[relation.ToID].Name != "helper" {
			continue
		}
		if from, ok := byID[relation.FromID]; ok && from.Name == "control" {
			continue
		}
		if lastSegment(relation.FromID) == "src/utils.ts" || byID[relation.FromID].Name == "run" {
			t.Fatalf("named expression self-name did not shadow the namespace receiver: %#v", relation)
		}
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "control", "helper") {
		t.Fatalf("unshadowed namespace call missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptNestedDottedNamespaceQualifiesAgainstParent(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `namespace A {
  namespace A.B {
    export function f() {}
    export function inner() {
      B.f();
    }
  }
  export function g() {
    A.B.f();
  }
}
export function h() {
  A.B.f();
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "g", "f") {
		t.Fatalf("parent-relative dotted namespace call from sibling scope missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "inner", "f") {
		t.Fatalf("call inside the dotted namespace body missing: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
	if hasRelationBySymbolName(snapshot, "CALLS", "h", "f") {
		t.Fatalf("top-level A.B.f() resolved to parent-relative namespace A.A.B: %#v", relationsOfType(snapshot.Relations, "CALLS"))
	}
}

func TestTypeScriptRelationScanParseTimeoutRecordsPartialFailure(t *testing.T) {
	saved := jsScanParseTimeout
	jsScanParseTimeout = 1 * time.Nanosecond
	defer func() { jsScanParseTimeout = saved }()

	repo := t.TempDir()
	// The relation-phase scope parse must be big enough that tree-sitter
	// observes the already-expired deadline mid-parse; the entity phase keeps
	// the full parse budget and is unaffected.
	var source strings.Builder
	source.WriteString("namespace Utils { export function parse() {} }\n")
	source.WriteString("export function run() { Utils.parse(); }\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&source, "export function generated%d(value: number) { return value; }\n", i)
	}
	writeFile(t, repo, "src/parser.ts", source.String())
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath != "src/parser.ts" {
			continue
		}
		if failure.Code == "E_PARSE_TIMEOUT" && strings.Contains(failure.EffectOnCompleteness, "relation-phase") {
			found = true
		}
	}
	if !found {
		t.Fatalf("relation-phase scope parse timeout was not surfaced as a partial failure: %#v", snapshot.Header.PartialFailures)
	}
	if len(snapshot.Symbols) == 0 {
		t.Fatalf("entity-phase parsing should be unaffected by the relation-phase timeout: %#v", snapshot.Symbols)
	}
}

func TestStreamSnapshotDedupesSameCodeFailuresAcrossPhases(t *testing.T) {
	// The entity-phase parse budget (treeSitterParseTimeout) is a constant, so
	// a real both-phase timeout cannot be forced end-to-end in a unit test.
	// Pin the cross-phase dedup at the exact merge seam StreamSnapshot uses:
	// the records the two phases produce for one file (entity-phase timeout,
	// then jsScanPartialFailure for the same file) must collapse to one, with
	// the entity-phase record winning, while distinct codes both survive.
	entity := PartialFailure{
		Code:                 "E_PARSE_TIMEOUT",
		Severity:             "warning",
		FilePath:             "src/big.ts",
		EffectOnCompleteness: "file record emitted but symbol parsing skipped because parser time budget was exceeded",
	}
	relation := jsScanPartialFailure("src/big.ts", fmt.Errorf("%w: tree-sitter scope parse exceeded budget", context.DeadlineExceeded))
	if relation.Code != entity.Code {
		t.Fatalf("phases no longer produce the same code for a blown parse budget: %q vs %q", entity.Code, relation.Code)
	}
	merged := mergePartialFailures([]PartialFailure{entity}, []PartialFailure{relation})
	if len(merged) != 1 {
		t.Fatalf("same code+file failure reported by both phases must dedupe to one record: %#v", merged)
	}
	if merged[0].EffectOnCompleteness != entity.EffectOnCompleteness {
		t.Fatalf("dedup must keep the entity-phase record: %#v", merged[0])
	}
	distinct := mergePartialFailures([]PartialFailure{{Code: "E_PARSE_ERROR", FilePath: "src/big.ts"}}, []PartialFailure{relation})
	if len(distinct) != 2 {
		t.Fatalf("different codes for the same file must both be reported: %#v", distinct)
	}
}

func TestStreamSnapshotFreshBuildReportsNoDuplicateFailures(t *testing.T) {
	saved := jsScanParseTimeout
	jsScanParseTimeout = 1 * time.Nanosecond
	defer func() { jsScanParseTimeout = saved }()

	repo := t.TempDir()
	// The entity phase records E_PARSE_ERROR (syntax error), the relation
	// phase records E_PARSE_TIMEOUT for the same file: both must survive the
	// merge, and no code+file pair may appear twice in a fresh build.
	var source strings.Builder
	source.WriteString("namespace Utils { export function parse() {} }\n")
	source.WriteString("export function run() { Utils.parse(); }\n")
	source.WriteString("export function broken( {\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&source, "export function generated%d(value: number) { return value; }\n", i)
	}
	writeFile(t, repo, "src/parser.ts", source.String())
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, failure := range snapshot.Header.PartialFailures {
		seen[failure.Code+"\x00"+failure.FilePath]++
	}
	for key, count := range seen {
		if count > 1 {
			t.Fatalf("fresh build reported %d records for %q: %#v", count, strings.ReplaceAll(key, "\x00", " "), snapshot.Header.PartialFailures)
		}
	}
	if seen["E_PARSE_ERROR\x00src/parser.ts"] != 1 || seen["E_PARSE_TIMEOUT\x00src/parser.ts"] != 1 {
		t.Fatalf("distinct-code failures from the two phases must both be reported: %#v", snapshot.Header.PartialFailures)
	}
}

func TestTypeScriptASTParameterNamesDriveDataFlowForwarding(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/flow.ts", `export function target(value: string) {}
export function sink(x: unknown) {}
export function run<T extends (helper: string) => void>(input: string) {
  target(input);
  sink(helper);
}
declare const helper: string;
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "DATA_FLOWS", "run", "target") {
		t.Fatalf("AST parameter names did not drive the argument-forward flow: %#v", relationsOfType(snapshot.Relations, "DATA_FLOWS"))
	}
	if hasRelationBySymbolName(snapshot, "DATA_FLOWS", "run", "sink") {
		t.Fatalf("generic-clause function type identifier leaked in as a phantom parameter flow: %#v", relationsOfType(snapshot.Relations, "DATA_FLOWS"))
	}
}

func TestTypeScriptTypeInferenceDropsConflictingProperties(t *testing.T) {
	content := `class A {
  private readonly service = inject(ServiceA);
}

class B {
  private readonly service = inject(ServiceB);
}
`
	fields := []SymbolRecord{
		{Kind: "field", Language: "TypeScript", Name: "service", StartLine: 2, EndLine: 2},
		{Kind: "field", Language: "TypeScript", Name: "service", StartLine: 6, EndLine: 6},
	}
	if got := typeScriptPropertyTypes(content, fields); len(got) != 0 {
		t.Fatalf("conflicting property types should be dropped, got %#v", got)
	}
}
