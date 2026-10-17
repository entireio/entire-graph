package sem

import "testing"

// review312GateWatcherOrderSource keeps the middleware source outside the
// framework callback so the callback's local alias initially carries a live
// capture source. The supplied body controls when local descriptors escape or
// run. The framework program is parsed only; it is never executed.
func review312GateWatcherOrderSource(body string) string {
	return `package routes
import (
	"net/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
var acceptedUse func()
var acceptedSet func()
func register() {
	r := chi.NewRouter()
	source := middleware.Recoverer
	r.Group(func(sub chi.Router) {
		alias := source
		use := func() { sub.Use(alias) }
		` + body + `
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

func TestReview312GateWatcherPreservesReversedEscapeOrder(t *testing.T) {
	t.Run("use-escapes-ordinary-before-rewrite-setter-omits-route", func(t *testing.T) {
		source := review312GateWatcherOrderSource(`acceptedUse = use
		set := func() { alias = middleware.StripPrefix("/api") }
		acceptedSet = set
		acceptedSet()
		acceptedUse()`)
		review312AssertSeededCallbackRoutes(t, source, false)
	})

	t.Run("use-escapes-before-ordinary-setter-retains-route", func(t *testing.T) {
		source := review312GateWatcherOrderSource(`acceptedUse = use
		set := func() { alias = middleware.Recoverer }
		acceptedSet = set
		acceptedSet()
		acceptedUse()`)
		review312AssertSeededCallbackRoutes(t, source, true)
	})

	t.Run("synchronous-definite-ordinary-overwrite-kills-rewrite", func(t *testing.T) {
		source := review312GateWatcherOrderSource(`set := func() { alias = middleware.StripPrefix("/api") }
		set()
		alias = middleware.Recoverer
		use()`)
		review312AssertSeededCallbackRoutes(t, source, true)
	})
}
