#!/usr/bin/env bash
# Compare code-only Entire Graph investigation with GPS on one pinned checkout.
set -euo pipefail

usage() {
  printf '%s\n' "Usage: $0 --source FASTAPI_CHECKOUT --task-file TASK.md [--ref REV] [--output DIR] [--model PROVIDER/MODEL]"
}

source_repo=""
task_file=""
ref="HEAD"
output_dir=""
model=""

while (($#)); do
  case "$1" in
    --source) source_repo="$2"; shift 2 ;;
    --task-file) task_file="$2"; shift 2 ;;
    --ref) ref="$2"; shift 2 ;;
    --output) output_dir="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

if [[ -z "$source_repo" || -z "$task_file" || ! -d "$source_repo/.git" || ! -f "$task_file" ]]; then
  usage
  exit 2
fi

source_repo="$(cd "$source_repo" && pwd)"
task_file="$(cd "$(dirname "$task_file")" && pwd)/$(basename "$task_file")"
output_dir="${output_dir:-$(mktemp -d /tmp/fastapi-gps-benchmark.XXXXXX)}"
mkdir -p "$output_dir"

code_repo="$output_dir/fastapi-code-only"
gps_repo="$output_dir/fastapi-gps"
task="$(<"$task_file")"

git -C "$source_repo" worktree add --detach "$code_repo" "$ref"
git -C "$source_repo" worktree add --detach "$gps_repo" "$ref"

common=(run --format json)
if [[ -n "$model" ]]; then common+=(--model "$model"); fi

code_prompt=$(printf '%s\n' \
  "Investigate this FastAPI task without GPS:" "" "$task" "" \
  "Use only code-oriented Entire Graph commands: search, def, neighbors, impact, diff." \
  "Do not call spec, anchor, context, check, why, review, or verify." \
  "Do not modify application code, tests, or Git state." \
  "Write a JSON report to $code_repo/code-only-report.json with implementation symbols, relevant tests, graph facts, uncertainty, commands run, and elapsed time." \
  "Label each conclusion confirmed_structural, heuristic_or_incomplete, or requires_verification.")

gps_prompt=$(printf '%s\n' \
  "Investigate this FastAPI task with Entire Graph GPS:" "" "$task" "" \
  "Create the minimum repository-local GPS intent needed for this task, then use spec validate, anchor bind/resolve, context, check, why, review, and impact --intent where useful." \
  "Do not modify FastAPI application code or tests and do not execute test commands." \
  "Write a JSON report to $gps_repo/gps-report.json with requirements, acceptance criteria, anchors, declared and inferred tests, graph facts, gaps, commands run, and elapsed time." \
  "Label each conclusion confirmed_structural, heuristic_or_incomplete, or requires_verification.")

"${common[@]}" --dir "$code_repo" "$code_prompt" >"$output_dir/code-only-events.json"
"${common[@]}" --dir "$gps_repo" "$gps_prompt" >"$output_dir/gps-events.json"

comparison_prompt=$(printf '%s\n' \
  "Compare the two FastAPI investigation reports below. Do not modify either worktree." \
  "Write Markdown to $output_dir/comparison.md with a table for implementation discovery, test discovery, traceability, uncertainty, false confidence risks, commands, elapsed time, and context cost." \
  "State only evidence supported by the reports; distinguish confirmed structural facts, incomplete/heuristic evidence, and claims requiring source or test verification." \
  "Code-only report: $code_repo/code-only-report.json" \
  "GPS report: $gps_repo/gps-report.json")
"${common[@]}" --dir "$output_dir" "$comparison_prompt" >"$output_dir/comparison-events.json"

printf '%s\n' "Benchmark artifacts: $output_dir"
printf '%s\n' "Comparison: $output_dir/comparison.md"
