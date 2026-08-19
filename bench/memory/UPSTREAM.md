# Upstream provenance

## The base the kit applies to

| | |
|---|---|
| upstream repo | `https://github.com/mem0ai/memory-benchmarks` |
| **upstream commit** | **`4b61c5d31b9c668a12b4f5e78064248a02c82d2b`** — short `4b61c5d31b9c`, dated 2026-05-13, `feat(results): update Mem0 Platform benchmark results with v3 + temporal reasoning (#8)` |
| upstream licence | Apache-2.0 (`LICENSE` at the repo root; relicensed from MIT in `f75666d33ef5`) |
| our fork on the benchmark host | `~/mem0harness` — a working copy, never initialised as a git repo, so the fork point was not recorded in git. It is pinned here **by content hash** against upstream instead (table below), which is stronger than a recorded SHA: every upstream-derived file in the fork is byte-verified against that commit. |

Reproduce the pin:

```bash
git clone https://github.com/mem0ai/memory-benchmarks.git && cd memory-benchmarks
git checkout 4b61c5d31b9c
md5sum benchmarks/common/metrics.py     # abdbb9f272e4265153b7e3e71837007e
```

## File-level manifest

`upstream md5` is the file at `4b61c5d31b9c`. `harness md5` is the file as it ran for the published
numbers.

### Unmodified upstream — NOT vendored here

These are byte-identical to upstream. They are third-party code with its own licence; take them
from the upstream clone.

| file | md5 (identical both sides) |
|---|---|
| `benchmarks/__init__.py` | `91882498349bd157592b52a06d1bf855` |
| `benchmarks/common/__init__.py` | `51d423adc472bf8c53048ee5beedd75e` |
| `benchmarks/common/metrics.py` | `abdbb9f272e4265153b7e3e71837007e` |
| `benchmarks/common/schema.py` | `29670569676eb1f57ca3cafef302cc08` |
| `benchmarks/common/utils.py` | `4fc59cb9e449551eac2b31b35230b0dd` |
| `benchmarks/locomo/prompts.py` | `8e0106beab951536141d39bf88d9ea27` |

`benchmarks/locomo/prompts.py` being unmodified is load-bearing: the answerer and judge prompts are
upstream's, untouched, and therefore identical for every arm.

### Substantially upstream, modified by us — patches vendored, files NOT vendored

| file | upstream md5 | harness md5 | patch |
|---|---|---|---|
| `benchmarks/common/llm_client.py` | `6a5da3c1d05dbf6a78cd364b59fc7a09` | `9e8029fdad08f75095684357d9069745` | `patches/0001-llm_client-azure-ai-provider-timeouts-reasoning.patch` |
| `benchmarks/common/mem0_client.py` | `44e367847d94be3a90cdfa1d21aebe96` | `041f93a130c1a91d1b81f67622555b8c` | `patches/0002-mem0_client-optional-date-injection.patch` |
| `benchmarks/locomo/run.py` | `f791a93df6257fe869ec6687865f8457` | `7b5402a4d865af5a085a09c6133eb76e` | `patches/0003-locomo-run-backends-search-retry-drop-accounting-runmeta.patch` |
| `docker/mem0/main.py` | `e4e1e6076c9016bc37de6715ea29e67a` | `3fe9a40ba1cc8b494daadee2b977f411` | `patches/0004-docker-mem0-server-topk-fix-and-ingest-usage-metering.patch` |
| `requirements.txt` | `13815b8f1ba4ecc628a44fc963a67679` | `e944e768d329e2e345b3929e8bd86478` | `patches/0006-requirements-bm25-deps.patch` |

All five apply cleanly to a fresh checkout of `4b61c5d31b9c` (`0005` excluded — it targets the
separate `codebase-memory-mcp` repo, not this harness):

```bash
for p in patches/000[1-4]-*.patch patches/0006-*.patch; do git apply --check -p1 "$p" && echo "OK $p"; done
```

What each patch does:

- **0001 `llm_client.py`** — adds the `azure_ai` / `foundry` provider branch used by the whole
  spine; makes the request timeout overridable via `LLM_TIMEOUT` (the 120s default is too short for
  a reasoning model on a top-200 context, and a timeout silently becomes an empty answer scored as
  a miss); applies a `max_completion_tokens` floor for gpt-5/o-series so hidden reasoning tokens
  cannot consume the entire budget and return empty content; adds optional `reasoning_effort`
  passthrough that self-disables if the endpoint rejects it. Every change is question-blind and
  applies identically to all arms.
