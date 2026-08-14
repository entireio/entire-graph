# Memory benchmark — results and reproduction kit

**The results are in [`LOCOMO-COMPARISON.md`](LOCOMO-COMPARISON.md).** Start there. It carries the
headline table, the mechanism behind it, where entire-graph wins, where it loses, and what is not
claimable.

This file is the other half: everything a third party needs to independently re-run that
comparison. The two together are self-contained — what the results were, and how to reproduce them.

This directory is **additive and self-contained**: it does not change any entire-graph source. It
contains our benchmark adapters, our fairness-guard code, the patches we applied to the upstream
harness, and the measurement spec the numbers were produced under.

It does **not** vendor the upstream harness itself. See [`UPSTREAM.md`](UPSTREAM.md) for the exact
upstream commit, the file-level provenance manifest, and which files are unmodified upstream code.

---

## 1. The measurement spine

This is what makes the comparison fair. Every arm — entire-graph and every competitor — runs the
identical spine. The only thing allowed to differ between arms is *which memories come back from
`search()`*.

- **Answerer AND judge are both `gpt-5.6-sol` (Azure AI, provider `azure_ai`) for every arm.**
  Same model generates the answer from retrieved context; same model judges it correct or not.
  No arm gets a different reader.
- **`top_k=200`, all n=1540 LoCoMo questions, no subsetting.** LoCoMo-10, categories 1–4
  (multi-hop, temporal, open-domain, single-hop); category 5 (adversarial) is excluded per the
  upstream harness. A subset row is never a headline row.
- **Ingest is native per system.** entire-graph performs **0 LLM calls** at ingest — deterministic,
  no network, no API key. LLM-extraction arms (cognee, letta, graphiti, supermemory, mem0) use
  `azure_ai/gpt-5.6-terra` as their internal extractor. This asymmetry is the architectural
  difference being *measured*, not a fairness violation — it is disclosed, not homogenized away.
- **`FAIR_MODE=1` hard-exits on arm-asymmetric settings.** Implemented in
  [`benchmarks/common/runmeta.py`](benchmarks/common/runmeta.py) (`assert_fair_mode`). If any of
  `EG_SESSION_EXPAND`, `EG_SESSION_EXPAND_CAP`, `EG_ANSWER_ENUM`, `EG_ANSWER_ENUM_R`,
  `EG_USER_PROFILE`, or the `--user-profile` CLI flag is active, the run raises `SystemExit` before
  it measures anything. `runmeta.capture()` additionally stamps every run artifact with env
  snapshot, argv, git state, and md5 of every file that can change a measured number — secret-named
  env vars are recorded as `sha256:<12 hex>` fingerprints, never in cleartext.
- **Scoring reads ONLY the aggregate `metrics_by_cutoff.top_200`** from the run's results JSON.
  Never a per-conversation re-derivation, never a hand-summed subset.
- **Gate: a run is void if drops exceed 1%, or if zero-context questions cluster by conversation.**
  A clustered zero-context pattern is the signature of the empty-buffer defect (§3.2), not of a
  capability limit.
- **Throughput settings that matter:** `--max-workers 3 --question-workers 10 --rpm 60`, and
  `LLM_TIMEOUT=600`. These are not cosmetic. The harness defaults of 100 question-workers and
  200 rpm saturate a shared Azure deployment and collapse effective throughput to near zero, which
  then manifests as timeouts and drops that look like arm weakness. Use the values above.

## 2. Reproducing

```bash
# 1. Upstream harness at the pinned commit (Apache-2.0)
git clone https://github.com/mem0ai/memory-benchmarks.git
cd memory-benchmarks && git checkout 4b61c5d31b9c

# 2. Our patches to the upstream files (see UPSTREAM.md for what each does)
for p in <path-to-this-dir>/patches/000[1-4]-*.patch; do git apply -p1 "$p"; done

# 3. Our adapters and the fairness guard (new files, no upstream code touched)
cp -r <path-to-this-dir>/benchmarks/common/*.py benchmarks/common/

# 4. Credentials, by NAME only — never commit values
#    AZURE_AI_API_KEY, AZURE_AI_ENDPOINT, AZURE_AI_API_VERSION
export AZURE_AI_API_KEY=... AZURE_AI_ENDPOINT=... AZURE_AI_API_VERSION=2024-05-01-preview

# 5. Run an arm
bash <path-to-this-dir>/run_locomo.sh cmm
```

