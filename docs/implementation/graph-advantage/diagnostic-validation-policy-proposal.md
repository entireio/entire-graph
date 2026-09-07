# Diagnostic-only validation gate proposal

Status: accepted local engineering decision for the bounded diagnostic path.
It grants no full-campaign approval and does not alter release gates.

## Decision

Add a new, versioned `diagnostic-validation-v1` gate for one narrow development
diagnostic after a small correctness fix. The gate may authorize **at most one**
product invocation and only the existing cache-OFF, full-profile, snapshot
diagnostic cell. It is never admission eligible, never a comparison or
stability-batch row, and never release or publication evidence.

The new gate accepts exact-source focused evidence only when all of these are
bound to the evaluator source and binary:

1. the named affected regression suite passed normally on pinned Linux;
2. the same affected suite passed with the race detector on pinned Linux;
3. pinned Linux correctness checks passed, with every declared stage and its
   allowed skips recorded;
4. the pinned Linux evaluator build passed and its binary hash matches the
   dispatch manifest; and
5. source archive, build manifest, corpus/input manifest, batch manifest,
   controller/control archive, remote source root, and canonical cell identity
   retain the existing exact bindings.

Any absent, malformed, failed, stale, differently scoped, or hash-mismatched
item refuses dispatch. The existing durable claim, one-attempt accounting,
first-issue stop, timeout, archive validation, runtime pins, and VM/process
guards continue unchanged.

The current `full-check-gate.json` schema and every retained full-check package
remain immutable. Controllers select exactly one gate kind. A package using
`diagnostic-validation-v1` must not contain, rewrite, normalize, or reinterpret
an older full-check gate, and no prior full check is presented as passing the
new source. The latest retained `check-450bede9` pass remains evidence only for
source `450bede9970e05c994d80986cf105dd068e9fc29`.

## Requirement basis and boundary

The implementation plan says: “For product code changes, run focused regression
tests during development and the repository's required `mise run check` before
admission/merge”
(`entire-plan/entire-graph-advantage-implementation-plan.md:412`).
It separately requires a failed correctness gate to block release
(`...implementation-plan.md:404`). The resumption plan directs the team to
“Fix concrete problems and run affected correctness checks,” avoid the complete
matrix while diagnosing known failures, and retain required repository checks
when relevant source changes require them
(`resumption-plan-20260907.md:34-42`). These requirements support focused,
source-specific development validation before a diagnostic while preserving a
fresh full repository check for the later admission boundary.

This exception is deliberately smaller than a sampled batch. It permits one
OFF/full/snapshot diagnostic whose purpose is to determine whether a specific
guard changes the unresolved timeout or produces bounded diagnostic evidence.
It does not authorize an ON arm, preparation/warm invocation, retry, alternate
profile or verb, stability sampling, comparative scoring, benchmark pooling,
campaign expansion, or a full campaign. The shared batch cap and first-issue
rules still apply; one attempted product call is consumed even if it fails or
times out. Full P1 remains subject to explicit user approval
(`resumption-plan-20260907.md:114-127`). Diagnostic rows remain separate from
formal release measurements (`resumption-plan-20260907.md:129-131`).

After the diagnostic and before implementation admission, any stability sample,
merge, release decision, or quantitative publication, run a fresh immutable
`mise run check` on the exact final source. That check must use the existing
full gate and pass its clean-head/source-integrity rules. A subsequent source
change invalidates that final gate for admission. Diagnostic evidence cannot
satisfy or waive it, even when the diagnostic completes successfully.

## Proposed artifact

Each newly prepared diagnostic package contains
`diagnostic-validation-gate.json`; its SHA-256 is recorded in the dispatch
manifest as `validation_gate_sha256`. The manifest uses
`validation_gate_kind: "diagnostic-validation-v1"` and omits the legacy
`full_check_gate` fields. Existing packages keep their current fields and bytes.

```json
{
  "schema": "diagnostic-validation-v1",
  "status": "passed",
  "purpose": "single-non-admission-timeout-diagnostic",
  "required_for_execution": true,
  "admission_eligible": false,
  "maximum_product_invocations": 1,
  "allowed_cell": {
    "cache": "off",
    "profile": "full",
    "verb": "snapshot",
    "scenario": "diagnostic",
    "arms": [false],
    "preparatory_invocations": 0,
    "retry_limit": 0,
    "comparison": false
  },
  "evaluator_source_commit": "<40 lowercase hex>",
  "source_archive_sha256": "<64 lowercase hex>",
  "evaluator_binary_sha256": "<64 lowercase hex>",
  "evaluator_build_manifest_sha256": "<64 lowercase hex>",
  "input_manifest_sha256": "<64 lowercase hex>",
  "pinned_linux": {
    "manifest_path": "<relative evidence path>",
    "manifest_sha256": "<64 lowercase hex>",
    "build_manifest_path": "<relative evidence path>",
    "source_commit": "<same commit>",
    "source_archive_sha256": "<same archive hash>",
    "remote_root": "<same remote source root>",
    "stage_results_path": "<relative evidence path>",
    "stage_results_sha256": "<64 lowercase hex>",
    "required_stage_ids": ["affected_normal", "affected_race", "correctness", "build"],
    "expected_stages": [
      {
        "id": "<required stage id>",
        "command": "<exact pinned command>",
        "required_tests": ["<exact required test name>"],
        "allowed_skips": []
      }
    ],
    "build_exit_code": 0,
    "binary_sha256": "<same binary hash>"
  },
  "final_full_check_required_before": [
    "implementation-admission",
    "stability-sampling",
    "merge",
    "release",
    "quantitative-publication"
  ],
  "claims": {
    "correctness": "focused-exact-source-only",
    "performance": "none",
    "stability": "none",
    "release": "none"
  }
}
```

