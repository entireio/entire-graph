package sem

import (
	"fmt"
	"testing"
)

func TestGoRouteWrappedMountDoesNotInventBareRoute(t *testing.T) {
	for _, target := range []string{"api.With(auth)", "wrap(api)", "api"} {
		t.Run(target, func(t *testing.T) {
			source := fmt.Sprintf(`package p
import "github.com/go-chi/chi/v5"
func routes() {
 r := chi.NewRouter()
 api := chi.NewRouter()
 api.Get("/users", users)
 r.Mount("/v1", %s)
 r.Get("/health", health)
}`, target)
			regs := goRouteRegistrations(source, nil)
			repo := t.TempDir()
			writeFile(t, repo, "routes.go", source+"\nfunc users() {}\nfunc health() {}\n")
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "mount-review")
			if err != nil {
				t.Fatal(err)
			}
			routes := map[string]int{}
			for _, relation := range snapshot.Relations {
				if relation.Type == "HANDLES_ROUTE" {
					routes[relation.ToID]++
				}
			}
			if routes[externalID("route", "/users")] != 0 || routes[externalID("route", "/health")] != 1 {
				t.Fatalf("snapshot invented a route or lost root control: %v", routes)
			}
			wantMounted := 0
			if target == "api" {
				wantMounted = 1
			}
			if routes[externalID("route", "/v1/users")] != wantMounted {
				t.Fatalf("snapshot mount evidence: %v", routes)
			}

			seen := map[string]bool{}
			for _, reg := range regs {
				seen[reg.Route] = true
			}
			if seen["/users"] || !seen["/health"] || seen["/v1/users"] != (target == "api") {
				t.Fatalf("unsafe mount or lost control: %+v", regs)
			}
		})
	}
}
func TestGoRouteClosureParameterDoesNotInventBareRoute(t *testing.T) {
	source := `package p
import "net/http"
func routes() {
 api := http.NewServeMux()
 setup := func(m *http.ServeMux) { m.HandleFunc("/users", users) }
 setup(api)
 http.Handle("/api/", http.StripPrefix("/api", api))
 http.HandleFunc("/health", health)
}`
	regs := goRouteRegistrations(source, nil)
	health := false
	for _, reg := range regs {
		if reg.Route == "/users" {
			t.Fatalf("invented closure parameter route: %+v", regs)
		}
		health = health || reg.Route == "/health"
	}
	if !health {
		t.Fatalf("lost independent root: %+v", regs)
	}
}

func TestGoRouteWrappedMountRespectsNestedBindings(t *testing.T) {
	for _, target := range []struct {
		expr     string
		wantRoot bool
	}{
		{`func(api chi.Router) chi.Router { return api }(other)`, true},
		{`func() chi.Router { api := other; return api }()`, true},
		{`func() chi.Router { return api }()`, false},
		{`holder.api(other)`, true},
	} {
		t.Run(target.expr, func(t *testing.T) {
			source := fmt.Sprintf(`package p
import "github.com/go-chi/chi/v5"
func routes() {
 r := chi.NewRouter()
 api := chi.NewRouter()
 other := chi.NewRouter()
 api.Get("/root", root)
 other.Get("/child", child)
 r.Mount("/v1", %s)
 r.Get("/health", health)
}`, target.expr)
			regs := goRouteRegistrations(source, nil)
			seen := map[string]bool{}
			for _, reg := range regs {
				seen[reg.Route] = true
			}
			if seen["/root"] != target.wantRoot || !seen["/health"] || (target.wantRoot && seen["/child"]) {
				t.Fatalf("wrong lexical mount scope: %+v", regs)
			}
		})
	}
}
