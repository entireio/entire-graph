# Remaining admission evidence audit

The six baseline timeout rows are three repetitions each of Kubernetes fast
and full snapshots. Every retained row says the fixed 120-second deadline
expired and reports process exit -9. They contain no completed phase result.
`baseline-timeouts.json` retains each full original row, archive hash, member
and line. These are missing baseline evidence, not zero-duration observations
or proof that a particular phase remains defective in the current source.

The completed 6cf92c9c syntax-only snapshot is a different profile. It cannot
close these fast/full cases. The three retained query profile replays close
query equivalence cases, not snapshot baseline timeouts. No reproduction,
benchmark, VM or provider execution was run for this audit.

## Full diagnostic evidence contract

P1 validation in the requirements plan includes malformed files and exact
output equivalence. The current harness records the full diagnostic count and
digest but samples only the first 32 entries. A 194-entry result therefore
cannot be reviewed completely from the saved sample alone. A digest proves
identity between complete sets; it does not reveal their omitted contents.

Add an optional explicit diagnostic artifact path to the test-only harness.
The artifact must contain all partial failures and warnings, full digests and
counts, request/source/binary identities, and enough provenance to associate
it with its observation. Write it after the request clock stops. Refuse an
existing destination, including a final symlink. Any write failure must remain
visible in the saved observation and fail its eligibility. With no path,
existing behavior and bounded observation rows remain unchanged.

This is an evidence interface, not an admission-policy change. Existing
zero-partial canary rules and all historical classifications remain unchanged.
Artifact collection itself can affect externally measured whole-process RSS;
any future run must freeze that setting and apply it symmetrically. No earlier
result gains full-review status retroactively. Tests use independent synthetic
diagnostic arrays, including records beyond the sample boundary; no benchmark
result supplies an expected score or performance target.

Sources: requirements plan P1 validation/completion, current
`internal/sem/extraction_corpus_evaluation_test.go`, frozen baseline archive
rows, `stopgaps-v2.md`, and retained corrective summaries. No external or
comparative implementation was consulted.

## Implemented interface

Set `"diagnostics_path": "diagnostics.json"` in the existing corpus request
manifest. Relative paths resolve against that manifest. The path must differ
from the observation output even through an existing parent-directory alias.
The helper rejects invalid output-path configurations before product work and
checks them again before writing. Existing diagnostic destinations are never
overwritten. A post-request artifact error makes the saved observation an
error when its output remains safe; if that output itself becomes unusable,
the process fails rather than overwriting a conflicting artifact.

The artifact retains the full arrays (including null versus empty arrays),
their original digests, request options, source/binary identity and observation
path. The external runner must still bind and collect this file with its
frozen request and corpus manifests. This change does not launch collection
or mark any old sampled result fully reviewed.

Eight focused tests include one actual tiny-repository subprocess smoke and
an aliased-path preflight rejection, complete arrays beyond 32 entries,
existing-file/final-symlink preservation, post-call error reporting, relative
manifest paths and byte-identical disabled output. Worker-reported final
focused test time: 1.191s; race: 3.305s. The graph-requested opt-in test command
completed in 0.650s without a corpus configured (skip, not corpus evidence).
