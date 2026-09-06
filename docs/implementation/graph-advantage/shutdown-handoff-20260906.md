# Laptop shutdown handoff — 2026-09-06

Paused explicitly at the user’s request. Do not resume work, heartbeat or
benchmarks until the user requests it. Branch `codex/graph-advantage`, worktree
`/Users/thomi/Projects/entire-graph-advantage`. Commit and push, no merge.

## Exact stopping point

- All three task Azure VMs confirmed deallocated. Heartbeat
  `monitor-p1-corpus-campaign` is PAUSED. All delegated work has returned.
- No local verification or remote collector remains active. Integration handle
  27301 completed; never re-poll or restart it as though still live.
- Product source `6cf92c9c` passed the immutable full check and pinned Linux
  76 top-level/10 live compiler tests. Its one corrective cold pair preserved
  exact semantics/194 known partials and stayed within the single latency/RSS
  screens. No statistical or release gate passed; old failures remain.
- Harness `aab356ae` adds explicit full diagnostic artifacts. Eight focused
  tests and race passed. Its full `mise run check` exited0 in705.514s, but
  immutable=false: repository gofmt modified six reviewer Go input files.
  Evidence and exact diff: `evidence/check-aab356ae/`. This is not relabeled
  a clean immutable pass.
- The packaging fix stores those original Go bytes as `.go.txt`; `tasks.json`
  maps original logical paths to stored paths. All16 original hashes match.
  No source input, label coordinate, or fixture origin changed.
- `full-diagnostics-collector/` is prepared and reviewed, not executed. Root
  added a guard requiring fingerprint and product corpus roots to agree;
  eight local synthetic tests passed in0.019s. It performs one OFF request,
  checks complete raw-array digests, and never admits a campaign.
- ADR0049 is PROPOSED only. It describes prospective reviewed-partial coverage
  strata while preserving corpus, thresholds, original parse-dominated set,
  explicit coverage and first-issue stops. Adoption tests and complete review
  payloads remain prerequisites. No eligibility rule changed.

## Resume sequence

1. Re-read current plan/review and this ledger; inspect status. Preserve the
   unrelated untracked `frozen-baseline-initial.json` and
   `frozen-baseline-pre-counts.json`. No memory/competitor/prior-session research.
2. Pin the new integration commit after the packaging fix. Reuse the clean
   verification checkout at
   `/var/folders/7g/r0pg1n495tb1snh2zvk9y0_r0000gn/T/graph-integration-0c9e80f5-61zo1a1q/source`
   if it survives; otherwise create a fresh isolated checkout. The six known
   formatter mutations there were restored only after retaining their diff.
   Run required checks on that immutable commit; do not call the old run clean.
3. Re-pin the prepared Linux source/build/controller and collector before
   execution. `evidence/diagnostics-linux-aab356ae/` contains PREPARATION ONLY,
   and its guard deliberately requires an immutable aab check that did not
   pass. Do not run it unchanged or bypass that guard. Its source archive was
   `/tmp/graph-aab356ae-source.tar.gz`; recreate from recorded recipe if lost.
4. Once correctness is verified, one separately identified OFF-only full
   diagnostics collection can supply the missing194-record review payload.
   Preserve all evidence; stop on an issue. It is not a new benchmark campaign.
5. Review every partial with source evidence. Resolve outstanding fast/full
   snapshot baseline timeouts separately. Only then consider ADR0049 adoption
   with tests and a prospectively frozen run. Never resume the old campaign.
6. Required P3/P4 human review is still pending. `reviewer-packet-v1` is neutral
   development material with blank answers, not realistic P3 changes or a
   held-out set. No performance/quality claim or default enablement is allowed.

## Rollback

Keep extraction reuse off, compiler off, depth2 and ranking current. The
optional full artifact is disabled by omitting `diagnostics_path`. Do not
re-enable working-tree snapshot caching. No installation, MCP, summaries or
Brain changes are part of this work.
