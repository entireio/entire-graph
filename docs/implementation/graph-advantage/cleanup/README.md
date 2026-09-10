# Public artifact cleanup

This directory is the machine-readable boundary for the branch-only Graph
Advantage evidence cleanup. The cleanup changes storage and references; it does
not rerun the product, benchmarks, evaluators, or cloud jobs, and it does not
change a recorded pass, failure, timeout, partial result, skip, or unknown into
a different outcome.

The admitted source boundary is the 1,901 paths with status `A` between baseline
`3a2a715fad1948e83dc7ebe0d307377ba29e065a` and pre-cleanup head
`6c1ac1d188af711e7a0e7e361bfc3e4f139ee617`. The 56 paths that were already
modified at that boundary, four protected untracked artifacts, product source,
authored corpus fixtures, and the paused completion diagnostic are excluded
from cleanup deletion. `removal-map.json` is the exact per-path deletion ledger:
every applied entry records the original content digest and a live replacement
path and digest.

## Completed records

- `evidence/canonical-v1/` contains 66 versioned run records, 18 NDJSON case
  streams, and 3,978 measured case rows. The separate correctness-query dataset
  adds 25 fixture cases. Missing measurements remain `null` with
  a reason. Repeated attempts have distinct stable IDs and observed ranking and
  result order is retained.
- `consumer-parity.json` records byte-identical extraction, relation, and
  compiler summaries through the compatibility reader, plus the five declared
  path-redacted correctness projections. It does not establish GraphRank P4:
  the branch has no per-query arm results paired with adjudicated relevance
  labels.
- `profile-summaries.json` retains complete cumulative pprof tables and timing
  context for all eight profiles. `archive-profile-summaries.json` binds the
  profile-derived archive and text members to those tables. The eight raw pprof
  objects also have content-addressed copies under
  `$HOME/.local/share/entire-graph-advantage/evidence/sha256/`; that operator
  store is optional supporting material rather than a public artifact service.
- `log-observations.json` preserves named Go and Python test outcomes, skips and
  reasons, bounded failure details, terminal package results, and status-line
  results. `special-log-observations.json`, `metadata-log-observations.json`,
  `residual-log-observations.json`, `platform-observations.json`, and
  `vm-statusline-observations.json` retain the meaningful command, toolchain,
  environment, process, goroutine, Apple sample, VM-role, and terminal-state
  facts. Progress noise and machine-local paths are not retained as evidence.
- `runtime-observations.json` preserves the complete parsed JSON values and
  original order for 111 runtime records from 181 raw aliases, with every
  machine-local path or operational identifier covered by an explicit
  redaction.
- `structured-archive-observations.json` covers 37 meaningful structured archive
  members. It preserves 1,569 ordered source records, including all 1,550
  campaign rows in worker/source order. Of those campaign rows, 116 are measured
  and 1,434 are explicitly `unrun`; six compiler outputs and
  query/snapshot values retain every non-local field. Four Git/package trace
  streams are reduced to execution aggregates already source-bound by the
  canonical diagnostic run. The unique query runner is retained byte-for-byte
  under `archive-retained-controls/`.
- `correctness-archive-mappings.json`, `correctness-query-cases.ndjson`, and
  `correctness-query-metadata.json` keep the distinct combination and promoted
  method/alias query datasets, their original case order and fixture origins.
  `archive-operational-observations.json`, `archive-provenance.json`, and the
  retained controls close the remaining archive-member chains without treating
  transport wrappers or generated caches as result evidence.
- `historical-control-provenance.json`, `tracked-manifest-provenance.json`, and
  the source reconstruction record preserve source/binary/input identities,
  Git modes, gates, invocation counts, exits, coverage, and limitations for
  removed duplicates. Source archives were removed only after their captured
  subsets matched the pinned Git blobs and modes.

`removal-map.json` is authoritative for the applied count and byte total. Counts
in prose must not be used as an alternate deletion allowlist.