[`run_locomo.sh`](run_locomo.sh) is the launcher the published runs actually used, with the
credential-sourcing line replaced by the env-var names.

Full per-arm spec, state-root rules, load envelope, and the pre-run verification checklist are in
[`FAIR-CONFIG.md`](FAIR-CONFIG.md). Results and retractions are in [`RESULTS.md`](RESULTS.md);
raw gate output for the arms scored by the automated scorer is in [`AUTO-SCORES.md`](AUTO-SCORES.md).
[`RUN-INDEX.md`](RUN-INDEX.md) lists every run — complete, incomplete, and oracle — with the window
it belongs to, so any number quoted anywhere can be traced to an artifact.

## 3. Three competitor defects we found and fixed — every one RAISED the competitor's score

This is the integrity core of the work. We audited the competitors' code paths as hard as our own,
and **every defect we found and fixed made a competitor look better, not worse.** Two of the three
turned an arm we would otherwise have beaten into an arm that beats or ties entire-graph. We
published the corrected numbers.

### 3.1 mem0 — `limit` swallowed by `**kwargs`; every search returned 20 memories, not 200

The benchmark's mem0 server forwarded the caller's requested result count as `limit`:

```python
params: dict[str, Any] = {"limit": req.limit}      # upstream docker/mem0/main.py:233
```

but the library method it calls is

```python
def search(self, query: str, *, top_k: int = 20, filters=..., threshold=0.1,
           rerank=False, explain=False, reference_date=None, show_expired=False, **kwargs):
```

(`mem0/memory/main.py:1374`, and the async twin at `:3026`). `limit` is not a named parameter, so
it was absorbed silently by `**kwargs` and discarded. `top_k` fell back to its default of **20**.
Every mem0 search in the benchmark returned 20 memories while the spine specified 200, and nothing
errored.

Fixed to `{"top_k": req.limit}` — in the running container at `/app/main.py:351`, and durably at
the build-context source `docker/mem0/main.py` (patch `0004`, hunk at upstream line 233).
Verifiable live before any mem0 run via `verify_mem0_topk_fix.sh`, which must print `VERIFIED`.

**mem0 gained +6.04pp: 87.40 → 93.44** (`field_mem0_loco`, 1439/1540, n=1540, drops=0). Both
endpoints of that delta are from the same measurement window, which is what makes the +6.04pp
subtraction valid. In that window the fix moved mem0 from clearly behind entire-graph (92.73) to
nominally ahead of it.

mem0's post-fix number in the later `plan_g` window is **93.77** (`plan_g_mem0`, 1444/1540). That
is the figure to use for the head-to-head against entire-graph's 94.68, because those two ran
concurrently — see §5. Do not subtract 87.40 from 93.77: they are different windows, and the
difference would not be the bug's effect.

### 3.2 cognee — empty-buffer defect: state on disk, retrieval buffer in memory

Several arms (cognee, graphiti, letta, supermemory) buffer ingested content only in the harness
process's memory and build the real retrieval index lazily on the first `search()` for a
conversation. The harness separately persists an *ingestion checkpoint to disk* so a restarted run
can skip conversations it already ingested.

Those two facts combine into a silent failure. If the process restarts after the checkpoint is
written but before/without the in-memory buffer being rebuilt, the resumed run **skips re-ingestion
and then searches nothing** — returning `[]`, which the answerer scores as a miss and the metric
records as a capability loss. It never raises.

