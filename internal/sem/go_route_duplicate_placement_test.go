package sem

import "testing"

// A handler registered on the same route twice, once on a router whose mount
// prefix is visible and once on one whose prefix is not, is a placed edge:
// one registration proves the route. The edge must not take whichever
// registration happened to come first in the file.
func TestGoRouteDuplicateRegistrationPrefersPlaced(t *testing.T) {
	placedFirst := `package api

import "net/http"

type Server struct {
	mux *http.ServeMux
}

func (s *Server) routes() {
	local := http.NewServeMux()
	local.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/login", s.handleLogin)
	http.ListenAndServe(":8080", local)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {}
`
	unplacedFirst := `package api

import "net/http"

type Server struct {
	mux *http.ServeMux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/login", s.handleLogin)
	local := http.NewServeMux()
	local.HandleFunc("/login", s.handleLogin)
	http.ListenAndServe(":8080", local)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {}
`
	inlineUnplacedFirst := `package api

import "net/http"

type Server struct {
	mux *http.ServeMux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {})
	local := http.NewServeMux()
	local.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {})
	http.ListenAndServe(":8080", local)
}
`
	for name, content := range map[string]string{"placed first": placedFirst, "unplaced first": unplacedFirst, "inline unplaced first": inlineUnplacedFirst} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, "server.go", content)
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
			if err != nil {
				t.Fatal(err)
			}
			var got []RelationRecord
			for _, relation := range snapshot.Relations {
				if relation.Type == "HANDLES_ROUTE" && relation.ToID == externalID("route", "/login") {
					got = append(got, relation)
				}
			}
			if len(got) != 1 {
				t.Fatalf("want one /login edge, got %#v", got)
			}
			want, resolution := 0.86, "exact"
			if name == "inline unplaced first" {
				want, resolution = 0.7, "pattern"
			}
			if got[0].Confidence != want || got[0].Resolution != resolution || goRouteRelationUnplaced(got[0]) || got[0].Reason != goRouteNamedReason && got[0].Reason != goRouteInlineReason {
				t.Fatalf("duplicate with a placed registration is not placed: %#v", got[0])
			}
		})
	}
}
