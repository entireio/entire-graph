#!/bin/bash
set -u

RESULT_URL=$(printf '%s' '__RESULT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
case "$RESULT_URL" in
  '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;;
  *) exit 72 ;;
esac

BASE=/opt/graph-validation/check-6f23da0a-linux-full-r1
SOURCE=$BASE/src
ROOT=/opt/graph-validation/statusline-linux-actual-task-6f23da0a-r1
SYSTEM_GO=/usr/local/go/bin/go
[ ! -e "$ROOT" ] || exit 71
mkdir -p "$ROOT/raw"

OVERALL=0
MISE=$BASE/input/mise
MANIFEST=$BASE/input/tracked-manifest-r1.tsv
HOME_BEFORE=${HOME-}

manifest() {
  (
    cd "$SOURCE" || return
    while IFS=$'\t' read -r mode sha path; do
      if [ -x "$path" ]; then
        actual_mode=100755
      else
        actual_mode=100644
      fi
      actual_sha=$(sha256sum "$path" | awk '{print $1}') || return
      printf '%s\t%s\t%s\n' "$actual_mode" "$actual_sha" "$path"
    done < "$MANIFEST"
  )
}

if [ ! -d "$SOURCE" ] || [ ! -x "$MISE" ] || [ ! -f "$MANIFEST" ] || [ ! -x "$SYSTEM_GO" ]; then
  OVERALL=73
fi
if [ "$OVERALL" -eq 0 ]; then
  echo '7572d7a7212a5bb16a964ab0a697a6751efa7fd8226dac6c38d9309f1e0e6f5c  '"$MANIFEST" | sha256sum -c - >"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=74
fi
if [ "$OVERALL" -eq 0 ]; then
  echo '00ec58da20aac5c9cfab08aa3210fbacec9353d4217f964a8c318612a38f5d33  '"$MISE" | sha256sum -c - >>"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=74
fi
if [ "$OVERALL" -eq 0 ]; then
  MANIFEST_LINES=$(wc -l < "$MANIFEST" | awk '{print $1}')
  printf 'tracked_manifest_lines=%s\n' "$MANIFEST_LINES" >"$ROOT/raw/manifest-count.txt"
  [ "$MANIFEST_LINES" -eq 2059 ] || OVERALL=75
fi
if [ "$OVERALL" -eq 0 ]; then
  manifest >"$ROOT/raw/before-tracked-manifest-r1.tsv" || OVERALL=76
fi
if [ "$OVERALL" -eq 0 ]; then
  cmp "$MANIFEST" "$ROOT/raw/before-tracked-manifest-r1.tsv" >"$ROOT/raw/before-identity.txt" 2>&1 || OVERALL=77
fi

export PATH=/usr/local/go/bin:/usr/bin:/bin
export MISE_DATA_DIR="$BASE/mise-data" MISE_CONFIG_DIR="$BASE/mise-config" MISE_CACHE_DIR="$BASE/mise-cache"
export GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0
export MISE_JOBS=1 GOFLAGS='-p=1 -v' GOMAXPROCS=4 MISE_YES=1

if [ "$OVERALL" -eq 0 ]; then
  {
    printf 'HOME=%s\n' "$HOME_BEFORE"
    printf 'PATH=%s\n' "$PATH"
    printf 'MISE_DATA_DIR=%s\n' "$MISE_DATA_DIR"
    printf 'MISE_CONFIG_DIR=%s\n' "$MISE_CONFIG_DIR"
    printf 'MISE_CACHE_DIR=%s\n' "$MISE_CACHE_DIR"
    printf 'GOPATH=%s\n' "$GOPATH"
    printf 'GOCACHE=%s\n' "$GOCACHE"
    printf 'GOPROXY=%s\n' "$GOPROXY"
    printf 'GOSUMDB=%s\n' "$GOSUMDB"
    printf 'GOTOOLCHAIN=%s\n' "$GOTOOLCHAIN"
    printf 'GOTELEMETRY=%s\n' "$GOTELEMETRY"
    printf 'GIT_CONFIG_GLOBAL=%s\n' "$GIT_CONFIG_GLOBAL"
    printf 'GIT_CONFIG_SYSTEM=%s\n' "$GIT_CONFIG_SYSTEM"
    printf 'GIT_TERMINAL_PROMPT=%s\n' "$GIT_TERMINAL_PROMPT"
    printf 'MISE_JOBS=%s\n' "$MISE_JOBS"
    printf 'GOFLAGS=%s\n' "$GOFLAGS"
    printf 'GOMAXPROCS=%s\n' "$GOMAXPROCS"
    printf 'MISE_YES=%s\n' "$MISE_YES"
    printf 'UMASK=%s\n' "$(umask)"
    command -V env sh awk stat base64 cmp sha256sum wc readlink date tar curl
    stat -c '%a %s %n' "$SOURCE/scripts/entire-graph-statusline.sh" "$SOURCE/scripts/entire-graph-statusline_test.sh" "$SOURCE/entire-graph"
  } >"$ROOT/raw/safe-environment.txt" 2>&1 || OVERALL=78
  if [ "${HOME-}" != "$HOME_BEFORE" ]; then OVERALL=78; fi