Fixed: `search()` now raises `RuntimeError("BUFFER_MISSING...")` on a missing in-memory buffer
instead of returning an empty list, in every affected client. Verify with
`grep -c 'BUFFER_MISSING' benchmarks/common/{cognee,graphiti,letta,supermemory}_client.py` — must
be ≥1 in each. The same guard was applied to *our own* `entire_client.py`, which had the same
silent-`[]` behaviour while the competitors already raised.

**cognee gained +13.77pp: 79.09 → 92.86** (1430/1540). It had hit 301 questions; graphiti 356.

### 3.3 cmm — shipped v0.9.0 returns structurally zero on a prose corpus

`codebase-memory-mcp` (DeusData) v0.9.0 excludes `'Section'` nodes from the BM25 result set in
both result queries of `src/mcp/mcp.c::bm25_search`:

```c
"  AND n.label NOT IN ('File','Folder','Module','Section','Variable','Project') "
```

at approximately **line 1705** and **line 1738**. Markdown headings index *as* `Section` nodes, so
on a markdown/prose corpus the shipped build indexes everything and retrieves **nothing** —
verified live, the tool returns `{"total":0,"search_mode":"bm25","results":[]}`.

Its shipped score on prose is therefore **structurally zero**, and publishing that zero would have
been meaningless. The published **91.30** (1406/1540, drops=0, zero-ctx=0) uses a one-line patch
that drops `'Section'` from that exclusion list in both queries and changes nothing else — see
[`patches/0005-cmm-v0.9.0-markdown-sections.patch`](patches/0005-cmm-v0.9.0-markdown-sections.patch),
which also adds an upstream regression test.

**This row must always be labelled `cmm (patched, Markdown-Section)`.** It is not the shipped
product's score; it is the most charitable version of the product. The unpatched binary remains
selectable via `CMM_BIN`.

### Why this matters

None of these three fixes helped entire-graph. Two of them (mem0, cognee) took arms that entire-graph
was beating and put them level with or ahead of it, and we published that. The third (cmm) turned an
unpublishable zero into a real 91.30 result. We state this plainly because it is the strongest
fairness claim available: **the audit was run against our own interest, and we kept the outcome.**

The defects we found on our own side are listed in `RESULTS.md` §6 and were removed the same way.

## 4. What is in here

| path | what |
|---|---|
| `benchmarks/common/entire_client.py` | the entire-graph adapter. Duck-typed drop-in for `Mem0Client`; ingest granularity via `EG_INGEST_GRANULARITY` (default `session` — one session per document, which is what the published runs used) |
| `benchmarks/common/graphify_client.py` | our port of graphify as a benchmark arm |
| `benchmarks/common/graphify_mem_bridge.py` | prose-memory bridge for the graphify arm |
| `benchmarks/common/cmm_client.py` | our port of `codebase-memory-mcp` as a benchmark arm |
| `benchmarks/common/runmeta.py` | run-provenance capture + the `FAIR_MODE` guard |
| `patches/0001`–`0004` | our diffs against upstream harness files (see `UPSTREAM.md`) |
| `patches/0005` | the cmm `Section` one-line patch + its regression test |
| `run_locomo.sh` | the launcher used for the published runs |
| `LOCOMO-COMPARISON.md` | **the results** — headline table, mechanism, wins, losses, and what is not claimable |
| `FAIR-CONFIG.md` | the fairness spec every run must cite |
| `RESULTS.md` | results, defect list, and retractions, verbatim |
| `RUN-INDEX.md` | every complete run with its window, config, `fair_mode` stamp, and aggregate — machine-extracted from the artifacts, including the incomplete and oracle runs |
| `AUTO-SCORES.md` | raw scorer output with gate counts, verbatim |
| `UPSTREAM.md` | upstream commit, licence, and file-level provenance manifest |

The adapters carry machine-specific default paths from the benchmark host (e.g. `_DEFAULT_BIN`
in `cmm_client.py`). They are all overridable by the env vars documented in each module docstring;
the files are vendored **verbatim** so that their md5s match the fingerprints recorded in the run
artifacts by `runmeta.code_hashes()`.

