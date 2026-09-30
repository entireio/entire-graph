package sem

import "testing"

// Independent PR312 witness for a seeded callback invoking a setter declared
// in its enclosing function. The authored framework source is parsed as text
// and is never executed.
func review312OuterSetterCallbackSource(rewrite bool) string {
	setter := `mw = middleware.Recoverer`
	if rewrite {
		setter = `mw = middleware.StripPrefix("/api")`
	}
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register() {
	r := chi.NewRouter()
	mw := middleware.Recoverer
	set := func() { ` + setter + ` }
	r.Group(func(sub chi.Router) {
		_ = sub
		set()
	})
	r.Use(mw)
	r.Get("/accounts", accounts)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`
}

func TestReview312OuterSetterInvokedBySeededCallback(t *testing.T) {
	t.Run("rewrite-setter-omits-served-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312OuterSetterCallbackSource(true), false)
	})
	t.Run("ordinary-setter-retains-served-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312OuterSetterCallbackSource(false), true)
	})
}
