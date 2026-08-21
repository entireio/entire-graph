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
git checkout 212af697863a42a7260ff5bba353db3753d00253
# everything below lives in bench/memory/
```

That commit pins the adapter used for the published results. The 0.4.0 release
corrects BM25 candidate selection for matches with negative IDF scores, so its
BM25 adapter is intentionally not byte-identical to the published run.

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
accounting, a server-side fix, and `requirements.txt` for the BM25 arm's two declared dependencies
(`rank-bm25`, `PyStemmer`; the arm's stopword list is inlined, not an NLTK dependency). Each patch is
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
| letta | 84.68 | not projectable | not projectable |
| supermemory | 82.08 | not measurable | not measurable |

The margin between the top two is **0.91 points at p = 0.125**, against a 0.65-point noise floor. We
report the ranking and do not claim statistical significance for that margin. The ingest column is
the comparison not subject to a tie, and it is the one that recurs on every document change.

Full spec in [`FAIR-CONFIG.md`](FAIR-CONFIG.md). Results, retractions and disclosures in
[`LOCOMO-COMPARISON.md`](LOCOMO-COMPARISON.md). Every quoted number traces to an artifact via
[`RUN-INDEX.md`](RUN-INDEX.md).

---

## Nightly CI

`.github/workflows/locomo-nightly.yml` runs the **entire-graph arm only**, nightly at 06:00 UTC,
over the full 1,540 questions. It reconstructs the pinned upstream harness from
[`UPSTREAM.md`](UPSTREAM.md)'s commit plus this repo's patches, so CI runs the published
configuration rather than a CI-specific variant.

### What the run costs

At Foundry list prices for `gpt-5.6-sol` ($5/M input, $30/M output) and the launcher's
`--top-k-cutoffs 200`, one run is roughly **$190** — call it **$5.7k/month**. Ingest is $0: the
entire-graph arm makes no model calls to build its index. Widening `--top-k-cutoffs` back to
`200,50,20,10` quadruples the answerer and judge calls and takes a run to roughly $475, which is
why the launcher does not.

### Why the job does not fail on a score change

It fails on **integrity**, never on a delta. Run-to-run noise here is **0.65pp**, and an identical
entire-graph configuration re-run 26 hours apart drifted **2.21pt**
([`LOCOMO-COMPARISON.md`](LOCOMO-COMPARISON.md) §7). A nightly threshold inside that band would
alert on noise every few nights and be ignored within a week. `ci/summarize_run.py` therefore gates
on: all 1,540 questions scored, `FAIR_MODE` active, no arm-asymmetric settings, zero dropped
searches, and a plausibility floor that treats a collapse as a broken harness rather than a
regression. Read the score as a trend across runs, not against last night's.

### Security model

- **`schedule` only fires on this repository's default branch.** A fork cannot schedule a workflow
  upstream, so the nightly path is not reachable from outside.
- **`pull_request_target` is absent, deliberately.** It is the trigger that hands repository
  secrets to fork code. Do not add it to that workflow.
- **`workflow_dispatch` is gated** by the `benchmark` environment's required reviewers, on top of
  the write access the trigger already needs.
- **No long-lived credential is stored in GitHub.** The job takes a short-lived GitHub OIDC token,
  federates into Azure, and reads the Foundry key from Key Vault at run time. Rotation happens in
  Key Vault without touching this repo; deleting the federated credential revokes CI access
  immediately.
- **Stated limit:** the harness authenticates to Foundry with an `api-key` header
  (`patches/0001-*`), so the key is in the job environment while the benchmark runs, and any job
  with code execution can read its own environment. That is why this workflow runs only on the
  default branch, never runs fork code, and pins every action to a full commit SHA.

### One-time setup (an operator does this once; it is not in the repo)

1. **Azure AD app + federated credential.** Create an app registration, then a federated credential
   with issuer `https://token.actions.githubusercontent.com`, audience `api://AzureADTokenExchange`,
   and subject `repo:entireio/entire-graph:environment:benchmark`. Scoping the subject to the
   environment — not to `ref:refs/heads/main` — is what makes the environment's reviewer gate part
   of the credential's trust boundary.
2. **Key Vault.** Put the Foundry key in a vault as the secret `azure-ai-api-key`, and grant the
   app registration `Key Vault Secrets User` on that vault only.
3. **GitHub environment.** Create an environment named `benchmark`; add required reviewers and
   restrict its deployment branches to `main`.
4. **GitHub repository variables** (these are identifiers, not credentials, which is why they are
   variables and not secrets): `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`,
   `AZURE_KEY_VAULT_NAME`, `AZURE_AI_ENDPOINT`.

Verify the wiring with a `workflow_dispatch` run before trusting the schedule.

### Runner sizing

The published runs took 2.5–3.5h on a 32-vCPU host. GitHub's hosted `ubuntu-latest` is far smaller
and its hard job ceiling is 6h; the workflow sets `timeout-minutes: 330` so a slow run fails with
its artifacts uploaded rather than being killed mid-write. The query phase is network-bound on the
Foundry deployment and the launcher throttles itself (`--max-workers 3 --question-workers 10
--rpm 60`), so the runner's core count mostly affects index build, not the 1,540 questions. If runs
start timing out, move to a larger runner before touching those throttles — they are load-bearing
for fairness, not performance tuning.
