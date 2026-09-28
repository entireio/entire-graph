# Reviewed Graph Core Integration Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development; one
> composition owner and independent review. No merges.

**Goal:** Produce one tested, reviewable composition of the pinned core fixes.

**Architecture:** Preserve the reviewed changes with traceable cherry-picks.
Resolve only composition overlaps, then verify the exact resulting tree.

**Tech Stack:** Go 1.27.1, CGO/tree-sitter, Git, existing shell tests.

## Goal and authority

Assemble the already-reviewed correctness, accounting, guidance, and verifier
fixes into one independently reviewable candidate. User authorizes implementation
and PRs only: no merges. This is one launch prerequisite, not launch completion,
an accuracy result, or a measured token-savings result.

Spec: `/Users/suhaan/devenv/.agent-comms/graph-launch-integration-map.md` and
`/Users/suhaan/devenv/.agent-comms/core-integration-pin-deltas.md`.

## Global Constraints

- Work only in `/Users/suhaan/devenv-worktrees/eg-core-integration-20260928`,
  branch `codex/graph-core-integration-20260928`, starting at
  `f22e6c952f3b375261942e6a17af67ca6b1d2e39`.
- PRs only; never merge, push to an existing shared branch, publish, release,
  install, or change user configuration. The controller owns PR delivery.
- Include only the pinned stacks below plus this implementation plan. Exclude
  PR285, PR289, unfinished Go binding work, website work, new output-budget
  defaults, and new savings/accuracy claims.
- Preserve frozen additive schema 1.x, no-egress behavior, stable symbol IDs,
  complete-body truthfulness, and explicit heuristic relation qualifications.
- You are not alone in the codebase. Preserve other workers' changes and all
  unrelated worktrees. Do not reset, clean, or delete them.
- Use traceable cherry-picks or explicit reviewed conflict resolutions; do not
  execute Git merges. Content already present must not be applied twice.
- No subagents from the implementer. One implementation owner, then independent
  controller-dispatched review.
- Source inspection is focused and Graph-first. The controller already issued
  the integration discovery query; its result is supplied separately.

## Task 1: Compose and verify the exact reviewed stacks

### Inputs and order

Apply the exact deduplicated commit closure from the base in this order. Record
original SHA to integrated SHA mapping, including any content-identical skips.

1. PR278 `2ae854618e532ff0796a657bc03ee2f8c7f29612` then PR279
   `3a82ae531304ca49626ea6e0bfb6f92053dee399`.
2. PR280 `f66716485fa8c9500f097c65eccd989cc5406b62`, then PR281
   `6549acf86a2353c6d1b1ddd4c120346e5648a7db`.
3. PR282 `4a465ad39d655a00a638e3b92d6b0ecd9e00744a`.
4. PR276 `db90c908f928565d0610f912a7b94b758d5bb87c`, then PR283
   `820a9608d656e83b448ec68e505be126ab9c8361`. PR283 forks at
   `6f0d6fec176a13ff382169f76c3556a7b143aa8b`; its tree already contains the
   two newer parent comment/skip-diagnostic hunks. Avoid duplicate application.
5. PR275 `f082732d8965c6e397a8297df6363d6a3b695ca4`, then PR277
   `c8a82c47de156b1d7a25cb51e4dd2e8954fca91b`, then PR284
   `242141843318e4b275f32eea52fab45341a43674`. Child ancestry includes only
   older PR275 `fc15d9e993b336c3e977dbe915781c7f8c671353`; retain the newer
   own-repository guide synchronization too.
6. PR288 `f99033dc37d3b54392a9ffdf8504caf346203c6d`.

### Implementation

- [ ] Prepare exact Git topology/mapping read-only while the controller's clean-base
  suite finishes. Proceed after exit0 or an explicit controller ruling on a
  demonstrated pre-existing/environmental failure; retain that failed receipt.
  Baseline receipts: `/tmp/eg-core-integration-baseline.KHAwBe`.
- [ ] Follow scratch-session adoption instructions if installed CLI supports them;
  if unavailable, document that limitation without installing or amending any
  published commit.
- [ ] Confirm exact pins exist locally and heads have not drifted from reviewed
  source. Report any drift; do not silently replace an input.
- [ ] Preserve commit attribution using `cherry-pick -x` when possible. For merge
  commits, inspect parent deltas first and escalate unclear provenance.
- [ ] Resolve only composition conflicts. Material overlaps: provider.go (280/281),
  cli/search.go (282/276/283), cli/help.go, AGENTS.md, and CHANGELOG.md. Retain all
  noncontradictory reviewed changes and document each manual resolution with
  original sides and final rationale. Use apply_patch for manual file edits.
- [ ] If a new behavior fix is needed, first report the failure. Do not enlarge
  this task to new retrieval, renderer, or memory policy.

### Verification

Run one final full Go suite on the exact composed tree, offline:

```
GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=maintenance.auto GIT_CONFIG_VALUE_0=false CGO_ENABLED=1 GOMAXPROCS=2 GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local /Users/suhaan/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.1.darwin-arm64/bin/go test -p 2 -count=1 ./...
```

Run the existing stats shell test suite with the same pinned Go toolchain on
PATH and process-local maintenance override; locate its exact invocation from
the included test surface. Do not overwrite pre-existing GIT_CONFIG_COUNT
entries: append the one override if any exist. The full Go
suite includes the PR288 verifier tests, so do not repeat that family absent a
specific failure. Retain stdout, stderr, numeric exit, exact source HEAD/tree,
and toolchain. Shell status variable must be `integration_exit_code`, not zsh's
readonly `status`. At most two heavy processes host-wide from this controller;
run heavy tests serially. No paid providers or new benchmark cohorts.

Check `git diff --check`, provenance mapping completeness, final diff scope,
and clean worktree. Commit conflict resolutions and plan. Report exact final
SHA, test commands/exits/receipt paths, skipped commits and why, conflicts,
and any test failure rather than calling the candidate green.

## Follow-through

The controller dispatches an independent spec and quality review of the complete
composition plus provenance mapping, fixes findings through the same owner,
and performs final branch review before creating a PR. No merge is authorized.
Retrieval quality, durable memory integration, real paired task-token evidence,
and launch/platform gates remain separate outstanding work.

## Review Focus

These are composition risks, not invitations for new product behavior:

- Duplicate parent hunks: verify the original-to-composed mapping and final blobs.
- Lost sibling behavior in provider/search conflicts: compare both input sides
  and run all included regression tests in the full suite.
- Guidance/accounting contradictions: inspect the final AGENTS/help/changelog
  hunks against the pinned inputs; retain no newly invented savings claims.
- Verifier security boundaries: full suite includes the reviewed PR288 cases;
  distinguish rejected shell syntax from an invoked test command.
- Pre-existing host test interference: preserve baseline failures and explicit
  process-local isolation evidence rather than relabeling retries as a clean run.
