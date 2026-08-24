package sem

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTF142R20IsOptInBudgetExceededRejectsCallerDeadline(t *testing.T) {
	t.Parallel()
	base := time.Now()
	callerDL := base.Add(10 * time.Millisecond)
	budgetDL := base.Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), callerDL)
	defer cancel()
	workCtx, cancelWork := context.WithDeadline(ctx, budgetDL)
	defer cancelWork()
	now := func() time.Time { return callerDL.Add(time.Millisecond) }
	gate := newBudgetGate(workCtx, budgetDL, now)

	if isOptInBudgetExceeded(ctx, gate, budgetDL, time.Hour, context.DeadlineExceeded) {
		t.Fatal("a stricter caller deadline must not classify as opt-in budget truncation")
	}
}

func TestTF142R20IsOptInBudgetExceededAcceptsOptInBudget(t *testing.T) {
	t.Parallel()
	base := time.Now()
	budgetDL := base.Add(time.Millisecond)
	ctx := context.Background()
	workCtx, cancelWork := context.WithDeadline(ctx, budgetDL)
	defer cancelWork()
	now := func() time.Time { return budgetDL.Add(time.Millisecond) }
	gate := newBudgetGate(workCtx, budgetDL, now)

	if !isOptInBudgetExceeded(ctx, gate, budgetDL, time.Second, context.DeadlineExceeded) {
		t.Fatal("an expired opt-in budget must classify as truncation")
	}
}

func TestTF142R20CallerDeadlineWithMaxDurationIsAnError(t *testing.T) {
	repo := budgetBombRepo(t)
	base := time.Now()
	callerBudget := 50 * time.Millisecond
	ctx, cancel := context.WithDeadline(context.Background(), base.Add(callerBudget))
	defer cancel()

	_, err := BuildProviderSnapshotWithOptions(ctx, repo, "test", ProviderSnapshotOptions{
		MaxDuration: time.Hour,
		nowFn:       func() time.Time { return base.Add(callerBudget + time.Millisecond) },
	})
	if err == nil {
		t.Fatal("caller deadline before MaxDuration must surface as an error, not truncate")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

func TestTF142R20PartialTypeCanonicalIDsStopsWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()
	const symbolsPerFile = budgetPollStride*3 + 5
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
	if calls > budgetPollStride*2 {
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
