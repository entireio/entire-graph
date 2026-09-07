# Kubernetes diagnostic source-review input

This directory is an unscored, unlabelled input packet for source review of the one retained cache-off syntax-only snapshot diagnostic. The ordered failure and warning arrays are lossless copies from `raw/output/diagnostics.json`; preserve their order and do not deduplicate or infer eligibility.

The packet contains source/configuration identities and semantic/coverage digests needed to bind review to the retained observation. It intentionally contains no elapsed time, RSS, process resource, or performance outcome fields. The raw archive remains unchanged beside this packet.

- Packet: `ordered-partials-v1.json`
- Packet SHA256: `610e298c5c882356b363974c2fdb75392de7659b0cc99b75ef16e7bd82474f3e`
- Source inventory: `source-inventory.json`
- Partial records: `194`
- Warning records: `1`
- Source review status: pending; no adjudication or adoption is recorded here.
