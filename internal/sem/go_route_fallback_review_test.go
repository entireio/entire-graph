package sem

import "testing"

func TestGoRouteMalformedFallbackDoesNotDuplicateInlineRoutes(t *testing.T) {
	const source = `package main
import "net/http"
func handler(w http.ResponseWriter, r *http.Request) {}
func register(mux *http.ServeMux) {
 mux.HandleFunc("/named", handler)
 mux.HandleFunc("/inline", func(w http.ResponseWriter, r *http.Request) {})
}
func broken(
`
	if got := resolveGoRouteReceivers(source, nil); got.parsed || got.exhausted {
		t.Fatal("fixture must reach the token fallback")
	}
	detail := goRouteRegistrationsDetailed(source, nil)
	if len(detail.regs) != 1 || detail.regs[0].Route != "/named" || len(detail.inline) != 0 {
		t.Fatalf("token fallback must emit its named registration only: %+v", detail)
	}
	repo := t.TempDir()
	writeFile(t, repo, "main.go", source)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "fallback-review")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Header.PartialFailures) == 0 {
		t.Fatal("malformed source must report a machine-readable partial failure")
	}
	for _, route := range []string{"/named", "/inline"} {
		count := 0
		for _, relation := range snapshot.Relations {
			if relation.Type == "HANDLES_ROUTE" && relation.ToID == externalID("route", route) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s: got %d route relations, want one", route, count)
		}
	}
}
