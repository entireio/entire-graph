package sem

import "testing"

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