## Paused work and limits

The completion diagnostic remains WIP. Its focused environment/configuration
regression passed, while its wider copied package still has five expected setup
errors from stale or absent prepared inputs. It was not launched by this cleanup.
The eight historical timeout observations remain time-bounded observations; no
completion time, peak RSS, comparative speedup, or release gate is inferred.
The positive live-compiler run remains separate from wider test skips. Historical
partial results and unknown resource measurements remain partial or unknown.

The cleanup does not claim that compact JSON can replace arbitrary raw output.
Only reviewed semantic facts, exact case data, reproducible fixtures, or hashes
with a verified replacement chain are eligible. A source digest by itself does
not prove that a unique result was preserved.

Normal archive, correctness-query, structured-observation, and preservation
checks are self-contained. `precleanup-parity-attestation.json` records the
one-time original-to-public parity result for all 65 archives and 840 members,
the 37 structured projections, five query archives with 25 ordered cases, and
five combination archives with 25 ordered responses. The combination records
preserve the complete sanitized response values, failure and fixture fields,
explicit source order, and `sources_by_stage`; the existing canonical query
cases remain a separate dataset. Normal checks bind
that attestation to the full public JSON/NDJSON values and verify replacement
chains; they never recover removed bytes from Git. `preservation-boundary.json`
contains the complete 1,901-added/56-modified path, blob, mode, and size audit.

Rechecking original archive bytes is deliberately separate and fail-closed:
pass `--external-archive-root` to `audit_archive_provenance.py`, pointing at a
verified external backup tree. Recomputing the original admission boundary is
likewise explicit through `verify_preservation_scope.py --external-history`.
Neither mode falls back to historical Git objects when its source is absent.
Before removing the old objects, regenerate the parity attestation from that
same external tree; this command verifies source bytes before writing output:

```sh
python3 docs/implementation/graph-advantage/cleanup/build_history_cleanup_attestation.py --external-archive-root /path/to/precleanup-archive-tree
```

## Verification

Run these from the repository root after the removal map and all replacement
files are frozen:

```sh
python3 docs/implementation/graph-advantage/evidence/canonical-v1/tools/validate.py --canonical-root docs/implementation/graph-advantage/evidence/canonical-v1 --evidence-root docs/implementation/graph-advantage/evidence
python3 -m unittest docs/implementation/graph-advantage/evidence/canonical-v1/tools/test_canonical.py
python3 -m unittest docs/implementation/graph-advantage/probes/test_canonical_consumers.py
python3 -m unittest discover -s docs/implementation/graph-advantage/cleanup -p 'test_*.py'
python3 docs/implementation/graph-advantage/cleanup/audit_archive_provenance.py --repo . --provenance docs/implementation/graph-advantage/cleanup/archive-provenance.json
python3 -c 'from pathlib import Path; import sys; sys.path.insert(0,"docs/implementation/graph-advantage/cleanup"); from validate_precleanup_attestation import verify; errors=verify(Path.cwd(),Path("docs/implementation/graph-advantage/cleanup/precleanup-parity-attestation.json")); print("\n".join(errors)); raise SystemExit(bool(errors))'
python3 docs/implementation/graph-advantage/cleanup/validate_archive_operational.py docs/implementation/graph-advantage/cleanup/archive-operational-observations.json
python3 docs/implementation/graph-advantage/cleanup/check_public_artifacts.py --repo .
python3 docs/implementation/graph-advantage/cleanup/verify_preservation_scope.py --repo . --scope docs/implementation/graph-advantage/cleanup/preservation-scope.json
git diff --check
```

The reviewed preservation audit reported 56 preserved modified paths, four
protected untracked artifacts, and zero errors. The committed cleanup scope
check also requires every deleted pre-cleanup path to appear as status `A` in
the admission record and rejects deletions in the authored corpus fixture or
paused completion-diagnostic trees. These are evidence-maintenance checks only;
they do not execute product or benchmark code.
