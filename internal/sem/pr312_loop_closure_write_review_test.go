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

// Independent PR312 loop-carried closure-write witness. Authored framework
// source is parsed as text only and never executed. Each case runs in one
// owned, timeout-bounded, awaited child without provider, index, repository,
// framework, network, or model execution.
const review312LoopClosureCaseEnv = "EG_REVIEW312_LOOP_CLOSURE_CASE"
const review312LoopClosurePrefix = "EG_REVIEW312_LOOP_CLOSURE_RESULT="

func review312LoopClosureSource(name string) (string, bool) {
	var setup string
	switch name {
	case "loop-carried-rewrite":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/api") }
		for i := 0; i < 2; i++ {
			r.Use(mw)
			set()
		}`
	case "loop-carried-ordinary-control":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.Recoverer }
		for i := 0; i < 2; i++ {
			r.Use(mw)
			set()
		}`
	case "loop-definite-ordinary-before-use-control":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/unused") }
		for i := 0; i < 2; i++ {
			mw = middleware.Recoverer
			r.Use(mw)
			set()
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
func register() {
	r := chi.NewRouter()
	` + setup + `
	r.Get("/loopstrip", loopHandler)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func loopHandler(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`, true
}

func TestReview312LoopClosureWriteChild(t *testing.T) {
	name := os.Getenv(review312LoopClosureCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review312LoopClosureSource(name)
	if !ok {
		t.Fatalf("unknown authored loop-closure case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review312LoopClosurePrefix, encoded)
}

func review312LoopClosureWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview312LoopClosureWriteChild$", "-test.count=1")
	command.Env = []string{review312LoopClosureCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("loop-closure helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned loop-closure child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review312LoopClosurePrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child loop-closure result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no loop-closure result for %s: %s", name, output)
	return nil
}

func review312AssertLoopClosureRoutes(t *testing.T, registrations []goHTTPRouteRegistration, wantLoop bool) {
	t.Helper()
	want := map[string]string{"healthy": "/health"}
	if wantLoop {
		want["loopHandler"] = "/loopstrip"
	}
	if len(registrations) != len(want) {
		t.Fatalf("loop-carried closure write produced routes %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("loop-carried closure write produced routes %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("loop-carried closure write produced routes %#v, missing %#v", registrations, want)
	}
}

func TestReview312LoopCarriesLocalClosureRewrite(t *testing.T) {
	review312AssertLoopClosureRoutes(t,
		review312LoopClosureWithin(t, "loop-carried-rewrite"), false)
}

func TestReview312LoopClosureOrdinaryControl(t *testing.T) {
	review312AssertLoopClosureRoutes(t,
		review312LoopClosureWithin(t, "loop-carried-ordinary-control"), true)
}

func TestReview312LoopClosureDefiniteOrdinaryBeforeUseControl(t *testing.T) {
	review312AssertLoopClosureRoutes(t,
		review312LoopClosureWithin(t, "loop-definite-ordinary-before-use-control"), true)
}
