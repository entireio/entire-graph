package sem

import "testing"

// review312CallbackLocalIIFESource exercises an immediate closure created and
// called inside the same framework callback. The framework source is parsed as
// text and is never executed.
func review312CallbackLocalIIFESource() string {
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register() {
	r := chi.NewRouter()
	r.Group(func(sub chi.Router) {
		mw := middleware.Recoverer
		func() { mw = middleware.StripPrefix("/api") }()
		mw = middleware.Recoverer
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

// review312CallbackLocalConditionalSource keeps both local descriptors in one
// seeded callback while invoking the setter from a conditional branch. Branch
// regions must not make a descriptor inherited from a different callback.
func review312CallbackLocalConditionalSource() string {
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register(choose bool) {
	r := chi.NewRouter()
	r.Group(func(sub chi.Router) {
		mw := middleware.Recoverer
		set := func() { mw = middleware.StripPrefix("/api") }
		use := func() { sub.Use(mw) }
		if choose {
			set()
		}
		mw = middleware.Recoverer
		use()
		sub.Get("/accounts", accounts)
	})
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`
}

// review312InheritedSetterCallbackSource keeps the named setter outside the
// framework callback. Calling it inside the callback must therefore retain the
// conservative inherited-effect behavior rather than treating it as a local
// same-region descriptor.
func review312InheritedSetterCallbackSource(rewrite bool) string {
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
		set()
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

func TestReview312CallbackLocalEffectOrdering(t *testing.T) {
	t.Run("same-callback-iife-definite-ordinary-overwrite-retains-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312CallbackLocalIIFESource(), true)
	})
	t.Run("same-callback-conditional-setter-definite-overwrite-retains-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312CallbackLocalConditionalSource(), true)
	})
	t.Run("nested-callback-inherited-rewrite-setter-omits-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312InheritedSetterCallbackSource(true), false)
	})
	t.Run("nested-callback-inherited-ordinary-setter-retains-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312InheritedSetterCallbackSource(false), true)
	})
}
