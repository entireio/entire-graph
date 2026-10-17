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

// Independent PR308 escaped-capture-write witness. Authored framework source
// is parsed as text only and never executed. Each case runs in one owned,
// timeout-bounded, awaited child without provider, index, repository,
// framework, network, or model execution.
const review308EscapedWriteCaseEnv = "EG_REVIEW308_ESCAPED_WRITE_CASE"
const review308EscapedWritePrefix = "EG_REVIEW308_ESCAPED_WRITE_RESULT="

func review308EscapedWriteSource(name string) (string, bool) {
	var setup string
	switch name {
	case "escaped-write-reads-future-rewrite":
		setup = `src := middleware.Recoverer
		mw := middleware.Recoverer
		cb := func() { mw = src }
		accept(cb)
		src = middleware.StripPrefix("/api")
		accepted()
		r.Use(mw)`
	case "escaped-direct-ordinary-write":
		setup = `mw := middleware.Recoverer
		cb := func() { mw = middleware.Recoverer }
		accept(cb)
		accepted()
		r.Use(mw)`
	case "escaped-rewrite-after-ordinary-overwrite":
		setup = `mw := middleware.Recoverer
		cb := func() { mw = middleware.StripPrefix("/api") }
		accept(cb)
		mw = middleware.Recoverer
		accepted()
		r.Use(mw)`
	default:
		return "", false
	}
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
var accepted func()
func accept(cb func()) { accepted = cb }
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

func TestReview308EscapedWriteChild(t *testing.T) {
	name := os.Getenv(review308EscapedWriteCaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308EscapedWriteSource(name)
	if !ok {
		t.Fatalf("unknown authored escaped-write case %q", name)
	}
	registrations := goHTTPRouteRegistrations(source, nil)
	encoded, err := json.Marshal(registrations)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", review308EscapedWritePrefix, encoded)
}

func review308EscapedWriteWithin(t *testing.T, name string) []goHTTPRouteRegistration {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308EscapedWriteChild$", "-test.count=1")
	command.Env = []string{review308EscapedWriteCaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
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
		t.Fatalf("escaped-write helper exceeded 5s for %s; child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned escaped-write child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308EscapedWritePrefix)
		if !ok {
			continue
		}
		var registrations []goHTTPRouteRegistration
		if err := json.Unmarshal([]byte(encoded), &registrations); err != nil {
			t.Fatalf("invalid owned-child escaped-write result: %v", err)
		}
		return registrations
	}
	t.Fatalf("owned child emitted no escaped-write result for %s: %s", name, output)
	return nil
}

func review308AssertEscapedWriteRoutes(t *testing.T, registrations []goHTTPRouteRegistration, wantAccounts bool) {
	t.Helper()
	want := map[string]string{"healthy": "/health"}
	if wantAccounts {
		want["accounts"] = "/accounts"
	}
	if len(registrations) != len(want) {
		t.Fatalf("escaped capture write produced routes %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		route, ok := want[registration.Handler]
		if !ok || route != registration.Route {
			t.Fatalf("escaped capture write produced routes %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("escaped capture write produced routes %#v, missing %#v", registrations, want)
	}
}

func TestReview308EscapedCaptureWriteTracksFutureSource(t *testing.T) {
	review308AssertEscapedWriteRoutes(t,
		review308EscapedWriteWithin(t, "escaped-write-reads-future-rewrite"), false)
}

func TestReview308EscapedCaptureWriteOrdinaryControl(t *testing.T) {
	review308AssertEscapedWriteRoutes(t,
		review308EscapedWriteWithin(t, "escaped-direct-ordinary-write"), true)
}

func TestReview308EscapedCaptureWriteSurvivesOverwriteBeforeInvocation(t *testing.T) {
	review308AssertEscapedWriteRoutes(t,
		review308EscapedWriteWithin(t, "escaped-rewrite-after-ordinary-overwrite"), false)
}
