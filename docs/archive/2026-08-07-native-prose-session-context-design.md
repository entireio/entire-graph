# Native Prose-Session Context Design

> [!WARNING]
> Archived on 2026-08-13. This design records an implemented change and is not
> current product documentation. See the [current documentation index](../README.md).

## Problem

The sealed v45 public-protocol reimplementation shows that Entire Graph usually
retrieves the evidence-bearing session but often answers incorrectly because its
native result contains only the best local snippet from that session. On LOCOMO,
Entire had exact evidence-session recall in 789 of 900 repeated cells but answered
only 459 correctly. Graphify returned roughly 50 kB of context per case; Entire
returned roughly 2.3 kB.

This is a native product deficiency, not a reader or grader problem. The context
expansion therefore lives in Entire Graph. The benchmark adapter may only forward
the already-sealed shared byte ceiling to native search; prompts, model, limits,
Graphify arm, and grader stay unchanged.

## Design

Entire already identifies overwhelmingly prose-oriented corpora and labels their
selected results with `retrieval_mode=prose-parent`. When that mode is active and
the caller has not explicitly selected a head-window size, native search will
expand the first ranked prose parents to an 80-line bounded read window using the
existing enclosure allocator.

The ranking and top-k membership do not change. The existing byte allocator still
enforces the caller's context ceiling and demotes lower-ranked results when needed.
An explicit `HeadWindowLines` value continues to win. Ordinary code search never
receives the prose-parent signal and remains byte-identical.

One contiguous window still cannot represent two distant useful turns without
also returning everything between them. Prose-parent results therefore expose an
additive `passages` list. The primary `snippet` remains unchanged for backward
compatibility; every additional passage is an exact non-overlapping line range
from the same selected file. Candidate regions that add uncovered query terms
are preferred, ties retain native ranking order, and allocation proceeds by
passage depth across parents so one session cannot spend all remaining bytes.
Accepted passages are source-ordered and validated against the same hard byte
ceiling. Ordinary code results serialize exactly as before because the field is
omitted when empty.

The parity harness must pass its shared 128,000-byte ceiling to Entire's native
`--max-context-bytes` option. The adapter performs no source expansion or
synthesis; it may reread returned ranges solely to validate them against the
materialized corpus. This removes an asymmetric 24 KiB inner cap while retaining
the shared outer truncation and native byte accounting.

## Fairness and evaluation

- Development uses only already-inspected v45/tune cases.
- No benchmark-only source reread, answer-aware selection, prompt change, grader
  change, or comparator change is allowed.
- Before evaluating a win, freeze a new protocol with a disjoint, uninspected
  holdout drawn deterministically from the remaining official cases.
- If a holdout is inspected, it is retired from future tuning.
- Entire must strictly beat Graphify on LOCOMO recall@10, LOCOMO QA, and
  LongMemEval-S QA to claim the quality win. Build LLM credits can only tie at zero.

## Safety

Tests lock the new default to prose-parent results and preserve a byte-level golden
for ordinary code search. The bounded window uses the existing content reader,
signals, truncation accounting, deterministic order, and context budget.
