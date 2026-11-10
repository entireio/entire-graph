package sem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
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

// TestTF142R8CancellableReaderBoundsEveryContentScan is the structural close
// for the class both round-eight findings sit in: a stage that RECEIVES the
// caller's context but never polls it. Round six proved no stage roots a fresh
// context; it did not prove every stage polls the one it was given.
//
// The expensive members of that class are expensive because they scan file
// CONTENT per file or per symbol, and every one of them reads through the
// single contentReader it is handed. Gating that reader bounds all of them --
// including producers nobody has audited -- to one in-flight read past a
// cancellation, with no producer signature changed.
func TestTF142R8CancellableReaderBoundsEveryContentScan(t *testing.T) {
	t.Parallel()

	reads := 0
	raw := func(path string) (string, bool) {
		reads++
		return "content of " + path, true
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if content, ok := cancellableReader(canceled, raw)("a.go"); ok || content != "" {
		t.Fatalf("a cancelled context must refuse the read, got %q ok=%v", content, ok)
	}
	if reads != 0 {
		t.Fatalf("a cancelled context must not reach the underlying reader, got %d read(s)", reads)
	}

	live := context.Background()
	content, ok := cancellableReader(live, raw)("a.go")
	if !ok || content != "content of a.go" {
		t.Fatalf("a live context must pass the read through unchanged, got %q ok=%v", content, ok)
	}
	if reads != 1 {
		t.Fatalf("a live context must reach the underlying reader exactly once, got %d", reads)
	}
	if cancellableReader(live, nil) != nil {
		t.Fatal("wrapping a nil reader must stay nil")
	}
}

// TestTF142R8RegistrationAliasScanStopsWhenCanceled is the narrowing direction
// of the provider.go:1038 finding: collectRegistrationAliases scanned every
// commands/*.json file through the content reader before anything observed a
// cancellation, so on a repository with many or large command manifests a
// cancelled run still paid for the whole alias pass (15.5 s on 3,000 391 KB
// manifests).
func TestTF142R8RegistrationAliasScanStopsWhenCanceled(t *testing.T) {
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

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	aliases := collectRegistrationAliases(ctxStop(canceled), paths, cancellableReader(canceled, raw))
	if reads != 0 {
		t.Fatalf("the alias scan read %d manifest(s) on an already-cancelled context", reads)
	}
	if len(aliases) != 0 {
		t.Fatalf("a cancelled alias scan must index nothing, got %v", aliases)
	}

	// Widening: an uncancelled scan is unchanged.
	live := context.Background()
	aliases = collectRegistrationAliases(ctxStop(live), paths, cancellableReader(live, raw))
	if reads != len(paths) {
		t.Fatalf("an uncancelled alias scan must read every manifest, got %d of %d", reads, len(paths))
	}
	if len(aliases["handler"]) != len(paths) {
		t.Fatalf("an uncancelled alias scan must index every command, got %d of %d", len(aliases["handler"]), len(paths))
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
// phase going after the caller had stopped: on one 16,000-declaration
// TypeScript file the walk alone ran for about 4 s after the parse.
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
