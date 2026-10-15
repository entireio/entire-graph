package sem

import (
	"fmt"
	"strings"
	"testing"
)

func TestGoRouteParameterTaintTraversesLongForwardingChain(t *testing.T) {
	const hops = 256
	for _, prefix := range []string{"", "/api"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			var src strings.Builder
			src.WriteString("package example\nimport \"net/http\"\nfunc handler(w http.ResponseWriter,r *http.Request) {}\nfunc route0(m *http.ServeMux) {m.HandleFunc(\"/leaf\",handler)}\n")
			for i := 1; i <= hops; i++ {
				fmt.Fprintf(&src, "func route%d(m *http.ServeMux) {route%d(m)}\n", i, i-1)
			}
			fmt.Fprintf(&src, "func serve(){m:=http.NewServeMux();route%d(m);", hops)
			if prefix == "" {
				src.WriteString("http.ListenAndServe(\":8080\",m)}\n")
			} else {
				fmt.Fprintf(&src, "http.Handle(\"/\",http.StripPrefix(%q,m))}\n", prefix)
			}
			repo := t.TempDir()
			writeFile(t, repo, "routes.go", src.String())
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "forwarding-depth")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, relation := range snapshot.Relations {
				if relation.Type == "HANDLES_ROUTE" && relation.ToID == externalID("route", "/leaf") {
					found = true
				}
			}
			if found != (prefix == "") {
				t.Fatalf("bare /leaf=%v with served prefix %q", found, prefix)
			}
			if len(snapshot.Header.PartialFailures) != 0 {
				t.Fatalf("ordinary forwarding chain exhausted analysis: %+v", snapshot.Header.PartialFailures)
			}
		})
	}
}
