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

// Independent PR312 branch/local-alias closure-write witness. Authored source
// is parsed as text only and never executed. Each case runs in one owned,
// timeout-bounded, awaited child without provider, index, repository,
// framework, network, or model execution.
const review312BranchClosureCaseEnv = "EG_REVIEW312_BRANCH_CLOSURE_CASE"
const review312BranchClosurePrefix = "EG_REVIEW312_BRANCH_CLOSURE_RESULT="

func review312BranchClosureSource(name string) (string, bool) {
	var setup string
	switch name {
	case "branch-may-call-rewrite-before-use":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/api") }
		if choose {
			set()
		}
		r.Use(mw)`
	case "branch-may-call-ordinary-control":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.Recoverer }
		if choose {
			set()
		}
		r.Use(mw)`
	case "branch-use-before-rewrite-no-later-use-control":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/unused") }
		if choose {
			r.Use(mw)
			set()
		}`
	case "loop-alias-carries-rewrite":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/api") }
		for i := 0; i < 2; i++ {
			alias := set
			r.Use(mw)
			alias()
		}`
	case "loop-shadowed-outer-setter-control":
		setup = `mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/unused") }
		for i := 0; i < 2; i++ {
			set := func() { mw = middleware.Recoverer }
			r.Use(mw)
			set()
		}
		set = func() {}
		set()`
	default:
		return "", false
	}
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register(choose bool) {
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

func TestReview312BranchClosureWriteChild(t *testing.T) {
	name := os.Getenv(review312BranchClosureCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review312BranchClosureSource(name)
	if !ok {
		t.Fatalf("unknown authored branch-closure case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review312BranchClosurePrefix, encoded)
}

func review312BranchClosureWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview312BranchClosureWriteChild$", "-test.count=1")
	command.Env = []string{review312BranchClosureCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("branch-closure helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned branch-closure child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review312BranchClosurePrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child branch-closure result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no branch-closure result for %s: %s", name, output)
	return nil
}

func review312AssertBranchClosureRoutes(t *testing.T, registrations []goHTTPRouteRegistration, wantAccounts bool) {
	t.Helper()
	want := map[string]string{"healthy": "/health"}
	if wantAccounts {
		want["accounts"] = "/accounts"
	}
	if len(registrations) != len(want) {
		t.Fatalf("branch closure write produced routes %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("branch closure write produced routes %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("branch closure write produced routes %#v, missing %#v", registrations, want)
	}
}

func TestReview312BranchCarriesLocalClosureRewrite(t *testing.T) {
	review312AssertBranchClosureRoutes(t,
		review312BranchClosureWithin(t, "branch-may-call-rewrite-before-use"), false)
}

func TestReview312BranchClosureOrdinaryControl(t *testing.T) {
	review312AssertBranchClosureRoutes(t,
		review312BranchClosureWithin(t, "branch-may-call-ordinary-control"), true)
}

func TestReview312BranchClosureUseBeforeRewriteTimingControl(t *testing.T) {
	review312AssertBranchClosureRoutes(t,
		review312BranchClosureWithin(t, "branch-use-before-rewrite-no-later-use-control"), true)
}

func TestReview312LoopClosureAliasCarriesRewrite(t *testing.T) {
	review312AssertBranchClosureRoutes(t,
		review312BranchClosureWithin(t, "loop-alias-carries-rewrite"), false)
}

func TestReview312LoopClosureShadowedOuterSetterControl(t *testing.T) {
	review312AssertBranchClosureRoutes(t,
		review312BranchClosureWithin(t, "loop-shadowed-outer-setter-control"), true)
}