The exact-source pinned Linux `affected_normal` and `affected_race` stages satisfy
the focused diagnostic gate. Local focused results remain separately retained
development evidence, but the gate does not require a duplicate local result
schema. Every referenced path must be relative, resolve beneath the evidence
root, be a regular file, and match its recorded hash. The gate carries expected
stage IDs, exact commands, required test names, and allowed skips; it does not
carry observed exit or test results. Those come from the separately hash-bound producer
`stage-results.json`, which also binds source/archive/binary identities and each
raw log and exit file. The validator cross-checks the two artifacts and requires
each raw exit file to contain exactly integer zero. `required_stage_ids` must be
an exact set with no missing, duplicate, or unreviewed stage. Each non-build
stage must report at least one matched and passed named test. Skips are accepted
only when explicitly listed by test name in the gate and identically reported by
the producer result; an unexpected skip, failure, zero-test regex, or
command-only success without the required named test count fails.

## Minimal controller adaptation

Adapt only the validation branch around the current controller's
`_validate_scoped_full_check_gate` call and manifest requirements
(`evidence/diagnostic-dispatch-450bede9/controller/controller.py:124-284,405-459`):

- parse `validation_gate_kind` as a closed enum of `full-check-v1` and
  `diagnostic-validation-v1`;
- preserve the existing full-check validator byte-for-byte or move it without
  semantic changes;
- for the new kind, require the new file/hash, validate the schema and evidence
  above, then require the existing batch validator to prove one derived
  invocation and the exact OFF/full/snapshot cell;
- reject both gate kinds present, neither present, unknown versions, or any
  diagnostic gate paired with `admission_eligible=true`;
- emit the selected gate kind and gate hash in prepared/runtime/result metadata
  so downstream review cannot confuse diagnostic validation with a full pass.

No generic policy framework, compatibility normalization, or migration of old
packages is needed. The adaptation is a narrow second gate path for future
diagnostic packages.

## Required negative tests

Before this policy is used for a product call, focused synthetic controller
tests must prove fail-closed behavior for:

1. unknown/missing schema, status other than `passed`, malformed hashes, escaping
   or absolute evidence paths, missing artifacts, changed artifact bytes, and
   non-regular files;
2. source commit, source archive, remote root, build manifest, binary, input
   manifest, control archive, batch manifest, dispatch ID, or cell ID mismatch;
3. missing affected-normal or affected-race stage, duplicate stage, empty or
   changed command, nonzero exit, missing result/log, zero matched/passed tests,
   missing expected test, failure, or unexpected skip;
4. missing/duplicate/extra pinned Linux stage, nonzero stage/build exit, Linux
   manifest source mismatch, archive mismatch, binary mismatch, or allowed-skip
   mismatch;
5. cache ON, profile other than full, verb other than snapshot, ON arm, more than
   one arm/cell/worker invocation, preparatory invocation, retry allowance,
   comparison/admission eligibility, or profiler contract drift;
6. both legacy and diagnostic gates, neither gate, an unknown gate kind, a
   diagnostic gate supplied to a sampling/admission/release scope, or downstream
   metadata that labels it as a full check;
7. reuse of the `450bede9` full-check artifact to certify a later source, and any
   attempt to mutate or regenerate a retained full-gate package; and
8. durable claim already present, active campaign/service, source/control drift,
   or a second dispatch attempt after timeout/failure.

Positive tests should be limited to a fully synthetic valid package plus a dry
preflight of a newly prepared exact-source diagnostic package. They consume no
product invocation. The next real diagnostic, if otherwise authorized, consumes
the single attempt and stops at its first issue.

## Rollback

Before first use, rollback is deletion of the new validator path and proposed
schema. After use, keep the diagnostic package and outcome immutable, disable
creation of new `diagnostic-validation-v1` packages, and restore the controller
default to the existing full-check-only path. Never delete or rewrite evidence
from an attempted invocation.
