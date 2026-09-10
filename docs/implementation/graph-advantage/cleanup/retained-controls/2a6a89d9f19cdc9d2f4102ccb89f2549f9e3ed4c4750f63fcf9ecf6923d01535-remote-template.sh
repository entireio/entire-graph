#!/bin/bash
set -u
RESULT_URL=$(printf '%s' '__RESULT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
case "$RESULT_URL" in '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;; *) exit 72;; esac
BASE=/opt/graph-validation/check-6f23da0a-linux-full-r1
SOURCE=$BASE/src
ROOT=/opt/graph-validation/statusline-linux-actual-task-6f23da0a
[ ! -e "$ROOT" ] || exit 71
mkdir -p "$ROOT/raw"
OVERALL=0
MISE=$BASE/input/mise
MANIFEST=$BASE/input/tracked-manifest.tsv
manifest() { (cd "$SOURCE" && while IFS=$'\t' read -r mode sha path; do actual_mode=$(stat -c '%a' "$path") || return; actual_sha=$(sha256sum "$path" | awk '{print $1}') || return; printf '%s\t%s\t%s\n' "$actual_mode" "$actual_sha" "$path"; done <"$MANIFEST"); }
if [ ! -d "$SOURCE" ] || [ ! -x "$MISE" ] || [ ! -f "$MANIFEST" ]; then OVERALL=73; fi
if [ "$OVERALL" -eq 0 ]; then
  { echo '05c38fdfd215feea2fb3c03fec63815fe89033a6bd5ae0e58334c15e89e244d2  '"$SOURCE/scripts/entire-graph-statusline.sh" | sha256sum -c -; echo 'bc5b460979273fc1251715f81c8254da114afbb87a1177faeac31ae16f5f0554  '"$SOURCE/scripts/entire-graph-statusline_test.sh" | sha256sum -c -; echo '6aeb64b910281ebe72f54ffe17546bb4d0bcf09a199e7173ba94c84e63f686ae  '"$SOURCE/mise.toml" | sha256sum -c -; } >"$ROOT/raw/source-identity.txt" 2>&1 || OVERALL=74
fi
if [ "$OVERALL" -eq 0 ]; then manifest >"$ROOT/raw/before-manifest.tsv" || OVERALL=75; fi
if [ "$OVERALL" -eq 0 ]; then cmp "$MANIFEST" "$ROOT/raw/before-manifest.tsv" >"$ROOT/raw/before-identity.txt" 2>&1 || OVERALL=76; fi
export PATH=/usr/local/go/bin:/usr/bin:/bin
export HOME="$BASE/home" MISE_DATA_DIR="$BASE/mise-data" MISE_CONFIG_DIR="$BASE/mise-config" MISE_CACHE_DIR="$BASE/mise-cache"
export GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0
export MISE_JOBS=1 GOFLAGS='-p=1 -v' GOMAXPROCS=4 MISE_YES=1
if [ "$OVERALL" -eq 0 ]; then
  { printf 'HOME=%s\nPATH=%s\nMISE_DATA_DIR=%s\nMISE_CONFIG_DIR=%s\nMISE_CACHE_DIR=%s\nGOPATH=%s\nGOCACHE=%s\nGOPROXY=%s\nGOSUMDB=%s\nGOTOOLCHAIN=%s\nGOTELEMETRY=%s\nGIT_CONFIG_GLOBAL=%s\nGIT_CONFIG_SYSTEM=%s\nGIT_TERMINAL_PROMPT=%s\nMISE_JOBS=%s\nGOFLAGS=%s\nGOMAXPROCS=%s\nMISE_YES=%s\nUMASK=%s\n' "$HOME" "$PATH" "$MISE_DATA_DIR" "$MISE_CONFIG_DIR" "$MISE_CACHE_DIR" "$GOPATH" "$GOCACHE" "$GOPROXY" "$GOSUMDB" "$GOTOOLCHAIN" "$GOTELEMETRY" "$GIT_CONFIG_GLOBAL" "$GIT_CONFIG_SYSTEM" "$GIT_TERMINAL_PROMPT" "$MISE_JOBS" "$GOFLAGS" "$GOMAXPROCS" "$MISE_YES" "$(umask)"; command -V env sh awk stat cksum tr cut tail id chmod mkdir find head mv; stat -c '%a %s %n' "$SOURCE/scripts/entire-graph-statusline.sh" "$SOURCE/scripts/entire-graph-statusline_test.sh" "$SOURCE/entire-graph" 2>&1; } >"$ROOT/raw/safe-environment.txt" 2>&1
  (cd "$SOURCE" && "$MISE" env) 2>&1 | grep -E '^(export )?(PATH|MISE_[A-Z_]+|GO[A-Z_]+|GIT_[A-Z_]+|HOME)=' >"$ROOT/raw/mise-activation-allowlist.txt" || true
  SELECTED_GO=$(cd "$SOURCE" && "$MISE" which go 2>&1); WHICH_RC=$?
  SELECTED_VERSION=$([ "$WHICH_RC" -eq 0 ] && "$SELECTED_GO" version 2>&1); VERSION_RC=$?
  MISE_VERSION=$("$MISE" --version 2>&1); MISE_RC=$?
  printf 'which_exit=%s\nversion_exit=%s\nmise_exit=%s\nselected_go=%s\nselected_version=%s\nmise_version=%s\n' "$WHICH_RC" "$VERSION_RC" "$MISE_RC" "$SELECTED_GO" "$SELECTED_VERSION" "$MISE_VERSION" >"$ROOT/raw/toolchain.txt"
  if [ "$WHICH_RC" -ne 0 ] || [ "$VERSION_RC" -ne 0 ] || [ "$MISE_RC" -ne 0 ] || [ "$SELECTED_GO" != /usr/local/go/bin/go ] || [ "$SELECTED_VERSION" != 'go version go1.26.1 linux/amd64' ]; then OVERALL=77; fi
fi
if [ "$OVERALL" -eq 0 ]; then
  set +e
  (cd "$SOURCE" && "$MISE" run test:statusline) >"$ROOT/raw/task.txt" 2>&1
  TASK_RC=$?
  set -e
  printf '%s\n' "$TASK_RC" >"$ROOT/raw/task.exit.txt"
  [ "$TASK_RC" -eq 0 ] || OVERALL=$TASK_RC
fi
manifest >"$ROOT/raw/after-manifest.tsv" 2>"$ROOT/raw/after-identity.txt" || [ "$OVERALL" -ne 0 ] || OVERALL=78
cmp "$MANIFEST" "$ROOT/raw/after-manifest.tsv" >>"$ROOT/raw/after-identity.txt" 2>&1 || [ "$OVERALL" -ne 0 ] || OVERALL=79
printf '%s\n' "$OVERALL" >"$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
printf 'STATUSLINE_ACTUAL_TASK_UPLOAD_ACK overall=%s upload=%s\n' "$OVERALL" "$UPLOAD"
[ "$UPLOAD" -eq 0 ] || exit "$UPLOAD"
exit "$OVERALL"