- **0002 `mem0_client.py`** — optional observation-date injection into the first ingest message,
  gated off by default behind `MEM0_DATE_INJECT=1`. Not enabled in the published runs.
- **0003 `benchmarks/locomo/run.py`** — registers the new backends; adds bounded retry around
  `search()` for transient failures only (deterministic 4xx still surface as bugs); **records the
  `search_dropped` flag in the per-question record** — upstream discarded it in the LoCoMo runner
  while already recording it in the LongMemEval runner, so retry-exhausted retrievals were being
  scored as capability misses; adds ingest-phase timing output; wires `runmeta` provenance capture
  and the `FAIR_MODE` guard; splits `--max-workers` (conversations) from a new `--question-workers`.
- **0004 `docker/mem0/main.py`** — the mem0 `top_k` fix described in `README.md` §3.1 (one line,
  at upstream line 233 / patched-container line 351), plus ingest token-usage metering for the
  cost table and optional Anthropic OAuth-bearer wiring. The metering and OAuth wiring are
  observation-only and gated behind env vars.
- **0006 `requirements.txt`** adds the `rank-bm25` and `PyStemmer` dependencies used only by the
  lexical BM25 arm.

### Not upstream at all — vendored here

Written by us; no upstream code involved.

| file | lines |
|---|---|
| `benchmarks/common/entire_client.py` | 696 |
| `benchmarks/common/graphify_client.py` | 364 |
| `benchmarks/common/cmm_client.py` | 418 |
| `benchmarks/common/graphify_mem_bridge.py` | 184 |
| `benchmarks/common/runmeta.py` | 129 |
| `benchmarks/common/bm25_client.py` | 369 |
| `benchmarks/common/test_bm25_client.py` | 40 |

### Ours but not vendored

`cognee_client.py`, `letta_client.py`, `graphiti_client.py`, `supermemory_client.py` and their
workers are also ours. `cognee_client.py`, `letta_client.py` and `supermemory_client.py` back
rows in the published LoCoMo head-to-head this kit reproduces; `graphiti_client.py` does not (its
run was a 529-question subset, excluded from the table — see `LOCOMO-COMPARISON.md` §2). The
empty-buffer guard they carry is described in `README.md` §3.2 and is reproducible from that
description.

## Third-party components referenced, not vendored

| component | source | version / commit |
|---|---|---|
| mem0 (library + OSS server) | `https://github.com/mem0ai/mem0` | `4debc58a83377b18be81ae1e5969a300736b2fac` |
| cognee | `https://github.com/topoteretes/cognee` | `38eece5bbb0cb9f5706fed908abd16dba0f5505e` |
| codebase-memory-mcp ("cmm", DeusData) | upstream release | v0.9.0, plus `patches/0005` |
| graphify | `https://github.com/Graphify-Labs/graphify` | `graphify_client.py` imports the real package (`extractors.markdown.extract_markdown`, `serve._score_query`) from a local checkout at `GRAPHIFY_SOURCE`, default `~/memarms/inputs/repos/graphify` — genuinely runs graphify, not a reimplementation. **Exact commit unpinned**: the same checkout path is used by the separate GraphMark comparison above, which cites commit `c9641bf1caaf41d64ce8a4a421f041939feecca3` — but that hash does not resolve against the public repo (verified via GitHub API, 2026-08-19), so it cannot be cited here as a working pin either. |
| letta | `https://github.com/letta-ai/letta` | [`0.16.8`](https://github.com/letta-ai/letta/releases/tag/0.16.8) — recovered from an in-repo debugging comment (`run.env`, ENV FIX 2026-08-08) citing `letta/settings.py:314` and `letta/server/db.py:30-31` behavior specific to that release; the live install it describes no longer exists to re-verify against directly |
| supermemory | `https://github.com/supermemoryai/supermemory` | [`server-v0.0.7-rc.2`](https://github.com/supermemoryai/supermemory/releases/tag/server-v0.0.7-rc.2) — the binary path recorded in the harness config, corroborated independently by `FAIR-CONFIG.md`'s service-state table (§B12) naming the same build |
| BM25 | `https://github.com/dorianbrown/rank_bm25` | not applicable — generic lexical baseline algorithm, not a versioned product under comparison |
