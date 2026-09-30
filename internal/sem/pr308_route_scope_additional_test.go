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

const review308FocusedCaseEnv = "EG_REVIEW308_FOCUSED_CASE"
const review308FocusedPrefix = "EG_REVIEW308_FOCUSED_RESULT="

func review308FocusedSource(name string) (string, bool) {
	switch name {
	case "seeded-route-closure":
		return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func register() {
	r := chi.NewRouter()
	r.Route("/api", func(sub chi.Router) {
		sub.Get("/accounts", accounts)
	})
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
`, true
	case "client-shadow-restoration":
		return `package routes
import "net/http"
type opaque interface { Get(string) }
func fetch(u opaque) {
	c := &http.Client{}
	c.Get("/before")
	{
		c := u
		c.Get("/unknown-shadow")
	}
	c.Get("/after")
}
`, true
	case "in-package-client-alias-assignment":
		return `package http
type opaque interface { Get(string) }
func fetch(c *Client) {
	alias := c
	alias.Get("/client-alias")
	var assigned *Client
	assigned = alias
	assigned.Get("/client-assigned")
}
func register(c opaque) {
	c.Get("/unknown")
}
`, true
	case "middleware-overwritten":
		return review308FocusedMiddleware(`mw := middleware.StripPrefix("/api")
	mw = middleware.Recoverer
	r.Use(mw)`), true
	case "middleware-inner-shadow":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	{
		mw := middleware.StripPrefix("/api")
		_ = mw
	}
	r.Use(mw)`), true
	case "middleware-outer-restored":
		return review308FocusedMiddleware(`mw := middleware.StripPrefix("/api")
	{
		mw := middleware.Recoverer
		r.Use(mw)
	}
	r.Use(mw)`), true
	default:
		return "", false
	}
}

func review308FocusedMiddleware(setup string) string {
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
`
}

func TestReview308FocusedChild(t *testing.T) {
	name := os.Getenv(review308FocusedCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308FocusedSource(name)
	if !ok {
		t.Fatalf("unknown focused case %q", name)
	}
	detail := goRouteRegistrationsDetailed(source, nil)
	result := review308Outcome{
		Registrations: detail.regs,
		BeforeMask:    routeLiteralsWithConstants(source, nil),
		AfterMask:     routeLiteralsWithConstants(goRouteMaskContent(source, detail.masks), nil),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review308FocusedPrefix, encoded)
}

func review308FocusedWithin(t *testing.T, name string) review308Outcome {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308FocusedChild$", "-test.count=1")
	command.Env = []string{review308FocusedCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "PATH", "TEMP", "TMP"} {
			if value, ok := os.LookupEnv(key); ok {
				command.Env = append(command.Env, key+"="+value)
			}
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("focused helper exceeded 5s for %s; owned child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned focused child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308FocusedPrefix)
		if !ok {
			continue
		}
		var result review308Outcome
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatalf("invalid owned-child result: %v", err)
		}
		return result
	}
	t.Fatalf("owned focused child emitted no result for %s: %s", name, output)
	return review308Outcome{}
}

func TestReview308FrameworkSeededClosureKeepsComposedRoute(t *testing.T) {
	got := review308FocusedWithin(t, "seeded-route-closure")
	if len(got.Registrations) != 1 || got.Registrations[0].Route != "/api/accounts" || got.Registrations[0].Handler != "accounts" {
		t.Fatalf("framework-seeded closure lost its composed route: %#v", got.Registrations)
	}
}

func TestReview308ClientMaskFollowsLexicalShadowAndRestoration(t *testing.T) {
	got := review308FocusedWithin(t, "client-shadow-restoration")
	for _, want := range []string{"/before", "/unknown-shadow", "/after"} {
		if !review308HasLiteral(got.BeforeMask, want) {
			t.Fatalf("authored literal %s did not reach the unmasked consumer: %v", want, got.BeforeMask)
		}
	}
	if len(got.AfterMask) != 1 || got.AfterMask[0] != "/unknown-shadow" {
		t.Fatalf("client masking did not follow lexical shadow/restoration: %v", got.AfterMask)
	}
}

func TestReview308InPackageClientFactsFollowAliasAndAssignment(t *testing.T) {
	got := review308FocusedWithin(t, "in-package-client-alias-assignment")
	for _, want := range []string{"/client-alias", "/client-assigned", "/unknown"} {
		if !review308HasLiteral(got.BeforeMask, want) {
			t.Fatalf("authored literal %s did not reach the unmasked consumer: %v", want, got.BeforeMask)
		}
	}
	if len(got.AfterMask) != 1 || got.AfterMask[0] != "/unknown" {
		t.Fatalf("in-package client facts did not follow alias/assignment without masking the unknown receiver: %v", got.AfterMask)
	}
}

func TestReview308MiddlewareOverwriteKeepsOrdinaryRoute(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-overwritten")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareInnerShadowDoesNotLeak(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-inner-shadow")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareOuterAliasRestoredAfterShadow(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-outer-restored")
	review308AssertNoRewrittenBareRoute(t, got.Registrations)
}

func review308AssertOrdinaryMiddlewareRoutes(t *testing.T, registrations []goHTTPRouteRegistration) {
	t.Helper()
	want := map[string]string{"accounts": "/accounts", "healthy": "/health"}
	if len(registrations) != len(want) {
		t.Fatalf("ordinary middleware must retain both root routes: got %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		if want[registration.Handler] != registration.Route {
			t.Fatalf("wrong root route after ordinary middleware: got %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("missing ordinary-middleware routes: got %#v, missing %#v", registrations, want)
	}
}
