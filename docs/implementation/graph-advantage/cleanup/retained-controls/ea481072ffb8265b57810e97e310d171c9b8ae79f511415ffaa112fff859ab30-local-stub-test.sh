#!/bin/bash
set -euo pipefail

REPO=${GRAPH_ADVANTAGE_REPO_ROOT}
EV="$REPO/docs/implementation/graph-advantage/evidence/statusline-linux-actual-task-6f23da0a-r1"
TEMPLATE="$EV/review/remote-template.sh"
SOURCE_ARCHIVE="$REPO/docs/implementation/graph-advantage/evidence/check-6f23da0a-linux-full/source.tar.gz"
EXPECTED_MANIFEST="$REPO/docs/implementation/graph-advantage/evidence/check-6f23da0a-linux-full/attempt-r1/review/expected-tracked-manifest.tsv"
TMP=$(mktemp -d /tmp/statusline-r1-stub.XXXXXX)
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/source" "$TMP/bin"
tar -xzf "$SOURCE_ARCHIVE" -C "$TMP/source"
: >"$TMP/source/entire-graph"
chmod 755 "$TMP/source/entire-graph"

printf '%s\n' '#!/bin/sh' 'if [ "$1" = version ]; then printf "%s\n" "go version go1.26.1 linux/amd64"; exit 0; fi' 'exit 2' >"$TMP/bin/go"
printf '%s\n' '#!/bin/sh' 'if [ "$1" = version ]; then printf "%s\n" "go version go1.26.1 linux/amd64"; exit 0; fi' 'exit 2' >"$TMP/bin/wrong-go"
printf '%s\n' '#!/bin/sh' 'case "$1" in' '  which) [ "${2-}" = go ] || exit 2; printf "%s\n" "$STUB_SELECTED_GO" ;;' '  --version) printf "%s\n" "mise 2026.4.11 linux-x64" ;;' '  run) [ "${2-}" = test:statusline ] || exit 2; printf "%s\n" task >>"$STUB_TASK_LOG" ;;' '  *) exit 2 ;;' 'esac' >"$TMP/bin/mise"
printf '%s\n' '#!/bin/sh' 'upload=' 'while [ "$#" -gt 0 ]; do' '  if [ "$1" = --upload-file ]; then shift; upload=$1; fi' '  shift' 'done' '[ -n "$upload" ] && [ -f "$upload" ] || exit 2' 'cp "$upload" "$STUB_UPLOAD"' >"$TMP/bin/curl"
printf '%s\n' '#!/bin/sh' '[ "$1" = -c ] || exit 2' 'shift 2' 'for path do /usr/bin/stat -f "%Lp %z %N" "$path" || exit; done' >"$TMP/bin/stat"
chmod 755 "$TMP/bin/go" "$TMP/bin/wrong-go" "$TMP/bin/mise" "$TMP/bin/curl" "$TMP/bin/stat"
MISE_SHA=$(sha256sum "$TMP/bin/mise" | awk '{print $1}')
RESULT_URL='${GRAPH_ADVANTAGE_BLOB_URL}'
RESULT_URL_B64=$(printf '%s' "$RESULT_URL" | base64 | tr -d '\n')

run_case() {
  case_name=$1
  selected_go=$2
  mutate_manifest=$3
  expected_rc=$4
  expected_tasks=$5
  base="$TMP/$case_name/base"
  root="$TMP/$case_name/root"
  bound="$TMP/$case_name/remote-bound.sh"
  mkdir -p "$base/input" "$base/mise-data" "$base/mise-config" "$base/mise-cache"
  ln -s "$TMP/source" "$base/src"
  cp "$TMP/bin/mise" "$base/input/mise"
  cp "$EXPECTED_MANIFEST" "$base/input/tracked-manifest-r1.tsv"
  if [ "$mutate_manifest" = yes ]; then
    printf '100644\t0000000000000000000000000000000000000000000000000000000000000000\tmissing-stub-file\n' >>"$base/input/tracked-manifest-r1.tsv"
  fi
  sed \
    -e "s|__RESULT_URL_B64__|$RESULT_URL_B64|" \
    -e "s|^BASE=/opt/graph-validation/check-6f23da0a-linux-full-r1$|BASE=$base|" \
    -e "s|^ROOT=/opt/graph-validation/statusline-linux-actual-task-6f23da0a-r1$|ROOT=$root|" \
    -e "s|^SYSTEM_GO=/usr/local/go/bin/go$|SYSTEM_GO=$TMP/bin/go|" \
    -e "s|^export PATH=/usr/local/go/bin:/usr/bin:/bin$|export PATH=$TMP/bin:/sbin:/usr/bin:/bin|" \
    -e "s|00ec58da20aac5c9cfab08aa3210fbacec9353d4217f964a8c318612a38f5d33|$MISE_SHA|" \
    -e "s|/usr/bin/curl|$TMP/bin/curl|" \
    "$TEMPLATE" >"$bound"
  chmod 755 "$bound"
  : >"$TMP/$case_name/tasks.log"
  export STUB_SELECTED_GO="$selected_go"
  export STUB_TASK_LOG="$TMP/$case_name/tasks.log"
  export STUB_UPLOAD="$TMP/$case_name/results-uploaded.tar.gz"
  set +e
  /bin/bash "$bound" >"$TMP/$case_name/stdout.txt" 2>"$TMP/$case_name/stderr.txt"
  actual_rc=$?
  set -e
  task_count=$(wc -l <"$TMP/$case_name/tasks.log" | awk '{print $1}')
  overall=$(<"$root/raw/overall.exit.txt")
  [ "$actual_rc" -eq "$expected_rc" ]
  [ "$overall" -eq "$expected_rc" ]
  [ "$task_count" -eq "$expected_tasks" ]
  [ -f "$TMP/$case_name/results-uploaded.tar.gz" ]
  printf 'case=%s script_exit=%s overall=%s fake_task_count=%s\n' "$case_name" "$actual_rc" "$overall" "$task_count"
}

run_case valid "$TMP/bin/go" no 0 1
run_case wrong-manifest "$TMP/bin/go" yes 74 0
run_case wrong-go "$TMP/bin/wrong-go" no 79 0
