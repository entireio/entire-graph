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

// Independent PR308 closure invocation-order witness. Authored framework
// source is parsed as text only and never executed. Each case runs in one
// owned, timeout-bounded, awaited child without provider, index, repository,
// framework, network, or model execution.
const review308ClosureOrderCaseEnv = "EG_REVIEW308_CLOSURE_ORDER_CASE"
const review308ClosureOrderPrefix = "EG_REVIEW308_CLOSURE_ORDER_RESULT="

func review308ClosureOrderSource(name string) (string, bool) {
	var setup string
	switch name {
	case "first-call-uses-ordinary-value":
		setup = `mw := middleware.Recoverer
	use := func() {
		r.Use(mw)
		mw = middleware.StripPrefix("/api")
	}
	use()`
	case "second-call-uses-rewritten-value":
		setup = `mw := middleware.Recoverer
	use := func() {
		r.Use(mw)
		mw = middleware.StripPrefix("/api")
	}
	use()
	use()`
	case "assignment-before-use-restores-ordinary-value":
		setup = `mw := middleware.StripPrefix("/api")
	use := func() {
		mw = middleware.Recoverer
		r.Use(mw)
	}
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

func TestReview308ClosureInvocationOrderChild(t *testing.T) {
	name := os.Getenv(review308ClosureOrderCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308ClosureOrderSource(name)
	if !ok {
		t.Fatalf("unknown authored closure invocation-order case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review308ClosureOrderPrefix, encoded)
}

func review308ClosureOrderWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308ClosureInvocationOrderChild$", "-test.count=1")
	command.Env = []string{review308ClosureOrderCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("closure invocation-order helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned closure invocation-order child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308ClosureOrderPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child closure invocation-order result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no closure invocation-order result for %s: %s", name, output)
	return nil
}

func review308AssertClosureOrderRoutes(t *testing.T, registrations []goHTTPRouteRegistration, wantAccounts bool) {
	t.Helper()
	want := map[string]string{"healthy": "/health"}
	if wantAccounts {
		want["accounts"] = "/accounts"
	}
	if len(registrations) != len(want) {
		t.Fatalf("closure invocation order produced routes %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("closure invocation order produced routes %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("closure invocation order produced routes %#v, missing %#v", registrations, want)
	}
}

func TestReview308ClosureInvocationOrder(t *testing.T) {
	cases := []struct {
		name         string
		wantAccounts bool
	}{
		{name: "first-call-uses-ordinary-value", wantAccounts: true},
		{name: "second-call-uses-rewritten-value", wantAccounts: false},
		{name: "assignment-before-use-restores-ordinary-value", wantAccounts: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			registrations := review308ClosureOrderWithin(t, test.name)
			review308AssertClosureOrderRoutes(t, registrations, test.wantAccounts)
		})
	}
}
