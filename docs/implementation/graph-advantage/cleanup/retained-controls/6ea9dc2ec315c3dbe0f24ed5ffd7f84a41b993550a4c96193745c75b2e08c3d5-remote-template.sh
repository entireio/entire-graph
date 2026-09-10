#!/bin/bash
set -u
RESULT_URL=$(printf '%s' '__RESULT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
case "$RESULT_URL" in '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;; *) exit 72;; esac
ROOT=/opt/graph-validation/statusline-linux-trace-6f23da0a
SOURCE=/opt/graph-validation/check-6f23da0a-linux-full-r1/src
HOME_VALUE=/opt/graph-validation/check-6f23da0a-linux-full-r1/home
[ ! -e "$ROOT" ] || exit 71
mkdir -p "$ROOT/raw" "$ROOT/work/inherited" "$ROOT/work/clean"
OVERALL=0
if [ ! -d "$SOURCE" ] || [ ! -f "$SOURCE/scripts/entire-graph-statusline.sh" ]; then OVERALL=73; fi
if [ "$OVERALL" -eq 0 ]; then echo '05c38fdfd215feea2fb3c03fec63815fe89033a6bd5ae0e58334c15e89e244d2  '"$SOURCE/scripts/entire-graph-statusline.sh" | sha256sum -c - >"$ROOT/raw/source-identity.txt" 2>&1 || OVERALL=74; fi
if [ "$OVERALL" -eq 0 ]; then
  sed '53c\set -x' "$SOURCE/scripts/entire-graph-statusline.sh" >"$ROOT/work/statusline-trace.sh" || OVERALL=75
  chmod 755 "$ROOT/work/statusline-trace.sh"
fi
if [ "$OVERALL" -eq 0 ]; then
  cat >"$ROOT/work/stub" <<'STUB'
#!/bin/sh
printf '%s\n' '{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":100,"estimated_savings_pct_of_session_tokens":1}'
STUB
  chmod 755 "$ROOT/work/stub"
  printf 'x\n' >"$ROOT/work/transcript.jsonl"
  mkdir -p "$ROOT/work/repo"
  printf '{"session_id":"probe","transcript_path":"%s","cwd":"%s","workspace":{"current_dir":"%s"}}' "$ROOT/work/transcript.jsonl" "$ROOT/work/repo" "$ROOT/work/repo" >"$ROOT/work/input.json"
fi
export PATH=/opt/graph-validation/check-6f23da0a-linux-full-r1/mise-data/installs/go/1.26/bin:/usr/local/go/bin:/usr/bin:/bin
export HOME="$HOME_VALUE"
export MISE_DATA_DIR=/opt/graph-validation/check-6f23da0a-linux-full-r1/mise-data
export MISE_CONFIG_DIR=/opt/graph-validation/check-6f23da0a-linux-full-r1/mise-config
export MISE_CACHE_DIR=/opt/graph-validation/check-6f23da0a-linux-full-r1/mise-cache
export GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0
export MISE_JOBS=1 GOFLAGS='-p=1 -v' GOMAXPROCS=4 MISE_YES=1
if [ "$OVERALL" -eq 0 ]; then
  { printf 'HOME=%s\nPATH=%s\nMISE_DATA_DIR=%s\nMISE_CONFIG_DIR=%s\nMISE_CACHE_DIR=%s\nGOPATH=%s\nGOCACHE=%s\nGOPROXY=%s\nGOSUMDB=%s\nGOTOOLCHAIN=%s\nGOTELEMETRY=%s\nGIT_CONFIG_GLOBAL=%s\nGIT_CONFIG_SYSTEM=%s\nGIT_TERMINAL_PROMPT=%s\nMISE_JOBS=%s\nGOFLAGS=%s\nGOMAXPROCS=%s\nMISE_YES=%s\n' "$HOME" "$PATH" "$MISE_DATA_DIR" "$MISE_CONFIG_DIR" "$MISE_CACHE_DIR" "$GOPATH" "$GOCACHE" "$GOPROXY" "$GOSUMDB" "$GOTOOLCHAIN" "$GOTELEMETRY" "$GIT_CONFIG_GLOBAL" "$GIT_CONFIG_SYSTEM" "$GIT_TERMINAL_PROMPT" "$MISE_JOBS" "$GOFLAGS" "$GOMAXPROCS" "$MISE_YES"; command -V sh awk stat cksum tr cut tail id chmod mkdir find head mv env; } >"$ROOT/raw/safe-environment.txt" 2>&1
  set +e
  TMPDIR="$ROOT/work/inherited" ENTIRE_GRAPH_BIN="$ROOT/work/stub" /bin/sh "$ROOT/work/statusline-trace.sh" <"$ROOT/work/input.json" >"$ROOT/raw/inherited.stdout.txt" 2>"$ROOT/raw/inherited.trace.txt"
  INHERITED_RC=$?
  env -i PATH="$PATH" HOME="$HOME" TMPDIR="$ROOT/work/clean" ENTIRE_GRAPH_BIN="$ROOT/work/stub" /bin/sh "$ROOT/work/statusline-trace.sh" <"$ROOT/work/input.json" >"$ROOT/raw/clean.stdout.txt" 2>"$ROOT/raw/clean.trace.txt"
  CLEAN_RC=$?
  set -e
  printf 'inherited_exit=%s\nclean_exit=%s\n' "$INHERITED_RC" "$CLEAN_RC" >"$ROOT/raw/exits.txt"
  [ "$INHERITED_RC" -eq 0 ] && [ "$CLEAN_RC" -eq 0 ] || OVERALL=76
fi
printf '%s\n' "$OVERALL" >"$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
printf 'STATUSLINE_TRACE_UPLOAD_ACK overall=%s upload=%s\n' "$OVERALL" "$UPLOAD"
[ "$UPLOAD" -eq 0 ] || exit "$UPLOAD"
exit "$OVERALL"
