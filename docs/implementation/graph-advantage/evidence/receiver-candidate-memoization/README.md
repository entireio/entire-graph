# Receiver candidate memoization

## Scope and evidence

The retained delayed relations profile at source
`950567a3c2796c99c9f46af84f7c0a8afac3351d` attributes 1.58 CPU-seconds
cumulatively (5.15% of 30.66 sampled CPU-seconds) to
`sharedTypeCandidates`; `receiverCallRelations` accounts for 7.03 cumulative
CPU-seconds. The source listing attributes 1.23 seconds to the initial
qualified-method call at the former `provider.go:7494`, 460 ms to the Go
unique-method fallback at `:7832`, 130 ms to the static type lookup at `:7610`,
and 10 ms to the typed receiver lookup at `:7604`. These are profiler samples
from one late 20.033-second window of a timed-out run. They identify repeated
work; they do not measure the change's speedup or show that it cures the
timeout.

Within one `receiverCallRelations` invocation, `from` and
`symbolsByShortName` are fixed. The affected resolution passes repeatedly ask
`sharedTypeCandidates` for the same short method or type name. The change adds
a lazy invocation-local map keyed by that short name and routes the bounded
set of reviewed passes through it. It does not prepopulate the workspace index
or retain results after the function returns. The map lookup uses the presence
boolean, so a cached nil slice remains distinguishable from an absent entry.

All consumers of the cached slices were audited. They range over records and
either return a value or append matches into a new local slice. None sorts,
reslices, overwrites, or appends into the supplied slice. In particular,
`receiverQualifiedMethodTarget`, `perlCallableForType`,
`typeLikeNamedWithMethod`, `firstTypeLikeNamedPreferFile`,
`dartSetterAccessor`, `uniqueMethodByShortName`, and their downstream
selectors leave the input untouched. `sharedTypeCandidates` itself uses a
full slice expression with zero capacity, so its result cannot append into the
workspace index's backing array.

The regression fixture was independently authored for this change. It fixes
the exact emitted relation fields for repeated `unknown.Ping()` calls in Go
while a TypeScript method with the same short name is present but ineligible.
Even when the scanner collapses the duplicate receiver/method pair, the
retained pair requests `Ping` in the initial qualified-symbol pass and again in
the Go unique-method fallback. The expected result locks target, file,
confidence, reason, scope, resolution, order, and call-site evidence.

Consulted sources were `AGENTS.md`, `.entire/graph-agent.md`, the current
`internal/sem/provider.go`, `internal/sem/langcompat.go`, the focused consumer
helpers in `internal/sem/{provider,perl,dart,swift,csharp}.go`, the approved
implementation plan and progress review, and the retained delayed profile in
`diagnostic-dispatch-950567a3/controller/raw/`. No competitor material,
historical transcript, benchmark, evaluator, VM, or network operation was
used.

## Validation

Environment: `go version go1.26.1 darwin/arm64`.

Final focused checks:

- `go test ./internal/sem -run '^(TestReceiverCallRelationsMemoizedCandidatesPreserveOutput|TestBuildProviderSnapshotResolvesReceiverCalls)$' -count=1`
  passed (`ok`, package time 0.889s; command wall time 7.853s).
- `go test -race ./internal/sem -run '^(TestReceiverCallRelationsMemoizedCandidatesPreserveOutput|TestBuildProviderSnapshotResolvesReceiverCalls)$' -count=1`
  passed (`ok`, package time 2.134s; command wall time 13.264s).
- Graph `VERIFY`: `go test ./internal/sem -run '^TestZigBareAndReceiverCallsResolve$'`
  passed (`ok`, package time 0.732s; command wall time 2.080s).
- `git diff --check` passed after formatting.

Two fixture-authoring attempts preceded the final checks. The first could not
compile because the concurrently edited return-flow test referenced source
symbols that had not landed yet; this was a moving shared-package state, not a
receiver regression. After that source was frozen, the first fixture run
showed that the legacy fallback's exact resolution field is `name_only`, while
the draft expectation said `type_inferred`. Correcting the expectation to the
pre-change observable value produced the final passing checks above.
