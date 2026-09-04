# FAIR-CONFIG — the one config every arm's run must cite

**Status: in force.** This is the configuration the published runs were produced under. Written
from fixes and verifications that were performed by running the harness, not by reading it. Every
run (preflight or full) must reference this file's git-free hash (see bottom) in its log header.

## 1. Model spine (identical across every arm, no exceptions)

| role | model |
|---|---|
| answerer | `gpt-5.6-sol` |
| judge | `gpt-5.6-sol` |
| provider | `azure_ai` |
| ingest extractor (LLM-extraction arms: cognee, letta, graphiti, supermemory) | `azure_ai/gpt-5.6-terra` |
| ingest extractor (eg) | none — 0-LLM, deterministic, by design |
| top_k | 200 |
| top_k_cutoffs | 200,50,20,10 |
| datasets | LoCoMo-10 (`datasets/locomo/locomo10.json`), LongMemEval-S (`datasets/longmemeval/longmemeval_s_cleaned.json`) |
| categories evaluated (LoCoMo) | 1,2,3,4 (multi-hop, temporal, open-domain, single-hop) — category 5 (adversarial) excluded |
| all_questions | `true` for every LongMemEval run — `--per-type` subsets are never a headline row |

## 2. Fixes required before ANY run counts

1. **mem0 top_k bug** — `/app/main.py:351` inside the `fieldmem0-mem0-1` container must read
   `params: dict[str, Any] = {"top_k": req.limit}`, not `{"limit": req.limit}`. Also fixed at the
   durable build-context source: `~/mem0harness/docker/mem0/main.py`. Verify live before trusting
   any mem0 run: `bash ~/mem0harness/verify_mem0_topk_fix.sh` must print `VERIFIED`.
2. **Empty-buffer defect** (cognee, graphiti, letta, supermemory clients) — `search()` must raise
   `RuntimeError("BUFFER_MISSING...")` on a missing in-memory buffer, never silently return `[]`.
   Verify: `grep -c 'BUFFER_MISSING' benchmarks/common/{cognee,graphiti,letta,supermemory}_client.py`
   must be ≥1 in each file.
3. **Cognee LLM-extractor env** — `COGNEE_LLM_MODEL=azure_ai/gpt-5.6-terra` must be set explicitly
   at launch (its own code default is the wrong `azure_ai/gpt-5`).

## 3. State-root rule (the lesson from this reboot)

**No benchmark state may live under `/tmp`.** systemd wipes `/tmp` on every boot; this is exactly
how cognee's 2.7GB LoCoMo state and graphiti's FalkorDB state were lost across the VM resize.
Every state-root env var (`COGNEE_STATE_ROOT`-equivalent tempfile roots, `GRAPHITI_STATE_ROOT`,
`LETTA_STATE_ROOT`) must point under `$HOME` (e.g. `$HOME/memarms/state/<arm>/`), never rely on
the default `tempfile.mkdtemp()` (which resolves under `/tmp`). Add this override to every launch
command going forward.

## 4. Per-arm launch template

```
AZURE_AI_ENDPOINT=<endpoint> AZURE_AI_API_VERSION=2024-05-01-preview \
  [COGNEE_LLM_MODEL=azure_ai/gpt-5.6-terra] \
  [<ARM>_STATE_ROOT=$HOME/memarms/state/<arm>_<dataset>] \
  .venv/bin/python -m benchmarks.<locomo|longmemeval>.run \
  --project-name <preflight_|field_>_<arm>_<dataset> --backend <arm> --provider azure_ai \
  --answerer-model gpt-5.6-sol --judge-model gpt-5.6-sol \
  --top-k 200 --top-k-cutoffs 200,50,20,10 \
  --max-workers <N, see load envelope> [--all-questions | --dataset-path ...] \
  --run-id <same as project-name>
```

Authenticate with Microsoft Entra ID before starting the launcher. GitHub Actions uses its OIDC
federated identity and refreshes the assertion during long runs; local and hosted runs use
`DefaultAzureCredential`. The original 2026-08 measurement artifacts used a Foundry API key. This
successor changes only the authentication header—the endpoint, request payload, model, prompts,
retrieval, and scoring remain unchanged—and its new harness hash must be recorded with every run.

