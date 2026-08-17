#!/usr/bin/env bash
# Full LoCoMo (n=1540) for one benchmark arm.
#
# This is the launcher the published runs used, with the credential-sourcing line
# replaced by the env-var NAMES it expects. Run it from the patched upstream
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

# Credentials — export these yourself, by name. Never commit values.
#   AZURE_AI_API_KEY   AZURE_AI_ENDPOINT   AZURE_AI_API_VERSION
: "${AZURE_AI_API_KEY:?export AZURE_AI_API_KEY}"
: "${AZURE_AI_ENDPOINT:?export AZURE_AI_ENDPOINT}"
: "${AZURE_AI_API_VERSION:=2024-05-01-preview}"
export AZURE_AI_API_KEY AZURE_AI_ENDPOINT AZURE_AI_API_VERSION

export PATH="$HOME/bin:$PATH"          # the `entire-graph` binary must be on PATH
export FAIR_MODE=1
export MEM0_HOST="${MEM0_HOST:-http://localhost:18888}"
export LLM_TIMEOUT="${LLM_TIMEOUT:-600}"

ARM="$1"; RESUME="${2:-}"
PN="full_${ARM}"

if pgrep -f -- "locomo.run --project-name ${PN} " > /dev/null 2>&1; then
  echo "REFUSING: ${PN} already running"; exit 3
fi

# State roots must live under $HOME. systemd wipes /tmp on boot, and the default
# tempfile.mkdtemp() resolves under /tmp — this is how multi-GB arm state was lost.
STATE_ROOT="${BENCH_STATE_ROOT:-$HOME/memarms/state}/${PN}"
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
