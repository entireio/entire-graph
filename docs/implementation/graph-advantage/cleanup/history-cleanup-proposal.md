# Branch-only history cleanup proposal

Status: proposal only. Do not execute it. The proposal applies only to
`refs/heads/codex/graph-advantage`. It does not authorize a rewrite, ref update,
checkpoint operation, deletion, or force push.

The rewrite input must be the final reviewed cleanup commit after all deletion,
hygiene, and self-contained validation changes have landed through the normal
branch workflow. This document cannot embed that commit's own identity. Before
any later rewrite review, select and record exact values for:

- `CLEANED_TIP`: final reviewed local cleanup commit;
- `EXPECTED_REMOTE_TIP`: freshly fetched remote
  `refs/heads/codex/graph-advantage` used by the lease;
- the final cleaned tree ID and final `removal-map.json` digest;
- the external backup bundle digest and the eight raw-profile CAS digests.

An absent value stops the procedure.

## Exact allowlists

The historical deletion allowlist is exactly the set of `path` values whose
final `removal-map.json` entry has `disposition: "applied"`. Each entry must
retain its pre-cleanup content digest and verified replacement path/digest. A
path pattern, extension, directory name, archive name, or similarity match may
not add another deletion.

Historical content transformations require a second, separately reviewed
allowlist. It must contain, for every transformed retained path, the path,
old Git blob, new Git blob, exact transformation rule, reason, and replacement
evidence. That allowlist is empty unless explicitly populated and approved.
Deletion-map membership never authorizes rewriting the contents of a retained
file.

The following are outside both allowlists and must retain their original refs
and reachable objects: `main`, all tags, all other local and remote branches,
and `refs/heads/entire/checkpoints/v1` including its checkpoint and snapshot
history. The 56 baseline-modified paths, four protected untracked artifacts,
product source, required fixtures/harnesses, authored corpus inputs, and the
paused completion diagnostic are excluded from deletion.

## Proposed sequence

1. Before selecting `CLEANED_TIP`, land and review the self-contained validation
   commit. Normal checks must bind the full public JSON/NDJSON values to the
   pre-cleanup parity attestation and complete preservation boundary without
   reading old Git objects. Original-byte parity remains an explicit,
   fail-closed external-backup audit and must be run before the purge; absence
   of that backup is an error. The reviewed change must not silently regenerate
   a smaller dataset.
2. Fetch without pruning. In a temporary clone, record `CLEANED_TIP`,
   `EXPECTED_REMOTE_TIP`, the cleaned tree, all protected ref tips, the final
   allowlist digests, and repository object-format/version details. Do not
   create or move a ref in the working repository.
3. Create and verify an external full bundle of the original branch and every
   protected ref. Separately verify the eight raw pprof CAS files against the
   public SHA-256 values. Keep both backups outside Git. The bundle is the
   recovery copy for original archive/source/result objects; the optional CAS
   remains the direct profile copy.
4. In the temporary clone only, rewrite commits reachable solely from
   `codex/graph-advantage`, starting from the approved cleaned tip. Apply only
   the exact deletion allowlist and separately approved content transformations.
   Preserve parent order, author, committer, timestamps, and messages where the
   selected tool permits. Never use a repository-wide purge pattern.
5. Emit a complete old-commit to new-commit map and a per-path action record.
   Preserve historical source/binary/workload identities inside evidence
   records as observed strings. Do not replace a measured commit ID with its
   rewritten commit ID or relabel a measured outcome; link the two identities
   only through the provenance map.
6. Require the rewritten tip tree to equal the approved cleaned tree exactly.
   Compare all 56 preserved modified paths and all retained fixtures/harnesses
   byte-for-byte and by mode. Verify that `main`, tags, every other branch, and
   `refs/heads/entire/checkpoints/v1` still name their recorded original tips.
7. Clone only the proposed rewritten branch into a fresh object store with no
   alternates, shared object directory, reference repository, or pre-rewrite
   refs. Expire reflogs and prune unreachable objects in that disposable clone;
   verify that the old cleanup/pre-cleanup objects are unavailable. Run the
   complete canonical, consumer-parity, archive-provenance, removal-map,
   fixture, and preservation checks there. Any test that succeeds only because
   an old object is still present blocks publication.
8. Review the exact old/new tips and trees, protected-ref comparison, deletion
   and content-transform allowlists, old-to-new map, fresh-clone check results,
   attribution changes, external bundle digest, and raw-profile CAS inventory.
   Only a new explicit user approval may publish the rewritten branch.
9. If approved, update only `refs/heads/codex/graph-advantage` with an explicit
   force-with-lease that names `EXPECTED_REMOTE_TIP`. Abort on lease mismatch.
   Do not push, delete, or rewrite any other ref. Fetch again and verify the
   remote branch tip/tree and every protected ref.

Rewriting changes commit IDs. Existing `Entire-Checkpoint` trailers continue to
refer to the original history and cannot be claimed as exact linkage for the
rewritten commits. The checkpoint ref remains untouched and does not follow the
new branch automatically. The external bundle and old-to-new provenance map are
therefore required even after a successful publication.

Before any history rewrite, the branch-only cleanup itself can be rolled back
without rewriting history: revert the cleanup commits in reverse chronological
order through the normal review workflow, then run the pre-cleanup validation.
That rollback intentionally restores the removed raw files and archives; it is
not a compact-evidence state and must not be described as one. After a history
rewrite, restoration instead uses the verified external bundle and requires its
own reviewed ref update.

A failed preparation or verification leaves the original remote branch intact.
This proposal cannot erase objects in other refs, clones, backups, or hosting
retention. A clean publication branch is not an automatic fallback and would
require a separate user request.
