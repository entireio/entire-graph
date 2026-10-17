package sem

import "testing"

// Independent PR312 witness for an opaque non-deferred callback invoking a
// setter declared in its enclosing function. The authored source is parsed as
// text and is never executed.
func review312OuterSetterOpaqueCallbackSource(rewrite bool) string {
	setter := `mw = middleware.Recoverer`
	if rewrite {
		setter = `mw = middleware.StripPrefix("/api")`
	}
	return `package routes
import (
	"net/http"
	"sync"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register() {
	r := chi.NewRouter()
	mw := middleware.Recoverer
	set := func() { ` + setter + ` }
	var once sync.Once
	once.Do(func() { set() })
	r.Use(mw)
	r.Get("/accounts", accounts)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`
}

func TestReview312OuterSetterInvokedByOpaqueCallback(t *testing.T) {
	t.Run("rewrite-setter-omits-served-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312OuterSetterOpaqueCallbackSource(true), false)
	})
	t.Run("ordinary-setter-retains-served-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312OuterSetterOpaqueCallbackSource(false), true)
	})
}
