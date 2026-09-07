# Return-flow relation profile diagnosis

Date: 2026-09-07

This note records a bounded diagnosis and the resulting source change. It is
not benchmark evidence and makes no end-to-end performance claim.

## Runtime evidence

The sixth diagnostic invocation ran `snapshot` with the `full` profile and the
extraction cache off against frozen Kubernetes commit
`b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe`. The Entire Graph source commit was
`25887f6954fc06e35bc3a7c699e3c524213dada4`. The single attempt timed out at
120 seconds and exited with `-9`; it had no cache-on arm, retry, or comparison.

The process log ended parsing at 54.239996094 seconds after observing 30,866
files and 418,711 symbols. It then logged the start of the relations phase but
never logged that phase's end. The complete CPU profile began at
54.201833659 seconds and ran for 20.031384297 seconds. Its trigger timestamp is
38 milliseconds before the logged phase boundary because of instrumentation
and log sequencing; its sampled product stacks are relation construction. It
covers only that early 20-second relations window and does not explain the
later unsampled portion before the timeout.

The profile contains 38.09 CPU-seconds of samples. Regular-expression execution
accounts for 19.89 cumulative sample-seconds. `returnFlowCalls` accounts for
8.66 cumulative sample-seconds, including 3.79 in
`expressionAssignedReturnFlows` and 3.74 in `assignmentFlowEvents`. The 3.74
sample-seconds are the maximum sampled target of this change, not an expected
saving and not evidence that the change will cure the timeout. Other sampled
work remains, including `goHTTPRouteRelations`, `receiverCallRelations`, and GC
marking. The profile includes profiler overhead. Peak RSS is unavailable because
the GNU time result was empty after the kill.

## Source conclusion and change

`expressionAssignedReturnFlows` previously extracted all assignment events
before scanning for a bare returned variable. It can emit a flow only while
iterating a `returnVarRe` match that passes the existing
`followsReturnedVariable` filter. When no such match exists, all assignment
events are discarded.

The implementation now obtains those same return matches first, filters the
match slice in place with the same eligibility predicate, and returns before
assignment extraction when the filtered slice is empty. If an eligible return
exists, it runs the unchanged event extraction and consumes the saved match
indexes with the same ordering and comparisons. `assignmentFlowEvents` is pure,
so this reordering preserves the observable result for arbitrary input text,
including malformed or cross-language-looking bodies. Direct call returns and
ordinary assigned returns remain handled by their existing paths in
`returnFlowCalls`.

The regression test carries the previous implementation, copied from the
pre-change Entire source, as a test-only legacy oracle. The fixture inputs were
independently authored from the required behaviors. They check exact
`returnFlowCall` values, order, reason, evidence kind, detail, and direction for
eligible and ineligible returns, conditional and fallback assignments,
destructuring, overwrite behavior, and malformed cross-language-looking text.

Focused validation used `go version go1.26.1 darwin/arm64`. These exact commands
and captured results passed:

```text
$ go test ./internal/sem -run '^TestReturnFlowCallsOrderIsTotal$'
ok  github.com/entireio/entire-graph/internal/sem  0.950s

$ go test ./internal/sem -run '^(TestExpressionAssignedReturnFlowsMatchesLegacyOracle|TestReturnFlowCallsKeepsDirectAndOverwrittenAssignmentFlows|TestBuildProviderSnapshotEmitsAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsDestructuredAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsBranchAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsFallbackAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsConditionalAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsPythonFallbackAssignedReturnDataFlow|TestBuildProviderSnapshotFallbackAssignedReturnHonorsOverwrite|TestBuildProviderSnapshotSequentialAssignmentKeepsLastReturnDataFlow)$'
ok  github.com/entireio/entire-graph/internal/sem  1.299s

$ go test -race ./internal/sem -run '^(TestExpressionAssignedReturnFlowsMatchesLegacyOracle|TestReturnFlowCallsKeepsDirectAndOverwrittenAssignmentFlows|TestBuildProviderSnapshotEmitsAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsDestructuredAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsBranchAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsFallbackAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsConditionalAssignedReturnDataFlow|TestBuildProviderSnapshotEmitsPythonFallbackAssignedReturnDataFlow|TestBuildProviderSnapshotFallbackAssignedReturnHonorsOverwrite|TestBuildProviderSnapshotSequentialAssignmentKeepsLastReturnDataFlow)$'
ok  github.com/entireio/entire-graph/internal/sem  2.610s
```

The full `internal/sem` package and `mise run check` were intentionally left for
the later integration check. The authoritative retained full check still covers
source `25887f6954fc06e35bc3a7c699e3c524213dada4`; it is historical evidence for
this newer source.

## Evidence boundary

No corpus evaluator, benchmark, VM, retry, or comparison was run for this
change. A later immutable diagnostic would be required to measure runtime
effects. The later relations-phase behavior remains unresolved, and this small
change does not by itself establish that the 120-second timeout is fixed.

## Consulted sources

- `AGENTS.md` and `.entire/graph-agent.md` in the Entire Graph repository.
- `entire-plan/entire-graph-advantage-implementation-plan.md`,
  specifically the P1 characterization, equivalence, and release-gate rules.
- `entire-plan/graph-advantage-progress-review-2026-09-05.md`.
- `docs/implementation/graph-advantage/evidence/diagnostic-dispatch-25887f69/controller/runtime-manifest.json`.
- The same diagnostic's raw process manifest, outcome, process log, profile
  status, and `relations-cpu.pprof`, inspected with local `go tool pprof`.
- Focused current source in `internal/sem/types.go`, `internal/sem/provider.go`,
  and `internal/sem/symbol_body.go`, plus the existing return-flow tests.

No competitor or comparison documents, prior transcripts, or memory were used.
