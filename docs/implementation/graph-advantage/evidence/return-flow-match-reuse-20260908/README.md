# Return-flow regex match reuse

## Scope and evidence basis

This change follows the P1 sequencing and correctness constraints in
`entire-plan/entire-graph-advantage-implementation-plan.md`
and uses only the current Entire source plus the retained seventh diagnostic
artifacts under `../diagnostic-dispatch-950567a3/controller/raw/`. The retained
late CPU profile identified repeated regex work in the return-flow path; it did
not establish a timing improvement or show that repeated stacks were
pathological.

The implementation is limited to one `returnFlowCalls` invocation and three
exact patterns over its stripped body:

- `returnVarRe` indexes are computed once and filtered into a separate slice,
  leaving the shared raw indexes unchanged.
- `assignCallRe` indexes are computed lazily and reused by the expression and
  direct assignment consumers. The expression scanner still returns before its
  assignment scans when there is no eligible bare returned variable.
- The eight full-body forwarding scanners reuse one lazy `flowCallSiteRe`
  result. The callback-substring scanner remains independent because its input
  is a callback substring rather than the full body.

Standalone helper entry points retain their previous behavior by creating
their own invocation-local match facts. There is no global cache, parser
context, or shared mutable state.

## Fixtures

`internal/sem/return_flow_match_reuse_test.go` was independently authored for
this change. Its handwritten exact-output fixture covers the eight shared
full-body forwarding scanners and separately covers the callback-substring
scanner. Differential fixtures compare `returnFlowCalls` with an isolated
composition of the current helper entry points across malformed mixed-language
text, UTF-8 surrounding ASCII captures, repeated callees and deterministic
ordering, and the no-parameter gate. Additional regressions cover mixed
eligible/ineligible returns without raw-index mutation, lazy loading, and
concurrent independent bodies.

The first test-first construction run failed to compile because the new fact
APIs had not been added yet. It was not a reproduced behavioral defect. The
initial handwritten fixture expectation also omitted the existing
`mixed(input, other)` flows and was corrected to match existing behavior; this
was not a product regression. The
behavioral evidence is the handwritten expected output, the isolated-versus-
shared differential checks, and the existing return-flow regression tests.

## Validation

Executed with `go version go1.26.1 darwin/arm64` at source base
`c07a5151774a49338ee8f654286a47d023b483a6`:

```text
go test ./internal/sem -run '^(TestExpressionAssignedReturnFlowsMatchesLegacyOracle|TestReturnFlowCallsKeepsDirectAndOverwrittenAssignmentFlows|TestReturnFlowCallsOrderIsTotal|TestReturnFlowMatchReuse.*|TestReturnFlowMatchFactsStayLazyAndDoNotMutateReturnMatches)$' -count=1
ok github.com/entireio/entire-graph/internal/sem 0.670s

go test -race ./internal/sem -run '^(TestExpressionAssignedReturnFlowsMatchesLegacyOracle|TestReturnFlowCallsKeepsDirectAndOverwrittenAssignmentFlows|TestReturnFlowCallsOrderIsTotal|TestReturnFlowMatchReuse.*|TestReturnFlowMatchFactsStayLazyAndDoNotMutateReturnMatches)$' -count=1
ok github.com/entireio/entire-graph/internal/sem 2.147s

go test ./internal/sem -run '^TestCapturedPreselectionTermPresenceMatchesGitContract$'
ok github.com/entireio/entire-graph/internal/sem 1.083s
```

The last command is the exact `VERIFY:` command returned by the locating graph
query. No benchmark, corpus evaluation, cloud action, full check, or runtime
performance claim is part of this evidence. A new immutable full check and
source-bound evaluator build remain required before any later product
diagnostic can evaluate this source.
