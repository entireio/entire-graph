# Native prose-session context design

## Problem

The sealed v45 public-protocol reimplementation shows that Entire Graph usually
retrieves the evidence-bearing session but often answers incorrectly because its
native result contains only the best local snippet from that session. On LOCOMO,
Entire had exact evidence-session recall in 789 of 900 repeated cells but answered
only 459 correctly. Graphify returned roughly 50 kB of context per case; Entire
returned roughly 2.3 kB.

This is a native product deficiency, not a reader or grader problem. The fix must
therefore live in Entire Graph. The benchmark adapter, prompts, model, limits,
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
