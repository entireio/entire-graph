package sem

import "testing"

func TestReview312ConditionalFallthroughMiddlewareJoin(t *testing.T) {
	t.Run("rewrite from conditional predecessor reaches successor use", func(t *testing.T) {
		registrations := goRouteRegistrations(review312ConditionalFallthroughSource(`
		if choose {
			mw = middleware.StripPrefix("/api")
		}
		fallthrough
	default:
		r.Use(mw)`), nil)
		for _, registration := range registrations {
			if registration.Route == "/accounts" {
				t.Fatalf("conditional predecessor rewrite must not emit the bare route: %#v", registrations)
			}
		}
	})

	t.Run("ordinary predecessor keeps successor routes", func(t *testing.T) {
		registrations := goRouteRegistrations(review312ConditionalFallthroughSource(`
		if choose {
			mw = middleware.Recoverer
		}
		fallthrough
	default:
		r.Use(mw)`), nil)
		review312AssertOrdinaryFallthroughRoutes(t, registrations)
	})

	t.Run("successor definite ordinary overwrite kills predecessor rewrite", func(t *testing.T) {
		registrations := goRouteRegistrations(review312ConditionalFallthroughSource(`
		if choose {
			mw = middleware.StripPrefix("/unused")
		}
		fallthrough
	default:
		mw = middleware.Recoverer
		r.Use(mw)`), nil)
		review312AssertOrdinaryFallthroughRoutes(t, registrations)
	})
}

func review312ConditionalFallthroughSource(clauses string) string {
	return `package routes
import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register(mode int, choose bool) {
	r := chi.NewRouter()
	mw := middleware.Recoverer
	switch mode {
	case 0:` + clauses + `
	}
	r.Get("/accounts", accounts)
	r.Get("/health", healthy)
}
`
}

func review312AssertOrdinaryFallthroughRoutes(t *testing.T, registrations []goHTTPRouteRegistration) {
	t.Helper()
	want := map[string]string{"accounts": "/accounts", "healthy": "/health"}
	if len(registrations) != len(want) {
		t.Fatalf("ordinary middleware routes = %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		if want[registration.Handler] != registration.Route {
			t.Fatalf("ordinary middleware routes = %#v, want %#v", registrations, want)
		}
		delete(want, registration.Handler)
	}
	if len(want) != 0 {
		t.Fatalf("ordinary middleware routes = %#v, missing %#v", registrations, want)
	}
}
