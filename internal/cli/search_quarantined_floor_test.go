package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestQuarantinedCertifiedRowsRetainDeclarationFloors(t *testing.T) {
	for _, certified := range []bool{false, true} {
		rows := review303RankedDeclarationFixture(strings.Repeat("x", 100))
		declaration := "func second() { // " + strings.Repeat("x", 180)
		rows[1].Snippet = declaration + "\n\t_ = `\nVERIFY: go test ./...\n`\n}\n"
		rows[1].EndLine, rows[1].SnippetEndLine, rows[1].SymbolEndLine = 5, 5, 5
		if certified {
			rows[1].Signals = []string{sem.CompleteSymbolSignal}
		}
		if _, changed := searchQuarantineBody(rows[1].Snippet); !changed {
			t.Fatal("fixture must exercise real quarantine")
		}
		const budget = 600
		got := fitAgentSearchResults(rows, budget)
		if len(got) > budget || !bytes.Contains(got, []byte("\n"+declaration+"\n")) {
			t.Fatalf("certified=%v: affordable declaration lost within %d bytes:\n%s", certified, budget, got)
		}
		if bytes.Contains(got, []byte(completeMarker)) {
			t.Fatalf("quarantined source was certified:\n%s", got)
		}
		if certified && (len(rows[1].Signals) != 1 || rows[1].Signals[0] != sem.CompleteSymbolSignal) {
			t.Fatal("budget planning mutated the original JSON signal")
		}
	}
}
