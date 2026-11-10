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

// Independent PR308 cases. The authored Go is parsed as text only: its HTTP
// calls are never executed. Each production-helper composition runs in an
// owned, bounded child. No provider, index, repository, or network is used.
const review308CaseEnv = "EG_REVIEW308_ROUTE_SCOPE_CASE"
const review308OutputPrefix = "EG_REVIEW308_ROUTE_SCOPE_RESULT="

type review308Outcome struct {
	Registrations []goHTTPRouteRegistration
	BeforeMask    []string
	AfterMask     []string
}

func review308Source(name string) (string, bool) {
	switch name {
	case "typed-function-literal":
		return `package routes
import "net/http"
func register() {
	sub := http.NewServeMux()
	func(m *http.ServeMux) { m.HandleFunc("/x", h) }(sub)
	http.Handle("/api/", http.StripPrefix("/api", sub))
	http.HandleFunc("/health", healthy)
}
func h(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`, true
	case "client-name-other-scope":
		return `package routes
import "net/http"
type opaque interface { Get(string) }
func fetch(c *http.Client) {
	c.Get("/client")
}
func register(c opaque) {
	c.Get("/unknown")
}
`, true
	case "genuine-root":
		return `package routes
import "net/http"
func register() {
	http.HandleFunc("/root", h)
}
func h(http.ResponseWriter, *http.Request) {}
`, true
	case "local-client":
		return `package routes
import "net/http"
type opaque interface { Get(string) }
func fetch(u opaque) {
	c := &http.Client{}
	c.Get("/client")
	u.Get("/unknown-control")
}
`, true
	default:
		return "", false
	}
}

func TestReview308RouteScopeChild(t *testing.T) {
	name := os.Getenv(review308CaseEnv)
	if name == "" {
		t.Skip("test-owned subprocess entry")
	}
	source, ok := review308Source(name)
	if !ok {
		t.Fatalf("unknown authored case %q", name)
	}
	// This is the real masks-to-literal-consumer composition used by the route
	// pass. It is not a provider/snapshot integration or an HTTP execution.
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
	fmt.Printf("%s%s\n", review308OutputPrefix, encoded)
}

func review308Within(t *testing.T, name string) review308Outcome {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestReview308RouteScopeChild$", "-test.count=1")
	command.Env = []string{review308CaseEnv + "=" + name, "GOMAXPROCS=1", "GOGC=50"}
	// Match the reviewed Windows loader adaptation, without inheriting the
	// runner's credentials, product configuration, or other helper modes.
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "PATH", "TEMP", "TMP"} {
			if value, ok := os.LookupEnv(key); ok {
				command.Env = append(command.Env, key+"="+value)
			}
		}
	}
	command.WaitDelay = 250 * time.Millisecond
	// CombinedOutput awaits/reaps this owned child. No goroutine is abandoned
	// if a production helper hangs: context cancellation kills the child.
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("helper exceeded 5s for %s; owned child killed and waited: %v", name, ctx.Err())
	}
	if err != nil {
		t.Fatalf("owned child failed for %s: %v\n%s", name, err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		encoded, ok := strings.CutPrefix(line, review308OutputPrefix)
		if !ok {
			continue
		}
		var result review308Outcome
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatalf("invalid owned-child result: %v", err)
		}
		return result
	}
	t.Fatalf("owned child emitted no result for %s: %s", name, output)
	return review308Outcome{}
}

func review308HasLiteral(routes []string, want string) bool {
	for _, route := range routes {
		if route == want {
			return true
		}
	}
	return false
}

func TestReview308TypedFuncLiteralDoesNotInventRootRoute(t *testing.T) {
	// Break caught: classifying m by its ServeMux type as a root, without the
	// caller's prefixed sub instance. Omission is allowed; bare /x -> h is not.
	got := review308Within(t, "typed-function-literal")
	health := 0
	for _, registration := range got.Registrations {
		if registration.Route == "/x" && registration.Handler == "h" {
			t.Errorf("fabricated bare /x -> h from a function-literal parameter passed a StripPrefix-wrapped mux: %#v", got.Registrations)
		}
		if registration.Route == "/health" && registration.Handler == "healthy" {
			health++
		}
	}
	if health != 1 {
		t.Errorf("unrelated genuine root must survive exactly once; got %#v", got.Registrations)
	}
}

func TestReview308ClientNameDoesNotMaskOtherScope(t *testing.T) {
	// Break caught: file-global http.Client name classification masking c in
	// register, where its type is unknown to the route pass. The existing
	// fallback contract retains unknown receivers; it does not certify them.
	got := review308Within(t, "client-name-other-scope")
	if !review308HasLiteral(got.BeforeMask, "/client") || !review308HasLiteral(got.BeforeMask, "/unknown") {
		t.Fatalf("authored source did not reach the unmasked literal consumer: %v", got.BeforeMask)
	}
	if review308HasLiteral(got.AfterMask, "/client") {
		t.Errorf("known HTTP client literal must be suppressed: %v", got.AfterMask)
	}
	if !review308HasLiteral(got.AfterMask, "/unknown") {
		t.Errorf("an unrelated scope's unknown receiver was suppressed by the client's name: %v", got.AfterMask)
	}
}

func TestReview308GenuineRootRegistrationControl(t *testing.T) {
	got := review308Within(t, "genuine-root")
	if len(got.Registrations) != 1 || got.Registrations[0].Route != "/root" || got.Registrations[0].Handler != "h" {
		t.Fatalf("genuine DefaultServeMux root registration lost or fabricated: %#v", got.Registrations)
	}
}

func TestReview308LocalHTTPClientMaskControl(t *testing.T) {
	got := review308Within(t, "local-client")
	if !review308HasLiteral(got.BeforeMask, "/client") || !review308HasLiteral(got.BeforeMask, "/unknown-control") {
		t.Fatalf("authored control did not exercise literal masking: %v", got.BeforeMask)
	}
	if len(got.AfterMask) != 1 || got.AfterMask[0] != "/unknown-control" {
		t.Fatalf("suppress the local HTTP client only, preserving the unknown receiver: %v", got.AfterMask)
	}
}
