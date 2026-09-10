#!/bin/bash
set -u
RESULT_URL_B64='__RESULT_URL_B64__'
RESULT_URL=$(printf '%s' "$RESULT_URL_B64" | base64 -d)
test -n "$RESULT_URL" || exit 72
case "$RESULT_URL_B64" in *__RESULT_URL_B64__*) exit 73;; esac
SOURCE_ROOT=/opt/graph-validation/diagnostics-linux-6f23da0a/src
SOURCE_ARCHIVE=/tmp/diagnostics-linux-6f23da0a-source.tar.gz
EXPECTED_SOURCE_SHA=e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b
EXPECTED_COMMIT=6f23da0ad4fa704da8c7bac53e1065030c89a4f1
ROOT=/opt/graph-validation/check-6f23da0a-linux-cache-fixtures
if [ -e "$ROOT" ]; then echo 'REMOTE_ROOT_ALREADY_EXISTS' >&2; exit 71; fi
mkdir -p "$ROOT/raw"
OVERALL=0
if [ ! -d "$SOURCE_ROOT" ] || [ ! -f "$SOURCE_ARCHIVE" ]; then OVERALL=74; fi
if [ "$OVERALL" -eq 0 ]; then echo "$EXPECTED_SOURCE_SHA  $SOURCE_ARCHIVE" | sha256sum -c - > "$ROOT/raw/source-identity.txt" 2>&1 || OVERALL=$?; fi
export PATH=/usr/local/go/bin:/usr/bin:/bin
export GOPATH=/opt/graph-validation/gopath
export GOCACHE=/opt/graph-validation/cache
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_TERMINAL_PROMPT=0
export GOPROXY=off
export GOSUMDB=off
export GOTOOLCHAIN=local
export GOTELEMETRY=off
export GOMAXPROCS=4
export GOFLAGS=-p=1
if [ "$OVERALL" -eq 0 ]; then
  set +e
  { printf 'source_commit=%s\n' "$EXPECTED_COMMIT"; printf 'source_root=%s\n' "$SOURCE_ROOT"; go version; /opt/graph-tools/gopls version; sha256sum /usr/local/go/bin/go /opt/graph-tools/gopls; if command -v mise >/dev/null 2>&1; then printf 'mise_available=true\n'; command -v mise; mise --version; else printf 'mise_available=false\n'; fi; printf 'tracked_mise_toml_sha256=6aeb64b910281ebe72f54ffe17546bb4d0bcf09a199e7173ba94c84e63f686ae\n'; if [ -e "$SOURCE_ROOT/mise.toml" ]; then printf 'source_archive_has_mise_toml=true\n'; sha256sum "$SOURCE_ROOT/mise.toml"; else printf 'source_archive_has_mise_toml=false\n'; fi; env | LC_ALL=C sort | grep -E '^(GO|GIT_CONFIG|GIT_TERMINAL|PATH=)'; test "$(go version)" = 'go version go1.26.1 linux/amd64'; test "$(/opt/graph-tools/gopls version)" = 'golang.org/x/tools/gopls v0.20.0'; } > "$ROOT/raw/environment.txt" 2>&1
  RC=$?
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/toolchain.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
REGEX='^(TestPreindexProviderSnapshotReusesSameTreeAcrossCommits|TestSearchReusesSameTreeCacheAcrossCommitsAndReportsCurrentHEAD|TestWarmSelectiveDerivationFailureFallsBackToFreshBuild)$'
if [ "$OVERALL" -eq 0 ]; then
  cd "$SOURCE_ROOT" || OVERALL=$?
fi
if [ "$OVERALL" -eq 0 ]; then
  set +e
  go test -race -v -count=1 -timeout=5m ./internal/sem -run "$REGEX" > "$ROOT/raw/tests.txt" 2>&1
  RC=$?
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/tests.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
printf '%s\n' "$OVERALL" > "$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
echo "CACHE_FIXTURE_DIAG_UPLOAD_ACK overall=$OVERALL upload=$UPLOAD"
if [ "$UPLOAD" -ne 0 ]; then exit "$UPLOAD"; fi
exit "$OVERALL"
