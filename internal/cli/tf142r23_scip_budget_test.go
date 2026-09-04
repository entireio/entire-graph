package cli

import (
	"strings"
	"testing"
)

// TestTF142R23SCIPRejectsMaxSeconds pins the SCIP half of the
// "complete-snapshot format cannot carry a truncation" rule. `--format scip`
// writes a single binary protobuf Index; there is no record in that wire format
// that can say E_ANALYSIS_BUDGET_EXCEEDED, and a stderr note does not travel
// with the artifact. A SCIP consumer handed a budget-truncated Index therefore
// reads every symbol that was never reached as a confident negative. The
// combination has to be refused at the flag boundary, exactly like
// --format compact-ndjson.
func TestTF142R23SCIPRejectsMaxSeconds(t *testing.T) {
	repo := f06fixSmallRepo(t)

	_, _, err := f06fixRun(t, repo, "snapshot", "--repo", repo, "--format", "scip", "--max-seconds", "1", "--no-cache")
	if err == nil {
		t.Fatal("--max-seconds with --format scip must be rejected: a truncated SCIP Index is indistinguishable from a complete one")
	}
	if !strings.Contains(err.Error(), "scip") || !strings.Contains(err.Error(), "--max-seconds") {
		t.Fatalf("the error must name both flags, got %v", err)
	}
	// 0 (explicitly unlimited) is still fine: nothing can be truncated.
	if _, _, err := f06fixRun(t, repo, "snapshot", "--repo", repo, "--format", "scip", "--max-seconds", "0", "--no-cache"); err != nil {
		t.Fatalf("--max-seconds 0 with scip output must stay accepted, got %v", err)
	}
}
