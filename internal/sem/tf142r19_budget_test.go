package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestTF142R19BudgetTruncationUsesSerialWorkers reproduces the finding at
// provider.go:1082: the budget deadline combined with a parallel file
// pipeline made the retained prefix depend on worker scheduling and cache
// warmth, so identical trees and options could emit different file and symbol
// sets. An opt-in MaxDuration now forces one worker so the prefix is a
// function of stable path order alone.
func TestTF142R19BudgetTruncationUsesSerialWorkers(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	for i := range 12 {
		writeFile(t, repo, fmt.Sprintf("pkg%02d/a.go", i), fmt.Sprintf("package pkg%02d\nfunc Fn%d() {}\n", i, i))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	const budget = time.Hour
	build := func(workers int) []byte {
		t.Helper()
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetEscapeHTML(false)
		err := streamSnapshotWithWorkerCount(t.Context(), repo, "test", ProviderSnapshotOptions{
			Worktree:    true,
			MaxDuration: budget,
			nowFn:       tf142r9LaggingClock(budget),
		}, workers, func(record any) error {
			return encoder.Encode(record)
		})
		if err != nil {
			t.Fatalf("workers=%d: an explicit budget must truncate, not fail: %v", workers, err)
		}
		return out.Bytes()
	}

	serial := build(1)
	parallel := build(8)
	if string(serial) != string(parallel) {
		t.Fatalf("budget-truncated snapshot differed by worker count:\n serial=%d bytes\nparallel=%d bytes", len(serial), len(parallel))
	}
}

// TestTF142R19GraphQLOperationRootAliasScanStopsMidSymbol reproduces the
// finding at provider.go:2789: the GraphQL operation-root alias pre-pass
// polled only once per file while its inner symbol loop could scan many
// graphql_schema_field records, so a generated file kept the relation phase
// running well past MaxDuration.
func TestTF142R19GraphQLOperationRootAliasScanStopsMidSymbol(t *testing.T) {
	t.Parallel()
	const symbolsPerFile = budgetPollStride*3 + 5
	files := []FileRecord{{RecordType: "file", ID: "file:schema.graphql", Path: "schema.graphql"}}
	recordsByFile := map[string][]SymbolRecord{"schema.graphql": make([]SymbolRecord, symbolsPerFile)}
	for i := range symbolsPerFile {
		recordsByFile["schema.graphql"][i] = SymbolRecord{
			RecordType:    "symbol",
			Kind:          "graphql_schema_field",
			Name:          fmt.Sprintf("field%d", i),
			QualifiedName: fmt.Sprintf("RootQuery.field%d", i),
			Signature:     "GraphQL schema query field",
			FilePath:      "schema.graphql",
		}
	}

	calls := 0
	stop := func() bool {
		calls++
		return calls >= 2
	}
	got := buildGraphQLOperationRootAliases(files, recordsByFile, stop)
	if len(got) != 0 {
		t.Fatalf("stopping inside the symbol loop must retain zero aliases, got %d", len(got))
	}
	if calls > budgetPollStride*2 {
		t.Fatalf("buildGraphQLOperationRootAliases polled shouldStop %d times before stopping: want an inner-loop poll, not only a per-file poll", calls)
	}
}
