#!/bin/bash
set -u
INPUT_URL=$(printf '%s' '__INPUT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
RESULT_URL=$(printf '%s' '__RESULT_URL_B64__' | base64 -d 2>/dev/null) || exit 72
case "$INPUT_URL" in '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;; *) exit 72;; esac
case "$RESULT_URL" in '${GRAPH_ADVANTAGE_BLOB_URL}'?*) ;; *) exit 72;; esac
ROOT=/opt/graph-validation/check-6f23da0a-linux-full-r1
[ ! -e "$ROOT" ] || exit 71
mkdir -p "$ROOT/raw" "$ROOT/input" "$ROOT/src" "$ROOT/mise-data" "$ROOT/mise-config" "$ROOT/mise-cache"
OVERALL=0
/usr/bin/curl --fail --silent --show-error "$INPUT_URL" -o "$ROOT/input.tar.gz" || OVERALL=73
[ "$OVERALL" -ne 0 ] || echo 'db5677fd458b2b9d0f56fc87755af9f7442c8750b9c7da57f7fdc1ab9816a63f  '"$ROOT/input.tar.gz" | sha256sum -c - >"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=74
[ "$OVERALL" -ne 0 ] || tar -xzf "$ROOT/input.tar.gz" -C "$ROOT/input" >>"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=75
[ "$OVERALL" -ne 0 ] || echo 'f9afe6ae83cdc8f396082276cb64d7890f91d368d9f87d868193c0f2e7019d2b  '"$ROOT/input/source.tar.gz" | sha256sum -c - >>"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=76
[ "$OVERALL" -ne 0 ] || echo '7572d7a7212a5bb16a964ab0a697a6751efa7fd8226dac6c38d9309f1e0e6f5c  '"$ROOT/input/tracked-manifest-r1.tsv" | sha256sum -c - >>"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=77
[ "$OVERALL" -ne 0 ] || echo '00ec58da20aac5c9cfab08aa3210fbacec9353d4217f964a8c318612a38f5d33  '"$ROOT/input/mise" | sha256sum -c - >>"$ROOT/raw/input-identity.txt" 2>&1 || OVERALL=78
[ "$OVERALL" -ne 0 ] || tar -xzf "$ROOT/input/source.tar.gz" -C "$ROOT/src" || OVERALL=79
manifest() { (cd "$ROOT/src" && while IFS=$'\t' read -r mode sha path; do if [ -x "$path" ]; then actual_mode=100755; else actual_mode=100644; fi; actual_sha=$(sha256sum "$path"|awk '{print $1}') || return; printf '%s\t%s\t%s\n' "$actual_mode" "$actual_sha" "$path"; done < "$ROOT/input/tracked-manifest-r1.tsv"); }
if [ "$OVERALL" -eq 0 ]; then manifest >"$ROOT/raw/before-tracked-manifest-r1.tsv" || OVERALL=80; fi
if [ "$OVERALL" -eq 0 ]; then cmp "$ROOT/input/tracked-manifest-r1.tsv" "$ROOT/raw/before-tracked-manifest-r1.tsv" >"$ROOT/raw/before-identity.txt" 2>&1 || OVERALL=81; fi
chmod 755 "$ROOT/input/mise"
export PATH=/usr/local/go/bin:/usr/bin:/bin
export MISE_DATA_DIR="$ROOT/mise-data" MISE_CONFIG_DIR="$ROOT/mise-config" MISE_CACHE_DIR="$ROOT/mise-cache"
export GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0
export MISE_JOBS=1 GOFLAGS='-p=1 -v' GOMAXPROCS=4 MISE_YES=1
MISE="$ROOT/input/mise"
if [ "$OVERALL" -eq 0 ]; then
 set +e
 "$MISE" link -f go@1.26 /usr/local/go >"$ROOT/raw/mise-link.txt" 2>&1
 LINK_RC=$?
 "$MISE" trust "$ROOT/src/mise.toml" >"$ROOT/raw/mise-trust.txt" 2>&1
 TRUST_RC=$?
 SELECTED_GO=$(cd "$ROOT/src" && "$MISE" which go 2>&1); WHICH_RC=$?
 SELECTED_VERSION=$([ "$WHICH_RC" -eq 0 ] && "$SELECTED_GO" version 2>&1); VERSION_RC=$?
 "$MISE" --version >"$ROOT/raw/mise-version.txt" 2>&1; MISE_RC=$?
 set -e
 { printf 'link_exit=%s\ntrust_exit=%s\nwhich_exit=%s\nversion_exit=%s\nmise_exit=%s\nselected_go=%s\nselected_version=%s\n' "$LINK_RC" "$TRUST_RC" "$WHICH_RC" "$VERSION_RC" "$MISE_RC" "$SELECTED_GO" "$SELECTED_VERSION"; } >"$ROOT/raw/toolchain.txt"
 if [ "$LINK_RC" -ne 0 ] || [ "$TRUST_RC" -ne 0 ] || [ "$WHICH_RC" -ne 0 ] || [ "$VERSION_RC" -ne 0 ] || [ "$MISE_RC" -ne 0 ] || [ "$(readlink -f "$SELECTED_GO")" != "$(readlink -f /usr/local/go/bin/go)" ] || [ "$SELECTED_VERSION" != 'go version go1.26.1 linux/amd64' ]; then OVERALL=82; fi
fi
if [ "$OVERALL" -eq 0 ]; then
 START=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
 set +e
 (cd "$ROOT/src" && "$MISE" run check) >"$ROOT/raw/mise-check.raw.log" 2>&1
 CHECK_RC=$?
 set -e
 END=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
 printf '%s\n' "$CHECK_RC" >"$ROOT/raw/check.exit.txt"
 printf 'start=%s\nend=%s\n' "$START" "$END" >"$ROOT/raw/timing.txt"
 [ "$CHECK_RC" -eq 0 ] || OVERALL=$CHECK_RC
fi
manifest >"$ROOT/raw/after-tracked-manifest-r1.tsv" 2>"$ROOT/raw/after-identity.txt" || [ "$OVERALL" -ne 0 ] || OVERALL=83
cmp "$ROOT/input/tracked-manifest-r1.tsv" "$ROOT/raw/after-tracked-manifest-r1.tsv" >>"$ROOT/raw/after-identity.txt" 2>&1 || [ "$OVERALL" -ne 0 ] || OVERALL=84
printf '%s\n' "$OVERALL" >"$ROOT/raw/overall.exit.txt"
tar -czf "$ROOT/results.tar.gz" -C "$ROOT" raw
/usr/bin/curl --fail --silent --show-error -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$ROOT/results.tar.gz" "$RESULT_URL"
UPLOAD=$?
printf 'LINUX_FULLCHECK_UPLOAD_ACK overall=%s upload=%s\n' "$OVERALL" "$UPLOAD"
[ "$UPLOAD" -eq 0 ] || exit "$UPLOAD"
exit "$OVERALL"
