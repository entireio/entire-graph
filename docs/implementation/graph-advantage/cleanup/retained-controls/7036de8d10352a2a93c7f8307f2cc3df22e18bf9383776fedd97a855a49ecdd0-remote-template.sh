#!/bin/bash
set -u
RESULT_URL=$(printf '%s' '__RESULT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
case "$RESULT_URL" in '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;; *) exit 72;; esac

SOURCE_COMMIT=25887f6954fc06e35bc3a7c699e3c524213dada4
SOURCE_ARCHIVE_SHA=16f5968934aaff779eab8801e27f0183f44a8dad86e5f7ed6edc5358811fb453
TRACKED_MANIFEST_SHA=1691ddc7be3f1e8f274779197e0b741a04a3fe717b063488effdc08a2d85fb07
GO_SHA=548e61b2d08ae52043be2f1924ed3c1d2b2c41967e360f3e317667f6fa912fc2
GOPLS_SHA=2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1
SOURCE_ROOT=/opt/graph-validation/check-25887f69-linux-full
SOURCE="$SOURCE_ROOT/src"
INPUT="$SOURCE_ROOT/input"
ROOT=/opt/graph-validation/diagnostics-linux-25887f69-evaluator-build-01

if [ -e "$ROOT" ]; then echo 'REMOTE_ROOT_ALREADY_EXISTS' >&2; exit 71; fi
mkdir -p "$ROOT/raw"
OVERALL=0
manifest() {
  (cd "$SOURCE" && while IFS=$'\t' read -r mode sha path; do
    if [ -x "$path" ]; then actual_mode=100755; else actual_mode=100644; fi
    actual_sha=$(sha256sum "$path" | awk '{print $1}') || return
    printf '%s\t%s\t%s\n' "$actual_mode" "$actual_sha" "$path"
  done < "$INPUT/tracked-manifest.tsv")
}

{
  printf 'source_commit=%s\n' "$SOURCE_COMMIT"
  printf 'source_root=%s\n' "$SOURCE_ROOT"
  test -d "$SOURCE"
  test -f "$INPUT/source.tar.gz"
  test -f "$INPUT/tracked-manifest.tsv"
  echo "$SOURCE_ARCHIVE_SHA  $INPUT/source.tar.gz" | sha256sum -c -
  echo "$TRACKED_MANIFEST_SHA  $INPUT/tracked-manifest.tsv" | sha256sum -c -
  test "$(wc -l < "$INPUT/tracked-manifest.tsv")" -eq 2292
  manifest > "$ROOT/raw/before-tracked-manifest.tsv"
  cmp "$INPUT/tracked-manifest.tsv" "$ROOT/raw/before-tracked-manifest.tsv"
} > "$ROOT/raw/source-before.txt" 2>&1 || OVERALL=74

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

if [ "$OVERALL" -eq 0 ]; then
  {
    if [ "${HOME+x}" = x ]; then printf 'home_set=true\nhome_value=%s\n' "$HOME"; else printf 'home_set=false\nhome_value=\n'; fi
    uname -a
    go version
    /opt/graph-tools/gopls version
    git --version
    sha256sum /usr/local/go/bin/go /opt/graph-tools/gopls
    printf 'go_command=%s\n' "$(command -v go)"
    printf 'go_realpath=%s\n' "$(readlink -f "$(command -v go)")"
    printf 'expected_go_realpath=%s\n' "$(readlink -f /usr/local/go/bin/go)"
    test "$(go version)" = 'go version go1.26.1 linux/amd64'
    test "$(/opt/graph-tools/gopls version)" = 'golang.org/x/tools/gopls v0.20.0'
    test "$(readlink -f "$(command -v go)")" = "$(readlink -f /usr/local/go/bin/go)"
    echo "$GO_SHA  /usr/local/go/bin/go" | sha256sum -c -
    echo "$GOPLS_SHA  /opt/graph-tools/gopls" | sha256sum -c -
    env | LC_ALL=C sort | grep -E '^(GO|GIT_CONFIG|GIT_TERMINAL|PATH=)'
  } > "$ROOT/raw/environment.txt" 2>&1 || OVERALL=75
fi

if [ "$OVERALL" -eq 0 ]; then
  set +e
  (cd "$SOURCE" && go test -c -o "$ROOT/p1-evaluator" ./internal/sem) > "$ROOT/raw/build.txt" 2>&1
  BUILD_RC=$?
  set -e
  printf '%s\n' "$BUILD_RC" > "$ROOT/raw/build.exit.txt"
  if [ "$BUILD_RC" -eq 0 ]; then
    sha256sum "$ROOT/p1-evaluator" > "$ROOT/raw/binary.sha256"
    go version -m "$ROOT/p1-evaluator" > "$ROOT/raw/binary-build-info.txt" 2>&1
  else
    OVERALL=$BUILD_RC
  fi
else
  printf 'not-run: identity preflight failed\n' > "$ROOT/raw/build.txt"
  printf '125\n' > "$ROOT/raw/build.exit.txt"
fi

manifest > "$ROOT/raw/after-tracked-manifest.tsv" 2> "$ROOT/raw/source-after.txt" || [ "$OVERALL" -ne 0 ] || OVERALL=76
cmp "$INPUT/tracked-manifest.tsv" "$ROOT/raw/after-tracked-manifest.tsv" >> "$ROOT/raw/source-after.txt" 2>&1 || [ "$OVERALL" -ne 0 ] || OVERALL=77
printf '%s\n' "$OVERALL" > "$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
printf 'EVALUATOR_COMPILE_ONLY_UPLOAD_ACK overall=%s upload=%s\n' "$OVERALL" "$UPLOAD"
[ "$UPLOAD" -eq 0 ] || exit "$UPLOAD"
exit "$OVERALL"
