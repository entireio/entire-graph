package sem

import (
	"strings"
	"testing"
)

// Go 1.22 ServeMux patterns are "[METHOD ][HOST]/[PATH]". The route is the
// path; the method is kept apart for evidence and the host is dropped. A
// pattern that starts with a slash is a plain path, exactly as before; any
// other shape is accepted only when it is unambiguous, and omitted otherwise.
func TestGoServeMuxPattern(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		method  string
		ok      bool
	}{
		{"/users", "/users", "", true},
		{"/", "/", "", true},
		{"/static/", "/static/", "", true},
		{"GET /users/{id}", "/users/{id}", "GET", true},
		{"POST /users", "/users", "POST", true},
		{"DELETE /admin/platform-admins/{handle...}", "/admin/platform-admins/{handle...}", "DELETE", true},
		{"GET /{$}", "/{$}", "GET", true},
		{"/{$}", "/{$}", "", true},
		{"/files/{path...}", "/files/{path...}", "", true},
		{"GET\t/tabbed", "/tabbed", "GET", true},
		{"GET  \t /spaced", "/spaced", "GET", true},
		{"PROPFIND /dav/", "/dav/", "PROPFIND", true},
		{"POST example.com/x", "/x", "POST", true},
		{"example.com/x", "/x", "", true},
		{"api.example.com:8443/v1/", "/v1/", "", true},
		{"GET example.com/", "/", "GET", true},
		{"GET /a/{b}/c/{d...}", "/a/{b}/c/{d...}", "GET", true},
		// Omitted: Go would reject these, or match something else.
		{"get /users", "", "", false},
		{"Get /users", "", "", false},
		{"GET", "", "", false},
		{"GET ", "", "", false},
		{"GET example.com", "", "", false},
		{"example.com", "", "", false},
		{"", "", "", false},
		{"GET /a b", "", "", false},
		{"GET {host}/x", "", "", false},
		{"GET /a/{$}/b", "", "", false},
		{"GET /a/{rest...}/b", "", "", false},
		{"GET /a/x{id}", "", "", false},
		{"GET /a/{id", "", "", false},
		{"GET /a/{1id}", "", "", false},
		{"GET /a/{}", "", "", false},
		{"http://example.com/x", "", "", false},
		{"G-ET /x", "", "", false},
		{"users", "", "", false},
	}
	for _, tc := range cases {
		path, method, ok := goServeMuxPattern(tc.pattern)
		if path != tc.path || method != tc.method || ok != tc.ok {
			t.Errorf("goServeMuxPattern(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.pattern, path, method, ok, tc.path, tc.method, tc.ok)
		}
	}
}

