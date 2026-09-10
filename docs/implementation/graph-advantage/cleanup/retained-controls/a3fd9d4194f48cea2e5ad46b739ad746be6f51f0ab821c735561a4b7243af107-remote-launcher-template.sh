#!/bin/bash
set -u
RESULT_URL=$(printf '%s' '__RESULT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
case "$RESULT_URL" in '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;; *) exit 72;; esac

SOURCE_COMMIT=effa358f2ceaca2b9accd829984272598fa78078
SOURCE_ARCHIVE_SHA=81c0ba6ba6f58c61270a6f70517b6ed23db806f1e64852ab5c69c9649a45fed3
TRACKED_MANIFEST_SHA=ac4cf87f80b0140d1e9b0ebb21c5a8350a90d7e2e3e22d74ad073b79a33ef267
GO_SHA=548e61b2d08ae52043be2f1924ed3c1d2b2c41967e360f3e317667f6fa912fc2
GOPLS_SHA=2b4652d6ac42a22942f63735d9c7e44e9dfbc1dade5d4fd09c0d4eb8fa3539b1
SOURCE_ROOT=/opt/graph-validation/check-effa358f-linux-full-01/src
SOURCE_ARCHIVE=/opt/graph-validation/check-effa358f-linux-full-01/input/source.tar.gz
TRACKED_MANIFEST=/opt/graph-validation/check-effa358f-linux-full-01/input/tracked-manifest.tsv
ROOT=/opt/graph-validation/diagnostics-linux-effa358f-compiler-01

if [ -e "$ROOT" ]; then echo 'REMOTE_ROOT_ALREADY_EXISTS' >&2; exit 71; fi
mkdir -p "$ROOT/raw"
OVERALL=0
manifest() {
  (cd "$SOURCE_ROOT" && while IFS=$'\t' read -r mode sha path; do
    if [ -x "$path" ]; then actual_mode=100755; else actual_mode=100644; fi
    actual_sha=$(sha256sum "$path" | awk '{print $1}') || return
    printf '%s\t%s\t%s\n' "$actual_mode" "$actual_sha" "$path" || return
  done < "$TRACKED_MANIFEST")
}

verify_source() {
  printf 'source_commit=%s\n' "$SOURCE_COMMIT" || return
  printf 'source_root=%s\n' "$SOURCE_ROOT" || return
  test -d "$SOURCE_ROOT" || return
  test -f "$SOURCE_ARCHIVE" || return
  test -f "$TRACKED_MANIFEST" || return
  echo "$SOURCE_ARCHIVE_SHA  $SOURCE_ARCHIVE" | sha256sum -c - || return
  echo "$TRACKED_MANIFEST_SHA  $TRACKED_MANIFEST" | sha256sum -c - || return
  manifest_count=$(wc -l < "$TRACKED_MANIFEST") || return
  test "$manifest_count" -eq 2512 || return
  manifest > "$ROOT/raw/before-tracked-manifest.tsv" || return
  cmp "$TRACKED_MANIFEST" "$ROOT/raw/before-tracked-manifest.tsv" || return
}

verify_source > "$ROOT/raw/source-before.txt" 2>&1 || OVERALL=74

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

verify_environment() {
  if [ "${HOME+x}" = x ]; then printf 'home_set=true\nhome_value=%s\n' "$HOME" || return; else printf 'home_set=false\nhome_value=\n' || return; fi
  uname -a || return
  go_version=$(go version) || return
  printf '%s\n' "$go_version" || return
  gopls_version=$(/opt/graph-tools/gopls version) || return
  printf '%s\n' "$gopls_version" || return
  git --version || return
  test -x /usr/bin/bwrap || return
  /usr/bin/bwrap --version || return
  sha256sum /usr/local/go/bin/go /opt/graph-tools/gopls /usr/bin/bwrap || return
  go_command=$(command -v go) || return
  go_realpath=$(readlink -f "$go_command") || return
  expected_go_realpath=$(readlink -f /usr/local/go/bin/go) || return
  printf 'go_command=%s\n' "$go_command" || return
  printf 'go_realpath=%s\n' "$go_realpath" || return
  printf 'expected_go_realpath=%s\n' "$expected_go_realpath" || return
  test "$go_version" = 'go version go1.26.1 linux/amd64' || return
  test "$gopls_version" = 'golang.org/x/tools/gopls v0.20.0' || return
  test "$go_realpath" = "$expected_go_realpath" || return
  echo "$GO_SHA  /usr/local/go/bin/go" | sha256sum -c - || return
  echo "$GOPLS_SHA  /opt/graph-tools/gopls" | sha256sum -c - || return
  env | LC_ALL=C sort | grep -E '^(GO|GIT_CONFIG|GIT_TERMINAL|PATH=)' || return
}

if [ "$OVERALL" -eq 0 ]; then
  verify_environment > "$ROOT/raw/environment.txt" 2>&1 || OVERALL=75
fi

verify_correctness_coverage() {
  test "$CORRECTNESS_RC" -eq 0 || return
  top_level_passes=$(grep -c '^--- PASS: Test' "$ROOT/raw/correctness.txt") || return
  printf 'top_level_passes=%s\n' "$top_level_passes" || return
  test "$top_level_passes" -eq 28 || return
  if grep -Eq '^[[:space:]]*--- (FAIL|SKIP):' "$ROOT/raw/correctness.txt"; then return 1; fi
  for test_name in \
    TestLiveCompilerDirectAndCandidate \
    TestLiveCompilerMissingDependenciesAndCancellation \
    TestLiveCompilerWorkspaceAliases \
    TestLiveCompilerTagsReplacementAndClosure \
    TestLiveCompilerSignatureAndWorkspaceInvalidation \
    TestLiveCompilerProcessBoundary \
    TestLiveCompilerSemanticMapping \
    TestLiveCompilerConversionsAreNotCalls \
    TestLiveAdvantageCombinationsAndNextQueryFreshness \
    TestLiveReviewCompilerOrdinaryQueries
  do
    awk -v name="$test_name" '$0 ~ "^--- PASS: " name " \\(" { count++ } END { exit count == 1 ? 0 : 1 }' "$ROOT/raw/correctness.txt" || return
    printf 'live_pass=%s\n' "$test_name" || return
  done
  test -s "$ROOT/raw/compiler-advantage.json" || return
  test -s "$ROOT/raw/compiler-review.json" || return
}

if [ "$OVERALL" -eq 0 ]; then
  export ENTIRE_GRAPH_COMPILER_LIVE=1
  export ENTIRE_GRAPH_ADVANTAGE_LIVE_OUTPUT="$ROOT/raw/compiler-advantage.json"
  export ENTIRE_GRAPH_REVIEW_LIVE_OUTPUT="$ROOT/raw/compiler-review.json"
  set +e
  (cd "$SOURCE_ROOT" && go test -race -v -timeout 30m ./internal/compiler ./internal/sem ./internal/cli -run 'Test(Compiler|LiveCompiler|LiveAdvantage|LiveReview|MapLocation|RPC|Capsule)' -skip 'QualityEvaluation|ExtractionEvaluation|TestExtractionCorpusMeasurement' -count=1) > "$ROOT/raw/correctness.txt" 2>&1
  CORRECTNESS_RC=$?
  set -e
  printf '%s\n' "$CORRECTNESS_RC" > "$ROOT/raw/correctness.exit.txt"
  if verify_correctness_coverage > "$ROOT/raw/correctness-coverage.txt" 2>&1; then
    printf '0\n' > "$ROOT/raw/correctness-coverage.exit.txt"
  else
    printf '78\n' > "$ROOT/raw/correctness-coverage.exit.txt"
  fi
  if [ "$CORRECTNESS_RC" -ne 0 ]; then
    OVERALL=$CORRECTNESS_RC
  elif [ "$(cat "$ROOT/raw/correctness-coverage.exit.txt")" -ne 0 ]; then
    OVERALL=78
  fi
else
  printf 'not-run: identity preflight failed\n' > "$ROOT/raw/correctness.txt"
  printf '125\n' > "$ROOT/raw/correctness.exit.txt"
  printf 'not-run: identity preflight failed\n' > "$ROOT/raw/correctness-coverage.txt"
  printf '125\n' > "$ROOT/raw/correctness-coverage.exit.txt"
fi

if [ "$OVERALL" -eq 0 ]; then
  set +e
  (cd "$SOURCE_ROOT" && go test -c -o "$ROOT/p1-evaluator" ./internal/sem) > "$ROOT/raw/build.txt" 2>&1
  BUILD_RC=$?
  set -e
  printf '%s\n' "$BUILD_RC" > "$ROOT/raw/build.exit.txt"
  if [ "$BUILD_RC" -eq 0 ]; then
    set +e
    test -s "$ROOT/p1-evaluator"
    BINARY_EXISTS_RC=$?
    sha256sum "$ROOT/p1-evaluator" > "$ROOT/raw/binary.sha256" 2>&1
    BINARY_SHA_RC=$?
    go version -m "$ROOT/p1-evaluator" > "$ROOT/raw/binary-build-info.txt" 2>&1
    BUILD_INFO_RC=$?
    set -e
    printf 'binary_exists_exit=%s\nbinary_sha_exit=%s\nbuild_info_exit=%s\n' "$BINARY_EXISTS_RC" "$BINARY_SHA_RC" "$BUILD_INFO_RC" > "$ROOT/raw/build-metadata-exits.txt"
    if [ "$BINARY_EXISTS_RC" -ne 0 ] || [ "$BINARY_SHA_RC" -ne 0 ] || [ "$BUILD_INFO_RC" -ne 0 ]; then
      OVERALL=79
    fi
  else
    OVERALL=$BUILD_RC
  fi
else
  printf 'not-run: prerequisite failed\n' > "$ROOT/raw/build.txt"
  printf '125\n' > "$ROOT/raw/build.exit.txt"
fi

manifest > "$ROOT/raw/after-tracked-manifest.tsv" 2> "$ROOT/raw/source-after.txt" || [ "$OVERALL" -ne 0 ] || OVERALL=76
cmp "$TRACKED_MANIFEST" "$ROOT/raw/after-tracked-manifest.tsv" >> "$ROOT/raw/source-after.txt" 2>&1 || [ "$OVERALL" -ne 0 ] || OVERALL=77
printf '%s\n' "$OVERALL" > "$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
printf 'PINNED_COMPILER_CORRECTNESS_AND_EVALUATOR_BUILD_UPLOAD_ACK overall=%s upload=%s\n' "$OVERALL" "$UPLOAD"
[ "$UPLOAD" -eq 0 ] || exit "$UPLOAD"
exit "$OVERALL"
