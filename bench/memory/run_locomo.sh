#!/usr/bin/env bash
# Full LoCoMo (n=1540) for one benchmark arm.
#
# This is the launcher the published runs used, with authentication updated to
# use Azure identity instead of a model key. Run it from the patched upstream
# memory-benchmarks checkout (see ../README.md §2).
#
# Fairness spine (do not vary between arms):
#   answerer + judge  gpt-5.6-sol      provider azure_ai      top_k 200
#   FAIR_MODE=1       all 1540 questions, no subsetting
#
# Throughput: --max-workers 3 --question-workers 10 --rpm 60 and LLM_TIMEOUT=600
# are load-bearing. The harness defaults (100 question-workers / 200 rpm) saturate
# a shared deployment and collapse throughput to near zero, which then shows up as
# timeouts and drops that look like arm weakness.
#
# Usage: run_locomo.sh <arm> [resume]
#
# `resume` is accepted only for arms whose memory lives outside this process
# (the Mem0 server arms). See the refusal below for why.
#
# `<arm>` is validated against the same backends the harness factory can build
# (benchmarks/common/entire_client.py::make_memory_client). See the arm gate below.
set -euo pipefail

# Authentication is identity-only; API keys and static bearer tokens are not
# accepted. Locally the harness uses DefaultAzureCredential (for example, an
# `az login` session or managed identity). In GitHub Actions it exchanges fresh
# runner OIDC assertions, which also requires AZURE_CLIENT_ID and AZURE_TENANT_ID.
: "${AZURE_AI_ENDPOINT:?export AZURE_AI_ENDPOINT}"
: "${AZURE_AI_API_VERSION:=2024-05-01-preview}"
if [[ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" || -n "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:-}" ]]; then
  : "${ACTIONS_ID_TOKEN_REQUEST_URL:?GitHub OIDC request URL is missing}"
  : "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:?GitHub OIDC request token is missing}"
  : "${AZURE_CLIENT_ID:?GitHub OIDC requires AZURE_CLIENT_ID}"
  : "${AZURE_TENANT_ID:?GitHub OIDC requires AZURE_TENANT_ID}"
fi
export AZURE_AI_ENDPOINT AZURE_AI_API_VERSION

export PATH="$HOME/bin:$PATH"          # the `entire-graph` binary must be on PATH
export FAIR_MODE=1
export MEM0_HOST="${MEM0_HOST:-http://localhost:18888}"
export LLM_TIMEOUT="${LLM_TIMEOUT:-600}"

ARM="$1"; RESUME="${2:-}"
PN="full_${ARM}"

# Admit exactly the arms the harness factory can construct. Anything else used
# to be launched anyway and then died inside the harness -- an unvendored arm on
# a RuntimeError from make_memory_client, a typo on an argparse `choices` error,
# both after the environment checks above had already passed. Keep these two
# lists in step with benchmarks/common/entire_client.py (_BUNDLED_BACKENDS and
# _UNVENDORED_BACKENDS); ci/test_kit_launcher_and_patches.py fails on drift.
BUNDLED_ARMS=" oss cloud entire graphify cmm bm25 "
UNVENDORED_ARMS=" cognee graphiti letta supermemory "
ADAPTER="benchmarks/common/${ARM}_client.py"

if [[ "$BUNDLED_ARMS" != *" $ARM "* ]]; then
  if [[ "$UNVENDORED_ARMS" != *" $ARM "* ]]; then
    echo "REFUSING: unknown arm '$ARM'." >&2
    echo "  Bundled arms:${BUNDLED_ARMS}" >&2
    echo "  Optional arms (adapter not vendored here):${UNVENDORED_ARMS}" >&2
    exit 5
  fi
  # The factory admits these only when the operator has supplied the module, so
  # the launcher applies exactly that test rather than a name allowlist. They do
  # NOT reach BUFFER_MISSING when absent -- they never construct at all, so
  # refusing them as "in-process buffered" would state a false reason.
  if [[ ! -f "$ADAPTER" ]]; then
    echo "REFUSING: the '$ARM' arm requires ${ADAPTER}, which this reproduction" >&2
    echo "  kit does not include. Supply that adapter module yourself, or choose" >&2
    echo "  a bundled arm:${BUNDLED_ARMS}" >&2
    exit 5
  fi
fi

if pgrep -f -- "locomo.run --project-name ${PN} " > /dev/null 2>&1; then
  echo "REFUSING: ${PN} already running"; exit 3
fi

# Resume is only sound for arms whose memory lives OUTSIDE this process.
# The entire, graphify, cmm and bm25 adapters buffer ingested turns in memory
# and materialize their corpus at first search. A resumed run finds completed
# ingestion checkpoints, skips every add(), and then raises BUFFER_MISSING on
# the first unfinished question -- so the advertised resume path cannot resume
# these arms at all, and a run that limped past it would be scoring a partial
# corpus while reporting a complete one. Refuse loudly instead.
IN_PROCESS_BUFFERED_ARMS=" entire graphify cmm bm25 "
if [[ -n "$RESUME" ]]; then
  if [[ "$RESUME" != "resume" ]]; then
    echo "REFUSING: the second argument must be exactly 'resume' (got '$RESUME')" >&2
    exit 2
  fi
  BUFFERED=""
  if [[ "$IN_PROCESS_BUFFERED_ARMS" == *" $ARM "* ]]; then
    BUFFERED=1
  elif [[ -f "$ADAPTER" ]] && grep -q 'BUFFER_MISSING' "$ADAPTER"; then
    # A supplied adapter that guards an in-process buffer has the same defect;
    # this is the check FAIR-CONFIG.md section 2 already prescribes for it.
    BUFFERED=1
  fi
  if [[ -n "$BUFFERED" ]]; then
    echo "REFUSING: the '$ARM' arm buffers ingestion in-process and cannot resume." >&2
    echo "  A resumed run skips every add() and then fails with BUFFER_MISSING," >&2
    echo "  or would score a partial corpus as a complete run." >&2
    echo "  Re-run this arm from the start (omit the 'resume' argument)." >&2
    exit 4
  fi
fi

# Keep every filesystem-backed arm under the explicit benchmark state root.
# Without ENTIRE_CORPUS_ROOT the entire adapter falls back to tempfile.mkdtemp(),
# making BENCH_STATE_ROOT misleading and losing its corpus on host cleanup.
STATE_ROOT="${BENCH_STATE_ROOT:-$HOME/memarms/state}/${PN}"
export ENTIRE_CORPUS_ROOT="$STATE_ROOT/entire"
export GRAPHIFY_STATE_ROOT="$STATE_ROOT/graphify"
export CMM_STATE_ROOT="$STATE_ROOT/cmm"
mkdir -p "$STATE_ROOT"

exec .venv/bin/python -m benchmarks.locomo.run \
  --project-name "$PN" --backend "$ARM" --provider azure_ai \
  --answerer-model gpt-5.6-sol --judge-model gpt-5.6-sol \
  --top-k 200 --top-k-cutoffs 200 \
  --max-workers 3 --question-workers 10 --rpm 60 \
  ${RESUME:+--resume} \
  --run-id "$PN"
