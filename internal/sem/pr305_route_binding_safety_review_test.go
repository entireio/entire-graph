package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Independent PR305 supplement. Copy into internal/sem in an isolated export.
// This file is standalone; it does not depend on or modify the original
// pr305_route_fixpoint_review_test.go. Only the actual string-input route helper
// is exercised, never a provider, Snapshot, Run, or a real repository.
// Authored and source-reviewed only: execution belongs to the root reviewer.
const review305BindingChildCaseEnv = "EG_REVIEW305_ROUTE_BINDING_CASE"
const review305BindingResultPrefix = "EG_REVIEW305_ROUTE_BINDING_RESULT="

func review305BindingFixture(name string) (string, bool) {
	switch name {
	case "before-after-self-update":
		return `package routes
func register(root *Router) {
	g := root.Group("/base")
	g.GET("/early", earlyHandler)
	g = g.Group("/self")
	g.GET("/late", lateHandler)
}
`, true
	case "nested-shadow":
		return `package routes
func register(root *Router) {
	g := root.Group("/outer")
	g.GET("/before", outerBeforeHandler)
	{
		g := root.Group("/inner")
		g.GET("/inside", innerHandler)
	}
	g.GET("/after", outerAfterHandler)
}
`, true
	case "non-group-assignment":
		return `package routes
func register(root *Router, replacement *Group) {
	g := root.Group("/base")
	g.GET("/before", beforeReplacementHandler)
	g = replacement
	g.GET("/after", afterReplacementHandler)
}
`, true
	case "scoped-descendants":
		return `package routes
func first(root *Router) {
	g := root.Group("/a")
	firstChild := g.Group("/child")
	firstChild.GET("/x", descendantAHandler)
}
func second(root *Router) {
	g := root.Group("/b")
	secondChild := g.Group("/child")
	secondChild.GET("/y", descendantBHandler)
}
`, true
	case "scoped-inline-chain":
		return `package routes
func first(root *Router) {
	g := root.Group("/a")
	g.Group("/inline").GET("/x", inlineAHandler)
}
func second(root *Router) {
	g := root.Group("/b")
	g.Group("/inline").GET("/y", inlineBHandler)
}
`, true
	default:
		return "", false
	}
}

// The strings use ordinary Go declaration order and lexical scopes. Router,
// Group, and handler declarations are intentionally external to the strings;
// the helper does not compile them. As in the original positive controls,
// root denotes the unprefixed router, and Group returns a group at its prefix.
// No oracle here calls sequential parameter assignments a global name cycle.
func TestReview305RouteBindingChild(t *testing.T) {
	name := os.Getenv(review305BindingChildCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	content, ok := review305BindingFixture(name)
	if !ok {
		t.Fatalf("unknown authored binding fixture %q", name)
	}
	registrations := goHTTPRouteRegistrations(content, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review305BindingResultPrefix, encoded)
}

func review305BindingRoutesWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview305RouteBindingChild$", "-test.count=1")
	// Start only this owned child case, with no inherited credentials, product
	// configuration, or other test helper modes. The executable path is absolute.
	command.Env = []string{review305BindingChildCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	// Portability adaptation of the independent fixture (not part of its
	// original text): on Windows the re-executed test binary cannot load its
	// DLLs from a three-variable environment (exit 0xc0000135,
	// STATUS_DLL_NOT_FOUND, in CI run 36673714080). Pass through only the
	// loader/temp variables that are set. No assertion and no non-Windows
	// behavior changes.
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "PATH", "TEMP", "TMP"} {
			if value, ok := os.LookupEnv(key); ok {
				command.Env = append(command.Env, key+"="+value)
			}
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	// CombinedOutput waits for/reaps the child. Context cancellation kills the
	// process instead of abandoning a goroutine stuck in the old helper loop.
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("route binding helper exceeded 2s for %s; owned child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned binding child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review305BindingResultPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned binding child result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned binding child emitted no route result for %s: %s", name, output)
	return nil
}

// Checks only justified associations. An omitted route is acceptable here:
// these safety tests do not force implementation of unsupported scope/write
// analysis. The exact positive test below separately rejects an empty result.
func review305BindingAssertAllowed(t *testing.T, registrations []goHTTPRouteRegistration, allowed map[string]string) {
	t.Helper()
	for _, registration := range registrations {
		want, exists := allowed[registration.Handler]
		if !exists || registration.Route != want {
			t.Fatalf("unjustified route association %#v; allowed %#v (omission is acceptable)", registration, allowed)
		}
	}
}

func TestReview305RouteBindingUsesPrefixAtRegistrationTime(t *testing.T) {
	// Catch applying the final /base/self prefix to the earlier registration.
	// Both straightforward positive routes are required, not merely optional.
	registrations := review305BindingRoutesWithin(t, "before-after-self-update")
	want := map[string]string{
		"earlyHandler": "/base/early",
		"lateHandler":  "/base/self/late",
	}
	review305BindingAssertAllowed(t, registrations, want)
	if len(registrations) != len(want) {
		t.Fatalf("missing or duplicate positive route: got %#v, want %#v", registrations, want)
	}
	seen := make(map[string]bool)
	for _, registration := range registrations {
		if seen[registration.Handler] {
			t.Fatalf("duplicate handler displaced a required positive route: %#v", registrations)
		}
		seen[registration.Handler] = true
	}
}

func TestReview305RouteBindingDoesNotLeakNestedShadow(t *testing.T) {
	// Catch letting the inner /inner binding replace either outer use of g.
	registrations := review305BindingRoutesWithin(t, "nested-shadow")
	review305BindingAssertAllowed(t, registrations, map[string]string{
		"outerBeforeHandler": "/outer/before",
		"innerHandler":       "/inner/inside",
		"outerAfterHandler":  "/outer/after",
	})
}

func TestReview305RouteBindingInvalidatesUnresolvedAssignment(t *testing.T) {
	// replacement is a caller-supplied *Group with no known prefix. Neither
	// stale /base/after nor bare /after is established by this source. Therefore
	// no exact afterReplacementHandler route is allowed. This does not claim
	// the runtime group has no route; it demands omission of an unknown prefix.
	registrations := review305BindingRoutesWithin(t, "non-group-assignment")
	review305BindingAssertAllowed(t, registrations, map[string]string{
		"beforeReplacementHandler": "/base/before",
	})
}

func TestReview305RouteBindingDoesNotDropAmbiguousParentPrefix(t *testing.T) {
	// firstChild and secondChild are deliberately unique names. Suppressing
	// duplicate g bindings but re-rooting either child at /child is unsound;
	// uncertainty must propagate, or the correct scoped prefix must be retained.
	registrations := review305BindingRoutesWithin(t, "scoped-descendants")
	review305BindingAssertAllowed(t, registrations, map[string]string{
		"descendantAHandler": "/a/child/x",
		"descendantBHandler": "/b/child/y",
	})
}

func TestReview305RouteBindingDoesNotLeakScopeIntoInlineChain(t *testing.T) {
	// Catch using second's /b prefix for first's inline group, or silently
	// dropping a suppressed g prefix and reporting bare /inline/x or /inline/y.
	registrations := review305BindingRoutesWithin(t, "scoped-inline-chain")
	review305BindingAssertAllowed(t, registrations, map[string]string{
		"inlineAHandler": "/a/inline/x",
		"inlineBHandler": "/b/inline/y",
	})
}
