# ADR 0043: Versioned evaluation inputs and staging

Status: accepted implementation decision; no campaign admission implied.

Requirements: plan P1.1/P1.6 and shared evidence rules; user instruction to
pause on findings, preserve earlier results, fix causes and then continue.

The original launcher hardcodes the historical evaluator blob and shared
staging paths. A new run ID isolates results but cannot safely select a new
verified executable. Overwriting the old build/source manifests would obscure
which code produced the retained failed campaign.

Add an explicit build-manifest option, retaining the historical manifest as
the compatible default. Validate the selected manifest's executable identity
and source inventory before any VM mutation. Resolve its source inventory
relative to a documented manifest boundary; never infer a newer binary from
the current branch name. The existing fixed matrix, thresholds, baseline
identity checks and canary requirements remain unchanged.

Stage the executable and harness under the unique run directory and launch
the worker against those exact paths. Keep supervisor/control paths coherent
with that run. Existing directories must not be reused. Preserve raw transport
responses before decoding them, with no signed URLs or credentials in local
evidence. Control-plane errors still stop admission/supervision.

Rejected alternatives: replacing the historical manifest/blob, copying a
second permanent harness tree, or relaxing stale-source checks. These either
lose provenance, duplicate maintenance, or admit an unverified executable.

Distinguishing tests: two build manifests select distinct pinned blobs and
run-local paths; missing/mismatched source inventories fail before transport;
existing run directories fail closed; the default manifest remains supported;
malformed Azure replies retain their raw response and never admit success.

This decision does not approve partial results, change failure treatment, run
a benchmark or pass a release gate. Rollback: omit the new option to select the
historical manifest; the same source and canary checks still apply.

Sources consulted: the supplied plan, Entire's launch/verification/supervisor
code and retained build manifests. Fixtures are independently authored control
cases, with no external product as an oracle.
