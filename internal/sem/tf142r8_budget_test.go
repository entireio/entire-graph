package sem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tf142r8LaggingContext is a context whose Err() stays nil for the first
// tolerate polls and reports context.DeadlineExceeded afterwards. It flips the
// stop predicate directly instead of racing a clock, so nothing here can flake
// on a loaded runner.
type tf142r8LaggingContext struct {
	context.Context
	polls    int
	tolerate int
}

func (c *tf142r8LaggingContext) Err() error {
	c.polls++
	if c.polls <= c.tolerate {
		return nil
	}
	return context.DeadlineExceeded
}

// TestTF142R8BudgetGateFiresOnTheClockNotOnTheTimer is the invariant behind the
// windows-latest failure of TestTF142R7SelectiveDerivationStopsBeforeThePreprocessing.
//
// context.WithDeadline reports expiry through a runtime timer, so ctx.Err()
// flips one timer granularity AFTER the deadline actually passed: microseconds
// on Linux and macOS, up to a system tick (~15.6 ms) on Windows. Every loop
// that observed the budget only through ctx.Err() therefore kept working
// through that window, and the selective derivation's preprocessing is short
// enough to finish inside it -- which is why an already-expired budget still
// filtered in 1 file and 2 symbols there and 0 here.
//
// The condition is reproduced exactly and deterministically: a context whose
// deadline has NOT been reached (so its Err() is nil, as on the far side of the
// timer lag) paired with a budget deadline the clock has already passed.
func TestTF142R8BudgetGateFiresOnTheClockNotOnTheTimer(t *testing.T) {
	t.Parallel()

	base := time.Now()
	// The context's own timer is nowhere near firing.
	ctx, cancel := context.WithDeadline(context.Background(), base.Add(time.Hour))
	defer cancel()
	if ctx.Err() != nil {
		t.Fatalf("fixture: the context must not have stopped yet, got %v", ctx.Err())
	}

	gate := budgetGate{work: ctx, deadline: base.Add(-time.Millisecond), now: func() time.Time { return base }}
	if !errors.Is(gate.err(), context.DeadlineExceeded) {
		t.Fatalf("a budget the clock has passed must read as expired even while the context timer lags, got %v", gate.err())
	}
	if !gate.expired() {
		t.Fatal("expired() must agree with err()")
	}

	// Before the deadline the gate must be silent, or every unbudgeted run
	// would truncate itself.
	live := budgetGate{work: ctx, deadline: base.Add(time.Second), now: func() time.Time { return base }}
	if live.err() != nil {
		t.Fatalf("a budget that has not run out must not report expiry, got %v", live.err())
	}
	// With no opt-in budget the gate reports exactly what the context reports.
	none := budgetGate{work: ctx, now: func() time.Time { return base.Add(time.Hour * 24) }}
	if none.err() != nil {
		t.Fatalf("with no MaxDuration the gate must defer to the context, got %v", none.err())
	}
}

// TestTF142R8BudgetedReaderBoundsEveryContentScan is the structural close for
// the class both round-eight findings sit in: a stage that RECEIVES the
// budgeted context but never polls it. Round six proved no stage roots a fresh
// context; it did not prove every stage polls the one it was given.
//
// The expensive members of that class are expensive because they scan file
// CONTENT per file or per symbol, and every one of them reads through the
// single contentReader it is handed. Gating that reader bounds all of them --
// including producers nobody has audited -- to one in-flight read past expiry,
// with no producer signature changed.
func TestTF142R8BudgetedReaderBoundsEveryContentScan(t *testing.T) {
	t.Parallel()

	reads := 0
	raw := func(path string) (string, bool) {
		reads++
		return "content of " + path, true
	}

	base := time.Now()
	expired := budgetGate{work: context.Background(), deadline: base.Add(-time.Second), now: func() time.Time { return base }}
	if content, ok := expired.reader(raw)("a.go"); ok || content != "" {
		t.Fatalf("an expired budget must refuse the read, got %q ok=%v", content, ok)
	}
	if reads != 0 {
		t.Fatalf("an expired budget must not reach the underlying reader, got %d read(s)", reads)
	}

	live := budgetGate{work: context.Background(), deadline: base.Add(time.Hour), now: func() time.Time { return base }}
	content, ok := live.reader(raw)("a.go")
	if !ok || content != "content of a.go" {
		t.Fatalf("a live budget must pass the read through unchanged, got %q ok=%v", content, ok)
	}
	if reads != 1 {
		t.Fatalf("a live budget must reach the underlying reader exactly once, got %d", reads)
	}
	if live.reader(nil) != nil {
		t.Fatal("wrapping a nil reader must stay nil")
	}
}

