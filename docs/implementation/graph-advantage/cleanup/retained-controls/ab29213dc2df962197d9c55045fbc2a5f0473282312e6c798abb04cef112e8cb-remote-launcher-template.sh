#!/bin/bash
set -u
SOURCE_URL_B64='__SOURCE_URL_B64__'
RESULT_URL_B64='__RESULT_URL_B64__'
SOURCE_SHA='__SOURCE_SHA__'
SOURCE_COMMIT='6f23da0ad4fa704da8c7bac53e1065030c89a4f1'
SOURCE_URL=$(printf '%s' "$SOURCE_URL_B64" | base64 -d)
RESULT_URL=$(printf '%s' "$RESULT_URL_B64" | base64 -d)
test -n "$SOURCE_URL" && test -n "$RESULT_URL" && test ${#SOURCE_SHA} -eq 64 || exit 72
case "$SOURCE_URL_B64$RESULT_URL_B64$SOURCE_SHA" in *__SOURCE_*|*__RESULT_*) exit 73;; esac
ROOT=/opt/graph-validation/diagnostics-linux-6f23da0a
ARCHIVE=/tmp/diagnostics-linux-6f23da0a-source.tar.gz
if [ -e "$ROOT" ]; then echo 'REMOTE_ROOT_ALREADY_EXISTS' >&2; exit 71; fi
mkdir -p "$ROOT/raw" "$ROOT/src"
OVERALL=0
curl --fail --silent --show-error "$SOURCE_URL" -o "$ARCHIVE" || OVERALL=$?
if [ "$OVERALL" -eq 0 ]; then echo "$SOURCE_SHA  $ARCHIVE" | sha256sum -c - || OVERALL=$?; fi
if [ "$OVERALL" -eq 0 ]; then tar -xzf "$ARCHIVE" -C "$ROOT/src" || OVERALL=$?; fi
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
cd "$ROOT/src" || OVERALL=$?
if [ "$OVERALL" -eq 0 ]; then
  set +e
  { printf 'source_commit=%s\n' "$SOURCE_COMMIT"; sha256sum "$ARCHIVE"; uname -a; go version; /opt/graph-tools/gopls version; git --version; sha256sum /usr/local/go/bin/go /opt/graph-tools/gopls; env | LC_ALL=C sort | grep -E '^(GO|GIT_CONFIG|GIT_TERMINAL|PATH=)'; test "$(go version)" = 'go version go1.26.1 linux/amd64'; test "$(/opt/graph-tools/gopls version)" = 'golang.org/x/tools/gopls v0.20.0'; } > "$ROOT/raw/environment.txt" 2>&1
  RC=$?
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/toolchain.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
REGEX='^(TestGoHTTPRouteCandidate.*|TestGoHTTPRouteRegistrations.*|TestGoHTTPRouteRelationsPreservesCompleteRecordsAndSkipsNonCandidates|TestGoHTTPHandleFuncResolvesRouteHandlerAndBridge|TestGoRouterMethodResolvesChiGinHandlerAndBridge|TestGoRouterSelectorHandlerResolvesUniqueMethodAndBridge|TestGoRouterGroupPrefixComposesHandlerAndBridge|TestGoChainedRouterGroupPrefixComposesHandlerAndBridge|TestGoNestedRouterGroupPrefixComposesHandlerAndBridge|TestGraphRankingCandidateContracts|TestGraphRankingSearchCaptureAndFreshness|TestGraphRankingSearchExactScopeAndGuidance|TestGraphRankingUnrelatedHubAndTransitionBudget|TestGraphRankingInputScanBoundAndCoverage|TestPageRankHandDerivedIteration|TestPageRankMassAndInvalidWeights|TestGraphRankScopeDuplicatesAndOrder|TestGraphRankFallback|TestGraphRankingEvaluation.*)$'
if [ "$OVERALL" -eq 0 ]; then
  set +e
  go test -v ./internal/sem -run "$REGEX" -count=1 -timeout=180s > "$ROOT/raw/affected_normal.txt" 2>&1
  RC=$?
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/affected_normal.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
if [ "$OVERALL" -eq 0 ]; then
  set +e
  GOFLAGS=-p=1 GOMAXPROCS=2 go test -race -v ./internal/sem -run "$REGEX" -count=1 -timeout=300s > "$ROOT/raw/affected_race.txt" 2>&1
  RC=$?
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/affected_race.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
if [ "$OVERALL" -eq 0 ]; then
  export ENTIRE_GRAPH_COMPILER_LIVE=1
  export ENTIRE_GRAPH_COMPILER_ADVANTAGE_OUTPUT="$ROOT/raw/compiler-advantage.json"
  export ENTIRE_GRAPH_COMPILER_REVIEW_OUTPUT="$ROOT/raw/compiler-review.json"
  set +e
  go test -race -v -timeout 30m ./internal/compiler ./internal/sem ./internal/cli -run 'Test(Compiler|LiveCompiler|LiveAdvantage|LiveReview|MapLocation|RPC|Capsule)' -skip 'QualityEvaluation|ExtractionEvaluation|TestExtractionCorpusMeasurement' -count=1 > "$ROOT/raw/correctness.txt" 2>&1
  RC=$?
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/correctness.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
if [ "$OVERALL" -eq 0 ]; then
  set +e
  go test -c -o "$ROOT/p1-evaluator" ./internal/sem > "$ROOT/raw/build.txt" 2>&1
  RC=$?
  if [ "$RC" -eq 0 ]; then sha256sum "$ROOT/p1-evaluator" > "$ROOT/raw/binary.sha256"; go version -m "$ROOT/p1-evaluator" > "$ROOT/raw/binary-build-info.txt" 2>&1; fi
  set -e
  printf '%s\n' "$RC" > "$ROOT/raw/build.exit.txt"
  [ "$RC" -eq 0 ] || OVERALL=$RC
fi
printf '%s\n' "$OVERALL" > "$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
echo "PINNED_LINUX_BOUNDARY_BUILD_UPLOAD_ACK overall=$OVERALL upload=$UPLOAD"
if [ "$UPLOAD" -ne 0 ]; then exit "$UPLOAD"; fi
exit "$OVERALL"
