package sem

import (
	"fmt"
	"testing"
)

// TestTF142R19GraphQLOperationRootAliasScanStopsMidSymbol reproduces the
// finding at provider.go:2789: the GraphQL operation-root alias pre-pass
// polled only once per file while its inner symbol loop could scan many
// graphql_schema_field records, so a generated file kept the relation phase
// running well after a cancellation.
func TestTF142R19GraphQLOperationRootAliasScanStopsMidSymbol(t *testing.T) {
	t.Parallel()
	const symbolsPerFile = stopPollStride*3 + 5
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
	if calls > stopPollStride*2 {
		t.Fatalf("buildGraphQLOperationRootAliases polled shouldStop %d times before stopping: want an inner-loop poll, not only a per-file poll", calls)
	}
}