// TestTF142R8RegistrationAliasScanStopsWhenTheBudgetIsGone is the narrowing
// direction of the provider.go:1038 finding: collectRegistrationAliases scans
// every commands/*.json file through the content reader before the first budget
// check existed, so a repository with many or large command manifests overshot
// MaxDuration by the whole alias pass. Measured with the real verb on 3,000
// 391 KB manifests: `snapshot --max-seconds 1` took 15.51 s before, 1.04 s after.
func TestTF142R8RegistrationAliasScanStopsWhenTheBudgetIsGone(t *testing.T) {
	t.Parallel()

	paths := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		paths = append(paths, fmt.Sprintf("commands/cmd%d.json", i))
	}
	reads := 0
	raw := func(path string) (string, bool) {
		reads++
		return `{"function":"handler"}`, true
	}

	base := time.Now()
	expired := budgetGate{work: context.Background(), deadline: base.Add(-time.Second), now: func() time.Time { return base }}
	aliases := collectRegistrationAliases(expired.expired, paths, expired.reader(raw))
	if reads != 0 {
		t.Fatalf("the alias scan read %d manifest(s) on an already-expired budget", reads)
	}
	if len(aliases) != 0 {
		t.Fatalf("an expired alias scan must index nothing, got %v", aliases)
	}

	// Widening: an unbudgeted scan is unchanged.
	live := budgetGate{work: context.Background(), now: time.Now}
	aliases = collectRegistrationAliases(live.expired, paths, live.reader(raw))
	if reads != len(paths) {
		t.Fatalf("an unbudgeted alias scan must read every manifest, got %d of %d", reads, len(paths))
	}
	if len(aliases["handler"]) != len(paths) {
		t.Fatalf("an unbudgeted alias scan must index every command, got %d of %d", len(aliases["handler"]), len(paths))
	}
}

// tf142r8NamespaceSource builds a TypeScript file with count namespaces and
// count same-named declarations. namespaceMergesDeclaration scans the whole
// namespace list once per declaration, so the post-parse merge filter is
// O(count^2) -- the work the js_scopes.go:149 finding says runs after the
// parse's own budget has been honoured.
func tf142r8NamespaceSource(count int) string {
	var b strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, "namespace NS%d { export function h%d() { return %d; } }\n", i, i, i)
	}
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, "function F%d() { NS%d.h%d(); }\n", i, i, i)
	}
	return b.String()
}

// TestTF142R8JSScopeWalkStopsAfterTheParse is the narrowing direction of the
// js_scopes.go:149 finding. Passing parseCtx to tree-sitter bounds the NATIVE
// parse only; the scope walk and the merge filter that run on the resulting
// tree never consulted the caller's context, so a large file kept the relation
// phase going past the budget. Measured with the real verb on one 16,000
// declaration TypeScript file: `snapshot --max-seconds 2` took 6.43 s before and
// 2.63 s after; `--max-seconds 3` took 6.21 s before and 3.05 s after.
//
// No stopwatch: the context flips its own predicate after a fixed number of
// polls, so the parse completes and the traversal is the thing that stops.
func TestTF142R8JSScopeWalkStopsAfterTheParse(t *testing.T) {
	t.Parallel()

	source := tf142r8NamespaceSource(400)
	// Two polls are the entry guards newJSScanState and buildJSScanState
	// already had before the parse; tolerating four means the parse ran to
	// completion and the traversal polled at least twice on the tree it built
	// before the context stopped it. On the unfixed code the count never gets
	// past two, because nothing after the parse polls at all.
	ctx := &tf142r8LaggingContext{Context: context.Background(), tolerate: 4}

	state, err := newJSScanState(ctx, "big.ts", source)
	if err == nil {
		t.Fatal("a context that stopped during the walk must surface an error, not a silently complete scan")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the walk must report the caller's stop reason, got %v", err)
	}
	if state.parsed {
		t.Fatal("a stopped walk must not report a parsed scope state")
	}
	if len(state.namespaces) != 0 || len(state.bindings) != 0 || len(state.calls) != 0 {
		t.Fatalf("a stopped walk must return the empty state, got %d namespace(s), %d binding(s), %d call(s)",
			len(state.namespaces), len(state.bindings), len(state.calls))
	}
	if ctx.polls <= ctx.tolerate {
		t.Fatalf("nothing after the parse polled the caller's context (%d poll(s), %d tolerated)", ctx.polls, ctx.tolerate)
	}
}

