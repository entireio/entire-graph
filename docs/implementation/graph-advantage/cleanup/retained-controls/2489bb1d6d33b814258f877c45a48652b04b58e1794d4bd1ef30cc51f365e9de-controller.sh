#!/bin/bash
set -euo pipefail
RG=rg-entire-graph-advantage-20260905
VM=graph-validation-linux
ACCOUNT=entiregraphadv20260905
CONTAINER=validation
PREFIX=diagnostics-linux-25887f69-evaluator-build-01
REPO=${GRAPH_ADVANTAGE_REPO_ROOT}
EV="$REPO/docs/implementation/graph-advantage/evidence/$PREFIX"
REMOTE_TEMPLATE="$EV/review/remote-template.sh"

terminal_capture() {
  for vm_name in graph-validation-linux graph-p1-worker-2 graph-p1-worker-3; do
    az vm deallocate -g "$RG" -n "$vm_name" --no-wait >/dev/null 2>&1 || true
  done
  for _ in $(seq 1 120); do
    states=$(az vm list -g "$RG" -d --query '[].powerState' -o tsv 2>/dev/null || true)
    nonterminal=$(printf '%s\n' "$states" | grep -vc '^VM deallocated$' || true)
    [ "$nonterminal" -eq 0 ] && break
    sleep 5
  done
  az vm get-instance-view -g "$RG" -n "$VM" -o json > "$EV/vm-terminal.json" 2>/dev/null || true
  az vm list -g "$RG" -d --query '[].{name:name,powerState:powerState}' -o json > "$EV/all-vms-terminal.json" 2>/dev/null || true
}
trap terminal_capture EXIT

test "$(git -C "$REPO" rev-parse 25887f6954fc06e35bc3a7c699e3c524213dada4^{commit})" = 25887f6954fc06e35bc3a7c699e3c524213dada4
test "$(shasum -a 256 "$REPO/docs/implementation/graph-advantage/evidence/check-25887f69-linux-full/raw/before-tracked-manifest.tsv" | awk '{print $1}')" = 1691ddc7be3f1e8f274779197e0b741a04a3fe717b063488effdc08a2d85fb07
test "$(shasum -a 256 "$REPO/docs/implementation/graph-advantage/evidence/check-25887f69-linux-full/raw/after-tracked-manifest.tsv" | awk '{print $1}')" = 1691ddc7be3f1e8f274779197e0b741a04a3fe717b063488effdc08a2d85fb07
cmp "$REPO/docs/implementation/graph-advantage/evidence/check-25887f69-linux-full/raw/before-tracked-manifest.tsv" "$REPO/docs/implementation/graph-advantage/evidence/check-25887f69-linux-full/raw/after-tracked-manifest.tsv"
test "$(cat "$REPO/docs/implementation/graph-advantage/evidence/check-25887f69-linux-full/raw/overall.exit.txt")" = 0

KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv > "$EV/result-blob-exists-before.txt"
test "$(cat "$EV/result-blob-exists-before.txt")" = false
EXPIRY=$(date -u -v+2H '+%Y-%m-%dT%H:%MZ')
RESULT_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --permissions cw --expiry "$EXPIRY" --https-only -o tsv)
RESULT_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/results.tar.gz?$RESULT_SAS"
RESULT_URL_B64=$(printf '%s' "$RESULT_URL" | base64 | tr -d '\n')
REMOTE_SCRIPT=$(sed -e "s|__RESULT_URL_B64__|$RESULT_URL_B64|" "$REMOTE_TEMPLATE")
unset RESULT_SAS RESULT_URL RESULT_URL_B64 KEY

az vm start -g "$RG" -n "$VM" --only-show-errors >/dev/null
set +e
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts "$REMOTE_SCRIPT" -o json > "$EV/transport.json" 2> "$EV/transport.stderr.txt"
RC=$?
set -e
unset REMOTE_SCRIPT
printf '%s\n' "$RC" > "$EV/transport.exit.txt"

KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv > "$EV/result-blob-exists.txt"
if [ "$(cat "$EV/result-blob-exists.txt")" = true ]; then
  az storage blob download --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --file "$EV/results.tar.gz" --only-show-errors >/dev/null
  tar -xzf "$EV/results.tar.gz" -C "$EV"
fi
unset KEY
exit "$RC"
