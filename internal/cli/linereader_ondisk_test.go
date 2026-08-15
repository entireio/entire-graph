package cli

import (
	"github.com/entireio/entire-graph/internal/sem"
)

// The commands construct one reader per invocation and pass it down, so that a
// committed snapshot is never annotated with working-tree source. The fixture
// suites below predate that and only ever want the plain on-disk reader.
//
// These helpers live in the test build for a reason: while they lived in
// neighbors.go / impact.go / symbolref.go they were production functions with no
// production caller, hardwiring the reader the commands had just stopped using.
// A regression in the wired-up path could not fail them, and their existence
// made it look covered. The `OnDisk` suffix keeps the choice of reader visible
// at each call site; the provenance-aware path is exercised end to end in
// head_source_provenance_test.go.

func buildNeighborResponseOnDisk(snapshot sem.ProviderSnapshot, flags neighborFlags) neighborResponse {
	return buildNeighborResponseFromReader(snapshot, flags, newRepoLineReader(snapshot.Header.RepoRoot))
}

func buildImpactResponseOnDisk(snapshot sem.ProviderSnapshot, flags impactFlags) impactResponse {
	return buildImpactResponseFromReader(snapshot, flags, newRepoLineReader(snapshot.Header.RepoRoot))
}

func symbolMatchBodiesOnDisk(repoRoot string, matches []sem.SymbolRecord, limit int) []symbolMatchBody {
	if repoRoot == "" {
		return nil
	}
	return symbolMatchBodiesFromReader(newRepoLineReader(repoRoot), matches, limit)
}
