package sem

import (
	"fmt"
	"strings"
	"testing"
)

func TestGoSignatureBudgetExhaustionReportsSnapshotPartialFailure(t *testing.T) {
	for _, depth := range []int{2, 11} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			var src strings.Builder
			src.WriteString("package fixture\n")
			for i := 0; i < depth; i++ {
				fmt.Fprintf(&src, "type A%d = func(a, b, c A%d)\n", i, i+1)
			}
			fmt.Fprintf(&src, "type A%d = int\n", depth)
			src.WriteString("type Contract interface { Run(v A0) }\ntype Worker struct{}\nfunc (Worker) Run(v A0) {}\nfunc invoke(c Contract) { c.Run(nil) }\n")
			repo := t.TempDir()
			writeFile(t, repo, "fixture.go", src.String())
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "signature-budget")
			if err != nil {
				t.Fatal(err)
			}
			warned := false
			for _, failure := range snapshot.Header.PartialFailures {
				if failure.Code == "E_GO_SIGNATURE_BUDGET_EXCEEDED" && failure.FilePath == "fixture.go" {
					warned = true
					if !strings.Contains(failure.EffectOnCompleteness, "interface") || failure.Detail == "" {
						t.Fatalf("missing loss explanation: %+v", failure)
					}
				}
			}
			if warned != (depth == 11) {
				t.Fatalf("warning=%v at depth %d; failures=%+v", warned, depth, snapshot.Header.PartialFailures)
			}
			implID := ""
			for _, symbol := range snapshot.Symbols {
				if symbol.QualifiedName == "Worker.Run" {
					implID = symbol.ID
				}
			}
			if implID == "" {
				t.Fatal("missing concrete method fixture")
			}
			inferred := false
			for _, relation := range snapshot.Relations {
				if relation.Type == "CALLS" && relation.ToID == implID {
					inferred = true
				}
			}
			if inferred != (depth == 2) {
				t.Fatalf("inferred concrete call=%v at depth %d", inferred, depth)
			}
		})
	}
}
