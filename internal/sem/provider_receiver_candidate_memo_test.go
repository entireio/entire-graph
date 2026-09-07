package sem

import (
	"reflect"
	"testing"
)

func TestReceiverCallRelationsMemoizedCandidatesPreserveOutput(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/receiver-cache\n\ngo 1.26\n")
	writeFile(t, repo, "worker.go", `package receivercache

type Worker struct{}

func (Worker) Ping() {}

func Repeat() {
	unknown.Ping()
	unknown.Ping()
}
`)
	// The scanner may collapse the identical receiver/method pair, but the one
	// retained Ping call is still resolved in two passes: the qualified-symbol
	// pass probes it first, then the Go unique-method fallback probes it again.
	// This same-short-name method must remain ineligible for the Go caller. It
	// makes repeated filtering observable through the relation result: reusing a
	// raw or differently filtered slice would suppress the unique Go target.
	writeFile(t, repo, "foreign.ts", `class ForeignWorker {
  Ping(): void {}
}
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	symbolsByID := make(map[string]SymbolRecord, len(snapshot.Symbols))
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
	}
	type callResult struct {
		ToName     string
		ToFile     string
		Confidence float64
		Reason     string
		Scope      string
		Resolution string
		Evidence   []Evidence
	}
	var got []callResult
	for _, relation := range snapshot.Relations {
		from, fromOK := symbolsByID[relation.FromID]
		to, toOK := symbolsByID[relation.ToID]
		if relation.Type != "CALLS" || !fromOK || !toOK || from.Name != "Repeat" {
			continue
		}
		got = append(got, callResult{
			ToName:     to.Name,
			ToFile:     to.FilePath,
			Confidence: relation.Confidence,
			Reason:     relation.Reason,
			Scope:      relation.RelationScope,
			Resolution: relation.Resolution,
			Evidence:   relation.Evidence,
		})
	}
	want := []callResult{{
		ToName:     "Ping",
		ToFile:     "worker.go",
		Confidence: 0.68,
		Reason:     "method call matched globally unique method name",
		Scope:      "file",
		Resolution: "name_only",
		Evidence: []Evidence{{
			Kind:      "call_site",
			FilePath:  "worker.go",
			StartLine: 7,
			EndLine:   10,
			Detail:    "unknown.Ping",
		}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receiver CALLS changed:\n got: %#v\nwant: %#v", got, want)
	}
}