// ServeMux registrations with a method or host pattern resolve to the path,
// on a local mux, the DefaultServeMux and an unplaced field mux, for named and
// inline handlers alike. Other frameworks keep their own literal rules, and a
// router behind StripPrefix or a Mount stays omitted whatever its pattern form.
func TestGoRouteServeMuxMethodPatterns(t *testing.T) {
	runGoRouteDetailCases(t, []goRouteAccuracyCase{
		{
			name: "local ServeMux and DefaultServeMux",
			content: `package main
import "net/http"
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", getUser)
	mux.HandleFunc("POST /users", createUser)
	mux.Handle("DELETE /users/{id}", http.HandlerFunc(deleteUser))
	mux.Handle("GET /files/{path...}", http.FileServer(http.Dir(".")))
	mux.HandleFunc("GET example.com/{$}", home)
	mux.HandleFunc("/plain", plain)
	http.HandleFunc("GET /healthz", healthz)
	http.ListenAndServe(":8080", mux)
}
`,
			want: []string{
				"/files/{path...} (inline)",
				"/healthz -> healthz",
				"/plain -> plain",
				"/users -> createUser",
				"/users/{id} -> deleteUser",
				"/users/{id} -> getUser",
				"/{$} -> home",
			},
		},
		{
			name: "struct-field ServeMux stays unplaced",
			content: `package api
import "net/http"
type Server struct{ mux *http.ServeMux }
func New() *Server { return &Server{mux: http.NewServeMux()} }
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.Handle("GET /api/admin/users", RequireSession(http.HandlerFunc(s.handleAdminUsers)))
}
`,
			want: []string{"/api/admin/users (inline) [unplaced]", "/healthz -> s.handleHealth [unplaced]"},
		},
		{
			name: "invalid patterns are omitted",
			content: `package main
import "net/http"
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("get /lower", lower)
	mux.HandleFunc("GET", methodOnly)
	mux.HandleFunc("GET example.com", noPath)
	mux.HandleFunc("GET /a/{$}/b", badDollar)
	mux.HandleFunc("GET /a/x{id}", partialWildcard)
	http.ListenAndServe(":8080", mux)
}
`,
			want: nil,
		},
		{
			name: "StripPrefix around a method-pattern mux keeps the inner routes omitted",
			content: `package main
import "net/http"
func main() {
	root := http.NewServeMux()
	api := http.NewServeMux()
	api.HandleFunc("GET /users", listUsers)
	root.Handle("GET /api/", http.StripPrefix("/api", api))
	http.ListenAndServe(":8080", root)
}
`,
			want: []string{"/api/ (inline)"},
		},
		{
			// chi Mount rewrites only chi's routing context, which a ServeMux
			// does not read: the mount stays opaque, as for a plain path.
			name: "chi Mount of a method-pattern ServeMux stays omitted",
			content: `package main
import (
	"net/http"
	"github.com/go-chi/chi/v5"
)
func main() {
	r := chi.NewRouter()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", getUser)
	r.Mount("/api", mux)
	http.ListenAndServe(":8080", r)
}
`,
			want: nil,
		},
		{
			name: "other frameworks keep their literal rules",
			content: `package main
import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/mux"
)
func main() {
	g := gin.New()
	g.Handle("GET", "/gin", ginH)
	m := mux.NewRouter()
	m.HandleFunc("GET /gorilla", gorillaH)
	http.ListenAndServe(":8080", m)
}
`,
			want: []string{"/gin (inline)"},
		},
	})
}

// End to end: a ServeMux with method patterns yields HANDLES_ROUTE edges to
// the path route, with the method in the evidence detail. A second method on
// the same path and symbol is the same edge and adds one evidence entry; the
// same pattern registered twice adds none.
func TestGoRouteServeMuxMethodPatternsSnapshot(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "server.go", `package main

import "net/http"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", getUser)
	mux.HandleFunc("POST example.com/users", createUser)
	mux.Handle("DELETE /users/{id}", http.HandlerFunc(deleteUser))
	mux.HandleFunc("PUT /users/{id}", getUser)
	mux.HandleFunc("GET /users/{id}", getUser)
	mux.HandleFunc("GET /files/{path...}", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("HEAD /files/{path...}", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("get /lower", lower)
	http.ListenAndServe(":8080", mux)
}

func getUser(w http.ResponseWriter, r *http.Request)    {}
func createUser(w http.ResponseWriter, r *http.Request) {}
func deleteUser(w http.ResponseWriter, r *http.Request) {}
func lower(w http.ResponseWriter, r *http.Request)      {}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "HANDLES_ROUTE" {
			continue
		}
		if strings.Contains(relation.ToID, "GET") || strings.Contains(relation.ToID, "example.com") || strings.Contains(relation.ToID, "lower") {
			t.Fatalf("route id carries pattern text: %#v", relation)
		}
		var details []string
		for _, evidence := range relation.Evidence {
			details = append(details, evidence.Detail)
		}
		got[lastSegment(relation.FromID)+" "+relation.ToID] = strings.Join(details, " | ")
	}
	want := map[string]string{
		"getUser " + externalID("route", "/users/{id}"):    "GET /users/{id} -> getUser | PUT /users/{id} -> getUser",
		"createUser " + externalID("route", "/users"):      "POST /users -> createUser",
		"deleteUser " + externalID("route", "/users/{id}"): "DELETE /users/{id} -> deleteUser",
		"main " + externalID("route", "/files/{path...}"):  "GET /files/{path...} | HEAD /files/{path...}",
	}
	for key, detail := range want {
		if got[key] != detail {
			t.Errorf("%s: evidence %q, want %q (all: %v)", key, got[key], detail, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("HANDLES_ROUTE edges = %v, want exactly %v", got, want)
	}
}
