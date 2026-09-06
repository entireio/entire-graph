# ADR 0049: prospectively reviewed partial-coverage evaluation strata

Status: proposed; no campaign admission change is active.

## Requirement and problem

The requirements plan P1 validation includes malformed files (line 118),
requires exact semantic equivalence (line 122), and preserves explicit partial
semantics (lines 42 and 303–305). Its evaluation rules require visible
incomplete runs and denominators (line 390). We infer from those requirements, rather than from an explicit permission,
that completed latency can be compared with honest partial-coverage labels.

Our first frozen protocol separately required both latency arms to be `ok`
(`p1-corpus-20260905/protocol.md`, measurement and failure handling). Its
canary inherits that restriction. The later user-directed stopgap pauses on
an *unreviewed* partial. A fixed corpus containing intentionally excluded or
malformed inputs can therefore preserve exact outputs yet never satisfy our
original complete-only canary. Adjudication does not make those outputs
complete, and changing their status would violate the plan.

This is a protocol-design discrepancy, not evidence that any performance gate
has passed. Earlier failed observations and protocol classifications remain
immutable. The 194-entry snapshot result is not fully reviewed merely because
its complete digest matches; its old retained sample exposes only 32 entries.

## Proposed decision

After collecting and reviewing complete diagnostic evidence, prepare a new
protocol and run identity with two explicit coverage strata: `complete` and
`reviewed_partial`. No implementation or execution of the second stratum is
admitted by this proposal alone.

A reviewed-partial observation remains `status=partial`; its full diagnostic
arrays, warning arrays, completeness and counts remain visible. Its completed
latency may be compared only against the matching arm in the same predefined
coverage stratum. It cannot support a complete-coverage claim. Reports retain
all attempted, interrupted, failed and unrun requests in their denominators
and separately report coverage and latency eligibility.

Eligibility requires a prospectively frozen review manifest containing the
entire ordered failure and warning payload, counts and digests, source/input
and policy identities, binary/build/harness identities, profile, verb, query,
scope, scenario/mutation, settings and applicable arm identities. Each entry
needs a reasoned classification and consulted source or independently authored
fixture. Expected size/minification exclusions remain distinct from genuine
parser coverage limitations; neither becomes a successful negative finding.
A digest without the full reviewed payload is insufficient.

Both arms must complete within unchanged resource/deadline limits, preserve
source fingerprints, and match exactly on semantic output, complete partial
and warning membership, ordering and completeness. A new or unreviewed
failure, any disagreement, truncated/missing artifact, missing resource metric,
identity drift, timeout or control failure pauses the whole run. No automatic
resume, inferred allowlist, wildcard code allowlist, or status normalization
is allowed. Any subsequent review change requires another versioned manifest
and separate run; no retroactive admission of historical rows.

Review records identify the reviewer, timestamp, source citations and immutable
review commit/artifact hash. The review input excludes timing, RSS and
comparative scores; coverage decisions cannot depend on a favorable arm.
Record and resolve disagreements before freezing eligibility. This is source
adjudication, not conversion of a partial result to complete coverage.

Metric eligibility is explicit:

- The 25% one-edit gate retains exactly its original baseline-selected subset.
  No formerly ineligible partial or timed-out baseline cell is promoted.
- The cold latency/RSS check still covers every cell of the fixed comparison
  set. A reviewed partial cell has a separately labeled within-cell comparison
  under the same thresholds; it is not dropped to make that gate easier.
- Zero reparses applies to unchanged files eligible for each enabled extraction
  stage. The file/stage eligibility inventory must be explicit even within a
  partial operation; unavailable inventory leaves this gate unverified.
- Completed-request counts include completed partial requests, but
  complete-coverage pair counts exclude them. Report both with clear names.
  Never pool complete and partial observations into an unlabeled median or
  use a timing comparison as evidence of complete semantic coverage.

The original fixed corpus, workload matrix, 30 paired repetitions, alternating
order, page-cache policy, resource limits and numerical thresholds remain.
Original parse-dominated membership remains fixed: previously incomplete
baseline cells cannot enter the 25% gate based on post-optimization profiles
or favorable gains. Report those cells and their incomplete membership.
The complete fixed cold comparison set remains required; a timeout or missing
cell cannot disappear into a smaller passing set. No latency win changes the
reported semantic coverage or authorizes default enablement.

## Alternatives

- Keep complete-only eligibility forever: preserves the first protocol, but
  cannot evaluate fixed malformed/partial fixtures as contemplated by P1.
- Relabel or drop partial records, change the corpus/limits, or admit any equal
  digest: rejected; hides coverage, creates unverifiable allowlists or changes
  the task after observing results.
- Keep partial results only as isolated correctness diagnostics: remains the
  active policy until this proposal's complete-review prerequisites are met.

## Distinguishing verification before adoption

Independent harness fixtures must accept only the exact reviewed completed
partial pair while preserving its status and coverage; reject changed details,
order, warning membership, source/configuration/binary identity, truncated or
missing full artifacts and timeouts; prevent any incomplete canary cell from
silently disappearing; preserve original complete-only mode and historical
scoring; and prove STOP/lease/failure propagation still prevents expansion.
The complete reviewed payloads and tests are prerequisites, not work already
done. All existing stopgaps remain active during preparation.

## Rollback and sources

Do not select the new policy; retain the original paused protocol and raw
artifacts. Features remain default-off. Sources are the requirements plan,
Entire's current harness and frozen protocol, user-directed stopgaps, and
retained diagnostic manifests. No external implementation or comparative
corpus supplied this decision or expected results.
