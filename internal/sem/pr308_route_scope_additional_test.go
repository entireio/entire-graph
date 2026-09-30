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
	case "closure-router-write-never-called":
		return `package routes
func register(root *Router) {
	g := root.Group("/a")
	rewrite := func() { g = root.Group("/b") }
	_ = rewrite
	g.GET("/after", afterHandler)
	stable := root.Group("/s")
	stable.GET("/in", inHandler)
}
func capturedRoot(root *Router) {
	rewrite := func() { root = root.Group("/b") }
	_ = rewrite
	root.GET("/root-after", rootAfterHandler)
}
`, true
	case "one-arg-explicit-unknown-mask-control":
		return `package routes
import "github.com/go-chi/chi/v5"
type opaque interface { Get(string) }
func makeOpaque() opaque { return nil }
func register(untracked opaque) {
	known := chi.NewRouter()
	explicit := makeOpaque()
	explicit.Get("/explicit-unknown")
	alias := untracked
	untracked.Get("/untracked")
	alias.Get("/parameter-alias")
	{
		untracked := makeOpaque()
		untracked.Get("/shadow-explicit-unknown")
	}
	known.Get("/known")
}
`, true
	case "one-arg-explicit-unknown-mask-in-package-control":
		source, _ := review308FocusedSource("one-arg-explicit-unknown-mask-control")
		return strings.Replace(source, "package routes", "package http", 1), true
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
	case "middleware-loop-future-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	for i := 0; i < 2; i++ {
		r.Use(mw)
		mw = middleware.StripPrefix("/api")
	}`), true
	case "middleware-loop-ordinary-only":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	for i := 0; i < 2; i++ {
		r.Use(mw)
		mw = middleware.Recoverer
	}`), true
	case "middleware-loop-later-unused-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	for i := 0; i < 2; i++ {
		r.Use(mw)
		mw = middleware.Recoverer
	}
	mw = middleware.StripPrefix("/unused")`), true
	case "middleware-loop-copied-ordinary-alias":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	alias := mw
	for i := 0; i < 2; i++ {
		r.Use(alias)
		mw = middleware.StripPrefix("/unused")
	}`), true
	case "middleware-loop-entry-alias":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	for i := 0; i < 2; i++ {
		alias := mw
		r.Use(alias)
		mw = middleware.StripPrefix("/api")
	}`), true
	case "middleware-loop-entry-alias-escapes":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	alias := mw
	for i := 0; i < 2; i++ {
		alias = mw
		mw = middleware.StripPrefix("/api")
	}
	r.Use(alias)`), true
	case "middleware-loop-definite-ordinary-before-use":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	for i := 0; i < 2; i++ {
		mw = middleware.Recoverer
		r.Use(mw)
		mw = middleware.StripPrefix("/unused")
	}`), true
	case "middleware-branch-use-before-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	if true {
		r.Use(mw)
		mw = middleware.StripPrefix("/unused")
	}`), true
	case "middleware-closure-branch-rewrite-outer-use":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() {
		if true {
			mw = middleware.StripPrefix("/api")
		}
	}
	use()
	r.Use(mw)`), true
	case "middleware-closure-branch-rewrite-inner-use":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	func() {
		if true {
			mw = middleware.StripPrefix("/api")
		}
		r.Use(mw)
	}()`), true
	case "middleware-closure-branch-ordinary-only":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	func() {
		if true {
			mw = middleware.Recoverer
		}
		r.Use(mw)
	}()
	r.Use(mw)`), true
	case "middleware-closure-call-during-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	mw = middleware.StripPrefix("/api")
	use()
	mw = middleware.Recoverer`), true
	case "middleware-closure-call-before-unused-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	use()
	mw = middleware.StripPrefix("/unused")`), true
	case "middleware-closure-alias-transient-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	alias := use
	mw = middleware.StripPrefix("/unused")
	mw = middleware.Recoverer
	alias()`), true
	case "middleware-closure-overwrite-kills-descriptor":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	use = func() {}
	mw = middleware.StripPrefix("/unused")
	use()`), true
	case "middleware-closure-shadow-keeps-outer-descriptor":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	{
		use := func() {}
		mw = middleware.StripPrefix("/unused")
		use()
	}
	mw = middleware.Recoverer
	use()`), true
	case "middleware-closure-multiwrite-snapshot":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	next := middleware.StripPrefix("/api")
	copy := func() {
		mw = next
		next = middleware.Recoverer
	}
	copy()
	r.Use(mw)`), true
	case "middleware-closure-nested-immediate-future-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() {
		func() { r.Use(mw) }()
	}
	mw = middleware.StripPrefix("/api")
	use()`), true
	case "middleware-closure-nested-immediate-ordinary":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() {
		func() { r.Use(mw) }()
	}
	mw = middleware.Recoverer
	use()`), true
	case "middleware-closure-escape-transient-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	accept := func(func()) {}
	accept(use)
	mw = middleware.StripPrefix("/api")
	mw = middleware.Recoverer`), true
	case "middleware-closure-escaped-write-keeps-copied-ordinary-alias":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	alias := mw
	write := func() { mw = middleware.StripPrefix("/unused") }
	var accepted func()
	accept := func(callback func()) { accepted = callback }
	accept(write)
	accepted()
	r.Use(alias)`), true
	case "middleware-closure-escaped-write-keeps-lexical-shadow":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	write := func() { mw = middleware.StripPrefix("/unused") }
	var accepted func()
	accept := func(callback func()) { accepted = callback }
	accept(write)
	accepted()
	{
		mw := middleware.Recoverer
		r.Use(mw)
	}`), true
	case "middleware-closure-nested-escaped-write-targets-outer-binding":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	var accepted func()
	accept := func(callback func()) { accepted = callback }
	use := func() {
		write := func() { mw = middleware.StripPrefix("/api") }
		accept(write)
	}
	use()
	mw = middleware.Recoverer
	accepted()
	r.Use(mw)`), true
	case "middleware-closure-go-transient-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	go use()
	mw = middleware.StripPrefix("/api")
	mw = middleware.Recoverer`), true
	case "middleware-closure-defer-transient-rewrite":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	use := func() { r.Use(mw) }
	defer use()
	mw = middleware.StripPrefix("/api")
	mw = middleware.Recoverer`), true
	case "middleware-closure-body-use-before-ordinary":
		return review308FocusedMiddleware(`mw := middleware.StripPrefix("/api")
	use := func() {
		r.Use(mw)
		mw = middleware.Recoverer
	}
	use()`), true
	case "middleware-closure-body-ordinary-before-use":
		return review308FocusedMiddleware(`mw := middleware.StripPrefix("/unused")
	use := func() {
		mw = middleware.Recoverer
		r.Use(mw)
	}
	use()`), true
	case "middleware-switch-unrelated-nonfallthrough":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	switch {
	case true:
		r.Use(mw)
	default:
		mw = middleware.StripPrefix("/unused")
	}`), true
	case "middleware-switch-successor-ordinary-kill":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	switch {
	case true:
		mw = middleware.StripPrefix("/unused")
		fallthrough
	default:
		mw = middleware.Recoverer
		r.Use(mw)
	}`), true
	case "middleware-switch-fallthrough-chain":
		return review308FocusedMiddleware(`mw := middleware.Recoverer
	switch {
	case true:
		mw = middleware.StripPrefix("/api")
		fallthrough
	case false:
		fallthrough
	default:
		r.Use(mw)
	}`), true
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

func TestReview308RouterCapturedWriteStaysConservativeAtDeclaration(t *testing.T) {
	got := review308FocusedWithin(t, "closure-router-write-never-called")
	if len(got.Registrations) != 1 || got.Registrations[0].Route != "/s/in" || got.Registrations[0].Handler != "inHandler" {
		t.Fatalf("captured router write must stay unknown even when the local closure is never called: %#v", got.Registrations)
	}
}

func TestReview308OneArgumentUnknownMaskKeepsUntrackedAndKnownControls(t *testing.T) {
	for _, name := range []string{"one-arg-explicit-unknown-mask-control", "one-arg-explicit-unknown-mask-in-package-control"} {
		t.Run(name, func(t *testing.T) {
			got := review308FocusedWithin(t, name)
			if len(got.Registrations) != 0 {
				t.Fatalf("one-argument calls must not become registrations: %#v", got.Registrations)
			}
			want := map[string]bool{"/untracked": true, "/parameter-alias": true, "/known": true}
			if len(got.AfterMask) != len(want) {
				t.Fatalf("one-argument fallback after masking = %#v, want parameter and known controls", got.AfterMask)
			}
			for _, route := range got.AfterMask {
				if !want[route] {
					t.Fatalf("one-argument fallback after masking = %#v, want parameter and known controls", got.AfterMask)
				}
				delete(want, route)
			}
			if len(want) != 0 {
				t.Fatalf("one-argument fallback after masking = %#v, missing %#v", got.AfterMask, want)
			}
		})
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

func TestReview308MiddlewareLoopObservesPossibleNextIterationRewrite(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-future-rewrite")
	review308AssertNoRewrittenBareRoute(t, got.Registrations)
}

func TestReview308MiddlewareLoopOrdinaryOnlyControl(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-ordinary-only")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareLoopWatcherDoesNotOutliveRegion(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-later-unused-rewrite")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareLoopDoesNotTaintCopiedOrdinaryAlias(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-copied-ordinary-alias")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareLoopEntryDependencyFollowsAlias(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-entry-alias")
	review308AssertNoRewrittenBareRoute(t, got.Registrations)
}

func TestReview308MiddlewareLoopEntryDependencySurvivesOuterAlias(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-entry-alias-escapes")
	review308AssertNoRewrittenBareRoute(t, got.Registrations)
}

func TestReview308MiddlewareLoopDefiniteOrdinaryOverwriteKillsDependency(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-loop-definite-ordinary-before-use")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareNonRepeatingUseIgnoresLaterRewrite(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-branch-use-before-rewrite")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareClosureBranchDependencyReachesOuterUse(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-closure-branch-rewrite-outer-use")
	review308AssertNoRewrittenBareRoute(t, got.Registrations)
}

func TestReview308MiddlewareClosureBranchDependencyReachesInnerUse(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-closure-branch-rewrite-inner-use")
	review308AssertNoRewrittenBareRoute(t, got.Registrations)
}

func TestReview308MiddlewareClosureBranchOrdinaryOnlyControl(t *testing.T) {
	got := review308FocusedWithin(t, "middleware-closure-branch-ordinary-only")
	review308AssertOrdinaryMiddlewareRoutes(t, got.Registrations)
}

func TestReview308MiddlewareClosureSynchronousCallTiming(t *testing.T) {
	review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, "middleware-closure-call-during-rewrite").Registrations)
	for _, name := range []string{
		"middleware-closure-call-before-unused-rewrite",
		"middleware-closure-alias-transient-rewrite",
		"middleware-closure-overwrite-kills-descriptor",
		"middleware-closure-shadow-keeps-outer-descriptor",
	} {
		t.Run(name, func(t *testing.T) {
			review308AssertOrdinaryMiddlewareRoutes(t, review308FocusedWithin(t, name).Registrations)
		})
	}
}

func TestReview308MiddlewareClosureFinalWritesUseCallSnapshot(t *testing.T) {
	review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, "middleware-closure-multiwrite-snapshot").Registrations)
}

func TestReview308MiddlewareNestedImmediateClosureTiming(t *testing.T) {
	t.Run("future-rewrite", func(t *testing.T) {
		review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, "middleware-closure-nested-immediate-future-rewrite").Registrations)
	})
	t.Run("ordinary", func(t *testing.T) {
		review308AssertOrdinaryMiddlewareRoutes(t, review308FocusedWithin(t, "middleware-closure-nested-immediate-ordinary").Registrations)
	})
}

func TestReview308MiddlewareClosureAsyncAndEscapeStayConservative(t *testing.T) {
	for _, name := range []string{
		"middleware-closure-escape-transient-rewrite",
		"middleware-closure-go-transient-rewrite",
		"middleware-closure-defer-transient-rewrite",
	} {
		t.Run(name, func(t *testing.T) {
			review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, name).Registrations)
		})
	}
}

func TestReview308MiddlewareEscapedWritePreservesBindingIdentity(t *testing.T) {
	for _, name := range []string{
		"middleware-closure-escaped-write-keeps-copied-ordinary-alias",
		"middleware-closure-escaped-write-keeps-lexical-shadow",
	} {
		t.Run(name, func(t *testing.T) {
			review308AssertOrdinaryMiddlewareRoutes(t, review308FocusedWithin(t, name).Registrations)
		})
	}
	t.Run("nested-capture-target", func(t *testing.T) {
		review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, "middleware-closure-nested-escaped-write-targets-outer-binding").Registrations)
	})
}

func TestReview308MiddlewareClosureBodyAssignmentOrder(t *testing.T) {
	review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, "middleware-closure-body-use-before-ordinary").Registrations)
	review308AssertOrdinaryMiddlewareRoutes(t, review308FocusedWithin(t, "middleware-closure-body-ordinary-before-use").Registrations)
}

func TestReview308MiddlewareSwitchFallthroughControls(t *testing.T) {
	for _, name := range []string{
		"middleware-switch-unrelated-nonfallthrough",
		"middleware-switch-successor-ordinary-kill",
	} {
		t.Run(name, func(t *testing.T) {
			review308AssertOrdinaryMiddlewareRoutes(t, review308FocusedWithin(t, name).Registrations)
		})
	}
	review308AssertNoRewrittenBareRoute(t, review308FocusedWithin(t, "middleware-switch-fallthrough-chain").Registrations)
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
