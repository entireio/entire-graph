package sem

import (
	"sort"
	"strings"
	"testing"
)

// goRouteDetailSet lists a file's named and inline registrations, marking the
// routes whose router is served under a prefix the file does not show.
func goRouteDetailSet(content string) []string {
	detail := goRouteRegistrationsDetailed(content, staticStringConstants(content))
	var out []string
	mark := func(unplaced bool) string {
		if unplaced {
			return " [unplaced]"
		}
		return ""
	}
	for _, reg := range detail.regs {
		out = append(out, reg.Route+" -> "+reg.Handler+mark(reg.Unplaced))
	}
	for _, reg := range detail.inline {
		out = append(out, reg.Route+" (inline)"+mark(reg.Unplaced))
	}
	sort.Strings(out)
	return out
}

func runGoRouteDetailCases(t *testing.T, cases []goRouteAccuracyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := goRouteDetailSet(tc.content)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("routes:\n  got  %q\n  want %q", got, want)
			}
		})
	}
}

// A router held in a struct field, a router parameter of a mountable root
// type, and a ServeMux handed to a wrapper are served under a prefix the file
// does not show. Their routes are stated relative to the router and marked
// unplaced instead of being dropped; a router the file shows being rewritten
// or mounted stays omitted, and so does any field that may hold a group.
func TestGoRouteRecallUnplacedRouters(t *testing.T) {
	runGoRouteDetailCases(t, []goRouteAccuracyCase{
		{
			name: "struct-field ServeMux with call-expression and named handlers",
			content: `package api
import "net/http"
type Server struct {
	mux  *http.ServeMux
	name string
}
func New() *Server { return &Server{mux: http.NewServeMux()} }
func (s *Server) routes() {
	s.mux.Handle("/", noCache(http.FileServerFS(static)))
	admin := s.gate(http.FileServerFS(static))
	s.mux.Handle("/admin/", admin)
	s.mux.HandleFunc("/login", s.handleLogin)
}
`,
			want: []string{"/ (inline) [unplaced]", "/admin/ (inline) [unplaced]", "/login -> s.handleLogin [unplaced]"},
		},
		{
			name: "exported chi field, field Group and an echo field",
			content: `package httpapi
import (
	"github.com/go-chi/chi/v5"
	"github.com/labstack/echo/v4"
)
type Router struct {
	Mux *chi.Mux
}
type Srv struct{ e *echo.Echo }
func (r *Router) registerHealth() {
	r.Mux.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {})
	r.Mux.Get("/readyz", readyz)
}
func (s *Srv) routes() {
	v1 := s.e.Group("/v1")
	v1.GET("/users", listUsers)
	s.e.GET("/ping", ping)
}
`,
			want: []string{"/healthz (inline) [unplaced]", "/ping -> ping [unplaced]", "/readyz -> readyz [unplaced]", "/v1/users -> listUsers [unplaced]"},
		},
		{
			name: "field assigned a constructor, aliased into a local",
			content: `package p
func (s *server) init() {
	s.router = mux.NewRouter()
}
func (s *server) routes() {
	r := s.router
	r.HandleFunc("/items", s.items)
}
`,
			want: []string{"/items -> s.items [unplaced]"},
		},
		{
			name: "local ServeMux handed to wrappers: call-expression handlers",
			content: `package server
import "net/http"
func (s *HTTPServer) newHTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", repoapi.NewHandler(s.repo, s.auth))
	mux.HandleFunc("/debug/login", s.handleDebugLogin)
	registerExtra(mux, s)
	return otelhttp.NewHandler(recoverMiddleware(mux), "srv")
}
func registerExtra(mux *http.ServeMux, s *HTTPServer) {
	mux.HandleFunc("/extra", s.handleExtra)
}
`,
			want: []string{"/api/v1/ (inline) [unplaced]", "/debug/login -> s.handleDebugLogin [unplaced]", "/extra -> s.handleExtra [unplaced]"},
		},
		{
			name: "fiber App and chi Mux parameters with a package const path",
			content: `package p
import (
	"github.com/go-chi/chi/v5"
	"github.com/gofiber/fiber/v3"
)
const PingPath = "/ping"
func RegisterPing(app *fiber.App) {
	_ = app.Get(PingPath, PingHandler)
	api := app.Group("/api")
	api.Get("/v", version)
}
func RegisterChi(m *chi.Mux) { m.Get("/c", cH) }
`,
			want: []string{"/api/v -> version [unplaced]", "/c -> cH [unplaced]", "/ping -> PingHandler [unplaced]"},
		},
		{
			name: "served routers stay placed",
			content: `package main
import "net/http"
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", health)
	http.ListenAndServe(":1", mux)
}
`,
			want: []string{"/health -> health"},
		},
	})
}

