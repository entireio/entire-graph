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
| retrieval budget | `top_k = 200` requested for every arm; supermemory's API hard-caps it at 100 (disclosed below) |
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

Five upstream files are modified: provider/authentication and timeout handling, optional date
injection, retry and drop accounting, a server-side fix, and `requirements.txt`. The requirements
patch pins the tested OpenAI client and Azure Identity library and declares the BM25 arm's two
dependencies (`rank-bm25`, `PyStemmer`; the stopword list is inlined, not an NLTK dependency).
Each patch is apply-checked against the pinned commit. Everything else is unmodified upstream code,
**including the reader and judge prompts**. See [`UPSTREAM.md`](UPSTREAM.md) for the file-level
provenance manifest and the full hash-locked environment.

### 3. Copy in the adapters

```bash
cp -r <kit>/benchmarks/common/*.py benchmarks/common/
```

New files only. Per-system adapters plus the fairness guard. No upstream source is touched.

### 4. Install the locked Python environment

```bash
KIT_DIR=/absolute/path/to/entire-graph/bench/memory
cp "$KIT_DIR/requirements-lock-py312.txt" .
python3.12 -m venv .venv
.venv/bin/python -m pip install \
  --require-hashes --only-binary=:all: --no-deps \
  -r requirements-lock-py312.txt
.venv/bin/python -m pip check
```

Do not replace this with `pip install -r requirements.txt` or upgrade pip during the run. The
committed lock fixes the complete dependency graph and every accepted artifact; `--no-deps`
prevents an undeclared package fetch. Copying the lock into the reconstructed harness lets
`runmeta.code_hashes()` bind its exact contents to every result artifact. See
[`UPSTREAM.md`](UPSTREAM.md) for its source digest, regeneration command, and platform scope.

### 5. Keyless authentication

```bash
az login
export AZURE_AI_ENDPOINT=...
export AZURE_AI_API_VERSION=2024-05-01-preview
```

The launcher requires the endpoint and has no API-key input. The patched harness
uses `DefaultAzureCredential` outside GitHub Actions, so a local Azure CLI login, managed identity,
or another supported Azure Identity credential supplies short-lived bearer tokens and refreshes
them during the run. Grant that identity the MaaS-only custom role documented below on the specific
Foundry resource. Microsoft's built-in `Cognitive Services User` role also works, but is broader:
it can list account keys and grants all Cognitive Services data actions. Owner or Contributor alone
does not grant data-plane inference access.

In GitHub Actions the harness instead exchanges fresh runner OIDC assertions directly with Entra.
The workflow supplies the application and tenant identifiers; there is no model key, static bearer
token, Key Vault lookup, or Azure CLI login in the job.

### 6. Run an arm

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
conversation as a void run. `run_locomo.sh` now refuses `resume` for every arm that buffers
in-process (`entire`, `graphify`, `cmm`, `bm25`) rather than letting a partial corpus report as a
complete run.

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

All 1,540 questions, shared reader and judge, `top_k = 200` requested, zero dropped questions in
every row. Two rows are not stock software and one did not receive the full retrieval budget; both
are marked and explained under the table.

| system | LoCoMo | ingest LLM calls | ingest tokens |
|---|---|---|---|
| **entire-graph** | **94.74** | **0** | **0** |
| mem0 OSS | 93.83 | 5,882 | 50.85M |
| cognee | 92.86 | 11,749 | 12.35M |
| BM25 | 91.88 | 0 | 0 |
| cmm (patched, Markdown-Section) † | 91.30 | 0 | 0 |
| graphify | 87.34 | 0 | 0 |
| letta | 84.68 | not projectable | not projectable |
| supermemory (patched, top-100 cap) ‡ | 82.08 | not measurable | not measurable |

† **cmm is not stock v0.9.0.** It was patched to emit Markdown sections
(`patches/0005-cmm-v0.9.0-markdown-sections.patch`, vendored here).

‡ **supermemory is not stock, and did not get the full retrieval budget.** Its server needed a
binary capability-flag patch and a wire-level parameter adapter before it would reach the shared
extraction model, plus a content-derived `custom_id` so retries survived its own dedup. Its search
API then hard-caps `limit` at 100, so it answered from an **effective top-100 budget while every
other row got 200** — an asymmetry that works against supermemory, disclosed rather than worked
around. Unlike the other modified arms, these two changes are **not vendored as patch files** (the
infrastructure that produced them was decommissioned); they are described in full, but this row is
the one row in the table you cannot rebuild from this repository alone. See `LOCOMO-COMPARISON.md`
§ ‡.

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
on: all 1,540 question records present and internally reconciled, the captured command selecting
the `entire` backend, `FAIR_MODE` active, the canonical LLM controls, no non-baseline `EG_*`,
`ENTIRE_*`, `MEM0_*`, or `HARNESS_*` retrieval overrides, zero dropped or empty-context searches,
and source hashes that match the reconstructed harness. A plausibility floor treats a collapse as
a broken harness rather than a regression. Read the score as a trend across runs, not against last
night's.