`preflight_*` project names are smoke tests only — a handful of questions, never scored as a
publishable row, never resumed into a `field_*` run.

## 5. Load envelope (32 vCPU benchmark host)

Target sustained load **under ~24** (raised from the 16-vCPU box's ~20 target, proportionally, with
headroom for the bge embedder's own 16 workers + qdrant + postgres + supermemory + any concurrent
egopt/audit work). Embedder must be re-verified under load (target <1s per call) before trusting a
concurrent multi-arm launch. Scale per-arm `--max-workers` down immediately if sustained load
exceeds this — restarting a run is exactly when the empty-buffer defect can recur, so any
throttle-triggered restart must re-run the empty-buffer audit before the result is trusted.

## 6. `--deep` as a disclosed eg product option (not a tuned winner)

`entire graph search --deep` (BM25 fusion) is a real product flag, gated by
`entire_client.py` (`["--deep"] if os.getenv("EG_DEEP")=="1"`, a no-op unless set). It changes only
eg's own retrieval — the category explicitly ruled fair (arms may differ in retrieval mechanism;
they may not differ in answerer/judge/prompts). Report BOTH the default and `--deep` eg rows,
clearly labelled with context sizes shown, never publish only the higher one.

## 7. Known-clean vs known-invalid going into this pass

| arm / result | verdict | reason |
|---|---|---|
| eg LoCoMo 92.73% | **CLEAN, provable** | code-path fingerprints: 0/1540 session-expand-shaped ids, 0/1540 with `user_profile`, `EG_ANSWER_ENUM` absent from LoCoMo prompts entirely (LME-only lever) |
| cognee LoCoMo 92.86% | **CLEAN** (re-audited post empty-buffer-fix) | but its `/tmp` ingest state is lost; re-running costs a full ~4.8h re-ingest — KEEP the banked artifact |
| mem0-OSS LoCoMo 87.40% | **INVALID** | exact top_k=20 signature confirmed (mean=median=max=min=20.0 across all 1540) — same root cause as the LME bug. Re-run is mandatory, retrieval-only (qdrant data survived). |
| letta LoCoMo 80.58% → **84.68%** | **RESOLVED — re-audited clean** | fresh VM, fresh postgres+pgvector, rebuilt agent-driven ingest (`field_letta_loco_v3`, 2026-08-19). Zero drops, zero empty-context, zero errors across all 1540 questions. No defect found in the original 80.58 — the original infrastructure was deleted before a re-audit could run against it, so this is a clean re-measurement that supersedes a suspect number, not a bug fix. |
| supermemory LoCoMo 77.60% → **82.08%** | **RESOLVED — two real defects found and fixed, one limitation disclosed** | fresh VM (`field_sm_loco_v3`, 2026-08-20). Confirmed: (1) the extraction-model incompatibility suspected here was real — supermemory's server was silently degrading structured-output calls, fixed with a 2-byte binary patch + wire-level Azure param adapter; (2) content-based dedup made retries self-defeating, fixed with a stable content-derived `custom_id`; (3) supermemory's search API hard-caps `limit` at 100 — disclosed (not fixable), `top_k=200` kept for cross-arm consistency, `limit_used=100` recorded per question. Zero drops, zero errors; 114/1540 (7.4%) empty-context retrievals are genuine misses, spread across 9/10 conversations, not clustered — see `LOCOMO-COMPARISON.md` §1 ‡. |
| graphiti | **n=529 subset only, per prior decision** | conv0/1/5 corrupted or pathological, conv6-9 never run, `/tmp` state now also lost — not a candidate for full re-run this pass |

## 8. Hash

`sha256sum FAIR-CONFIG.md` — every run's log header must cite this hash so a reader can verify
which config version produced which row.

---

# PART B — fixes, invariants and hashes (2026-08-13 17:30Z)

Everything below was applied to the live harness on the benchmark host and verified by running it,
not by reading it. Where a check is missing, it says so.

## B1. mem0 `top_k` — the bug, and why the fix now survives a rebuild