// What must stay omitted after the recall change.
func TestGoRouteRecallKeepsRewrittenAndGroupRoutersOmitted(t *testing.T) {
	runGoRouteDetailCases(t, []goRouteAccuracyCase{
		{
			name: "field router wrapped in StripPrefix, mounted, or behind StripPrefix middleware",
			content: `package p
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
type S struct {
	api  *http.ServeMux
	sub  *chi.Mux
	main *chi.Mux
}
func (s *S) routes(top *http.ServeMux, r chi.Router) {
	s.api.HandleFunc("/users", users)
	top.Handle("/api/", http.StripPrefix("/api", s.api))
	s.sub.Get("/items", items)
	r.Mount("/sub", s.sub)
	s.main.Use(func(next http.Handler) http.Handler { return http.StripPrefix("/v", next) })
	s.main.Get("/orders", orders)
}
`,
			// top is a root parameter: its own /api/ pattern is placed.
			want: []string{"/api/ (inline)"},
		},
		{
			name: "fields that may hold a group, or a non-router",
			content: `package p
type S struct {
	g   *gin.RouterGroup
	r   chi.Router
	sub *mux.Router
}
type T struct{ mux sync.Mutex }
type U struct{ mux *http.ServeMux }
func (s *S) a(e *gin.Engine) {
	s.g.GET("/g", gH)
	s.r.Get("/r", rH)
	s.sub = s.root.PathPrefix("/api").Subrouter()
	s.sub.HandleFunc("/sub", subH)
}
func (u *U) b() { u.mux.HandleFunc("/ambiguous", ambiguousH) }
func c() { server.mux.HandleFunc("/pkg", pkgH) }
`,
			want: nil,
		},
		{
			name: "a ServeMux handed to a same-file stripping helper, and a returned or Group-passed one",
			content: `package p
import "net/http"
func strip(h http.Handler) http.Handler { return http.StripPrefix("/api", h) }
func a() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/a", aH)
	return otelhttp.NewHandler(strip(m), "a")
}
func b() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/b", bH)
	return m
}
`,
			want: nil,
		},
		{
			name: "an unplaced parameter fed a group by a same-file caller",
			content: `package p
func register(app *fiber.App) { app.Get("/x", xH) }
func main() {
	app := fiber.New()
	register(app.Group("/api"))
	app.Listen(":1")
}
`,
			want: nil,
		},
	})
}

// The provider emits an unplaced route with a lowered confidence, a pattern
// resolution and a ROUTE_PREFIX_UNRESOLVED warning, and never matches it to
// an HTTP client call by path.
func TestGoRouteRecallSnapshotMarksUnplacedRoutes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package api

import "net/http"

type Server struct {
	mux *http.ServeMux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.Handle("/static/", http.FileServer(http.Dir(".")))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {}

func ping() {
	http.Get("http://localhost/login")
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]RelationRecord{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "HANDLES_ROUTE" {
			found[lastSegment(relation.FromID)+" "+relation.ToID] = relation
		}
		if relation.Type == "CALLS" && strings.HasSuffix(relation.ToID, "handleLogin") {
			t.Fatalf("client call matched to an unplaced route: %#v", relation)
		}
	}
	for key, confidence := range map[string]float64{
		"Server.handleLogin " + externalID("route", "/login"): 0.66,
		"Server.routes " + externalID("route", "/static/"):    0.5,
	} {
		relation, ok := found[key]
		if !ok {
			t.Fatalf("missing %s in %v", key, found)
		}
		if relation.Confidence != confidence || relation.Resolution != "pattern" || len(relation.WarningCodes) != 1 || relation.WarningCodes[0] != "ROUTE_PREFIX_UNRESOLVED" {
			t.Fatalf("%s not marked unplaced: %#v", key, relation)
		}
	}
}
