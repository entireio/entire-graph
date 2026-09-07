# Route-boundary diagnostic preparation

Dispatch: `p1-diag-8689fc3d-k8s-route-boundary-off-01`.

This new package is prepared for exactly one Kubernetes cache-OFF,
full-profile snapshot diagnostic at source
`8689fc3d790e4290ce2b62500d270c96c02087b3`. The pinned Linux r1
affected-normal, affected-race, compiler correctness, and evaluator-build
evidence is complete and bound by exact identities in
`diagnostic-validation-gate.json` and `manifest.json`. This is prepared-only
state; execution still requires independent controller/package review.

The first pinned Linux evidence attempt failed in transport before extraction,
tests, or build. It cannot satisfy this gate. Its failed evidence remains in
`diagnostics-linux-8689fc3d`; only a complete reviewed retry at
`diagnostics-linux-8689fc3d-r1` may supply the pending stage results.

The package uses `diagnostic-validation-v1`. It is not admission eligible, not a
comparison, not a stability sample, not benchmark or release evidence, and does
not authorize a full campaign. The passed full check at `450bede9` is historical
evidence for that source only and is neither copied nor cited as a current pass.
A fresh immutable `mise run check` remains required on the final exact source
before implementation admission, sampling, merge, or release.

Only reusable control inputs from `diagnostic-dispatch-450bede9` may be copied
when finalizing this package. Do not copy its claim, transport, results, raw
runtime artifacts, verification claims, or `full-check-gate.json`. A failed
preflight must create no claim. No cloud, VM, product, or corpus action has been
performed by this preparation.