fi

if [ "$OVERALL" -eq 0 ]; then
  SELECTED_GO=$(cd "$SOURCE" && "$MISE" which go 2>&1)
  WHICH_RC=$?
  SELECTED_VERSION=$([ "$WHICH_RC" -eq 0 ] && "$SELECTED_GO" version 2>&1)
  VERSION_RC=$?
  SELECTED_REALPATH=$([ "$WHICH_RC" -eq 0 ] && readlink -f "$SELECTED_GO" 2>&1)
  SELECTED_REALPATH_RC=$?
  SYSTEM_REALPATH=$(readlink -f "$SYSTEM_GO" 2>&1)
  SYSTEM_REALPATH_RC=$?
  MISE_VERSION=$("$MISE" --version 2>&1)
  MISE_RC=$?
  {
    printf 'which_exit=%s\n' "$WHICH_RC"
    printf 'version_exit=%s\n' "$VERSION_RC"
    printf 'selected_realpath_exit=%s\n' "$SELECTED_REALPATH_RC"
    printf 'system_realpath_exit=%s\n' "$SYSTEM_REALPATH_RC"
    printf 'mise_exit=%s\n' "$MISE_RC"
    printf 'selected_go=%s\n' "$SELECTED_GO"
    printf 'selected_go_realpath=%s\n' "$SELECTED_REALPATH"
    printf 'system_go_realpath=%s\n' "$SYSTEM_REALPATH"
    printf 'selected_version=%s\n' "$SELECTED_VERSION"
    printf 'mise_version=%s\n' "$MISE_VERSION"
  } >"$ROOT/raw/toolchain.txt"
  if [ "$WHICH_RC" -ne 0 ] || [ "$VERSION_RC" -ne 0 ] || [ "$SELECTED_REALPATH_RC" -ne 0 ] || [ "$SYSTEM_REALPATH_RC" -ne 0 ] || [ "$MISE_RC" -ne 0 ] || [ "$SELECTED_REALPATH" != "$SYSTEM_REALPATH" ] || [ "$SELECTED_VERSION" != 'go version go1.26.1 linux/amd64' ]; then
    OVERALL=79
  fi
fi

if [ "$OVERALL" -eq 0 ]; then
  START=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  (cd "$SOURCE" && "$MISE" run test:statusline) >"$ROOT/raw/mise-test-statusline.raw.log" 2>&1
  TASK_RC=$?
  END=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  printf '%s\n' "$TASK_RC" >"$ROOT/raw/task.exit.txt"
  printf 'start=%s\nend=%s\n' "$START" "$END" >"$ROOT/raw/timing.txt"
  [ "$TASK_RC" -eq 0 ] || OVERALL=$TASK_RC
fi

if [ -d "$SOURCE" ] && [ -f "$MANIFEST" ]; then
  manifest >"$ROOT/raw/after-tracked-manifest-r1.tsv" 2>"$ROOT/raw/after-identity.txt" || [ "$OVERALL" -ne 0 ] || OVERALL=80
  cmp "$MANIFEST" "$ROOT/raw/after-tracked-manifest-r1.tsv" >>"$ROOT/raw/after-identity.txt" 2>&1 || [ "$OVERALL" -ne 0 ] || OVERALL=81
else
  printf 'source or tracked manifest unavailable\n' >"$ROOT/raw/after-identity.txt"
fi

printf '%s\n' "$OVERALL" >"$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
printf 'STATUSLINE_ACTUAL_TASK_R1_UPLOAD_ACK overall=%s upload=%s\n' "$OVERALL" "$UPLOAD"
[ "$UPLOAD" -eq 0 ] || exit "$UPLOAD"
exit "$OVERALL"