// TestTF142R8JSScopeUnbudgetedIsUnchanged is the widening direction: an
// uncancelled context must still produce the complete scope state, so the new
// polling cannot silently shrink an ordinary index.
func TestTF142R8JSScopeUnbudgetedIsUnchanged(t *testing.T) {
	t.Parallel()

	const count = 400
	source := tf142r8NamespaceSource(count)
	state, err := newJSScanState(context.Background(), "big.ts", source)
	if err != nil {
		t.Fatalf("an unbudgeted scope scan must succeed: %v", err)
	}
	if !state.parsed {
		t.Fatal("an unbudgeted scope scan must report a parsed state")
	}
	if len(state.namespaces) != count {
		t.Fatalf("an unbudgeted scope scan must find every namespace, got %d of %d", len(state.namespaces), count)
	}
	if len(state.calls) != count {
		t.Fatalf("an unbudgeted scope scan must find every dotted call, got %d of %d", len(state.calls), count)
	}
	// The F* declarations do not merge with any namespace, so every one of them
	// must survive the merge filter the new poll runs inside.
	if len(state.bindings) < count {
		t.Fatalf("an unbudgeted scope scan must keep every declaration binding, got %d of at least %d", len(state.bindings), count)
	}
}

// TestTF142R8ClockTriggeredStopStillMarksTheSnapshotTruncated closes the hole a
// level-triggered predicate opens if the sink is left reading the raw context.
//
// shouldStop fires the instant the clock passes the deadline. The context's own
// timer fires later. In that window the derivation stops but workCtx.Err() is
// still nil, so a sink that classifies off the context leaves budgetHit false:
// the snapshot is missing everything after the deadline, carries no
// E_ANALYSIS_BUDGET_EXCEEDED marker, reports completeness "ok", and is
// therefore CACHEABLE -- every later unbudgeted query would be served the gap
// as the complete index. That is the round-one HIGH finding's failure mode
// reintroduced through the back door.
//
// The window is reproduced deterministically rather than raced: MaxDuration is
// an hour, so the context timer is nowhere near firing, and the injected clock
// jumps two hours after the deadline is derived, so the gate is expired and the
// context is not.
func TestTF142R8ClockTriggeredStopStillMarksTheSnapshotTruncated(t *testing.T) {
	repo := tf142r5SelectiveRepo(t)
	cacheDir := t.TempDir()

	if _, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir, false); err != nil {
		t.Fatalf("warm the complete snapshot: %v", err)
	}

	// The clock stays live long enough for the preprocessing to complete, so
	// stopNow never fires and budgetHit is still false when the relation phase
	// starts; the budget then runs out DURING relation generation, where the
	// predicate is shouldStop (which deliberately records no reason). That
	// leaves the sink as the only place the truncation can be recorded.
	const livePolls = 12
	var polls atomic.Int64
	base := time.Now()
	selective := ProviderSnapshotOptions{
		Profile:     ProfileFull,
		OnlyFiles:   []string{"deep.js"},
		MaxDuration: time.Hour,
		nowFn: func() time.Time {
			if polls.Add(1) <= livePolls {
				return base
			}
			return base.Add(2 * time.Hour)
		},
	}
	snapshot, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test", selective, cacheDir, false)
	if err != nil {
		t.Fatalf("an explicit budget must truncate, not fail: %v", err)
	}
	if !partialFailuresTruncated(snapshot.Header.PartialFailures) {
		t.Fatalf("a derivation stopped by the clock must be marked truncated, got %#v", snapshot.Header.PartialFailures)
	}
	if snapshot.Header.Stats.CompletenessLevel != "unsafe" {
		t.Fatalf("a truncated derivation must be unsafe, got %q", snapshot.Header.Stats.CompletenessLevel)
	}
	if polls.Load() <= livePolls {
		t.Fatalf("the clock never got past the preprocessing (%d poll(s)), so the sink was not the thing under test", polls.Load())
	}
}
