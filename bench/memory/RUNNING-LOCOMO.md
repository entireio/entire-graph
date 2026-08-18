# Running LoCoMo yourself

Everything needed to independently re-run the comparison in
[`LOCOMO-COMPARISON.md`](LOCOMO-COMPARISON.md), including the five settings that silently produce
wrong numbers if you get them wrong. We got each of them wrong at least once.

[`README.md`](README.md) is the terse version of this. This file is the one to read first if you
have not run this harness before.

---

## What you are running

LoCoMo is a long-conversation question-answering benchmark: ten multi-session conversations, 1,540
questions across four types. A memory system ingests the conversations, then for each question
returns the evidence it believes relevant. A reader model answers from that evidence and a judge
model scores the answer.

**The benchmark measures retrieval, not memory architecture.** Across every system we tested,
P(correct | gold evidence retrieved) is **0.96 to 0.97**. The reader is saturated. Almost all of the
spread between systems is in what came back from `search()`.

## Before you start

- An OpenAI-compatible chat endpoint, with a deployment for the reader/judge model and one for any
  extraction model your arms use. We used Azure AI.
- Roughly **2 to 3 hours per arm** for the question phase at the settings below.
- Ingest time varies enormously: **seconds** for a deterministic index, up to **5.7 hours** for an
  LLM-extraction system on this corpus.
- Disk. Some arms hold multiple gigabytes of state.

## The spine

Every arm must share these. If any differs between two arms, those two numbers cannot be compared,
and no amount of care elsewhere fixes it.

| parameter | value |
|---|---|
| reader (answerer) | one model, identical for every arm |
| judge | the same, identical for every arm |
| retrieval budget | `top_k = 200` for every arm |
| questions | all 1,540, no subsetting |
| ingest model | native per system, disclosed rather than equalised |
| scoring | the run's own aggregate, never a hand-summed subset |

**Why ingest is not equalised.** A deterministic index calls no model at ingest; an extraction system
calls one per chunk. That asymmetry is the thing being measured. Forcing them to match would erase
the result. It is disclosed in the output, not homogenised away.

---

## The run

### 0. Get the kit

```bash
git clone https://github.com/entireio/entire-graph.git
cd entire-graph
git checkout feat/prose-retrieval-and-benchmarks   # PR #104; after merge, main
# everything below lives in bench/memory/
```

### 1. Upstream harness at a pinned commit

```bash
git clone https://github.com/mem0ai/memory-benchmarks.git
cd memory-benchmarks
git checkout 4b61c5d31b9c
```

Pin it. The harness changes, and a floating checkout means your numbers and ours were produced by
different code. The fork point is verified byte-identical by checksum, not by a recorded hash.

### 2. Apply the patches

```bash
for p in <kit>/patches/000[1-4]-*.patch <kit>/patches/0006-*.patch; do git apply -p1 "$p"; done
```

`patches/0005` is excluded from that glob on purpose: it targets `codebase-memory-mcp`'s own repo,
not this harness, and is applied separately (see §3 below).