The OSS server received the caller's value correctly but handed it to the library under the
wrong keyword: `params = {"limit": req.limit}` against a signature of
`search(query, *, top_k=20, **kwargs)`. `limit` was swallowed by `**kwargs` and every mem0
search in the matrix silently returned the library default of 20 — on 500/500 LongMemEval and
1540/1540 LoCoMo questions. The client was never at fault; `mem0_client.py` sends `"limit"`
because that is what the deployed `SearchRequest` declares, and it must stay that way.

Fixed at `docker/mem0/main.py:351` → `params: dict[str, Any] = {"top_k": req.limit}`.

That file is now the **single source of truth**, and three independent layers carry it:

| layer | mechanism | survives |
|---|---|---|
| image | `fieldmem0-mem0:latest` rebuilt with the fixed `main.py` baked in (`28e6b4ab432c`; pre-fix image preserved as `fieldmem0-mem0:pre-topkfix` = `4b2533c444b3`) | `docker rm` + recreate, `--force-recreate` |
| mount | `~/mem0harness/docker/mem0/main.py` bind-mounted read-only over `/app/main.py` | image rebuilt from a stale context |
| boot | `~/memarms/mem0_boot.sh` refuses to start if the source lacks the fix, then asserts `limit=200 → 200` and exits non-zero if not | silent regression of either layer above |

`~/memarms/mem0fix/main.py` is a symlink to the canonical file, so the two copies cannot drift.

**Start mem0 only via `bash ~/memarms/mem0_boot.sh`** (`RECREATE=1` to rebuild the container).
Proof, run twice including one full destroy-and-recreate from the rebuilt image:

```
in-container line 351:     params: dict[str, Any] = {"top_k": req.limit}
VERIFY limit=5   -> returned 5
VERIFY limit=200 -> returned 200
mem0 boot OK
```

Cross-checked with the independent `~/mem0harness/verify_mem0_topk_fix.sh`, which prints
`VERIFIED: top_k fix is live and working (counts vary correctly with the request).`

## B2. The mem0 endpoint is port 18888, and nothing defaults to it

`mem0_client.py:72` defaults to `http://localhost:8888`; the container publishes **18888**.
A run that omits the host connects to nothing, retries five times, and — before the fix in B5 —
recorded the result as an empty context rather than an error. **Every mem0 launch must set
`MEM0_HOST=http://127.0.0.1:18888`** (or pass `--mem0-host`). This is not optional and it is not
currently in `run.env`.

## B3. Effective `top_k` must be measured, never assumed

The entire top_k bug was invisible in configuration: every launcher said 200 and every search
returned 20. Nominal config is not evidence. Before any arm's numbers are believed, read the
**returned item count** out of the artifacts and confirm it (a) exceeds the suspicious round
number 20 and (b) **varies across questions** — a constant count is the signature of a silent cap.

```
python - <<'PY'
import json,glob,collections
for f in sorted(glob.glob("results/*/predicted_<run_id>/*.json")):
    d=json.load(open(f)); c=collections.Counter()
    c[d["retrieval"]["total_results"]]+=1
print(c)   # a single key == capped; a spread == real
PY
```

## B4. State roots live under `$HOME`, never `/tmp`

systemd cleared `/tmp` on the 16→32 vCPU reboot even though the filesystem itself persisted;
that destroyed cognee's 2.7 GB corpus state and `gr_full_state`. Several clients default to
`tempfile.mkdtemp()`, which resolves under `/tmp` — `entire_client.py:131`
(`ENTIRE_CORPUS_ROOT`) and `letta_client.py:51` (`LETTA_STATE_ROOT`) among them. Every launch
must pin its state root explicitly:

```
ENTIRE_CORPUS_ROOT=$HOME/memarms/state/eg_<dataset>
LETTA_STATE_ROOT=$HOME/memarms/state/letta_<dataset>
COGNEE_STATE_ROOT=$HOME/memarms/state/cognee_<dataset>
```

## B5. Two silent-failure defects found and fixed during pre-flight

Both convert an infrastructure fault into a measured capability loss, which is the most
dangerous class of benchmark bug: the number looks real.

1. **`locomo/run.py:474` discarded the search-drop flag.** After five failed retries the wrapper
   returns `([], True)`; LoCoMo unpacked it into `_dropped` and threw it away, so a question
   whose search never succeeded was answered against an empty context and scored as a miss.
   LongMemEval already recorded it (`longmemeval/run.py:617,729`) — the two benchmarks disagreed.
   LoCoMo now records `retrieval.search_dropped` too. **Any run containing dropped searches must
   have them dropped symmetrically across arms, or be re-run.**

