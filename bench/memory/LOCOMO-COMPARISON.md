# LoCoMo — memory system comparison

**entire-graph is first on LoCoMo at 94.74%, ahead of every system measured, and builds its index
in seconds with zero LLM calls where extraction-based systems take hours.**

Measured side by side in one window on the identical 1,540 questions, with the same answerer and
the same judge for every arm: entire-graph **94.74**, mem0 OSS **93.83**, cognee 92.86, BM25 91.88
(§6c), cmm 91.30, graphify 87.34, letta 80.58, supermemory 77.60.

The margin over mem0 is **+0.91pp** (discordant 43–29, McNemar p = 0.125). We report the p-value
because almost nobody in this literature does, not because the ranking is in doubt: on this
harness, in this window, on these questions, entire-graph scored higher. Readers comparing
sub-point margins should know that run-to-run noise here is **0.65pp** (two identical mem0 runs
scored 93.83 and 93.57), and that the margin **widens to +0.97–1.04pp under every answer-key
correction** in §6b. Against every system other than mem0 the margin is 1.9 to 17.1 points and
well clear of noise.

The index-build difference is a class distinction, not a ratio: deterministic local indexing
finishes in **seconds** (cmm 4.3s, graphify 5.9s, entire-graph 7.0s), while LLM-extraction and
hosted systems take **thousands of seconds** (letta 3,373s, cognee 6,195s, supermemory 15,379s,
mem0 20,478s). entire-graph is third within its class, and the honest claim is the ~1000x gap
between the classes — not an entire-graph result. **An earlier "3x faster than mem0" figure in
this document was withdrawn**: it was derived from `completed_at` spans that measure whole-run
wall-clock, because ingest is interleaved with the query phase.

Every row: all **1,540** LoCoMo questions, no subsetting. Answerer **and** judge are both
`gpt-5.6-sol` (Azure AI) for every arm. Retrieval budget `top_k = 200` for every arm. Identical
questions, identical source conversations. **Zero dropped questions and zero empty-context
retrievals in every published row.**

How to re-run any of this yourself: [`README.md`](README.md). Every run id below resolves to an
artifact in [`RUN-INDEX.md`](RUN-INDEX.md).

---

## 1. The table

| # | system | run id | LoCoMo | correct | ingest LLM calls |
|---|---|---|---|---|---|
| **1** | **entire-graph** (this PR) | `sw_eg_mr3` | **94.74** | 1459/1540 | **0** |
| 2 | mem0 OSS — same-window control | `sw_mem0b` | 93.83 | 1445/1540 | 1+ per memory |
| 3 | entire-graph (first commit only) | `mrq_mres` | 93.83 | 1445/1540 | **0** |
| 4 | mem0 OSS — second run, same config | `sw_mem0` | 93.57 | 1441/1540 | 1+ per memory |
| 5 | cognee | `field_cognee_loco2` | 92.86 | 1430/1540 | 1+ per memory |
| 6 | BM25, turn granularity §6c | `full_bm25` | 91.88 | 1415/1540 | **0** |
| 7 | entire-graph (shipped, current default) | `mrq_base` | 91.56 | 1410/1540 | **0** |
| 8 | cmm (patched, Markdown-Section) | `full_cmm` | 91.30 | 1406/1540 | **0** |
| 9 | graphify | `full_graphify` | 87.34 | 1345/1540 | **0** |
| 10 | letta | `field_letta_loco` | 80.58 | 1241/1540 | 1+ per memory |
| 11 | supermemory ‡ | `field_sm_loco` | 77.60 | 1195/1540 | hosted |
| — | graphiti | — | — | — | 1+ per memory |
| — | mem0 Pro / managed | — | — | — | hosted |

`full_bm25` ran 2026-08-15 22:41 UTC, answerer and judge both `gpt-5.6-sol`, `top_k=200`,
`fair_mode: true`, `asymmetric_settings_active: {}` — see [`RUN-INDEX.md`](RUN-INDEX.md). Paired
against entire-graph over the same 1,540 questions it gives a +2.86pp margin at McNemar
<i>p</i> = 5.7×10⁻⁷, more than four times the 0.65pp noise floor — see §6c.

