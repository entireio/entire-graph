# Locate-cost benchmarks

Four deterministic, offline, zero-cost benchmarks for the question the tool exists to answer:
**what does it cost to find code, and does the graph beat grep at it?**

Nothing here calls a model or a paid API. Everything runs the real binary against a frozen
repository and is re-runnable by anyone against a pinned script hash.

| script | question |
|---|---|
| `head_to_head.py` | cost to locate a known target: graph vs grep vs grep-with-the-name |
| `doccomment_bench.py` | does an author's own prose find the symbol it describes? |
| `budget_falsifier.py` | which target does a smaller `--max-context-bytes` *lose*? |
| `replay_qualify.py` | how many recorded calls can be replayed at all, and at what fidelity? |

## Why these exist, and what they are not

Every attempt in this project to derive a token-savings number from agent transcripts has
failed, three times in one night: replay tiers over-counted 16x, a rank analysis collapsed
from n=55 to n=8 under a stricter parser, and an aggregate hit@5 hid a target lost at rank 9.
Fixture-executed measurement caught all three and has not itself failed.

So the rule these encode: **transcripts are admissible for diagnosis, never for a rate.**
Rates come from executing the binary against a frozen tree.

They measure **cost to locate**, which is a property of the tools and can be executed. They do
**not** measure session-token savings, which is counterfactual — you cannot observe what an
agent would have read instead — and needs a randomized A/B.

## Method notes that are load-bearing

- **Labels are not ours.** `doccomment_bench` and `head_to_head` build cases from each symbol's
  own doc comment, with the symbol name and its case-split words stripped, so the query is
  prose and cannot contain the answer. Otherwise it degenerates into an identifier match — the
  shape the tool is already good at and not the shape agents send.
- **Quote medians, not means.** The grep arms are heavy-tailed on common identifiers: one
  sampled symbol named `resolve` matched 4,492 lines for 1.37 MB and set an arm's mean
  single-handed (70,445 mean against a 650 median). Both are printed; the median is the number.
- **Freeze the fixture.** Re-running after committing files into the repository under test
  moved hit@1 by 33% with no code change. Use a detached worktree at a pinned revision, and
  never the repository being developed. Source state is checked before and after; a change
  voids the run.
- **A failed arm is dropped, never scored as a miss.** `rg` exit 1 means no matches and is a
  real result; anything above it is a broken run.
- **Aggregates hide losses.** Two budgets can post an identical hit@5 while one has dropped a
  target at rank 9. `budget_falsifier.py` prints the per-case set difference for that reason.

## Usage

```sh
python3 bench/locate/replay_qualify.py --test          # self-test, 14 fixtures
git worktree add --detach /tmp/fx <rev>                # frozen fixture
python3 bench/locate/head_to_head.py <binary> /tmp/fx 20 4096 /tmp/receipts
python3 bench/locate/budget_falsifier.py <binary> /tmp/fx 20 4096 24576
```

Each prints the fixture revision, dirty state, binary sha256 and its own script sha256, so a
result can be tied to exactly what produced it.

## Status of what they have measured

**No comparative number in this directory is currently valid. Do not quote one.**

The figures that stood here — locator 64/100 vs 20/100, declaration 16/100 vs 20/100 — were
produced against a doc-phrase baseline that peer review has since shown to be broken in three
ways at once, each of which flattered the graph:

- It was handed the **unstripped** doc comment, which normally contains the target's name,
  while the graph was asked with that name removed. Different inputs to the two arms is the
  precise asymmetry this benchmark exists to prevent, and I wrote it myself.
- Its search string was **non-adjacent words joined by spaces**, drawn from a comment whose
  physical lines had already been space-joined. Passed to `rg -F`, that is a fixed string
  which occurs in no file. Its 20/100 was in part a measurement of a phrase that cannot match.
- It **scored itself**, setting locator and declaration from a single condition, so the two
  columns could not take different values.

All three are fixed and each has a synthetic falsifier in `--test` that fails on the old code.

## The equal-input rule

Settled with the reviewer after the third of those defects, and now the rule this directory
is built on:

> Every evaluated arm receives **byte-identical canonical query text**, and may derive its
> search **from those bytes alone** — never from the original doc comment, the target's name,
> its span, or knowledge of a hit.

The canonical query is the doc comment with the target's name and its case-split parts blanked,
**with the physical line breaks kept**. The breaks are there because a lexical arm has to know
where contiguous source text ends, and they are given to *everyone* rather than to one arm as
side knowledge. A first repair got this wrong in a subtler way than the original bug: it read
the line structure straight off the source file, which is still a second channel the graph has
no access to.

Keeping the breaks was verified to be neutral for the graph rather than assumed — the same
query with and without them returns a byte-identical ranking, the same five symbols at the same
scores.
The corrected instrument has not been run at cohort scale, and it is the run, not the repair,
that decides what is true. Rerun it yourself and read the number off your own output.

## Claims these benchmarks have already destroyed

Kept because the withdrawals are more useful than the survivors:

- **"grep cost scales with repository size."** Refuted — median grep cost is not monotonic in
  repo size across five fixtures. It tracks how common the symbol name is.
- **"A lexical search structurally cannot bridge a description to an identifier."** Refuted —
  the description is in the file, above the declaration. The doc-phrase arm was added because
  of this and beats the graph on one fixture.
- **"Exact replay is unreachable by construction."** Refuted — the query JSON already carries
  `commit` and `tree`. The identity was in the result, not the argv.
- **"The doc-phrase arm's weakness is coverage, not precision."** Withdrawn. That read a
  locator == declaration equality off the results table — an equality the arm's own `return`
  statement forced. A property of my code, published as a property of lexical search.
- **A 24× declaration gap**, then 10.7×, then 1.6×, then 0.80×, now withdrawn pending a rerun.
  Every reduction came from a defect in these scripts rather than from new data: an OR-ed
  metric, a weak baseline, body text credited to the wrong result, and finally a baseline
  searching for strings that cannot occur.

Every one of those was found by running something rather than reasoning about it. Treat a
mechanism story from this directory as a hypothesis until a fixture kills it.
