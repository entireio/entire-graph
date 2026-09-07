# Route-prefilter relations CPU diagnostic preparation (p1-diag-450bede9-k8s-route-prefilter-off-01)

This is a new fail-closed preparation package for exactly one bounded
Kubernetes full-profile, cache-off snapshot diagnostic. It has not launched a
VM, claimed a worker, called the evaluator, accessed the corpus, or made a
performance/admission claim. The prior 90ac3e16 dispatch and all of its runtime
artifacts remain separate and are not copied here.

The selected cell is worker 1, trial 0, `arms=[false]`, with one total
invocation, no retry, no ON arm, no warm invocation, a 120-second request
deadline, and profiler opt-in (`ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE=1`).
The profiler contract remains a 20-second window, 90-second latest start, and
8 MiB bound.

The post-fix provider source commit is pinned to
`450bede9970e05c994d80986cf105dd068e9fc29` at remote build root
`/opt/graph-validation/diagnostics-linux-450bede9`; the binary SHA-256 is
`b4d7e1a5ef72d4e09d045e562ce08cb38ff63a9c3912aef0e002bc70bb5f0674`, the source archive SHA-256 is
`d67f74b99b16dd081fdd956f820fae16be2eb524b772055914faf1fe5b72cda6`, and the build manifest SHA-256 is `80e1dafd3a861be1cbba67fec08fd717c89cd2860dd3502aab947fe73c7fe546`. The frozen corpus identity is also bound. The fresh immutable full-check for
450bede9970e05c994d80986cf105dd068e9fc29 passed with the source-specific gate
and evidence hashes in `full-check-gate.json`; execution has not been
launched. The previous 90ac3e16 full-check result does not certify this
changed provider.

Only reusable control source, synthetic regression tests, and the selected
corpus manifest/scenario were copied. No old runtime, claim, raw result,
source archive, binary, VM evidence, or execution proof is present.