### Security model

- **`schedule` only fires on this repository's default branch.** A fork cannot schedule a workflow
  upstream, so the nightly path is not reachable from outside.
- **`pull_request_target` is absent, deliberately.** It is the trigger that hands repository
  secrets to fork code. Do not add it to that workflow.
- **`workflow_dispatch` requires write access** and uses the `benchmark` environment, whose
  required reviewers gate manual runs. Scheduled runs use the separate `benchmark-nightly`
  environment, restricted to `main` but intentionally configured without required reviewers, so
  the nightly schedule does not wait for manual approval.
- **No model key or long-lived credential is stored in GitHub.** The harness obtains a GitHub OIDC
  assertion only in the benchmark job, exchanges it directly for a Foundry data-plane bearer token,
  and refreshes the assertion and access token during the multi-hour run. There is no
  `azure/login`, Azure CLI session, Key Vault, or static bearer token.
- **The federated principal is data-plane only.** It has a custom role containing only
  `Microsoft.CognitiveServices/accounts/MaaS/*`, scoped to one Foundry resource, and no control-plane,
  subscription, resource-group, deployment-management, key-listing, or Key Vault action.
- **Stated limit:** `id-token: write` is a job-level permission, so code in any step can request a
  GitHub OIDC assertion. The workflow scopes the Entra application and tenant identifiers to the
  benchmark step and installs dependencies before that step, but installed dependency code executes
  later with those identifiers. OIDC removes the reusable model key; it does not make trusted
  runtime code harmless. As a separate supply-chain control, CI installs the committed complete
  dependency graph from `requirements-lock-py312.txt` with exact versions, artifact hashes,
  binary-only enforcement, and dependency discovery disabled, then runs `pip check`. This prevents
  a newly published or modified package from entering a scheduled run without a reviewed lockfile
  change; it cannot protect against malicious behavior already present in an intentionally locked
  dependency or in the benchmark code itself.

### One-time setup (an operator does this once; it is not in the repo)

1. **Create one Entra application and service principal for this workflow.** Record the application
   (client) ID and directory (tenant) ID; neither is a credential.

   ```bash
   TENANT_ID=$(az account show --query tenantId -o tsv)
   APP_ID=$(az ad app create --display-name entire-graph-locomo --query appId -o tsv)
   az ad sp create --id "$APP_ID"
   APP_OBJECT_ID=$(az ad app show --id "$APP_ID" --query id -o tsv)
   SP_OBJECT_ID=$(az ad sp show --id "$APP_ID" --query id -o tsv)
   ```