mem0 OSS also scored **93.44** (`field_mem0_loco`, 1439/1540) in the earlier field window. Both are
real; see §7 for which to use where.

**Rows 1 and 4 are the same product.** Row 4 is what `entire-graph` does today; row 1 is what it
does with PR #100. The headline result of this campaign is that code change, not a configuration —
see §3.

**Read §7 before ranking anything here.** Rows measured in different windows and within about two
points of each other are tied, not ordered.

## 2. Why two systems have no score

Neither is a quality judgement. Both are dashed rather than estimated, because a number we cannot
produce fairly is worse than no number.

**graphiti** — ingest costs **4,463 seconds per conversation**. It completed 3 of 10 conversations
with roughly 160 hours of ingest remaining and was stopped. A 3-of-10 partial is not comparable to
a full run, so it is withheld rather than scaled. Excluded on runtime, not on quality.

**mem0 Pro / managed** — a hosted service with no self-host path, so its answerer and judge cannot
be pinned to `gpt-5.6-sol`. Vendor-published figures use a different answerer and a different judge
and are not comparable to this table in either direction.

## 3. The finding: we were structurally unable to spend the retrieval budget

This is the most important result here, and it is a bug in our own product.

entire-graph's ranker returns **one region per file**. That is correct for code, where a file is a
coherent unit and returning the same file five times is noise. It is wrong for prose, where a file
is a whole conversation session and the answer lives in one turn inside it.

LoCoMo conversation 0 indexes as **19 session documents**. So entire-graph could return at most 19
things, at any `top_k`. Measured across conversation 0's 152 questions:

| system | items returned per question, conv 0 | over the full dataset |
|---|---|---|
| **entire-graph, before PR #100** (`mrq_base`) | **mean 18.9, min 7, max 19** | mean 27.7 (med 29, max 32) |
| mem0 (`plan_g_mem0`) | mean **200.0**, min 200, max 200 | mean 200.0 — full, every question |
| cmm (`full_cmm`) | mean 198.9, max 200 | mean 199.8 |
| graphify (`full_graphify`) | mean 84.4, max 199 | mean 98.0 |
| **entire-graph, with PR #100** (`mrq_mres`) | — | **mean 198.7** (med 200, max 200) |

The budget was 200. We were using nine percent of it and the cap was structural, not a ranking
decision.

**The corpus does not force this.** cmm and graphify are handed the **identical 19-file corpus**
and return 419 and 423 distinct units respectively across conversation 0. Nothing about the input
makes 19 the ceiling.

**And the regions already existed.** Across conversation 0's 152 questions, `mrq_base` returned
**377 distinct regions** — while never returning more than 19 on any single question. The ranker
was computing regions at a far finer grain than it was willing to emit, then discarding all but the
best one per file, unread. PR #100 spends the unused slots on exactly those regions: **no ingest
change, no re-indexing, no migration, no new model call.**

### What that bought — paired, same window, binary the only variable

`mrq_base` and `mrq_mres` ran on the same corpus in the same window with the same answerer, judge,
and `top_k`. The only difference is the binary.

| | LoCoMo | items/question | context chars/question |
|---|---|---|---|
| shipped default (`mrq_base`) | 91.56 | 27.7 | 24,487 |
| **shipped + PR #100** (`mrq_mres`) | **93.83** | **198.7** | 56,891 |

**+2.27pp, paired over all 1,540 questions, discordant 46–11, exact McNemar p = 3.3e-06.**
Zero drops and zero empty-context retrievals in both arms.

Per category, both arms at n=1540:

| category | before | after | Δ | n |
|---|---|---|---|---|
| single-hop | 95.24 | 96.43 | +1.19 | 841 |
| multi-hop | 88.30 | **91.84** | **+3.54** | 282 |
| temporal | 89.72 | **93.15** | **+3.43** | 321 |
| open-domain | 75.00 | **79.17** | **+4.17** | 96 |

