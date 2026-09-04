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

`upstream md5` is the file at `4b61c5d31b9c`. `harness md5` is the file produced by the current
reproducibility kit. The Foundry request, response, prompt, model, and scoring behavior matches the
published runs; the current kit replaces their API-key transport with Microsoft Entra bearer
authentication so scheduled jobs do not carry a long-lived inference credential.

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
| `benchmarks/common/llm_client.py` | `6a5da3c1d05dbf6a78cd364b59fc7a09` | `592bbcc560b15b88aabb2c9d0280380f` | `patches/0001-llm_client-azure-ai-provider-timeouts-reasoning.patch` |
| `benchmarks/common/mem0_client.py` | `44e367847d94be3a90cdfa1d21aebe96` | `bb763cabd9e586cf9aa2699c67f96358` | `patches/0002-mem0_client-optional-date-injection.patch` |
| `benchmarks/locomo/run.py` | `f791a93df6257fe869ec6687865f8457` | `c3331bce8631d07cf69ae94cb82f821c` | `patches/0003-locomo-run-backends-search-retry-drop-accounting-runmeta.patch` |
| `docker/mem0/main.py` | `e4e1e6076c9016bc37de6715ea29e67a` | `3fe9a40ba1cc8b494daadee2b977f411` | `patches/0004-docker-mem0-server-topk-fix-and-ingest-usage-metering.patch` |
| `requirements.txt` | `13815b8f1ba4ecc628a44fc963a67679` | `51c617883adf40e4ca22b79533f4662a` | `patches/0006-requirements-bm25-deps.patch` |

All five apply cleanly to a fresh checkout of `4b61c5d31b9c` (`0005` excluded — it targets the
separate `codebase-memory-mcp` repo, not this harness):

```bash
for p in patches/000[1-4]-*.patch patches/0006-*.patch; do git apply --check -p1 "$p" && echo "OK $p"; done
```

What each patch does:

- **0001 `llm_client.py`** — adds the `azure_ai` / `foundry` provider branch used by the whole
  spine; authenticates its unchanged `/models/chat/completions` request through the vendored
  `entra_auth.py` helper, which refreshes Microsoft Entra bearer tokens and never sends an API key;
  makes the request timeout overridable via `LLM_TIMEOUT` (the 120s default is too short for a
  reasoning model on a top-200 context, and a timeout silently becomes an empty answer scored as a
  miss); applies a `max_completion_tokens` floor for gpt-5/o-series so hidden reasoning tokens
  cannot consume the entire budget and return empty content; adds optional `reasoning_effort`
  passthrough that self-disables if the endpoint rejects it. Every inference change is
  question-blind and applies identically to all arms.
- **0002 `mem0_client.py`** — two changes. Optional observation-date injection into the first
  ingest message, gated off by default behind `MEM0_DATE_INJECT=1` and not enabled in the
  published runs. And, **always active**, `_search_oss` and `_search_cloud` now raise
  `SEARCH_EXHAUSTED` instead of returning `[]` once their own retries are spent: upstream
  swallowed the failure, so the caller could not tell an infrastructure failure from an empty
  index and scored it as a capability miss. Every other adapter already raises (README §3.2);
  this is the same guard applied to the one client that was missed.
- **0003 `benchmarks/locomo/run.py`** — registers the new backends; adds bounded retry around
  `search()` for transient failures only (deterministic 4xx still surface as bugs); **records the
  `search_dropped` flag in the per-question record** — upstream discarded it in the LoCoMo runner
  while already recording it in the LongMemEval runner, so retry-exhausted retrievals were being
  scored as capability misses. The drop is counted from an explicit
  signal — patch 0002 makes mem0 raise `SEARCH_EXHAUSTED` — so `[]` keeps meaning a genuine
  zero-match retrieval. Inferring a drop from emptiness instead would retry a valid query and then
  count it against the denominator, corrupting the accounting from the other direction; adds ingest-phase timing output; wires `runmeta` provenance capture
  and the `FAIR_MODE` guard; splits `--max-workers` (conversations) from a new
  `--question-workers`, both validated as `>= 1` because either at zero caps a semaphore that
  then blocks every task forever; and rejects `HARNESS_SEARCH_RETRIES < 1`, which would run no
  search at all and mark every question dropped.
- **0004 `docker/mem0/main.py`** — the mem0 `top_k` fix described in `README.md` §3.1 (one line,
  at upstream line 233 / patched-container line 351), plus ingest token-usage metering for the
  cost table and optional Anthropic OAuth-bearer wiring. The metering and OAuth wiring are
  observation-only and gated behind env vars.
- **0006 `requirements.txt`** pins `azure-identity==1.25.3` for refreshable keyless Foundry
  authentication and `openai==1.50.0`, the existing minimum whose `/models` request behavior is
  covered by the integration check; it also adds the `rank-bm25` and `PyStemmer` dependencies used
  only by the lexical BM25 arm.

### Hash-locked Python environment