2. **`entire_client.py:337` returned `[]` on a missing buffer while all four competitor clients
   raised.** cognee, graphiti, letta and supermemory each raise `RuntimeError("BUFFER_MISSING…")`;
   eg alone failed mute. The identical fault was therefore loud for every competitor and invisible
   for eg. eg now raises the same error. Verified — all five are guarded:

   ```
   cognee 1   graphiti 1   letta 1   supermemory 1   entire 1
   ```

## B6. Retrieval-only re-runs are impossible for five of six arms

This contradicts the working assumption that surviving stores imply cheap re-runs, so it is
stated plainly. `_buffers` is in-process only; **no rehydrate path exists anywhere in the
harness** (`grep -rn "rehydrate|_load_buffer|restore_buffer|from_checkpoint" benchmarks/common/`
returns nothing). `locomo/run.py:300-305` returns early on a complete ingestion checkpoint
*without* repopulating the buffer, so a resumed run reaches `search()` with no buffer and now —
correctly — raises.

| arm | store survived reboot | retrieval-only re-run possible |
|---|---|---|
| mem0 | yes — qdrant `field_memories`, 804 LoCoMo + 3546 LME points | **yes**, verified |
| supermemory | yes — `~/memarms/sm/data`, 1.3 GB | no — BUFFER_MISSING, needs re-ingest |
| letta | yes — postgres `letta`, 50,455 archival_passages / 157 archives | no — BUFFER_MISSING, needs re-ingest |
| eg | corpus root was under `/tmp` | no — needs re-ingest (0-LLM, so cheap and free) |
| cognee | no — 2.7 GB wiped from `/tmp` | no — needs re-ingest |
| graphiti | `gr_full_state` wiped; no neo4j running | no — needs re-ingest + neo4j |

Only mem0's re-run is free. The other five cost their ingest again — for eg that is deterministic
0-LLM work; for cognee, letta, graphiti and supermemory it is `gpt-5.6-terra` extraction spend.

## B7. Run artifacts are now self-documenting (this is why the audit was hard)

`benchmarks/common/runmeta.py` (new) is called at all four metadata sites —
`longmemeval/run.py:1263,1446` and `locomo/run.py:870,1024` — writing `metadata.env_capture`:

- `env` — every `EG_* ENTIRE_* MEM0_* QDRANT_* SUPERMEMORY_* SM_* LETTA_* COGNEE_* GRAPHITI_*
  NEO4J_* REDIS_* EMBED_* OPENAI_* AZURE_* ANTHROPIC_* LLM_* FAIR_* BENCH_* HARNESS_* COLLECTION_*`
  variable, with any name matching `KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API` replaced by a
  `sha256:` fingerprint so configs are comparable without leaking credentials
- `argv` — the literal command line, which is what records `--user-profile`
- `asymmetric_settings_active` and `fair_mode`
- `code_md5` — the 16-entry reconstructed-harness map in B9, including the Entra helper and
  dependency lock
- `host`

An audit should never again have to reconstruct a config from launcher scripts.

## B8. Arm-asymmetric settings are now refused by the harness, not just by policy

`runmeta.assert_fair_mode(args)` runs immediately after argument parsing
(`longmemeval/run.py:1111`, `locomo/run.py:788`). Under `FAIR_MODE=1` it hard-exits if any of
`EG_SESSION_EXPAND`, `EG_SESSION_EXPAND_CAP`, `EG_ANSWER_ENUM`, `EG_ANSWER_ENUM_R`,
`EG_USER_PROFILE`, or `--user-profile` is active. Verified live on both benchmarks:

```
FAIR_MODE=1 but arm-asymmetric settings are active: EG_SESSION_EXPAND=2, EG_ANSWER_ENUM=2
Unset them, or run without FAIR_MODE=1 for an exploratory run.

FAIR_MODE=1 but arm-asymmetric settings are active: --user-profile=True
```