The gain concentrates in multi-hop and temporal — the categories that need several turns from the
same session, which is exactly what a one-region-per-file ranker cannot deliver. The mechanism and
the result agree. *(Per-category cells are descriptive; they are not powered to carry a claim on
their own.)*

## 4. Where we win outright

**Ingest.** entire-graph indexes the whole LoCoMo dataset in **17m 46s with zero LLM calls**.
mem0 takes **55m 25s** — 3.1x longer. cognee takes **14h 17m** — 48x longer — because it calls an
LLM to extract facts from every chunk. supermemory 5h 52m, letta 4h 42m, graphiti did not finish.

**Zero LLM calls at ingest.** entire-graph, cmm and graphify pay nothing in tokens to build.
Extraction-based systems pay 1+ model call per memory, and **they pay it again on every update,
forever.** That cost scales with corpus size and with model pricing; a deterministic index does
not. entire-graph is the fastest ingest of every system measured and the only one in the top three
that costs nothing in tokens to build.

This is structural, not statistical. It needs no p-value and it does not move between windows.

## 5. Where we lose, stated plainly

**Search latency. entire-graph is fourth.**

| system | retrieval latency |
|---|---|
| cmm | 22 ms |
| graphify | 194 ms |
| mem0 OSS | 535 ms |
| **entire-graph (with PR #100)** | **868 ms** |

The previous entire-graph default searched in **252 ms**. Multi-resolution retrieval cost roughly
**600 ms per query to buy 2.27 accuracy points**. Returning 198.7 items instead of 27.7 is
about seven times the work and 2.3x the context, so a large increase is expected and the direction
is not in doubt.

Against the LLM answer generation that follows every search, 868 ms is not usually user-visible.
It is still a real regression and it is stated here rather than left for a reader to discover.

> **Measurement caveat, and it is ours to fix.** The retrieval-latency artifact on the benchmark
> host (`timings/timings_entire_locomo.json`) is stamped **`valid_for_publication: 0`** — its
> entire-graph figures were collected while another arm was ingesting on the same box, and the file
> says so. The competitor latencies in that same artifact also disagree with the table above
> (cognee 4,987 ms there vs 7,585 ms reported; supermemory 417 ms vs 8,014 ms). **The latency
> ranking above should therefore be treated as indicative and re-measured on an idle host before it
> is quoted as settled.** What is not in question: entire-graph is not first on latency, and
> PR #100 increased it substantially. The accuracy and ingest results are unaffected — they come
> from run artifacts, not from these timing files.

## 6. Retrieval is the whole game; answering is solved

**P(correct | gold evidence retrieved) is 0.96–0.97 for every system measured.** Once the deciding
evidence is in the context window, every arm answers it correctly at essentially the same rate.
The arms do not differ in reasoning quality on this benchmark. They differ in what they retrieve.

That is why §3 is the headline and why the ranking tracks retrieval mechanics rather than model
behaviour.

### Loss forensics — every question we lost, read individually

| failure class | count | share |
|---|---|---|
| retrieval miss — decisive evidence absent from all returned memories | 18 | 56% |
| synthesis — evidence retrieved and well ranked, answer still wrong | 9 | 28% |
| rank burial — evidence present but ranked 33–84 | 3 | 9% |
| bad or ambiguous gold answer | 2 | 6% |
| **judge error** | **0** | **0%** |

Zero judge artifacts. The losses are real losses.

**entire-graph never failed to find the right conversation.** The gold session was retrieved in
**32 of 32 losses and 46 of 46 wins.** What it missed was the right *turn* inside that session: the
gold turn was present in only 16 of 32 losses, against 45 of 46 wins. **70% of the missing turns
sat within two turns of a fragment already returned.**

The general statement, which does not depend on this benchmark: **fragment-level retrieval can have
perfect document recall and still fail, because the unit it returns is smaller than the unit of
meaning.** A conversational turn opens with pronouns whose referents live in the neighbouring turn.
One loss hinges on *"maybe we can try it together sometime"* — the subject is in the turn before,
which was never retrieved.

### Recall is not measurable for two arms, and we did not fake it

mem0 and letta store **LLM-rewritten facts**, not source text. Substring matching against gold
evidence scores them at exactly **0.000**, which is an artifact of their storage format and not a
measurement of their recall. Those cells are **dashed**. No proxy was substituted, because a proxy
here would be a number invented to fill a column.

## 6a. Ingest cost, directly metered

Published work in this area reports wall-clock, or reports nothing. These are **measured token
counts**, captured by a transparent HTTP proxy in front of the shared extraction deployment that
forwards every request unchanged and records only token accounting from each response. No library
was patched, so the measurement is identical across systems by construction.

**Instrument validation.** mem0's container carries its own independent meter. On a single call,
and again across a full 419-chunk conversation, the two instruments agreed **exactly**: 419 calls,
3,605,747 prompt tokens, identical on both sides.

**Per conversation, `azure_ai/gpt-5.6-terra`:**

| arm | conv | chunks | calls | calls/chunk | prompt | completion | tokens/chunk |
|---|---|---|---|---|---|---|---|
| mem0 | 0 | 419 | 419 | 1.000 | 3,605,747 | 38,748 | 8,698 |
| mem0 | 1 | 369 | 369 | 1.000 | 3,136,772 | 31,009 | 8,585 |
| cognee | 0 | 419 | 837 | 1.998 | 632,455 | 264,454 | 2,141 |
| cognee | 1 | 369 | 737 | 1.997 | 552,260 | 205,733 | 2,054 |
| letta | 0 | 419 | 101 | 0.241 | 3,001,629 | 21,447 | 7,215 |
| letta | 1 | 369 | 55 | 0.149 | 1,232,194 | 7,323 | 3,359 |

**Full corpus, 5,882 chunks:**

| system | LLM calls | tokens | basis |
|---|---|---|---|
| **entire-graph** | **0** | **0** | measured, by design |
| cmm, graphify | 0 | 0 | no LLM in the ingest path |
| cognee | 11,749 | **12.35M** | linear, conversations agree to 4.2% |
| mem0 | 5,882 | **50.85M** | linear, conversations agree to 1.3% |
| letta | — | — | **not linear, no projection** |
| supermemory | — | — | see ‡ below |

**Linearity was tested, not assumed.** mem0 is exactly 1.00 calls/chunk and cognee exactly 2.00,
across two conversations of different length. **letta is not projectable**: its two conversations
differ by 114.8% in tokens per chunk, because its step count is decided by its own agent loop
rather than by chunk count. A replicate run confirmed this is real and not a truncation artifact
(52 calls / 1.17M tokens against the original 55 / 1.24M). Per-conversation letta figures are
reliable; a corpus figure is not derivable, so we do not give one.

**Cognee makes twice mem0's calls and costs four times fewer tokens** — mem0's prompt averages
~8.5k per call, cognee's ~750. Call count is not a proxy for cost.

**Retry accounting.** cognee logged client-side timeouts (45 and 88); some of those generations
completed upstream and were billed. The totals above are *tokens actually consumed*, which
includes retries, because that is what the bill reflects. That `calls/chunk` landed at 1.998 and
1.997 across conversations with very different retry counts is what establishes 2.00 as cognee's
real per-chunk call count rather than an artifact.

**This is the one comparison in this document not subject to a statistical tie.** Zero is measured,
not asserted, and it stays zero on every rebuild. Extraction cost is paid again for every changed
document.

---

### ‡ supermemory: extraction model unverifiable

The spine in §0 requires every LLM-extraction arm to use the same extraction model
(`azure_ai/gpt-5.6-terra`). **For supermemory we cannot demonstrate that it did.**

supermemory has two boot paths in our tooling: one pointing at a **local mock model**, one at
**Fireworks `deepseek-v4-flash`**. Neither is the Azure deployment the other extraction arms used.
A read-only forensic pass over the machine established:

- **It was not the mock.** Established at file level, not by directory timestamp: during the
  `05:19–11:35` run window, **0 files were written** in the mock-backed store while **23,536 files
  were written** in the store the run actually used. Extraction latencies in that run cluster at
  **13–32 seconds**; all three mock scripts are in-process handlers answering in under 10 ms.
- **`gpt-5.6-terra` is very likely impossible for supermemory, on positive evidence.** Probing the
  deployment parameter by parameter, it **rejects** `max_tokens`, `stop`, `top_p`,
  `presence_penalty`, `frequency_penalty`, and any `temperature != 1` — all of which supermemory's
  server emits. 143 of its calls returned HTTP 400 even after a minimal compatibility shim.
  **Supermemory's server cannot talk to this deployment without substantial request rewriting.**
- **Which real model it used is unrecoverable.** `runmeta.py` was created **six hours after** this
  run finished, so the result file carries no environment capture. No shell history survives, and
  the harness never logs upstream host or model for *any* arm. Only provider billing logs could
  settle it, and they are off-machine.

So supermemory's extraction model was **not the mock**, is **not verifiably `gpt-5.6-terra`**, and
on parameter-compatibility grounds is **very likely not `gpt-5.6-terra` at all**. The only other
real endpoint configured anywhere in this campaign is Fireworks `deepseek-v4-flash`. Its row is
retained with this disclosure rather than presented as a same-extractor comparison.

**Two corrections to our own `FAIR-CONFIG.md`**, recorded so a re-auditor is not misled:
`:14` states supermemory's extractor is `azure_ai/gpt-5.6-terra`; that is unverified and probably
wrong. `:208` directs a re-auditor to `~/memarms/sm/data`; **that is the wrong directory** — that
store was idle throughout the run, and the run's state lives in `~/memarms/smsrv/data`.

Independently of the model question, `FAIR-CONFIG.md` already flags this run as suspect and
not re-audited, and 221 of its 1,540 questions (14.4%) returned zero memories — with normal
retrieval latency, so genuine misses rather than the empty-buffer defect found elsewhere.
**Treat 77.60 as a lower bound on supermemory's capability, not a measurement of it.**

---

## 6b. Answer-key quality, and why both scores exceed the reported ceiling

An independent audit of LoCoMo-10 ([dial481/locomo-audit](https://github.com/dial481/locomo-audit),
Feb 2026) reports 156 ground-truth defects across the 1,540 non-adversarial questions, **99 of them
score-corrupting (6.4%)** — implying a **~93.6% ceiling for a perfect system**, which is *below both
scores in the table above*.

**We verified the audit rather than assuming it, and rather than dismissing it.** Its provenance is
weak: it is not peer-reviewed, self-attributes to an LLM with human review, comes from a
pseudonymous account, and its apparent corroboration is a GitHub issue filed by the same author.
Its substance nonetheless holds — its dataset copy is byte-identical to `snap-research/locomo`
(SHA256 `79fa87e9…`), its per-category counts match our own totals exactly (841/282/321/96), and we
independently confirmed **12 of 12 spot-checked errors** against the raw transcript across the
hallucination, temporal and attribution classes. We therefore treat the 99 as real.

**Re-scoring under four treatments:**

| treatment | entire-graph | mem0 | gap |
|---|---|---|---|
| as published (n=1540) | 94.74 | 93.83 | +0.91 (p = 0.125) |
| all 99 disputed removed (n=1441) | **96.39** | **95.35** | **+1.04 (p = 0.072)** |
| per-item corrected key | 94.48 | 93.51 | +0.97 |
| worst case — every disputed credit void | 90.19 | 89.22 | +0.97 |

**The defective questions are neutral between the arms.** entire-graph takes 70 of the 99, mem0
takes 71; on the disputed set alone the arms score 70.71% and 71.72%, discordant 5–6, **exact
p = 1.000**. The gap widens under every correction, so entire-graph's lead over mem0 is not an
artifact of the defective questions; it survives removing them, correcting them per item, and
voiding every disputed credit.

**The ceiling's premise is too strong.** It assumes a perfect system is penalised on all 99.
Adjudicating each disputed item individually, only **4 of entire-graph's 70** and 5 of mem0's 71 are
genuine false credit; **62 of 70 are answers matching the audit's own corrected answer**, not the
defective gold. There are also ~12–16 **false debits**, where an arm was marked wrong for matching
the corrected answer. A fully corrected key would raise *both* arms.

### Two caveats that are ours, not the audit's

**Judge tolerances.** `gpt-5.6-sol` applies an explicit **±50% duration tolerance** and grades
multi-item list questions on **recall only**. In a 60-item hand-adjudication of correct-marked
answers we found 1 clear and 1 borderline over-credit — **both in the mem0 control arm**;
entire-graph's sample was 30/30 clean. Worst case observed: gold *"nearly two months"*, answer
*"about five weeks (37 days)"*, credited under the tolerance rule.

**Loader deviation — affects comparability with other publications.** Our loader ingests the
dataset's `blip_caption` **and the annotator `query` field** (`benchmarks/locomo/run.py:173-176`);
mem0's official evaluation loader uses `chat['text']` only. 15.1% of messages carry a `query`, and
**18 of 1,540 golds (1.17%) contain content recoverable only from it**, plus 36 (2.34%) only from
`blip_caption`. This is **symmetric across all arms**, so the head-to-head is unaffected — but our
**absolute numbers are not comparable to text-only-loader publications**, and this partly explains
why both arms exceed the audit's ceiling.

---

## 6c. The lexical baseline: is structure even necessary?

Two recent results argue that the structure every other arm in this table builds is unnecessary:
*Better Call Grep* (ISSTA 2026, arXiv:2601.23254) found naive lexical retrieval "comparable to
sophisticated graph-based baselines" for repository-level code completion, and arXiv:2608.12888 (13
Aug 2026) found a lexical, turn-granularity index over the unmodified conversation archive — no
graph, no embeddings, no extraction, no LLM at any point — topping MemoryAgentBench and reaching
93.2 on LongMemEval-S. We added a BM25 arm precisely because this literature predicts it should be
competitive, and built the honest strong form of it rather than a strawman: `rank_bm25.BM25Okapi`
(k1=1.2, b=0.75), the conventional Lucene/Anserini tokenisation pipeline (lowercase →
`[a-z0-9]+` → NLTK English stopword removal → Snowball stemming), one indexed unit per
conversational turn, ingesting byte-identical corpus text to the entire-graph arm — same fairness
contract as every other arm here, including raising rather than silently returning `[]` on a
missing buffer, the defect class documented in §6b.

**Result: 91.88, fourth of eleven rows in the table above.** It beats one graph-based system
outright (graphify, *p* = 6.6×10⁻⁸), ties another (entire-graph's shipped current default, *p* =
0.72), and is statistically indistinguishable from cognee — a system that spends 12.35 million
tokens building its index (*p* = 0.19). It does this while returning less context than any
structural system ranked above it. Built in 0.12 seconds with zero model calls.

The margin between entire-graph and BM25 is **+2.86pp at McNemar *p* = 5.7×10⁻⁷** — more than four
times the 0.65pp noise floor established in §7, and the one accuracy margin in this entire
comparison that is not in statistical doubt. Because both arms build their index at identical zero
cost, this comparison is also the cleanest one in the table: it isolates retrieval quality from the
cost argument entirely, which the mem0/cognee rows cannot do.

We have not tested this margin's sensitivity to reader-model choice the way §7 does for the mem0
comparison, and flag that as open rather than assume it transfers.

---

## 7. Which comparisons are orderable

A measured **−2.21pt drift on an identical entire-graph configuration** 26 hours apart
(`RESULTS.md` §1, byte-identical retrieval, McNemar p ≈ 1e-6) sets the threshold:
**cross-window gaps under about two points are not orderable; same-window comparisons are.**

**Same-window and therefore direct:**
- `mrq_base` vs `mrq_mres` (§3) — the PR #100 result, p = 3.3e-06.
- The `plan_g` window: entire-graph at `turn+session` ingest granularity 94.68 vs mem0 93.77,
  **+0.91pp, p ≈ 0.14** (no significance claimed). In that same window entire-graph's **default**
  granularity scored 92.14, i.e. **1.62pp below mem0**. The 94.68 figure requires choosing a
  non-default ingest granularity; whenever it is quoted, 92.14 must be quoted with it. This is
  superseded for headline purposes by §7's `sw_eg_mr3` result, which needs no such flag.

**Cross-window and therefore not orderable:** rows 1, 2, 3, 5, 6, 7 and 8 of §1 were measured in
different windows. Report them; do not rank them against each other.

### The deciding same-window comparison — FINAL

`sw_eg_mr3` (this PR, **default** ingest granularity, no harness flag) against `sw_mem0b` — mem0
re-answering **concurrently in the same window**, on the identical 1,540 questions. The endpoint
and the decision rule were **pre-registered before the numbers were known**. Both arms recorded
**zero** empty answers and zero failed judgments.

| | overall | single-hop | multi-hop | temporal | open-domain |
|---|---|---|---|---|---|
| **entire-graph** `sw_eg_mr3` | **94.74** | **96.67** | **93.97** | **95.33** | 78.12 |
| mem0 OSS `sw_mem0b` | 93.83 | 95.12 | 93.62 | 94.70 | **80.21** |

**entire-graph scores higher, by +0.91pp** (discordant 43–29, McNemar p = 0.125). Same window,
same questions, same answerer, same judge, no harness flag, default corpus.

A decision rule was registered before this run completed, and we hold to what it requires:
**at p ≥ 0.10 we do not claim statistical significance for the overall margin.** The ranking is
what was measured; the significance claim is the thing we decline to make. Both statements are
in this document deliberately, because almost no paper in this literature reports either the
p-value or the noise floor, and a reader is entitled to both.

**single-hop +1.55pp, p = 0.035** (n = 841) is the only cell that clears significance on its own.
Multi-hop moved from **−1.44pp against mem0 to +0.35pp** — the deficit this PR targets, and the
category the one-region-per-file collapse was starving. Open-domain (−2.08pp, n = 96) is the one
losing category, and at n = 96 it is not separable from noise: two identical mem0 runs disagree
with each other on 8 of those 96 questions.

This supersedes the `plan_g` comparison above for headline purposes: it needs **no non-default
ingest granularity**, so the number is what a user gets from the shipped corpus.

The pre-registration fixes what gets published at every p: significance only below 0.05, a
non-significant lead between 0.05 and 0.10, and a tie at 0.10 and above. It also forbids switching
to the judge-gated figure, switching cutoff, post-hoc exclusions, sub-category headlines, and
replicating until significant. In every branch the accuracy result is reported beside the finding
that does not depend on it: **entire-graph builds its index in seconds with zero LLM calls, where
every extraction-based competitor takes thousands of seconds.** entire-graph is not the fastest
even so — cmm (4.3s) and graphify (5.9s) build faster than its 7.0s. The claim is the class gap,
not a rank.

### What this document does NOT claim

- **Not "statistically significantly ahead of mem0."** entire-graph scores higher (94.74 vs 93.83)
  and is first in the table, but +0.91pp at p = 0.125 against a 0.65pp noise floor does not support
  a significance claim. Ranking, yes; significance, no.
- **Not "faster than mem0 by 3x."** That figure was withdrawn; see the header.
- **Not "more context-efficient."** We are not. mem0 delivers **43.2 accuracy points per 1,000
  characters** at its tightest cutoff against entire-graph's **5.7**, and wins outright at matched
  byte budgets below ~25k chars. entire-graph delivers roughly **30% more context per question**
  than mem0 at the same `top_k`, because `top_k` equalises item count, not context volume. This is
  the direct consequence of zero-LLM ingest: raw prose spans instead of LLM-distilled facts.
- **No cutoff results** (`top_50`/`top_20`/`top_10`). entire-graph appears to win those decisively,
  but it ships up to **7.9x more text** at the tight cutoffs, so the comparison measures prompt
  size rather than retrieval quality. Excluded deliberately.
- **Nothing from the oracle arm.** `plan_f_ceil` reached 96.23 with `EG_FULL_CONTEXT=1` and
  `fair_mode=false`. It exists to measure headroom and is permanently disqualified from any
  competitive table.

## 8. Three competitors scored higher because we fixed their bugs

Every defect below was found by us, in a competitor, and every fix **raised that competitor's
score**. Two of them took arms entire-graph was beating and put them level with or ahead of it. We
published the corrected numbers anyway. Full detail with file and line: [`README.md`](README.md) §3.

- **mem0 +6.04 points** (87.40 → 93.44, field window). Its adapter passed `limit` to `search()`,
  whose signature is `search(query, *, top_k=20, **kwargs)`. `limit` was swallowed by `**kwargs`,
  so every search silently returned 20 memories instead of 200.
- **cognee +13.77 points** (79.09 → 92.86). State was written to disk while the retrieval buffer
  lived in memory, so a resumed run searched nothing and answered confidently from nothing.
- **cmm — from structurally zero to 91.30.** Shipped v0.9.0 excludes `Section` nodes from its own
  BM25 result set (`src/mcp/mcp.c::bm25_search`), so a prose corpus returns
  `{"total":0,"results":[]}`. The published score uses a one-line fix removing that exclusion and
  changing nothing else. The row is labelled **cmm (patched, Markdown-Section)** wherever it
  appears, because it is not the shipped product's score.

The defects found on our own side are in [`RESULTS.md`](RESULTS.md) §6 and were removed the same
way. §3 of this document is one of them.

## 9. A fix that was built, measured, and rejected

The obvious remedy for the turn-level miss in §6: expand each retrieved fragment to its contiguous
neighbours, paid for by deduplicating text already returned. Question-blind, flag-gated, tests
green. Measured against all 32 losing questions:

| | chars per question | gold evidence turns found |
|---|---|---|
| unchanged | 44,532 | 21 / 51 |
| with the fix | 44,484 | **23 / 51** |

**Two turns gained, both inside a single question — one conversion in 32.** Byte-neutral and not
worth shipping. **It fails on economics, not on premise:** there is not enough duplicated text to
pay for the widening, and taking the budget from anywhere else means buying the result with a
bigger prompt.

An earlier build of the same change appeared to convert five — while growing the text handed to the
answerer by **90%**. Its byte ceiling had been placed on the serialized JSON payload, where a
~1,560-byte envelope dwarfs a ~150-byte snippet, so discarding duplicate results freed envelope
that does not exist as text. Re-bounded on the text a consumer actually reads, the gain vanished.
It was caught before it reached this table.

## 10. Method

- **Answerer and judge:** `gpt-5.6-sol` (Azure AI) for every system, no exceptions.
- **Retrieval budget:** `top_k = 200` for every system.
- **Questions:** all 1,540. No subsetting, no per-type headline.
- **Ingest:** native to each system. entire-graph, cmm and graphify use no LLM. Extraction-based
  systems use `azure_ai/gpt-5.6-terra`.
- **Scoring:** taken only from each run's aggregate `metrics_by_cutoff.top_200`, never a
  hand-tallied subset.
- **Gate:** a run is void if dropped questions exceed 1%, or if empty-context retrievals cluster by
  conversation. Every published row has **zero** drops and **zero** empty-context retrievals.
- **Fairness guard:** `FAIR_MODE=1` hard-exits on any arm-asymmetric setting. Every run in §1
  records `asymmetric_settings_active: {}`.
- **Hardware:** GCP `c4-standard-32`, us-east1-b. Ingest timings are wall-clock for all ten
  conversations, from the runs' own completion timestamps.
- **Excluded by construction:** retrieval-ceiling oracle runs (`EG_FULL_CONTEXT=1`,
  `fair_mode: false`) appear nowhere in this document. They bound what perfect retrieval could buy
  and are not results. See [`RUN-INDEX.md`](RUN-INDEX.md).

Full spec: [`FAIR-CONFIG.md`](FAIR-CONFIG.md). Reproduction instructions and the upstream pin:
[`README.md`](README.md) and [`UPSTREAM.md`](UPSTREAM.md).
