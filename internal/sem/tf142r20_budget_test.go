package sem

import (
	"fmt"
	"testing"
)

func TestTF142R20PartialTypeCanonicalIDsStopsWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()
	const symbolsPerFile = stopPollStride*3 + 5
	files := []FileRecord{{RecordType: "file", ID: "file:a.cs", Path: "a.cs"}}
	recordsByFile := map[string][]SymbolRecord{"a.cs": make([]SymbolRecord, symbolsPerFile)}
	for i := range symbolsPerFile {
		recordsByFile["a.cs"][i] = SymbolRecord{
			RecordType:    "symbol",
			Kind:          "class",
			Name:          fmt.Sprintf("PartialType%d", i),
			QualifiedName: fmt.Sprintf("Ns.PartialType%d", i),
			Signature:     "public partial class PartialType",
			Language:      "C#",
			FilePath:      "a.cs",
		}
	}

	calls := 0
	stop := func() bool {
		calls++
		return calls >= 2
	}
	canonical, canonicalFile := partialTypeCanonicalIDs(stop, files, recordsByFile)
	if len(canonical) != 0 || len(canonicalFile) != 0 {
		t.Fatalf("stopping inside the prepass must retain no canonical map, got %d/%d entries", len(canonical), len(canonicalFile))
	}
	if calls > stopPollStride*2 {
		t.Fatalf("partialTypeCanonicalIDs polled shouldStop %d times before stopping: want an inner-loop poll", calls)
	}
}

func TestTF142R20RegistrationAliasFinalizeStopsWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()
	read := func(string) (string, bool) { return `{"function":"sharedHandler"}`, true }
	paths := []string{"commands/z.json", "commands/a.json"}

	calls := 0
	stop := func() bool {
		calls++
		return calls >= 3
	}
	aliases := collectRegistrationAliases(stop, paths, read)
	got := aliases["sharedHandler"]
	if len(got) != 2 {
		t.Fatalf("both manifests should be indexed before finalize, got %v", got)
	}
	if got[0] != "z" || got[1] != "a" {
		t.Fatalf("finalize must skip sort/dedupe once stopped: got %v, want [z a] in append order", got)
	}
}