**Every scored run sets `FAIR_MODE=1`.** Without it the guard is silent by design, so exploratory
work is still possible — but an exploratory run cannot be published, and `metadata.fair_mode`
records which it was. What the three settings did:

- `EG_SESSION_EXPAND=2` + `EG_SESSION_EXPAND_CAP=0` made `entire_client.py:471` read
  `(corpus/f"{stem}.md").read_text()` — full session bodies straight from the **dataset source**,
  uncapped, roughly 590K chars per question. No competitor has any comparable path.
- `EG_ANSWER_ENUM` appended `_ENUM_PROTOCOL` to the answerer prompt at
  `longmemeval/prompts.py:302-306` for eg's runs only.
- `--user-profile` added a `## User Profile` section built by `entire_client.py:587`, present on
  498/500 eg questions and 0/500 mem0 questions.

The fair config uses eg's plain atomic retrieval: none of these set, no dataset-source reads.

## B9. Canonical code hashes — parity is provable

The five LoCoMo arms in the audited matrix did **not** run identical code (the search-retry
wrapper landed 04:43, the buffer-invariant guard 14:57), which alone invalidates cross-arm
comparison. A fresh reconstruction of the current kit reports the following exact 16-entry
`metadata.env_capture.code_md5` map. It now binds the Entra authentication helper and copied
dependency lock as well as the benchmark code:

```
51c48c7e947c8ce51581a65e789e5174  benchmarks/common/bm25_client.py
f6c94d43cab55d51c82d7c4117a384cd  benchmarks/common/cmm_client.py
46b628c9f8f53f84f6c2ce07f07ba318  benchmarks/common/entire_client.py
3f7d918dc36ccc066ebdd4cbad3e80dc  benchmarks/common/entra_auth.py
055bae76ec1da3f7d86a4349aee31daf  benchmarks/common/graphify_client.py
592bbcc560b15b88aabb2c9d0280380f  benchmarks/common/llm_client.py
041f93a130c1a91d1b81f67622555b8c  benchmarks/common/mem0_client.py
abdbb9f272e4265153b7e3e71837007e  benchmarks/common/metrics.py
5fcdb16d2711068bc3355d820a45c63f  benchmarks/common/runmeta.py
7083a692eecbee5f73834e8f1d7f6804  benchmarks/common/test_bm25_client.py
4fc59cb9e449551eac2b31b35230b0dd  benchmarks/common/utils.py
8e0106beab951536141d39bf88d9ea27  benchmarks/locomo/prompts.py
41158a8eb87cdeeb53d23c3ad845b7bc  benchmarks/locomo/run.py
180750bea9900b826dd5990fc9e16787  benchmarks/longmemeval/prompts.py
632e01d52537e5b931994d61a246cd9b  benchmarks/longmemeval/run.py
72544a7a6b0f0a10103d640b1f281e68  requirements-lock-py312.txt
```

The patched `docker/mem0/main.py` runs outside the harness process and is therefore not in this
map; [`UPSTREAM.md`](UPSTREAM.md) pins it separately by content hash. Regenerate the map from the
fully reconstructed harness with the same function that writes run metadata:

```bash
cd ~/mem0harness
PYTHONPATH=. python - <<'PY'
from benchmarks.common import runmeta

for path, digest in runmeta.code_hashes().items():
    print(f"{digest}  {path}")
PY
```

## B10. Copy-pasteable launch

```bash
cd ~/mem0harness
set -a; . ~/memarms/run.env; set +a          # credentials only

bash ~/memarms/mem0_boot.sh                   # asserts limit=200 -> 200 before anything runs

export FAIR_MODE=1                            # refuses arm-asymmetric settings
export MEM0_HOST=http://127.0.0.1:18888       # the client default (8888) is wrong
unset EG_SESSION_EXPAND EG_SESSION_EXPAND_CAP EG_ANSWER_ENUM EG_ANSWER_ENUM_R EG_USER_PROFILE

ARM=<oss|entire|supermemory|letta|cognee|graphiti>
DS=<locomo|longmemeval>
export ENTIRE_CORPUS_ROOT=$HOME/memarms/state/eg_$DS
export LETTA_STATE_ROOT=$HOME/memarms/state/letta_$DS
export COGNEE_STATE_ROOT=$HOME/memarms/state/cognee_$DS
[ "$ARM" = cognee ] && export COGNEE_LLM_MODEL=azure_ai/gpt-5.6-terra

.venv/bin/python -m benchmarks.$DS.run \
  --project-name field_${ARM}_${DS} --run-id field_${ARM}_${DS} \
  --backend $ARM --provider azure_ai \
  --answerer-model gpt-5.6-sol --judge-model gpt-5.6-sol \
  --top-k 200 --top-k-cutoffs 200,50,20,10 \
  --max-workers 8 --all-questions
```

