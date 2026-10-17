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

// Independent PR312 closure-descriptor invalidation witness. Authored source
// is parsed as text only and never executed. Each case runs in one owned,
// timeout-bounded, awaited child without provider, index, repository,
// framework, network, or model execution.
const review312DescriptorCaseEnv = "EG_REVIEW312_DESCRIPTOR_CASE"
const review312DescriptorPrefix = "EG_REVIEW312_DESCRIPTOR_RESULT="

func review312DescriptorSource(name string) (string, bool) {
	var middlewareValue string
	switch name {
	case "never-called-overwrite-preserves-rewrite-callback":
		middlewareValue = `middleware.StripPrefix("/api")`
	case "never-called-overwrite-ordinary-control":
		middlewareValue = `middleware.Recoverer`
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
	mw := ` + middlewareValue + `
	use := func() { r.Use(mw) }
	overwrite := func() { use = func() {} }
	_ = overwrite
	use()
	r.Get("/accounts", accounts)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`, true
}

func TestReview312DescriptorInvalidationChild(t *testing.T) {
	name := os.Getenv(review312DescriptorCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review312DescriptorSource(name)
	if !ok {
		t.Fatalf("unknown authored descriptor-invalidation case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review312DescriptorPrefix, encoded)
}

func review312DescriptorWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview312DescriptorInvalidationChild$", "-test.count=1")
	command.Env = []string{review312DescriptorCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("descriptor-invalidation helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned descriptor-invalidation child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review312DescriptorPrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child descriptor-invalidation result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no descriptor-invalidation result for %s: %s", name, output)
	return nil
}

func review312AssertDescriptorRoutes(t *testing.T, registrations []goHTTPRouteRegistration, wantAccounts bool) {
	t.Helper()
	want := map[string]string{"healthy": "/health"}
	if wantAccounts {
		want["accounts"] = "/accounts"
	}
	if len(registrations) != len(want) {
		t.Fatalf("descriptor invalidation produced routes %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("descriptor invalidation produced routes %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("descriptor invalidation produced routes %#v, missing %#v", registrations, want)
	}
}

func TestReview312CapturedWritePreservesPossibleRewriteDescriptor(t *testing.T) {
	review312AssertDescriptorRoutes(t,
		review312DescriptorWithin(t, "never-called-overwrite-preserves-rewrite-callback"), false)
}

func TestReview312CapturedWriteOrdinaryDescriptorControl(t *testing.T) {
	review312AssertDescriptorRoutes(t,
		review312DescriptorWithin(t, "never-called-overwrite-ordinary-control"), true)
}
