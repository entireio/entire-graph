package sem

import "testing"

// http.StripPrefix reached through router middleware or through a same-file
// helper rewrites the path before the router matches it, so the router's own
// registrations are not the served paths. They are omitted, never published
// bare. A helper or middleware that strips something else keeps them.
func TestGoRouteAccuracyStripPrefixMiddlewareAndHelpers(t *testing.T) {
	runGoRouteAccuracyCases(t, []goRouteAccuracyCase{
		{
			name: "chi Use closure returning StripPrefix",
			content: `package main
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func main() {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { return http.StripPrefix("/api", next) })
	r.Get("/users", listUsers)
	http.ListenAndServe(":1", r)
}
`,
			want: nil,
		},
		{
			name: "gorilla Use closure wrapping StripPrefix in a HandlerFunc",
			content: `package main
import (
	"net/http"
	"github.com/gorilla/mux"
)
func main() {
	r := mux.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			http.StripPrefix("/api", next).ServeHTTP(w, req)
		})
	})
	r.HandleFunc("/users", listUsers)
	http.ListenAndServe(":1", r)
}
`,
			want: nil,
		},
		{
			name: "middleware closure held in a local",
			content: `package main
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func main() {
	r := chi.NewRouter()
	strip := func(next http.Handler) http.Handler { return http.StripPrefix("/api", next) }
	r.Use(strip)
	r.Get("/users", listUsers)
	http.ListenAndServe(":1", r)
}
`,
			want: nil,
		},
		{
			name: "same-file middleware function and middleware factory",
			content: `package main
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func stripAPI(next http.Handler) http.Handler { return http.StripPrefix("/api", next) }
func strip(p string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return http.StripPrefix(p, next) }
}
func a() {
	r := chi.NewRouter()
	r.Use(stripAPI)
	r.Get("/a", aH)
	http.ListenAndServe(":1", r)
}
func b() {
	r := chi.NewRouter()
	r.Use(strip("/api"))
	r.Get("/b", bH)
	http.ListenAndServe(":2", r)
}
`,
			want: nil,
		},
		{
			name: "same-file helper wraps the served router, directly or through another helper",
			content: `package main
import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/mux"
)
func wrap(h http.Handler) http.Handler { return http.StripPrefix("/api", h) }
func outer(h http.Handler) http.Handler { return logged(inner(h)) }
func inner(h http.Handler) http.Handler { return http.StripPrefix("/v2", logged(h)) }
func logged(h http.Handler) http.Handler { return h }
func a() {
	r := gin.New()
	r.GET("/users", listUsers)
	http.ListenAndServe(":1", wrap(r))
}
func b() {
	m := mux.NewRouter()
	m.HandleFunc("/items", listItems)
	srv := &http.Server{Handler: outer(m)}
	srv.ListenAndServe()
}
`,
			want: nil,
		},
		{
			name: "helpers and middleware that strip something else keep the routes",
			content: `package main
import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"
)
func logged(h http.Handler) http.Handler { return h }
func register(m *http.ServeMux, files http.Handler) {
	m.Handle("/static/", http.StripPrefix("/static/", files))
	m.HandleFunc("/users", users)
}
func a() {
	r := gin.New()
	r.GET("/items", items)
	http.ListenAndServe(":1", logged(r))
}
func b() {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { return logged(next) })
	r.Get("/orders", orders)
	http.ListenAndServe(":2", r)
}
func c() {
	m := http.NewServeMux()
	register(m, http.FileServer(http.Dir(".")))
	http.ListenAndServe(":3", m)
}
`,
			want: []string{"/users -> users", "/items -> items", "/orders -> orders"},
		},
	})
}
