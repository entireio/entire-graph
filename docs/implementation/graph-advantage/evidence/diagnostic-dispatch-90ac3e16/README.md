# Relations CPU-profile diagnostic dispatch (90ac3e16)

This is a prepared, fail-closed control package for exactly one bounded Kubernetes
full-profile snapshot diagnostic. It has not launched a VM, claimed a worker,
called the evaluator, accessed the corpus, or made a performance/admission claim.
The prior `diagnostic-dispatch-12574522` and `diagnostic-dispatch-12574522-r2`
packages are unchanged.

The selected cell is cache-off, `worker=1`, `trial=0`, `arms=[false]`, with a
120-second evaluator deadline, no retry, no ON arm, no comparison, and the
existing cap-100/budget/source controls. The collector explicitly sets
`ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE=1`. The evaluator writes
`relations-cpu.pprof` and `relations-cpu.pprof.json` beside the observation. A
complete profile must report the 20-second window, start no later than 90 seconds,
remain at most 8 MiB, match its declared byte count and SHA-256, and carry the
diagnostic-only, no-performance-claim, and overhead markers. Timeout or incomplete
status is retained as diagnostic evidence and is never promoted as a complete
request. A `missed_safe_window` status may report its late trigger and is retained
as an incomplete diagnostic.

The evaluator source is pinned to `90ac3e16b96c34ab219ecd2a90eca4600fd0c586`,
the binary SHA-256 is
`43c85dfa31358f4911d565e005ddc943cf58d8ddc09790f60714e152e241b86b`, and the
input manifest/source digest are bound to the retained Kubernetes corpus identity.
The committed Linux build manifest is copied from
`evidence/diagnostics-linux-90ac3e16/build-manifest.json` and verified at
SHA-256 `210ee00a9b340434bdf38e22aa18f547a2dd4dbe2db2b60128dab1033a077611`.
The immutable full-check gate is passed by the exact same source revision
`90ac3e16b96c34ab219ecd2a90eca4600fd0c586`; its scoped source difference set is
empty and the covered Git object identities match. The gate binds the retained
result, raw log, and source-integrity files under
`../check-90ac3e16-retry-1/` (SHA-256 values are recorded in
`full-check-gate.json`). The recorded command is exactly
`MISE_JOBS=1 GOFLAGS=-p=1 GOMAXPROCS=2 GIT_TERMINAL_PROMPT=0 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null mise run check`.
Controller preflight now validates those immutable files, hashes, command,
source heads, clean status, and empty scoped diff before any cloud or claim
operation. This package remains prepared-only and has made no VM, cloud,
collector, evaluator, corpus, or benchmark invocation.

`control-files.tar.gz` contains only the copied control modules, selected corpus
manifest/scenario, and bound build/batch manifests. It contains no raw results,
claims, binary, source archive, or full-check evidence. The controller does not
own VM lifecycle or deallocation; an external worker must deallocate in its
finalization path.

## Origins and checks

Control templates came from retained
`evidence/diagnostic-dispatch-12574522-r2/{collector,controller,corpus}`. The
profile contract is from
`internal/sem/extraction_corpus_relations_cpu_profile_test.go`: fixed artifact
names, 20-second window, 90-second latest start, 8 MiB bound, atomic status, and
diagnostic markers. Tests use temporary files, fake processes/transports, and
local module imports only; they do not invoke Go, the evaluator, a corpus, Azure,
a VM, or the network. Focused results are retained in
`collector-tests.txt`, `controller-tests.txt`, and `verification.json`: 8 retained
r2 collector regressions plus 11 CPU-profile/identity tests, and 21 retained r2
controller regressions plus 4 package tests.