No `--user-profile`. No `EG_*`. Same models, same top_k, same code hashes for every arm.
Swap `field_` for `preflight_` for smokes; a `preflight_` run is never a published row.

## B11. Pre-flight smoke results (2026-08-13, LoCoMo conv 0, 3 questions, `top_k=200`)

`~/mem0harness/preflight_smoke.py` exercises each arm's real `search()` with no ingest and no
answerer/judge spend.

| arm | status | items returned | context chars |
|---|---|---|---|
| mem0 | **OK** | `[200, 200, 200]` | `[38317, 37900, 38573]` |
| eg | BUFFER_MISSING (expected) | — | — |
| supermemory | BUFFER_MISSING (expected) | — | — |
| letta | BUFFER_MISSING (expected) | — | — |
| cognee | BUFFER_MISSING (expected) | — | — |
| graphiti | BUFFER_MISSING (expected) | — | — |

mem0 is the headline check: **200 items, not 20**, at 0.33–0.61 s per search. The five
BUFFER_MISSING results are the correct new behaviour from B5/B6 — a standalone retrieval smoke
has no in-process buffer. Their real pre-flight is the first few questions of an ingesting run,
which cannot be done under the current no-runs hold.

## B12. Service state after the 16→32 vCPU reboot

| service | state | evidence |
|---|---|---|
| qdrant | up, data intact | `fieldmem0-qdrant-1`, collections `field_memories` (+entities), 42 distinct user_ids |
| mem0 OSS | up, **fix live** | `fieldmem0-mem0-1` on :18888, `limit=200 → 200` |
| postgres | up, letta data intact | `memarms-pg` :55434, db `letta`, 50,455 archival_passages |
| bge embedder | up, 16 workers | `:18080/health` → `{"status":"ok","model":"BAAI/bge-large-en-v1.5","dim":1024}` |
| supermemory | up | `supermemory-server-0.0.7-rc.2`, 1.3 GB under `~/memarms/sm/data` |
| neo4j / redis | **not running** | graphiti cannot run until these are started |

Embedder throughput did **not** improve with more workers — 41.8 req/s at 6 workers vs 40.4 at 16
(200 requests, 32 concurrent; p50 618 ms → 685 ms). At 64 concurrent it degrades to 35.7 req/s,
p95 3.9 s. The ceiling is not worker count, and the measurement client is single-process and may
itself be the limit. 16 workers is kept for headroom under concurrent arms, but **treat the
sub-1 s target as unverified above ~32 concurrent** and re-measure with a real multi-arm load
before trusting it.

## B13. Not fixed / not verified — stated plainly

- **neo4j and redis are down.** graphiti cannot run at all until they are started; they were not
  started.
- **The five buffered arms have no live retrieval smoke.** Their pipelines are proven only as far
  as `search()` raising correctly. End-to-end proof needs an ingesting run, blocked by the hold.
- **Embedder headroom under real multi-arm load is unmeasured** (see B12).
- **`MEM0_HOST` is still absent from `run.env`.** Until it is added there, every launcher must
  export it (B2/B10) or connect to nothing.
- **`metadata.git` depends on how the harness was reconstructed.** Nightly CI retains the upstream
  clone's `.git` directory, so it records commit `4b61c5d31b9c668a12b4f5e78064248a02c82d2b`
  with `dirty: true` after patches and copied files. Older manually copied harness directories may
  still record an empty commit. `code_md5` remains the content-level parity record in both cases
  (B9).
- **The audited historical numbers are not repaired by any of this.** Every LoCoMo and LongMemEval
  mem0 row in the existing matrix was produced at effective top_k=20 and must be re-run, not
  rescaled.