2. **Opt this repository into GitHub's immutable OIDC subject format.** Its stable owner ID is
   `33188652` and repository ID is `1252350673`; keeping those IDs in the `sub` prevents a renamed,
   transferred, or later recreated namespace from inheriting this trust. Before changing this
   repository-wide setting, inventory any other OIDC consumers and add their immutable subjects to
   the corresponding cloud trust policies so the migration does not break them.

   ```bash
   gh api repos/entireio/entire-graph --jq '{owner_id: .owner.id, repository_id: .id}'
   gh api repos/entireio/entire-graph/actions/oidc/customization/sub
   gh api --method PUT \
     -H "X-GitHub-Api-Version: 2026-03-10" \
     repos/entireio/entire-graph/actions/oidc/customization/sub \
     -F use_default=true \
     -F use_immutable_subject=true
   gh api repos/entireio/entire-graph/actions/oidc/customization/sub
   ```

   The last command must report `"use_immutable_subject": true`. See
   [GitHub's immutable OIDC subject guidance](https://docs.github.com/actions/reference/security/oidc#immutable-subject-claims).

3. **Add exactly two federated identity credentials.** The exact immutable environment subjects,
   rather than a broad branch or mutable repository-name subject, make each GitHub environment part
   of the trust boundary. The manual credential names `benchmark`; the unattended schedule names
   `benchmark-nightly`. Both use the `api://AzureADTokenExchange` audience requested by the harness.

   ```bash
   az ad app federated-credential create \
     --id "$APP_OBJECT_ID" \
     --parameters '{
       "name": "github-entire-graph-benchmark",
       "issuer": "https://token.actions.githubusercontent.com",
       "subject": "repo:entireio@33188652/entire-graph@1252350673:environment:benchmark",
       "audiences": ["api://AzureADTokenExchange"]
     }'

   az ad app federated-credential create \
     --id "$APP_OBJECT_ID" \
     --parameters '{
       "name": "github-entire-graph-benchmark-nightly",
       "issuer": "https://token.actions.githubusercontent.com",
       "subject": "repo:entireio@33188652/entire-graph@1252350673:environment:benchmark-nightly",
       "audiences": ["api://AzureADTokenExchange"]
     }'
   ```

4. **Grant only MaaS data-plane access on the target Foundry resource.** Microsoft documents
   `Microsoft.CognitiveServices/accounts/MaaS/*` as the custom-role permission for this multi-model
   inference endpoint. Use that instead of the built-in `Cognitive Services User` role: despite its
   name, that built-in role can list account keys and grants every Cognitive Services data action.
   Make the custom role assignable only in the containing resource group, then scope its assignment
   to the `Microsoft.CognitiveServices/accounts` resource itself.

   ```bash
   FOUNDRY_RESOURCE_ID=$(az cognitiveservices account show \
     --resource-group <resource-group> \
     --name <foundry-resource-name> \
     --query id -o tsv)
   FOUNDRY_RESOURCE_GROUP_ID=$(az group show \
     --name <resource-group> \
     --query id -o tsv)

   jq -n --arg scope "$FOUNDRY_RESOURCE_GROUP_ID" '{
     Name: "Entire Graph LoCoMo Inference",
     IsCustom: true,
     Description: "Invoke MaaS models for the LoCoMo benchmark; no keys or control plane",
     Actions: [],
     NotActions: [],
     DataActions: ["Microsoft.CognitiveServices/accounts/MaaS/*"],
     NotDataActions: [],
     AssignableScopes: [$scope]
   }' > locomo-inference-role.json

   ROLE_ID=$(az role definition create \
     --role-definition locomo-inference-role.json \
     --query name -o tsv)
   az role assignment create \
     --assignee-object-id "$SP_OBJECT_ID" \
     --assignee-principal-type ServicePrincipal \
     --role "$ROLE_ID" \
     --scope "$FOUNDRY_RESOURCE_ID"
   ```

   The `/models/chat/completions` API used here requests
   `https://cognitiveservices.azure.com/.default`. Do not grant `Cognitive Services User`,
   Contributor, Owner, Key Vault access, or a subscription-level role, and do not create or store a
   model API key for CI. See
   [Foundry keyless authentication](https://learn.microsoft.com/azure/foundry/foundry-models/how-to/configure-entra-id)
   and [Entra workload identity federation](https://learn.microsoft.com/entra/workload-id/workload-identity-federation-create-trust).

5. **Create both GitHub environments.** Restrict `benchmark` and `benchmark-nightly` to deployment
   branch `main`. Add required reviewers to `benchmark` for manual dispatches. Do not add required
   reviewers to `benchmark-nightly`; GitHub would otherwise queue every scheduled run for approval.
6. **Set three GitHub repository variables:** `AZURE_CLIENT_ID` to `APP_ID`, `AZURE_TENANT_ID` to
   `TENANT_ID`, and `AZURE_AI_ENDPOINT` to
   `https://<foundry-resource-name>.services.ai.azure.com`. They are identifiers, not secrets.

Verify the wiring with a `workflow_dispatch` run before trusting the schedule.

### Runner sizing

The published runs took 2.5–3.5h on a 32-vCPU host. GitHub's hosted `ubuntu-latest` is far smaller.
The workflow gives the benchmark command 270 minutes, sends SIGINT at that boundary, allows a
10-minute forced-shutdown backstop, and keeps the step/job ceilings at 285/330 minutes. That reserve
lets the `always()` diagnostics, fail-closed summary, and partial-result upload run before GitHub's
hard job ceiling. The query phase is network-bound on the Foundry deployment and the launcher
throttles itself (`--max-workers 3 --question-workers 10 --rpm 60`), so the runner's core count
mostly affects index build, not the 1,540 questions. If runs start timing out, move to a larger
runner before touching those throttles — they are load-bearing for fairness, not performance tuning.
