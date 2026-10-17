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

// Independent PR308 middleware timing witness. Authored framework source is
// parsed as text only and never executed. Each production-helper composition
// runs in one owned, timeout-bounded, awaited child without provider, index,
// repository, framework, network, or model execution.
const review308MiddlewareTimingCaseEnv = "EG_REVIEW308_MIDDLEWARE_TIMING_CASE"
const review308MiddlewareTimingPrefix = "EG_REVIEW308_MIDDLEWARE_TIMING_RESULT="

func review308MiddlewareTimingSource(name string) (string, bool) {
	var setup string
	switch name {
	case "closure-transient-rewrite-then-ordinary":
		setup = `mw := middleware.Recoverer
	use := func() {
		r.Use(mw)
	}
	mw = middleware.StripPrefix("/unused")
	mw = middleware.Recoverer
	use()`
	case "closure-ordinary-only":
		setup = `mw := middleware.Recoverer
	use := func() {
		r.Use(mw)
	}
	mw = middleware.Recoverer
	use()`
	case "switch-fallthrough-may-rewrite":
		setup = `mw := middleware.Recoverer
	switch {
	case chooseRewrite:
		mw = middleware.StripPrefix("/api")
		fallthrough
	default:
		r.Use(mw)
	}`
	case "switch-fallthrough-ordinary-only":
		setup = `mw := middleware.Recoverer
	switch {
	case chooseRewrite:
		mw = middleware.Recoverer
		fallthrough
	default:
		r.Use(mw)
	}`
	default:
		return "", false
	}
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register(chooseRewrite bool) {
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

func TestReview308MiddlewareTimingChild(t *testing.T) {
	name := os.Getenv(review308MiddlewareTimingCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308MiddlewareTimingSource(name)
	if !ok {
		t.Fatalf("unknown authored middleware-timing case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review308MiddlewareTimingPrefix, encoded)
}

func review308MiddlewareTimingWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308MiddlewareTimingChild$", "-test.count=1")
	command.Env = []string{review308MiddlewareTimingCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("middleware-timing helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned middleware-timing child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308MiddlewareTimingPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child middleware-timing result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no middleware-timing result for %s: %s", name, output)
	return nil
}

func review308AssertTimingOrdinaryRoutes(t *testing.T, registrations []goHTTPRouteRegistration) {
	t.Helper()
	want := map[string]string{"accounts": "/accounts", "healthy": "/health"}
	if len(registrations) != len(want) {
		t.Fatalf("proved ordinary middleware must retain both roots: got %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("wrong proved-ordinary route: got %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("missing proved-ordinary routes: got %#v, missing %#v", registrations, want)
	}
}

func TestReview308MiddlewareClosureDefiniteOrdinaryKillControl(t *testing.T) {
	for _, name := range []string{
		"closure-transient-rewrite-then-ordinary",
		"closure-ordinary-only",
	} {
		t.Run(name, func(t *testing.T) {
			review308AssertTimingOrdinaryRoutes(t, review308MiddlewareTimingWithin(t, name))
		})
	}
}

func TestReview308MiddlewareSwitchFallthroughPropagatesRewrite(t *testing.T) {
	registrations := review308MiddlewareTimingWithin(t, "switch-fallthrough-may-rewrite")
	health := 0
	for _, registration := range registrations {
		if registration.Route == "/accounts" && registration.Handler == "accounts" {
			t.Errorf("invented bare /accounts despite a fallthrough StripPrefix path: %#v", registrations)
		}
		if registration.Route == "/health" && registration.Handler == "healthy" {
			health++
		}
	}
	if health != 1 {
		t.Errorf("unrelated genuine /health root must survive exactly once: %#v", registrations)
	}
}

func TestReview308MiddlewareSwitchFallthroughOrdinaryControl(t *testing.T) {
	review308AssertTimingOrdinaryRoutes(t,
		review308MiddlewareTimingWithin(t, "switch-fallthrough-ordinary-only"))
}
