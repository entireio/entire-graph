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

// Independent PR312 descriptor-only join witness. Authored source is parsed as
// text and never executed. Each case runs in one owned, timeout-bounded,
// awaited child without provider, index, repository, framework, network, or
// model execution.
const review312DescriptorOnlyCaseEnv = "EG_REVIEW312_DESCRIPTOR_ONLY_CASE"
const review312DescriptorOnlyPrefix = "EG_REVIEW312_DESCRIPTOR_ONLY_RESULT="

func review312DescriptorOnlySource(name string) (string, bool) {
	var optionalBranch string
	switch name {
	case "with-empty-branch":
		optionalBranch = `if choose {}`
	case "without-branch":
		optionalBranch = ``
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
	mw := middleware.Recoverer
	var slot any = middleware.Recoverer
	set := func() { slot = middleware.StripPrefix("/api") }
	slot = func() { r.Use(mw) }
	` + optionalBranch + `
	mw = middleware.StripPrefix("/unused")
	set = func() {}
	set()
	slot = nil
	_ = slot
	r.Get("/accounts", accounts)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`, true
}

func TestReview312DescriptorOnlyJoinChild(t *testing.T) {
	name := os.Getenv(review312DescriptorOnlyCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review312DescriptorOnlySource(name)
	if !ok {
		t.Fatalf("unknown descriptor-only case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review312DescriptorOnlyPrefix, encoded)
}

func review312DescriptorOnlyWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview312DescriptorOnlyJoinChild$", "-test.count=1")
	command.Env = []string{review312DescriptorOnlyCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("descriptor-only helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned descriptor-only child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review312DescriptorOnlyPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child descriptor-only result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no descriptor-only result for %s: %s", name, output)
	return nil
}

func review312AssertDescriptorOnlyRoutes(t *testing.T, registrations []goHTTPRouteRegistration) {
	t.Helper()
	want := map[string]string{"accounts": "/accounts", "healthy": "/health"}
	if len(registrations) != len(want) {
		t.Fatalf("descriptor-only join produced routes %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("descriptor-only join produced routes %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("descriptor-only join produced routes %#v, missing %#v", registrations, want)
	}
}

func TestReview312DescriptorOnlyJoinEmptyBranchControl(t *testing.T) {
	review312AssertDescriptorOnlyRoutes(t,
		review312DescriptorOnlyWithin(t, "with-empty-branch"))
}

func TestReview312DescriptorOnlyJoinNoBranchControl(t *testing.T) {
	review312AssertDescriptorOnlyRoutes(t,
		review312DescriptorOnlyWithin(t, "without-branch"))
}
