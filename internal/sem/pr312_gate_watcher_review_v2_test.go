package sem

import "testing"

// Independent ordering witness for a local use descriptor captured while an
// intermediate middleware binding is still ordinary. A subsequently escaped
// setter may rewrite that binding before the descriptor is called. Authored
// framework source is parsed only; no provider, repository, framework,
// network, or model is executed.
func review312GateWatcherV2Source(rewrite, nestedUse bool) string {
	setter := `alias = middleware.Recoverer`
	if rewrite {
		setter = `alias = middleware.StripPrefix("/api")`
	}
	useDeclaration := ""
	useCall := `sub.Use(alias)`
	if nestedUse {
		useDeclaration = `use := func() { sub.Use(alias) }`
		useCall = `use()`
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
		` + useDeclaration + `
		set := func() { ` + setter + ` }
		accepted = set
		alias = source
		accepted()
		` + useCall + `
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

func TestReview312GateWatcherPreservesLaterWriteToInitiallyOrdinaryCapture(t *testing.T) {
	t.Run("nested-use-declared-before-rewrite-setter-omits-route", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312GateWatcherV2Source(true, true), false)
	})
	t.Run("direct-use-after-rewrite-setter-control", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312GateWatcherV2Source(true, false), false)
	})
	t.Run("nested-use-declared-before-ordinary-setter-control", func(t *testing.T) {
		review312AssertSeededCallbackRoutes(t,
			review312GateWatcherV2Source(false, true), true)
	})
}
