package sem

import "testing"

// Independent PR312 HTTP-client masking witness. These tests exercise only
// parsed source text through the pure route resolver, its byte-span masks, and
// the generic route-literal fallback. No provider, index, repository,
// framework, network, or model is used.

func review312ClientMaskRoutes(source string) ([]goHTTPRouteRegistration, []string) {
	detail := goRouteRegistrationsDetailed(source, nil)
	masked := goRouteMaskContent(source, detail.masks)
	return detail.regs, routeLiterals(masked)
}

func review312AssertServedRouterAndNoFallback(t *testing.T, source string) {
	t.Helper()
	registrations, fallback := review312ClientMaskRoutes(source)
	if len(registrations) != 1 || registrations[0].Route != "/accounts" || registrations[0].Handler != "accounts" {
		t.Fatalf("ordinary served router registrations = %#v, want exactly /accounts -> accounts", registrations)
	}
	if len(fallback) != 0 {
		t.Fatalf("generic route fallback = %#v, want no routes after client and router literals are masked", fallback)
	}
}

func review312ClosureClientSource(invocation string) string {
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func register() {
	r := chi.NewRouter()
	client := &http.Client{}
	` + invocation + `
	r.Get("/accounts", accounts)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
`
}

func TestReview312CapturedHTTPClientCallsStayOutOfRouteFallback(t *testing.T) {
	cases := []struct {
		name       string
		invocation string
	}{
		{
			name:       "immediate-closure",
			invocation: `func() { client.Get("/up") }()`,
		},
		{
			name:       "go-closure",
			invocation: `go func() { client.Get("/up") }()`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			review312AssertServedRouterAndNoFallback(t, review312ClosureClientSource(test.invocation))
		})
	}
}

func TestReview312ReassignedPackageHTTPClientStaysOutOfRouteFallback(t *testing.T) {
	review312AssertServedRouterAndNoFallback(t, `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
var client = &http.Client{}
func init() {
	client = &http.Client{}
}
func register() {
	r := chi.NewRouter()
	client.Get("/up")
	r.Get("/accounts", accounts)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
`)
}

func TestReview312BudgetExhaustionStillMasksHTTPClientFallback(t *testing.T) {
	source := `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func register() {
	r := chi.NewRouter()
	client := &http.Client{}
	client.Get("/up")
	r.Get("/accounts", accounts)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
`
	receivers := resolveGoRouteReceiversWithBudget(source, nil, 0)
	if !receivers.exhausted {
		t.Fatal("budget-zero route walk did not report exhaustion")
	}
	masked := goRouteMaskContent(source, receivers.masks)
	if fallback := routeLiterals(masked); len(fallback) != 0 {
		t.Fatalf("generic route fallback after exhausted walk = %#v, want neither client /up nor router /accounts", fallback)
	}
}
