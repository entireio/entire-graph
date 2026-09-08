package sem

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Round six. The three findings were three instances of one shape: a stage
// reached AFTER the budget check, holding a parse context of its own that the
// caller's budget could not reach. The fix is structural rather than per-site:
// every tree-sitter parse in this package now derives its context from the
// caller, and TestTF142R6NoUnbudgetedParseContext is the invariant that keeps
// it that way for stages nobody has written yet.

// TestTF142R6RouteBridgeObservesStop pins the finding at provider.go:3972. The
// caller's guard between relation stages could not run until the whole
// calls-by-handlers Cartesian product had already been built, so a
// high-cardinality route was charged in full to a budget that had expired.
// Deterministic: the predicate is flipped by the test, never by a clock.
func TestTF142R6RouteBridgeObservesStop(t *testing.T) {
	t.Parallel()
	const n = 40
	routeHandlers := map[string][]SymbolRecord{}
	httpCallsByRoute := map[string][]RelationRecord{}
	for i := 0; i < n; i++ {
		routeHandlers["GET /x"] = append(routeHandlers["GET /x"], SymbolRecord{ID: fmt.Sprintf("h%03d", i), FilePath: "h.go"})
		httpCallsByRoute["GET /x"] = append(httpCallsByRoute["GET /x"], RelationRecord{FromID: fmt.Sprintf("c%03d", i), ToID: "t"})
	}

	// Already expired on entry: not one pair of the product may be built.
	if got := routeBridgeRelations(func() bool { return true }, routeHandlers, httpCallsByRoute); len(got) != 0 {
		t.Errorf("an expired budget must build no route-bridge relations, got %d", len(got))
	}

	// Expiring mid-product: the loop must stop at the next pair, not at the
	// end of the product. n*n pairs are reachable; a handful are not.
	probes := 0
	got := routeBridgeRelations(func() bool {
		probes++
		return probes > 4
	}, routeHandlers, httpCallsByRoute)
	if len(got) > 4 {
		t.Errorf("the product must stop within a few pairs of the predicate flipping, got %d of %d", len(got), n*n)
	}

	// Widening: the unbudgeted path is unchanged. A nil predicate carries no
	// check at all and still builds the whole product.
	if got := routeBridgeRelations(nil, routeHandlers, httpCallsByRoute); len(got) != n*n {
		t.Errorf("an unbudgeted route bridge must be unchanged: got %d relations, want %d", len(got), n*n)
	}

	// graphqlSchemaResolverRelations is the same fields-by-resolvers product,
	// was not reported, and is fixed in the same pass rather than left for a
	// later round to rediscover.
	fields := map[string][]SymbolRecord{}
	resolvers := map[string][]SymbolRecord{}
	for i := 0; i < n; i++ {
		fields["Query.x"] = append(fields["Query.x"], SymbolRecord{ID: fmt.Sprintf("f%03d", i), FilePath: "s.graphql"})
		resolvers["Query.x"] = append(resolvers["Query.x"], SymbolRecord{ID: fmt.Sprintf("r%03d", i), FilePath: "r.ts"})
	}
	if got := graphqlSchemaResolverRelations(func() bool { return true }, fields, resolvers); len(got) != 0 {
		t.Errorf("an expired budget must build no GraphQL resolver relations, got %d", len(got))
	}
	if got := graphqlSchemaResolverRelations(nil, fields, resolvers); len(got) != n*n {
		t.Errorf("an unbudgeted GraphQL resolver product must be unchanged: got %d, want %d", len(got), n*n)
	}
}

// TestTF142R6SourceMaskingObservesBudget pins the finding at parser.go:336.
// Language-specific masking ran between the stop predicate and the budgeted
// parse context, and Rust's mask runs up to eight tree-sitter unwrap passes of
// its own, each previously rooted at context.Background.
func TestTF142R6SourceMaskingObservesBudget(t *testing.T) {
	t.Parallel()
	source := "cfg_net! {\npub struct Listener { pub fd: u64 }\n}\n"

	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	if masked := maskRustUnsupportedSyntax(expired, source); masked != source {
		t.Errorf("an expired budget must run no unwrap passes, source was rewritten")
	}
	entities, language, status := TreeSitterParser{}.ParseWithStatusCtx(expired, "lib.rs", source)
	if language != "Rust" {
		t.Fatalf("language = %q, want Rust", language)
	}
	if len(entities) != 0 || !status.ParseError || status.Code != "E_PARSE_TIMEOUT" {
		t.Errorf("an expired budget must abandon the file: entities=%d status=%+v", len(entities), status)
	}

	// Widening: without a budget the mask still unwraps and the wrapped item
	// is still a symbol, so unbudgeted output is unchanged.
	if masked := maskRustUnsupportedSyntax(context.Background(), source); masked == source {
		t.Errorf("an unbudgeted Rust mask must still unwrap cfg_*! wrappers")
	}
	unbudgeted, _, _ := TreeSitterParser{}.ParseWithStatus("lib.rs", source)
	found := false
	for _, entity := range unbudgeted {
		if entity.Name == "Listener" {
			found = true
		}
	}
	if !found {
		t.Errorf("an unbudgeted parse must still recover the wrapped item, got %d entities", len(unbudgeted))
	}
}

// TestTF142R6JSScanObservesBudget pins the finding at provider.go:2704. The
// relation phase's per-file scope parse ran on a 5s timeout derived from
// context.Background, so every remaining file could spend that much after the
// wall-clock budget had already expired.
func TestTF142R6JSScanObservesBudget(t *testing.T) {
	t.Parallel()
	source := "namespace A { export function parse() {} }\nA.parse();\n"

	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	state, err := newJSScanState(expired, "a.ts", source)
	if err == nil {
		t.Fatalf("an expired budget must report the abandoned scope parse, got nil error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the abandoned scan must carry the caller's cause, got %v", err)
	}
	if state == nil || state.parsed || len(state.namespaces) != 0 {
		t.Errorf("an expired budget must yield an unparsed state, got %+v", state)
	}
	if failure := jsScanPartialFailure("a.ts", err); failure.Code != "E_PARSE_TIMEOUT" {
		t.Errorf("an abandoned scan must surface as E_PARSE_TIMEOUT, got %s", failure.Code)
	}

	// Widening: the unbudgeted scan is unchanged.
	state, err = newJSScanState(context.Background(), "a.ts", source)
	if err != nil || !state.parsed || len(state.namespaces) != 1 {
		t.Errorf("an unbudgeted scope parse must be unchanged: err=%v parsed=%v namespaces=%d", err, state.parsed, len(state.namespaces))
	}
	if scopes := jsNamespaceScopes(context.Background(), source); len(scopes) != 1 {
		t.Errorf("an unbudgeted namespace scan must be unchanged, got %d scopes", len(scopes))
	}
}
