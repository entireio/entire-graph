# Prepared 25887f69 Kubernetes snapshot diagnostic

Dispatch `p1-diag-25887f69-k8s-route-boundary-off-01` is prepared for one
cache-OFF, full-profile, snapshot diagnostic against the frozen Kubernetes
input at commit `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe` and effective tracked-input
SHA-256 `d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`.
It uses evaluator source `25887f6954fc06e35bc3a7c699e3c524213dada4`
and binary SHA-256
`661629c22ab2f633a92871a4db1de5b56612b81d43c8bd91c6496d2ecc7f589a`.

The full-check gate consumes the immutable archive-native evidence committed at
`2bd309ebbefea49fd5daa7f4f21fe7249a76a49b`. It validates the exact source and
binary bindings, zero overall and check exits, the actual ordered mise task
commands, full-check result and raw-log hashes, and identical before/after
2,292-entry Git `100644`/`100755` mode-and-content manifests with SHA-256
`1691ddc7be3f1e8f274779197e0b741a04a3fe717b063488effdc08a2d85fb07`.
The source provenance binds archive SHA-256
`16f5968934aaff779eab8801e27f0183f44a8dad86e5f7ed6edc5358811fb453`.
The full check passed with 145 statusline checks, zero failures, and three
disclosed platform/ownership skips. This package makes no full-coverage claim.

The selected dispatch contains one derived product invocation, zero
preparatory invocations, one OFF arm, and no ON arm, warm-up, retry, comparison,
stability sample, or admission. The evaluator process deadline is 120 seconds;
the relations CPU profile window is 20 seconds with the retained 90-second
latest-start and 8 MiB limits. The existing global batch ceiling remains 100,
while this bounded diagnostic's effective attempt cap is exactly one.

Current reservation and product invocation counts are zero. There is no
`controller/dispatch-claim.json`. The exact future controller invocation, only
after independent review and authorization, is:

```sh
python3 docs/implementation/graph-advantage/evidence/diagnostic-dispatch-25887f69/controller/controller.py --execute
```

That controller would first create the durable claim, revalidate every package
hash, remote binary hash, active-service guard, and fresh output/claim paths,
then invoke exactly one process equivalent to:

```text
/usr/bin/time -v -- <p1-evaluator> -test.run=^TestExtractionCorpusMeasurement$ -test.count=1 -test.v -test.timeout=130s
```

The collector enforces the separate 120-second wall deadline and runs the
process with the frozen corpus root, no-egress Go/Git environment, cache OFF,
full profile, snapshot operation, and relations CPU profiling enabled.

This is preparation only. The controller, evaluator, product, corpus, and
diagnostic have not been invoked; no claim has been created. Full P1 campaign
execution remains outside this package and requires separate user approval.