[`requirements-lock-py312.txt`](requirements-lock-py312.txt) resolves every direct and transitive
dependency in the patched upstream `requirements.txt` to one exact version and records the hashes
published for that release. Its source `requirements.txt` must have MD5
`51c617883adf40e4ca22b79533f4662a` (Git blob
`9f246fa9aee1d635304a2f151a80996ef4a499fb`); the committed lock has SHA-256
`be938c7db3329662071487394ca123e04581322229fbe405bb1103bcc624203f`.

Regenerate it only after reconstructing the pinned harness and applying patches `0001` through
`0004` plus `0006` as above. From that reconstructed harness checkout:

```bash
KIT_ROOT=/absolute/path/to/entire-graph
CUSTOM_COMPILE_COMMAND='uvx --python 3.12 --from pip-tools==7.6.1 pip-compile --generate-hashes --resolver=backtracking --strip-extras --no-annotate --output-file="$KIT_ROOT/bench/memory/requirements-lock-py312.txt" requirements.txt' \
  uvx --python 3.12 --from pip-tools==7.6.1 pip-compile \
    --generate-hashes --resolver=backtracking --strip-extras --no-annotate \
    --output-file="$KIT_ROOT/bench/memory/requirements-lock-py312.txt" requirements.txt
```

Review the resulting version and hash changes before updating the recorded lock digest. The lock is
specific to CPython 3.12. CI installs it without first upgrading pip and without allowing pip to
discover any unlisted dependency:

```bash
cp "$KIT_ROOT/bench/memory/requirements-lock-py312.txt" .
.venv/bin/python -m pip install \
  --require-hashes --only-binary=:all: --no-deps \
  -r requirements-lock-py312.txt
.venv/bin/python -m pip check
```

`--require-hashes` rejects changed artifacts, `--only-binary=:all:` prevents an sdist build from
executing arbitrary setup code, and `--no-deps` prevents an undeclared transitive fetch. `pip check`
then verifies that the explicitly listed graph is complete and internally compatible. The lock was
installed cleanly with CPython 3.12 and its complete wheel set was separately resolved for x86-64
Linux/manylinux, the GitHub-hosted runner target. Keeping the copied lock at the harness root also
lets `runmeta.code_hashes()` include it in every result artifact.

### Not upstream at all — vendored here

Written by us; no upstream code involved.

| file | lines |
|---|---|
| `benchmarks/common/entire_client.py` | 696 |
| `benchmarks/common/graphify_client.py` | 364 |
| `benchmarks/common/cmm_client.py` | 418 |
| `benchmarks/common/graphify_mem_bridge.py` | 184 |
| `benchmarks/common/runmeta.py` | 726 |
| `benchmarks/common/test_runmeta.py` | 914 |
| `benchmarks/common/bm25_client.py` | 369 |
| `benchmarks/common/test_bm25_client.py` | 40 |
| `benchmarks/common/entra_auth.py` | 94 |
| `benchmarks/common/test_entra_auth.py` | 166 |
| `requirements-lock-py312.txt` | 1418 |

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
| graphify | `https://github.com/Graphify-Labs/graphify` | [`v0.9.43`](https://github.com/Graphify-Labs/graphify/releases/tag/v0.9.43) — `graphify_client.py` imports the real package (`extractors.markdown.extract_markdown`, `serve._score_query`) from a local checkout at `GRAPHIFY_SOURCE`, default `~/memarms/inputs/repos/graphify`; genuinely runs graphify, not a reimplementation. **Not a confirmed exact pin**: the checkout itself no longer exists to check directly, so this is the release that was current for the 2026-08-14 measurement window (published 19:17 UTC that day, superseded by v0.9.44 the next day). The already-published commit citation for the same checkout path (`docs/benchmarks.md`, `c9641bf1caaf41d64ce8a4a421f041939feecca3`) does not resolve against the public repo, so it isn't cited here. |
| letta | `https://github.com/letta-ai/letta` | [`0.16.8`](https://github.com/letta-ai/letta/releases/tag/0.16.8) — recovered from an in-repo debugging comment (`run.env`, ENV FIX 2026-08-08) citing `letta/settings.py:314` and `letta/server/db.py:30-31` behavior specific to that release; the live install it describes no longer exists to re-verify against directly |
| supermemory | `https://github.com/supermemoryai/supermemory` | [`server-v0.0.7-rc.2`](https://github.com/supermemoryai/supermemory/releases/tag/server-v0.0.7-rc.2) — the binary path recorded in the harness config, corroborated independently by `FAIR-CONFIG.md`'s service-state table (§B12) naming the same build |
| BM25 | `https://github.com/dorianbrown/rank_bm25` | [`0.2.2`](https://github.com/dorianbrown/rank_bm25/releases/tag/0.2.2) (`requirements.txt` pins `>=0.2.2`; the package has had no release since 2022-02-16, so this floor resolves unambiguously) |
| Azure Identity for Python | `https://github.com/Azure/azure-sdk-for-python/tree/main/sdk/identity/azure-identity` | `1.25.3`; PyPI wheel SHA-256 `f4d0b956a8146f30333e071374171f3cfa7bdb8073adb8c3814b65567aa7447c` |
