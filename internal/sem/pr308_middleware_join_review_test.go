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

// Independent PR308 control-flow-join witness. The authored Go snippets are
// parsed as text only; none of their HTTP or middleware calls execute. Each
// production-helper composition runs in one owned, bounded child. No provider,
// index, repository, framework, network, or model is invoked.
const review308MiddlewareJoinCaseEnv = "EG_REVIEW308_MIDDLEWARE_JOIN_CASE"
const review308MiddlewareJoinPrefix = "EG_REVIEW308_MIDDLEWARE_JOIN_RESULT="

func review308MiddlewareJoinSource(name string) (string, bool) {
	var setup string
	switch name {
	case "branch-both-rewrite":
		setup = `mw := middleware.StripPrefix("/api")
	if chooseRewrite {
		mw = middleware.StripPrefix("/v2")
	}
	r.Use(mw)`
	case "branch-may-rewrite":
		setup = `mw := middleware.Recoverer
	if chooseRewrite {
		mw = middleware.StripPrefix("/api")
	}
	r.Use(mw)`
	case "loop-both-rewrite":
		setup = `mw := middleware.StripPrefix("/api")
	for chooseRewrite {
		mw = middleware.StripPrefix("/v2")
		break
	}
	r.Use(mw)`
	case "closure-both-rewrite":
		setup = `mw := middleware.StripPrefix("/api")
	func() {
		mw = middleware.StripPrefix("/v2")
	}()
	r.Use(mw)`
	case "closure-future-rewrite":
		setup = `mw := middleware.Recoverer
	use := func() {
		r.Use(mw)
	}
	mw = middleware.StripPrefix("/api")
	use()`
	case "branch-ordinary-only":
		setup = `mw := middleware.Recoverer
	if chooseRewrite {
		mw = middleware.Recoverer
	}
	r.Use(mw)`
	case "closure-future-ordinary":
		setup = `mw := middleware.Recoverer
	use := func() {
		r.Use(mw)
	}
	mw = middleware.Recoverer
	use()`
	case "closure-inner-shadow-ordinary":
		setup = `mw := middleware.StripPrefix("/outer")
	use := func() {
		mw := middleware.Recoverer
		r.Use(mw)
	}
	_ = mw
	use()`
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

func TestReview308MiddlewareJoinChild(t *testing.T) {
	name := os.Getenv(review308MiddlewareJoinCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308MiddlewareJoinSource(name)
	if !ok {
		t.Fatalf("unknown authored middleware-join case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review308MiddlewareJoinPrefix, encoded)
}

func review308MiddlewareJoinWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308MiddlewareJoinChild$", "-test.count=1")
	command.Env = []string{review308MiddlewareJoinCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "PATH", "TEMP", "TMP"} {
			if value, ok := os.LookupEnv(key); ok {
				command.Env = append(command.Env, key+"="+value)
			}
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	// CombinedOutput awaits and reaps this one owned child after success,
	// failure, or context cancellation. The child parses authored strings only
	// and starts no descendants.
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("middleware-join helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned middleware-join child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308MiddlewareJoinPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child middleware-join result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no middleware-join result for %s: %s", name, output)
	return nil
}

func review308AssertJoinOmitsBareRoute(t *testing.T, registrations []goHTTPRouteRegistration) {
	t.Helper()
	health := 0
	for _, registration := range registrations {
		if registration.Route == "/accounts" && registration.Handler == "accounts" {
			t.Errorf("invented bare /accounts despite a possible StripPrefix path: %#v", registrations)
		}
		if registration.Route == "/health" && registration.Handler == "healthy" {
			health++
		}
	}
	if health != 1 {
		t.Errorf("unrelated genuine /health root must survive exactly once: %#v", registrations)
	}
}

func TestReview308MiddlewareJoinConservativelyOmitsPossibleBareRoute(t *testing.T) {
	// The first, loop, and closure cases rewrite on every feasible value of mw.
	// The mixed case follows the task's explicit policy permitting conservative
	// omission when a rewritten public route is uncertain.
	for _, name := range []string{
		"branch-both-rewrite",
		"branch-may-rewrite",
		"loop-both-rewrite",
		"closure-both-rewrite",
		"closure-future-rewrite",
	} {
		t.Run(name, func(t *testing.T) {
			review308AssertJoinOmitsBareRoute(t, review308MiddlewareJoinWithin(t, name))
		})
	}
}

func TestReview308MiddlewareJoinOrdinaryOnlyControl(t *testing.T) {
	for _, name := range []string{
		"branch-ordinary-only",
		"closure-future-ordinary",
		"closure-inner-shadow-ordinary",
	} {
		t.Run(name, func(t *testing.T) {
			registrations := review308MiddlewareJoinWithin(t, name)
			want := map[string]string{"accounts": "/accounts", "healthy": "/health"}
			if len(registrations) != len(want) {
				t.Fatalf("ordinary-only middleware flow must retain both roots: got %#v, want %#v", registrations, want)
			}
			for _, registration := range registrations {
				route, ok := want[registration.Handler]
				if !ok || route != registration.Route {
					t.Fatalf("wrong ordinary-only route: got %#v, want %#v", registrations, want)
				}
				delete(want, registration.Handler)
			}
			if len(want) != 0 {
				t.Fatalf("missing ordinary-only routes: got %#v, missing %#v", registrations, want)
			}
		})
	}
}