**No credential values appear anywhere in this directory.** Credentials are referenced by env-var
name only (`AZURE_AI_API_KEY`, `AZURE_AI_ENDPOINT`, `AZURE_AI_API_VERSION`, `ANTHROPIC_OAUTH_TOKEN`,
`CMM_BIN`, …).

## 5. Which comparisons are orderable, and which are not

*Summarised in [`LOCOMO-COMPARISON.md`](LOCOMO-COMPARISON.md) §7; the full derivation is here.*

**The rule: same-window comparisons are orderable; cross-window gaps under about 2 points are
not.** This follows from a measured drift, and it cuts both ways — it licenses the head-to-head
below just as firmly as it forbids ranking arms that never ran together.

### The drift that sets the threshold

`RESULTS.md` §1 documents a **−2.21 point drift on an identical entire-graph config** (92.73 →
90.52) measured 26 hours apart, with 299/300 retrieved-memory-ID lists byte-identical, same
answerer, same judge, same `top_k`. Paired split 45/11 discordant, McNemar p ≈ 1e-6: systematic,
not sampling noise. The served model's behaviour changed between windows.

So a gap of a point or two between two arms measured on different days carries no information. It
is the drift band, not a difference in capability.

### Same-window head-to-head (`plan_g`, all three concurrent, 2026-08-14 ~14:42–14:46 UTC)

These three ran **in one window against one endpoint**, all with `FAIR_MODE=1` and no
arm-asymmetric settings active (`asymmetric_settings_active: {}` in each run's `runmeta` block).
The drift argument does not apply to them, and the ordering is a direct measurement:

| run id | arm / config | score | correct |
|---|---|---|---|
| `plan_g_hyb` | entire-graph, `EG_INGEST_GRANULARITY=turn+session` | **94.68** | 1458/1540 |
| `plan_g_mem0` | mem0-OSS (post-`top_k` fix) | **93.77** | 1444/1540 |
| `plan_g_base` | entire-graph, default `session` granularity | **92.14** | 1419/1540 |

entire-graph's hybrid ingest granularity beats mem0 by **+0.91pp** here, and that is orderable.

**State the third row whenever you state the first.** In the same window, entire-graph's *default*
`session` granularity scores **1.62pp below** mem0. The win belongs to the `turn+session` hybrid,
not to entire-graph's out-of-the-box configuration, and reporting only the winning row would be
exactly the kind of selection this kit exists to rule out.

### Cross-window rows — report, do not rank

The field-window rows (`RESULTS.md` §2: mem0 93.44, cognee 92.86, entire-graph 92.73) were measured
hours apart from each other. They span 0.71 points against a 2.21-point drift band. Report them;
do not order them. The same applies to `full_cmm` (91.30) and `full_graphify` (87.34), which ran in
a later window again.

### Oracle rows — never publish

`plan_f_ceil` (96.23) and `plan_s_ceil` (94.00) ran with `EG_FULL_CONTEXT=1` and
**`fair_mode: false`**. They are retrieval-ceiling oracles that hand the answerer the whole
haystack. They exist to bound what retrieval could theoretically buy. They are not a result and
must never appear in a comparison table.

### Pending: same-window mem0 control

A same-window mem0 control (`sw_mem0`) is in flight — mem0 re-answering in the current window
against the current entire-graph binary, which removes the cross-window question from the headline
entirely rather than arguing around it. Interim at n=414: entire-graph 95.41 vs mem0 93.48,
**+1.93pp**, discordant 12–4. *This paragraph will be replaced with the final n=1540 result and its
run id when the run lands; until then the interim figure is not a publishable row.*

### What is not in doubt

None of the above touches cost and speed (`RESULTS.md` §5), where entire-graph's advantage is
**orders of magnitude** — 0.55s vs ~131s build per history, 0 ingest LLM calls vs LLM extraction,
corpus ingest 8.08s vs 20,471s. That gap is thousands of times wider than any drift band and is
the claim that does not depend on which window you measured in.