Five upstream files are modified: provider timeouts, optional date injection, retry and drop
accounting, a server-side fix, and `requirements.txt` for the BM25 arm's two pinned dependencies
(`rank-bm25`, `PyStemmer`; the arm's stopword list is inlined, not an NLTK dependency). Each is
apply-checked against the pinned commit. Everything else is unmodified upstream code, **including
the reader and judge prompts**. See [`UPSTREAM.md`](UPSTREAM.md) for the file-level provenance
manifest.

### 3. Copy in the adapters

```bash
cp -r <kit>/benchmarks/common/*.py benchmarks/common/
```

New files only. Per-system adapters plus the fairness guard. No upstream source is touched.

### 4. Credentials, by name

```bash
export AZURE_AI_API_KEY=...
export AZURE_AI_ENDPOINT=...
export AZURE_AI_API_VERSION=2024-05-01-preview
```

The launcher refuses to start without these. Never commit the values, and prefer a key you can
rotate afterwards.

### 5. Run an arm

```bash
bash <kit>/run_locomo.sh cmm
```

One arm at a time, or at most three concurrently. The launcher refuses to start a second copy of the
same project name, a guard we added after two processes on one name destroyed a run.

---

## Five things that silently produce wrong numbers

Each produced a plausible-looking result that was false. None raised an error at the time.

### 1. The default concurrency

The harness defaults to **100 question-workers at 200 rpm**. Against a shared deployment that is
roughly 300 concurrent requests, which saturates the endpoint so thoroughly that almost every call
times out and retries.

**Use `--max-workers 3 --question-workers 10 --rpm 60` with `LLM_TIMEOUT=600`.** Fewer, more patient
requests finish dramatically faster. One arm went from **0 questions in ten minutes to 660 in
eighteen** after this change alone.

### 2. State roots under `/tmp`

The default temporary directory resolves under `/tmp`, which systemd wipes on boot. Multi-gigabyte
arm state disappeared this way.

**Put state roots under `$HOME`.** `run_locomo.sh` does this; check it if you adapt the script.

### 3. Resuming into an empty buffer

Several clients write ingest checkpoints to disk while holding the retrieval buffer **in memory**. A
resumed run sees the checkpoints, skips ingest, then searches *nothing*, answering every question
confidently from an empty context.

This destroyed one system's score by **13.77 points** before we found it. Make `search()` raise on a
missing buffer rather than return an empty list, and treat zero-context questions clustered by
conversation as a void run.

### 4. Connect timeouts, not read timeouts

`LLM_TIMEOUT` sets the **read** timeout. The **connect** timeout is separate and was hardcoded at 15
seconds. Every worker's first call opens a fresh connection simultaneously; on a NAT'd host those
handshakes are dropped and every one fails at exactly 15.02 s. Unjittered backoff then keeps the
whole group synchronised through all five retries.

Cost: ten questions per run scored wrong, worth **0.65 points**, which was **larger than the effect
we were trying to measure**. Raise the connect timeout and jitter the backoff.

### 5. Comparing runs from different windows

We re-ran an identical configuration 26 hours apart and got results **2.21 points apart**, with
byte-identical retrieval on 299 of 300 questions. The systems had not changed; the endpoint's
behaviour had.

**Run the arms you intend to compare concurrently.** Two identical runs of one system differ by
**0.65 points** and disagree on **11.3% of individual answers**. Treat that as your noise floor and
distrust any margin under it.

---

## Is your run valid?

Check these before reading the score. A run failing any of them is void, and dropping it is cheaper
than explaining it later.

- **Dropped questions under 1%**, dropped symmetrically across arms.
- **No zero-context retrievals**, and specifically none clustered by conversation, which is the
  signature of trap 3.
- **No empty generated answers.** A cluster at the start of a run is trap 4.
- **One process per project name.** Two processes sharing checkpoints corrupt each other silently.
- **Read the score from the run's own aggregate**, not from a re-derivation.

**Sanity check on any competitor.** A system scoring near zero is a broken integration until proven
otherwise. Three of the seven systems we measured were suppressed by defects in their own shipped
code, worth **+6.04**, **+13.77**, and one that returned no results on prose at all. Diagnose before
publishing.

---

## What we measured

All 1,540 questions, shared reader and judge, `top_k = 200`, zero dropped questions in every row.

| system | LoCoMo | ingest LLM calls | ingest tokens |
|---|---|---|---|
| **entire-graph** | **94.74** | **0** | **0** |
| mem0 OSS | 93.83 | 5,882 | 50.85M |
| cognee | 92.86 | 11,749 | 12.35M |
| BM25 | 91.88 | 0 | 0 |
| cmm | 91.30 | 0 | 0 |
| graphify | 87.34 | 0 | 0 |
| letta | 80.58 | not projectable | not projectable |
| supermemory | 77.60 | not measurable | not measurable |

The margin between the top two is **0.91 points at p = 0.125**, against a 0.65-point noise floor. We
report the ranking and do not claim statistical significance for that margin. The ingest column is
the comparison not subject to a tie, and it is the one that recurs on every document change.

Full spec in [`FAIR-CONFIG.md`](FAIR-CONFIG.md). Results, retractions and disclosures in
[`LOCOMO-COMPARISON.md`](LOCOMO-COMPARISON.md). Every quoted number traces to an artifact via
[`RUN-INDEX.md`](RUN-INDEX.md).
