package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Independent PR305 fixtures. Copy into internal/sem in an isolated export.
// Every potentially hanging invocation runs in a test-owned subprocess. These
// fixtures do not invoke a provider, Snapshot, Run, a parser, or a real repository.
// They have been authored, not executed, by this reviewer.
const review305ChildCaseEnv = "EG_REVIEW305_ROUTE_CASE"
const review305ResultPrefix = "EG_REVIEW305_ROUTE_RESULT="

type review305RouteFixture struct {
	content   string
	constants map[string]string
}

func review305Fixture(name string) (review305RouteFixture, bool) {
	switch name {
	case "direct-and-constant":
		return review305RouteFixture{
			content: `package routes
func register(root *Router) {
	http.HandleFunc("/health", health)
	api := root.Group(apiPrefix)
	api.GET(usersPath, listUsers)
}
`,
			constants: map[string]string{"apiPrefix": "/api", "usersPath": "/users"},
		}, true
	case "reverse-chain":
		// Package-level initialization permits references to later variables in
		// Go. The textual matches form a unique-name, acyclic chain in the order
		// least helpful to this helper's forward propagation pass.
		var content strings.Builder
		content.WriteString("package routes\nvar root *Router\n")
		for i := 32; i >= 1; i-- {
			parent := "root"
			if i > 1 {
				parent = fmt.Sprintf("g%d", i-1)
			}
			fmt.Fprintf(&content, "var g%d = %s.Group(\"/p%d\")\n", i, parent, i)
		}
		content.WriteString("func register() { g32.GET(\"/leaf\", chainHandler) }\n")
		return review305RouteFixture{content: content.String()}, true
	case "rebound-scopes":
		return review305RouteFixture{content: `package routes
func first(root *Router) {
	g := root.Group("/a")
	g.GET("/x", handlerA)
}
func second(root *Router) {
	g := root.Group("/b")
	g.GET("/y", handlerB)
}
func handlerA() {}
func handlerB() {}
`}, true
	case "reset-then-self":
		return review305RouteFixture{content: `package routes
func register(root *Router) {
	g := root.Group("/base")
	g = g.Group("/self")
	g.GET("/leaf", reboundHandler)
}
`}, true
	case "self-cycle":
		return review305RouteFixture{content: `package routes
func register(g *Group) {
	g = g.Group("/self")
	g.GET("/leaf", selfHandler)
}
`}, true
	case "mutual-cycle":
		return review305RouteFixture{content: `package routes
func register(a, b *Group) {
	a = b.Group("/a")
	b = a.Group("/b")
	a.GET("/leaf", cycleHandler)
}
`}, true
	default:
		return review305RouteFixture{}, false
	}
}

// This entry is selected only inside the owned child process. A normal test
// selection skips it. The actual production helper is called directly.
func TestReview305RouteFixpointChild(t *testing.T) {
	name := os.Getenv(review305ChildCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	fixture, ok := review305Fixture(name)
	if !ok {
		t.Fatalf("unknown authored fixture %q", name)
	}
	registrations := goHTTPRouteRegistrations(fixture.content, fixture.constants)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review305ResultPrefix, encoded)
}

func review305RoutesWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview305RouteFixpointChild$", "-test.count=1")
	// Do not inherit credentials, product/session configuration, or a helper
	// mode from the runner. The absolute executable path needs no PATH.
	command.Env = []string{review305ChildCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	command.WaitDelay = 250 * time.Millisecond
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("pure route helper did not finish within 2s for %s; owned child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned route-helper child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review305ResultPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no route result for %s: %s", name, output)
	return nil
}

func TestReview305RouteFixpointPreservesExpectedRoutes(t *testing.T) {
	var chain strings.Builder
	for i := 1; i <= 32; i++ {
		fmt.Fprintf(&chain, "/p%d", i)
	}
	chain.WriteString("/leaf")
	for _, fixture := range []struct {
		name string
		want map[string]string
	}{
		{"direct-and-constant", map[string]string{"health": "/health", "listUsers": "/api/users"}},
		{"reverse-chain", map[string]string{"chainHandler": chain.String()}},
		{"reset-then-self", map[string]string{"reboundHandler": "/base/self/leaf"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			registrations := review305RoutesWithin(t, fixture.name)
			if len(registrations) != len(fixture.want) {
				t.Fatalf("wrong route count: got %#v, want %#v", registrations, fixture.want)
			}
			seen := make(map[string]bool)
			for _, registration := range registrations {
				want, exists := fixture.want[registration.Handler]
				if !exists || seen[registration.Handler] || registration.Route != want {
					t.Fatalf("unexpected route: got %#v, want %#v", registrations, fixture.want)
				}
				seen[registration.Handler] = true
			}
		})
	}
}

func TestReview305RouteFixpointTerminates(t *testing.T) {
	for _, name := range []string{"rebound-scopes", "self-cycle", "mutual-cycle"} {
		t.Run(name, func(t *testing.T) {
			registrations := review305RoutesWithin(t, name)
			// Termination alone has no oracle for arbitrary repeated prefixes.
			// Log, but do not bless the cap-dependent output as a correct route.
			t.Logf("terminated with registrations: %#v", registrations)
		})
	}
}

func TestReview305RouteFixpointDoesNotInventCrossScopeRoute(t *testing.T) {
	registrations := review305RoutesWithin(t, "rebound-scopes")
	allowed := map[string]string{"handlerA": "/a/x", "handlerB": "/b/y"}
	for _, registration := range registrations {
		want, exists := allowed[registration.Handler]
		if !exists || registration.Route != want {
			t.Fatalf("fabricated cross-scope route %#v; permitted scoped routes are %#v (omission is also acceptable)", registration, allowed)
		}
	}
	// Intentional safety oracle: accurately resolve or omit an uncertain route.
	// It does not demand a new scope resolver. The old head hangs here; b102's
	// cap is predicted to return /b/x -> handlerA. That would expose the inherited
	// file-global-name limitation, not regress a previously terminating result.
}
