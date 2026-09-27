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

On prose queries the graph locates the target far more often than a single grep, at a
comparable or smaller median payload. That direction has replicated across five frozen
fixtures and two languages. Absolute ratios move a great deal with sampling and should be
read from a current run rather than quoted from here.

A scaling story — that grep cost grows with repository size — was measured and **refuted**:
median grep cost is not monotonic in repository size and tracks how common the symbol name
is instead.
