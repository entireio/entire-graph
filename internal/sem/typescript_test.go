package sem

import (
	"strings"
	"testing"
)

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

func TestTypeScriptSameFileNamespaceCallStillResolves(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/parser.ts", `export function run() {
  Parser.parse();
}

namespace Parser {
  export function parse() {}
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRelationBySymbolName(snapshot, "CALLS", "run", "parse") {
		t.Fatalf("same-file namespace call was lost: %#v", relationsOfType(snapshot.Relations, "CALLS"))
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

// An arrow function (or function expression) assigned to a variable or class
// field must carry only its parameter list (plus return-type annotation and
// `=>`) as its Signature, never the whole function body — otherwise any body
// edit reads as a signature change in entity diffs.
func TestTypeScriptArrowFunctionSignatureExcludesBody(t *testing.T) {
	entities, language := TreeSitterParser{}.Parse("handlers.ts", `const handle = (a: number, b: string): boolean => {
  const x = a + 1;
  console.log(b);
  doSomethingExpensive(x);
  return x > 0;
};

class Service {
  create = (name: string) => {
    const trimmed = name.trim();
    return new Thing(trimmed);
  };
}
`)
	if language != "TypeScript" {
		t.Fatalf("language = %q", language)
	}
	seen := map[string]Entity{}
	for _, entity := range entities {
		seen[entity.Name] = entity
	}

	handle, ok := seen["handle"]
	if !ok {
		t.Fatalf("missing arrow-function variable %q in %#v", "handle", entities)
	}
	if want := "handle = (a: number, b: string): boolean =>"; handle.Signature != want {
		t.Fatalf("arrow-function signature = %q, want %q", handle.Signature, want)
	}
	if strings.Contains(handle.Signature, "return") || strings.Contains(handle.Signature, "doSomethingExpensive") {
		t.Fatalf("arrow-function signature leaked the body: %q", handle.Signature)
	}

	create, ok := seen["Service.create"]
	if !ok {
		t.Fatalf("missing class-field arrow method %q in %#v", "Service.create", entities)
	}
	if want := "create = (name: string) =>"; create.Signature != want {
		t.Fatalf("class-field arrow signature = %q, want %q", create.Signature, want)
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
