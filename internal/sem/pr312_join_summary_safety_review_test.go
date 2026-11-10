package sem

import (
	"strconv"
	"strings"
	"testing"
)

func review312JoinSummarySource(setup string) string {
	return `package routes
import (
	"net/http"
	"os"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)
func register() {
	r := chi.NewRouter()
	_ = os.Getenv
	` + setup + `
	r.Get("/seq", h)
	http.HandleFunc("/health", healthy)
	http.ListenAndServe(":0", r)
}
func h(http.ResponseWriter, *http.Request) {}
func healthy(http.ResponseWriter, *http.Request) {}
`
}

func review312SequentialJoinSummarySource(branches int) string {
	var setup strings.Builder
	setup.WriteString(`mw := middleware.Recoverer
	if os.Getenv("F0") != "" {
		mw = middleware.StripPrefix("/api")
	}
`)
	for i := 1; i < branches; i++ {
		setup.WriteString(`	if os.Getenv("F`)
		setup.WriteString(strconv.Itoa(i))
		setup.WriteString(`") != "" {
		mw = middleware.Recoverer
	}
`)
	}
	setup.WriteString(`	r.Use(mw)`)
	return review312JoinSummarySource(setup.String())
}

func review312NestedJoinSummarySource(depth int, rewrite bool) string {
	var setup strings.Builder
	setup.WriteString("mw := middleware.Recoverer\n")
	for i := 0; i < depth; i++ {
		setup.WriteString(strings.Repeat("\t", i+1))
		setup.WriteString("for i")
		setup.WriteString(strconv.Itoa(i))
		setup.WriteString(" := 0; i")
		setup.WriteString(strconv.Itoa(i))
		setup.WriteString(" < 1; i")
		setup.WriteString(strconv.Itoa(i))
		setup.WriteString("++ {\n")
	}
	setup.WriteString(strings.Repeat("\t", depth+1))
	if rewrite {
		setup.WriteString(`mw = middleware.StripPrefix("/api")`)
	} else {
		setup.WriteString("mw = middleware.Recoverer")
	}
	setup.WriteByte('\n')
	for i := depth - 1; i >= 0; i-- {
		setup.WriteString(strings.Repeat("\t", i+1))
		setup.WriteString("}\n")
	}
	setup.WriteString("\tr.Use(mw)")
	return review312JoinSummarySource(setup.String())
}

func review312AssertJoinSummaryRoutes(t *testing.T, source string, wantSeq bool) {
	t.Helper()
	want := map[string]string{"/health": "healthy"}
	if wantSeq {
		want["/seq"] = "h"
	}
	registrations := goRouteRegistrations(source, nil)
	if len(registrations) != len(want) {
		t.Fatalf("join-summary registrations = %#v, want %#v", registrations, want)
	}
	for _, registration := range registrations {
		handler, ok := want[registration.Route]
		if !ok || handler != registration.Handler {
			t.Fatalf("join-summary registrations = %#v, want %#v", registrations, want)
		}
		delete(want, registration.Route)
	}
	if len(want) != 0 {
		t.Fatalf("join-summary registrations = %#v, missing %#v", registrations, want)
	}
}

func TestReview312JoinSummarySafety(t *testing.T) {
	t.Run("earliest-rewrite-survives-24-sequential-joins", func(t *testing.T) {
		review312AssertJoinSummaryRoutes(t, review312SequentialJoinSummarySource(24), false)
	})
	t.Run("12-nested-ordinary-loops-retain-route", func(t *testing.T) {
		review312AssertJoinSummaryRoutes(t, review312NestedJoinSummarySource(12, false), true)
	})
	t.Run("12-nested-rewrite-loops-omit-route", func(t *testing.T) {
		review312AssertJoinSummaryRoutes(t, review312NestedJoinSummarySource(12, true), false)
	})
}
