# P2 implementation and validation

Scope: optional Linux gopls v0.20.0 backend, always default off. This is a
correctness/coverage report, not a comparative recall or performance claim.

| Plan task | Implemented and checked | Remaining release evidence |
|---|---|---|
| P2.1 | Pinned server; Bubblewrap whole-process-tree network namespace; read-only capsule; unprivileged Linux positive navigation and hostile grandchild probes | No broader platform claim; macOS/Windows unavailable |
| P2.2 | Captured capsule and package closure, context identity, UTF-16 mapping, bounded stdio lifecycle/configuration/cancellation, edit rejection and process cleanup | Ambient/external module cache import intentionally unsupported; missing closure reports unavailable |
| P2.3 | Live concrete/promoted method, generic call, interface declaration and separate implementations, workspace alias import, local replacement, build tags and closure-variable location | Dynamic closure target inference remains unresolved rather than guessed |
| P2.4 | Exact declaration-token mapping, call-site reconciliation, separate candidate category, effective-view filtering limited to disputed site, unchanged native facts, compact/SCIP refusal | Public schema/help integration and complete repository checks tracked by main ledger |
| P2.5 | Caller/target digest validation; context invalidation for source/dependency, workspace, tags, package set, server and toolchain; live unchanged caller with changed tags; explicit unavailable dependencies | Frozen hard-Go static-versus-compiler direct recall/precision evaluation has not been run; improvement gate unproven |

Reconciliation stores `call_site_line` only to identify an otherwise unambiguous
line-addressed static evidence item. Removing that item from an enriched view
never removes unrelated evidence sites or changes the native snapshot. Multiple
direct answers remain ambiguous. The experimental candidate relation is
`X-entire-graph:COMPILER_IMPLEMENTATION_CANDIDATE` and has no runtime-call
confidence.

The Linux live tests use the actual production launcher. A descendant cannot
connect to a host listener or route a TCP connection to the reserved TEST-NET
address; the latter returns network-unreachable. Source mutation returns EROFS.
Cancellation kills the namespace process tree; a bounded best-effort LSP
`$/cancelRequest` is sent even when cancellation races initial send completion.
All protocol writes are serialized. Runtime package discovery never downloads,
runs generators/tests, or imports an ambient host dependency cache.

Sources consulted for this continuation: the complete implementation plan,
applicable graph-agent instructions, Entire compiler/overlay implementation and
ADRs 0034/0036. Protocol semantics follow the official LSP/gopls/Go sources
already recorded in ADR 0034; no new external implementation source was used.
Fixtures in `internal/compiler/adapter_live_test.go`, `process_linux_test.go`,
`adapter_test.go` and `internal/sem/compiler_overlay_test.go` are independently
authored from the plan. Expected declaration names/token offsets and negative
cases are hand-derived, with actual pinned gopls results checked against them.

Evidence:

- `evidence/linux-p2-v4.txt`: final Linux race run, including live backend and semantic mapping (source file hashes in `linux-source-p2-v4.json`).
- `evidence/compiler-final-local.txt`: local race compiler/overlay regression run.
- `evidence/compiler-cancellation-race.txt`: ten repetitions after the initial cancellation race fix.
- `evidence/linux-p2-v2-initial-failure.txt`: retained nested-module package-pattern failure; fixed by querying module import paths.
- `evidence/linux-p2-v3.txt` and `compiler-cancellation-initial-failure.txt`: retained cancellation send-completion race failure; fixed on both cancellation paths.
- Earlier Linux v2 pass establishes the live source/network/closure/build-tag tests independently of the later cancellation fix.

Reproduction on the dedicated pinned Linux environment:

```
ENTIRE_GRAPH_COMPILER_LIVE=1 go test -race -v -timeout 30m ./internal/compiler ./internal/sem -run 'Test(Compiler|LiveCompiler|MapLocation|RPC|Capsule)'
```

Rollback: select `--compiler off` (the default); compiler results are never
persisted and the native static graph remains available. Do not report the hard-Go
recall improvement gate as passed from these contract fixtures alone.
