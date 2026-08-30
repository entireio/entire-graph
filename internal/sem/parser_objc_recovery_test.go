package sem

import (
	"strings"
	"testing"
)

// TestObjectiveCRecoveredMethodsAreQualifiedByTheirContainer pins the
// duplication that the recovery scanner used to cause. It supplements the
// tree-sitter walk for implementation methods the walk misses, but it named
// them by their bare selector while the walk names them `Class.selector`.
// appendMissingEntities keys on kind+name, so the two never collided: every
// method the walk DID find gained a second, unqualified record at the same
// source location.
//
// The cost was not cosmetic. The symbol table reported each method twice, each
// call into the method produced two CALLS edges, and the pair matched itself as
// a SIMILAR_TO near-clone.
func TestObjectiveCRecoveredMethodsAreQualifiedByTheirContainer(t *testing.T) {
	t.Parallel()
	entities, language := TreeSitterParser{}.Parse("Ledger.m", `@interface Ledger : NSObject
- (int)add:(int)amount;
@end

@implementation Ledger

- (int)add:(int)amount {
    return amount;
}

- (int)twice:(int)amount {
    return [self add:amount] * 2;
}

@end
`)
	if language != "Objective-C" {
		t.Fatalf("language = %q, want Objective-C", language)
	}

	counts := map[string]int{}
	for _, entity := range entities {
		if entity.Kind == "method" {
			counts[entity.Name]++
		}
	}
	for _, want := range []string{"Ledger.add", "Ledger.twice"} {
		if counts[want] != 1 {
			t.Errorf("method %q has %d records, want exactly 1: %#v", want, counts[want], counts)
		}
	}
	for name := range counts {
		if !strings.Contains(name, ".") {
			t.Errorf("method %q is not qualified by its container: %#v", name, counts)
		}
	}
}

// TestObjectiveCRecoveredMethodsUseCategoryOwner checks that a category
// (`@implementation Foo (Bar)`) attributes its methods to Foo, matching how the
// tree-sitter walk names the same class_implementation node, and that a method
// written outside any container is still recovered rather than dropped.
func TestObjectiveCRecoveredMethodsUseCategoryOwner(t *testing.T) {
	t.Parallel()
	entities, _ := TreeSitterParser{}.Parse("Ledger+Math.m", `@implementation Ledger (Math)

- (int)square:(int)amount {
    return amount * amount;
}

@end

static int Helper(int amount) { return amount; }
`)
	found := map[string]bool{}
	for _, entity := range entities {
		found[entity.Kind+":"+entity.Name] = true
	}
	if !found["method:Ledger.square"] {
		t.Errorf("category method should be attributed to Ledger: %#v", found)
	}
	if found["method:square"] {
		t.Errorf("category method should not also appear unqualified: %#v", found)
	}
}
