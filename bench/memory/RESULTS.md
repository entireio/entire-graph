# RESULTS — memory-systems field comparison (as of 2026-08-14 04:40 UTC)

> **Historical snapshot — do not quote these numbers as current.** The letta and supermemory rows
> below were later found suspect and re-run: letta 80.58 → **84.68**, supermemory 77.60 → **82.08**
> (two real defects fixed, a top-100 search cap disclosed). Current numbers and their
> qualifications live in `LOCOMO-COMPARISON.md`; run provenance in `RUN-INDEX.md`.

Harness: mem0's own benchmark suite. Spine identical across arms: answerer **and** judge
`gpt-5.6-sol`, provider `azure_ai`, `top_k=200`, same prompts/datasets. Ingest native per system;
LLM-extraction arms use `azure_ai/gpt-5.6-terra`; **eg is 0-LLM at ingest**.

---

## 1. THE HEADLINE FINDING: measurement drift exceeds the spread between arms

The **same eg config**, with **identical retrieval** (299/300 retrieved-memory-ID lists byte-identical),
same answerer, same judge, same top_k, measured **26 hours apart**:

| run | timestamp | score |
|---|---|---|
| `field_eg_loco` | 2026-08-13 01:05 | **92.73%** (1428/1540) |
| `field_eg_fair2` | 2026-08-14 03:15 | **90.52%** (1394/1540) |
| | | **Δ −2.21 pts** |

Paired: both-correct 1383 · banked-only **45** · fair2-only **11** · both-wrong 101.
56 discordant, split 45/11 — random nondeterminism would be ~28/28. **McNemar p ≈ 1e-6: systematic,
not sampling noise.** The served model's behaviour drifted between runs.

**Consequence:** the top three arms span **0.71 points** (93.44 / 92.86 / 92.73) and were measured
**hours apart**. The drift on an identical config is **3× that spread**. They are **not
distinguishable** by these measurements. Any ranking among them is an artifact of *when* each ran.

*Leading hypothesis is endpoint/model drift. Cannot fully exclude a subtle `FAIR_MODE=1` effect
(only fair2 set it), though the banked run provably had no arm-asymmetric settings active.*

---

## 2. LoCoMo n=1540 — valid runs

| arm | score | measured (UTC) | notes |
|---|---|---|---|
| mem0-OSS | **93.44%** (1439/1540) | Aug 13 17:55 | fair, after top_k fix; 0 drops |
| cognee | **92.86%** (1430/1540) | Aug 13 15:56 | after empty-buffer fix |
| eg | **92.73%** (1428/1540) | Aug 13 01:05 | proven clean (code-fingerprint) |
| **eg (re-run, same config)** | **90.52%** (1394/1540) | Aug 14 03:15 | the drift datapoint |
| eg `--deep` | **87.14%** (1342/1540) | Aug 14 03:20 | see §3 |
| letta | 80.58% (1241/1540) | Aug 13 13:52 | predates client fix (disclosed) |
| supermemory | 77.60% (1195/1540) | Aug 13 11:35 | predates client fix; 14.4% zero-retrieval |
| graphiti | n=529 subset only | — | 558/1540; ~160h to finish; not completable |

Per-category (n=1540): single-hop 841 · multi-hop 282 · temporal 321 · open-domain 96.

**Superseded / invalid — do NOT publish:**
`field_mem0_loco` 87.40 (pre-top_k-fix) · `field_cognee_loco2` 79.09 (empty-buffer corruption) ·
`field_eg_loco_fair` 9.00 & `field_eg_loco_deepfair` 18.96 (PATH broken: `entire-graph` not found) ·
`field_cognee_loco_v2` 20.18 (killed mid-run) · `egopt_deep`/`egopt_sx2c10` 0.00 (killed, unjudged).

---

## 3. `--deep` REFUTED — and the way it failed is the result

Pre-registered before results: expected **93.0–93.5%**, ceiling 93.96%. Same-window paired test
(both Aug 14 ~03:15–03:20, same endpoint) — so this comparison **is** interpretable:

| | default | `--deep` | Δ |
|---|---|---|---|
| overall | **90.52** | 87.14 | **−3.38** |
| single-hop | 94.5 | 93.8 | −0.7 |
| temporal | 86.6 | 81.3 | −5.3 |
| multi-hop | 87.6 | 80.9 | **−6.7** |
| open-domain | 77.1 | 66.7 | **−10.4** |

**Coverage is not accuracy.** `--deep` measurably *improved* gold-evidence coverage
(83.59% → 86.52%; multi-hop 44.3% → 55.7%) yet *lowered* accuracy most in multi-hop — the very
category where coverage improved most. Mechanism: `--deep` concentrates retrieval into 8 sessions
instead of 18, trading cross-session breadth for within-session depth; LoCoMo needs the breadth.

**The offline proxy pointed the opposite way from the real metric.** Do not tune on coverage.

---

## 4. eg's retrieval ceiling — measured mechanism

`entire-graph search` returns **at most one hit per file**. One session = one file, so eg is
structurally capped at (number of sessions):

```
top_k=200 requested → 18 results · 18 distinct files · max 1 hit/file · ZERO files with >1 hit
--deep               → 16 results ·  8 distinct files · max 3 hits/file · 6 files with >1 hit
```

This explains eg's profile exactly: single-hop coverage **96.2%** (one turn suffices) vs multi-hop
**44.3%** (needs several turns from the *same* session — structurally impossible). `--deep` is the
only mode that breaks the ceiling, but §3 shows breaking it costs more than it buys.

