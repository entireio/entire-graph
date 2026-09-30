package sem

import (
	"strconv"
	"strings"
	"testing"
)

// Independent PR312 witness for sequential ordinary-middleware joins. The
// generated source is small, parsed in memory, and has no StripPrefix value.
func review312OrdinaryJoinCapSource(branches int) string {
	var source strings.Builder
	source.WriteString(`package routes
import (
	"net/http"
	"os"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register() {
	r := chi.NewRouter()
	r.Get("/health", health)
	mw := middleware.Logger
`)
	for i := 0; i < branches; i++ {
		source.WriteString(`	if os.Getenv("F`)
		source.WriteString(strconv.Itoa(i))
		source.WriteString(`") != "" {
		mw = middleware.Recoverer
	}
`)
	}
	source.WriteString(`	r.Use(mw)
	r.Get("/seq", h)
	http.ListenAndServe(":0", r)
}
func health(http.ResponseWriter, *http.Request) {}
func h(http.ResponseWriter, *http.Request) {}
`)
	return source.String()
}

func TestReview312OrdinaryMiddlewareJoinCapKeepsServedRoutes(t *testing.T) {
	cases := []struct {
		name     string
		branches int
	}{
		{name: "at-cap", branches: 16},
		{name: "first-over-cap", branches: 17},
		{name: "later-over-cap", branches: 24},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			registrations := goRouteRegistrations(review312OrdinaryJoinCapSource(test.branches), nil)
			if len(registrations) != 2 {
				t.Fatalf("%d ordinary joins registrations = %#v, want /health and /seq", test.branches, registrations)
			}
			got := map[string]string{}
			for _, registration := range registrations {
				got[registration.Route] = registration.Handler
			}
			if got["/health"] != "health" || got["/seq"] != "h" {
				t.Fatalf("%d ordinary joins routes = %#v, want /health -> health and /seq -> h", test.branches, got)
			}
		})
	}
}
