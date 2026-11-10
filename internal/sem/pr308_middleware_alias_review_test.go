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

// Independent PR308 middleware-alias witness. HTTP/router calls below are
// authored source text, never executed. Only the pure production route helper
// runs, in an owned child; no provider, index, repository, or network is used.
// Oracle basis: chi v5.3.2 middleware.StripPrefix delegates to
// net/http.StripPrefix, which rejects a request missing the given prefix.
// Thus /accounts is not a public route of the /api-rewritten router. Root
// independently checked that upstream behavior; this fixture does not execute
// or reimplement the middleware. It permits omission rather than demanding
// synthesis of /api/accounts.
const review308MiddlewareCaseEnv = "EG_REVIEW308_MIDDLEWARE_CASE"
const review308MiddlewarePrefix = "EG_REVIEW308_MIDDLEWARE_RESULT="

func review308MiddlewareSource(name string) (string, bool) {
	var setup string
	switch name {
	case "inline-strip":
		setup = `r.Use(middleware.StripPrefix("/api"))`
	case "aliased-strip":
		setup = `mw := middleware.StripPrefix("/api")
	r.Use(mw)`
	case "ordinary-middleware":
		setup = `r.Use(middleware.Recoverer)`
	default:
		return "", false
	}
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register() {
	r := chi.NewRouter()
	` + setup + `
	r.Get("/accounts", accounts)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`, true
}

func TestReview308MiddlewareAliasChild(t *testing.T) {
	name := os.Getenv(review308MiddlewareCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308MiddlewareSource(name)
	if !ok {
		t.Fatalf("unknown authored middleware case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review308MiddlewarePrefix, encoded)
}

func review308MiddlewareWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308MiddlewareAliasChild$", "-test.count=1")
	command.Env = []string{review308MiddlewareCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	// Only the already-reviewed Windows loader/temp allowance is inherited.
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "PATH", "TEMP", "TMP"} {
			if value, ok := os.LookupEnv(key); ok {
				command.Env = append(command.Env, key+"="+value)
			}
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	// Await/reap the owned child, including after context cancellation. Do not
	// leave a helper goroutine behind if production route analysis hangs.
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("route helper exceeded 5s for %s; owned child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308MiddlewarePrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no middleware result for %s: %s", name, output)
	return nil
}

func review308AssertNoRewrittenBareRoute(t *testing.T, registrations []goHTTPRouteRegistration) {
	t.Helper()
	health := 0
	for _, registration := range registrations {
		if registration.Route == "/accounts" && registration.Handler == "accounts" {
			t.Errorf("invented bare /accounts -> accounts despite /api StripPrefix middleware: %#v", registrations)
		}
		if registration.Route == "/health" && registration.Handler == "healthy" {
			health++
		}
	}
	if health != 1 {
		t.Errorf("unrelated genuine root must survive exactly once: %#v", registrations)
	}
}

func TestReview308InlineStripPrefixSafetyControl(t *testing.T) {
	// The existing direct-call policy is nonvacuous: it withholds the rewritten
	// route while retaining a separate root route.
	review308AssertNoRewrittenBareRoute(t, review308MiddlewareWithin(t, "inline-strip"))
}

func TestReview308AliasedStripPrefixDoesNotInventBareRoute(t *testing.T) {
	// Break caught: recognizing only an inline CallExpr passed to Use while
	// treating the identical middleware stored in mw as non-rewriting.
	review308AssertNoRewrittenBareRoute(t, review308MiddlewareWithin(t, "aliased-strip"))
}

func TestReview308OrdinaryMiddlewareKeepsRootRouteControl(t *testing.T) {
	// Dropping every router with Use would satisfy the safety tests but lose
	// this genuine registration. Recoverer does not rewrite the request path.
	registrations := review308MiddlewareWithin(t, "ordinary-middleware")
	want := map[string]string{"accounts": "/accounts", "healthy": "/health"}
	if len(registrations) != len(want) {
		t.Fatalf("ordinary middleware must retain both root routes: got %#v, want %#v", registrations, want)
	}
	seen := map[string]bool{}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || seen[registration.Handler] || route != registration.Route {
			t.Fatalf("wrong or duplicate root route: got %#v, want %#v", registrations, want)
		}
		seen[registration.Handler] = true
	}
}