Loss decomposition (eg, 112 losses): **50 answerer-failures (44.6%)** where gold evidence *was*
retrieved · 29 partial-coverage · 33 retrieval-miss. Perfect-retrieval ceiling = **96.75%**.

---

## 5. Cost & speed — unaffected by any of the above, and orders of magnitude

| arm | build/history | LLM calls/history | LME n=500 ingest ETA |
|---|---|---|---|
| **eg** | **0.55 s** | **0** | **~5 min** |
| mem0 | ~131 s | LLM extract | ~18 h |
| letta | 346 s | — | ~48 h |
| cognee | 457 s | — | ~63 h |
| supermemory | 1,538 s | — | ~213 h |
| graphiti | 4,463 s | ~2,000 | ~620 h |

Corpus ingest (10 convs / 5,882 chunks): eg **8.08 s** vs mem0 **20,471 s** (~2,533×).
End-to-end micro-run (conv0, 25Q, workers=1): eg **5 s** vs cognee 504 s vs mem0 1,522 s (~300×).

**This is where eg's advantage is real and unarguable** — three orders of magnitude, far outside any
drift band, and it makes LongMemEval tractable for eg where graph architectures need days.

---

## 6. Defects found and fixed (all self-found; all but one favoured eg)

1. **mem0 effective top_k=20 not 200** — deployed server passed `limit=` into a library expecting
   `top_k=`; silently absorbed. Cost mem0 **6.04 pts** (87.40 → 93.44). Fixed + durable.
2. **Empty-buffer corruption** — checkpoint on disk + retrieval state in memory → silent empty
   context after restart. Hit cognee (301 q) and graphiti (356 q). Cost cognee **13.77 pts**
   (79.09 → 92.86). `search()` now raises.
3. **eg's corpus injection** (`EG_SESSION_EXPAND=2`, CAP=0 reading dataset `.md` off disk,
   ~590K chars/question ≈ whole haystack) — worth +4.80 pts to eg. Disabled.
4. **eg-only answerer prompt block** (`EG_ANSWER_ENUM`) — +0.47 to eg, mem0 never got it.
5. **eg-only "## User Profile" section** on 498/500 questions; architecturally exclusive.
6. **eg's 95.07 was the top of a 17-run test-set sweep**; untuned baseline 89.80.
7. **eg silent-`[]` on missing buffer** while all 4 competitors raised — fault loud for them, mute
   for eg. Fixed.
8. **`locomo/run.py` discarded the search-drop flag** — retry-exhausted retrievals scored as
   capability misses.
9. **`entire-graph` not on PATH** — every eg retrieval failed instantly; killed two runs.
10. **supermemory's 77.60 hides a 14.4% zero-retrieval rate** (88.48% on the 1319 that retrieved).

Now enforced in code: `FAIR_MODE=1` hard-exits on any arm-asymmetric setting (including the
`--user-profile` CLI flag); `runmeta.py` captures env + argv + code md5s into run metadata.

---

## 7. Retractions

- ingest ratio 6,900× → 7,840× → **2,533×** (span-vs-summed-work, then buffered-vs-inline accounting)
- "2 of 3 graphiti drops were ours" — withdrawn (5s connect timeout, not load)
- "cognee published 80.3 ≈ our 79.09 proves calibration" — coincidence of the corruption
- **eg LME 95.07 → retracted**; honest untuned baseline **89.80**
- cognee 79.09 → **92.86**; mem0 87.40 → **93.44**
- **`--deep` as a path to 94% → refuted by measurement**

---

## 8. LongMemEval — final

| arm | score | n | basis |
|---|---|---|---|
| eg | **89.80** | 500 | honest untuned atomic baseline |
| mem0-OSS | 82.2 | 500 | **PROVISIONAL** — measured while capped at effective top_k=20; the fix was worth +6.04 pts to mem0 on LoCoMo, so mem0's true LME number is very likely HIGHER and the eg-vs-mem0 gap SMALLER than shown |
| cognee | **75.00** | **48** | stratified subset (seed 42); 95% CI approx [62.8, 87.2]; NOT comparable to n=500 rows |
| letta | **DISCARDED** | — | see below |
| supermemory / graphiti | not run | — | measured 213 h / 620 h at n=500 — that IS the finding |

**letta LongMemEval DISCARDED (landed 09:50 UTC, scored 9.40% / 47 of 500).** Not a capability
measurement — a harness retrieval failure. Diagnostics on the completed run: median
`total_results` = **0**, median context = **0 chars**, and **214 of 300 sampled questions (71%)
were answered from completely empty context**. letta scores 80.58% on LoCoMo, so 9.40% is
implausible on its face. Per the standing plausibility rule — *a competitor scoring near-zero is a
broken port until proven otherwise* — this is discarded, not published. Same rule previously saved
graphiti from being published at "0-20%" when that was a kill artifact.

eg's 89.80 sits just above cognee's upper CI bound (87.2), so eg is nominally ahead of cognee but
the n=48 subset is too small to be decisive. **No LongMemEval claim should be made against mem0
until its post-fix re-run exists.**

### Not completed

| run | progress | verdict |
|---|---|---|
| graphiti LoCoMo | 582/1540 | **will not finish** (~160 h at observed rate) — n=529 subset stands |
| mem0 LongMemEval re-run (post-top_k-fix) | not started | the single highest-value remaining run |

## 9. What to publish

Lead with §1 (drift), §5 (cost), §3 (the negative result), §6 (defects found). **Do not publish a
LoCoMo ranking among mem0/cognee/eg** — state they are indistinguishable and show the drift
evidence. eg's defensible claim: **matches the field on accuracy within measurement error while
ingesting 240–8,100× cheaper with zero ingest LLM calls.**
