package sem

import (
	"fmt"
	"strings"
	"testing"
)

func TestGoRouteBudgetExhaustionReportsSnapshotPartialFailure(t *testing.T) {
	for _, depth := range []int{1, 4000} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			var source strings.Builder
			source.WriteString("package routes\nfunc register(e *Echo) {\n g := e.Group(\"/api\")\n")
			for i := 0; i < depth; i++ {
				fmt.Fprintf(&source, "{ g.GET(\"/x%d\", func(){})\n", i)
			}
			source.WriteString(strings.Repeat("}\n", depth) + "}\n")
			repo := t.TempDir()
			writeFile(t, repo, "routes.go", source.String())
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "route-budget")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, failure := range snapshot.Header.PartialFailures {
				if failure.Code == "E_GO_ROUTE_BUDGET_EXCEEDED" && failure.FilePath == "routes.go" {
					found = true
					if !strings.Contains(failure.EffectOnCompleteness, "HANDLES_ROUTE") || failure.Detail == "" {
						t.Fatalf("loss is not explained: %+v", failure)
					}
				}
			}
			if found != (depth == 4000) {
				t.Fatalf("budget warning=%v at depth %d: %+v", found, depth, snapshot.Header.PartialFailures)
			}
			routes := 0
			for _, relation := range snapshot.Relations {
				if relation.Type == "HANDLES_ROUTE" {
					routes++
				}
			}
			if depth == 1 && routes != 1 {
				t.Fatalf("control lost its route: %d", routes)
			}
			if depth == 4000 && routes != 0 {
				t.Fatalf("exhausted analysis guessed %d routes", routes)
			}
		})
	}
}
