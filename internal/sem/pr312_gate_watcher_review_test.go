package sem

import "testing"

// Independent gate witness for a sticky escaped rewrite on an intermediate
// binding that is later assigned a live captured source. Authored framework
// source is parsed only; no provider, repository, framework, network, or model
// is executed.
func review312GateWatcherSource(rewrite, nestedUse bool) string {
	setter := `alias = middleware.Recoverer`
	if rewrite {
		setter = `alias = middleware.StripPrefix("/api")`
	}
	use := `sub.Use(alias)`
	if nestedUse {
		use = `use := func() { sub.Use(alias) }
		use()`
	}
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
var accepted func()
func register() {
	r := chi.NewRouter()
	source := middleware.Recoverer
	r.Group(func(sub chi.Router) {
		alias := source
		set := func() { ` + setter + ` }
		accepted = set
		alias = source
		accepted()
		` + use + `
		sub.Get("/accounts", accounts)
	})
	source = middleware.Recoverer
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func accounts(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`
}

func TestReview312GateWatcherUsesEffectiveStickyIntermediate(t *testing.T) {
	t.Run("nested-use-after-rewrite-setter-omits-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312GateWatcherSource(true, true), false)
	})
	t.Run("direct-use-after-rewrite-setter-control", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312GateWatcherSource(true, false), false)
	})
	t.Run("nested-use-after-ordinary-setter-control", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312GateWatcherSource(false, true), true)
	})
}
