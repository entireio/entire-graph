package sem

import "testing"

// Independent PR312 witness for closure-write joins inside a framework-seeded
// callback. The framework source is parsed as text and is never executed.
func review312SeededCallbackJoinSource(setup string) string {
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register(choose bool) {
	r := chi.NewRouter()
	mw := middleware.Recoverer
	r.Group(func(sub chi.Router) {
		` + setup + `
		sub.Use(mw)
		sub.Get("/accounts", accounts)
	})
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`
}

func review312AssertSeededCallbackRoutes(t *testing.T, source string, wantAccounts bool) {
	t.Helper()
	want := map[string]string{"/health": "healthy"}
	if wantAccounts {
		want["/accounts"] = "accounts"
	}
	registrations := goRouteRegistrations(source, nil)
	if len(registrations) != len(want) {
		t.Fatalf("seeded callback registrations = %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		handler, ok := want[registration.Route]
		if !ok || handler != registration.Handler {
			t.Fatalf("seeded callback registrations = %#v, want %#v", registrations, want)
		}
		delete(want, registration.Route)
	}
	if len(want) != 0 {
		t.Fatalf("seeded callback registrations = %#v, missing %#v", registrations, want)
	}
}

func TestReview312SeededCallbackJoinTargetsActiveCapture(t *testing.T) {
	t.Run("conditional-rewrite-omits-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t, review312SeededCallbackJoinSource(`set := func() { mw = middleware.StripPrefix("/api") }
		if choose {
			set()
		}`), false)
	})
	t.Run("conditional-ordinary-setter-retains-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t, review312SeededCallbackJoinSource(`set := func() { mw = middleware.Recoverer }
		if choose {
			set()
		}`), true)
	})
	t.Run("definite-ordinary-overwrite-kills-rewrite", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t, review312SeededCallbackJoinSource(`set := func() { mw = middleware.StripPrefix("/api") }
		if choose {
			set()
		}
		mw = middleware.Recoverer`), true)
	})
}
