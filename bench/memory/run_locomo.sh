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

if pgrep -f -- "locomo.run --project-name ${PN} " > /dev/null 2>&1; then
  echo "REFUSING: ${PN} already running"; exit 3
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
