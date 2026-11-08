package sem

import (
	"fmt"
	"go/ast"
	"testing"
)

// closureEffects is the sole producer registry for closure descriptors during
// a resolver walk. With no effects, no visible binding can carry a descriptor,
// so visibleClosureRewriteTargets need not charge any scope-walk steps. This is
// a deterministic resolver-budget assertion, not a timing or resource claim.
func TestReview312VisibleClosureTargetsEmptyEffectsFastPath(t *testing.T) {
	resolver := &goRouteResolver{
		budget:         0,
		closureEffects: map[*ast.FuncLit]*goRouteClosureEffect{},
	}
	packageScope := newGoRouteScope(nil)
	for i := 0; i < 32; i++ {
		packageScope.vars[fmt.Sprintf("packageValue%d", i)] = &goRouteBinding{}
	}
	functionScope := newGoRouteScope(packageScope)
	for i := 0; i < 8; i++ {
		functionScope.vars[fmt.Sprintf("localValue%d", i)] = &goRouteBinding{kind: goRouteUnknown}
	}

	if got := resolver.visibleClosureRewriteTargets(functionScope); len(got) != 0 {
		t.Fatalf("visible rewrite targets = %#v, want none without descriptor effects", got)
	}
	if resolver.steps != 0 {
		t.Fatalf("empty descriptor fast path charged %d steps, want no scope walk", resolver.steps)
	}
}
