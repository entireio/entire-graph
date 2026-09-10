#!/bin/bash
set -euo pipefail

RG=rg-entire-graph-advantage-20260905
VM=graph-validation-linux
ACCOUNT=entiregraphadv20260905
CONTAINER=validation
PREFIX=statusline-linux-actual-task-6f23da0a-r1
REPO=${GRAPH_ADVANTAGE_REPO_ROOT}
EV="$REPO/docs/implementation/graph-advantage/evidence/$PREFIX"
TEMPLATE="$EV/review/remote-template.sh"
VALIDATION_VMS=(graph-p1-worker-2 graph-p1-worker-3 graph-validation-linux)

cleanup() {
  for cleanup_vm in "${VALIDATION_VMS[@]}"; do
    az vm deallocate -g "$RG" -n "$cleanup_vm" --no-wait >/dev/null 2>&1 || true
  done
  for _ in $(seq 1 120); do
    all_off=true
    for cleanup_vm in "${VALIDATION_VMS[@]}"; do
      state=$(az vm get-instance-view -g "$RG" -n "$cleanup_vm" --query "instanceView.statuses[?starts_with(code, 'PowerState/')].code | [0]" -o tsv 2>/dev/null || true)
      [ "$state" = PowerState/deallocated ] || all_off=false
    done
    [ "$all_off" = true ] && break
    sleep 5
  done
  az vm get-instance-view -g "$RG" -n "$VM" -o json >"$EV/vm-terminal.json" 2>/dev/null || true
  az vm list -g "$RG" -d --query '[].{name:name,powerState:powerState}' -o json >"$EV/all-vms-terminal.json" 2>/dev/null || true
}
trap cleanup EXIT

KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv >"$EV/result-blob-exists-before.txt"
test "$(<"$EV/result-blob-exists-before.txt")" = false

EXPIRY=$(date -u -v+2H '+%Y-%m-%dT%H:%MZ')
RESULT_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --permissions cw --expiry "$EXPIRY" --https-only -o tsv)
RESULT_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/results.tar.gz?$RESULT_SAS"
RESULT_URL_B64=$(printf '%s' "$RESULT_URL" | base64 | tr -d '\n')
SCRIPT=$(sed "s|__RESULT_URL_B64__|$RESULT_URL_B64|" "$TEMPLATE")
unset RESULT_SAS RESULT_URL RESULT_URL_B64 KEY

az vm start -g "$RG" -n "$VM" --only-show-errors >/dev/null
set +e
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts "$SCRIPT" -o json >"$EV/transport.json" 2>"$EV/transport.stderr.txt"
TRANSPORT_RC=$?
set -e
unset SCRIPT
printf '%s\n' "$TRANSPORT_RC" >"$EV/transport.exit.txt"

KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv >"$EV/result-blob-exists.txt"
if [ "$(<"$EV/result-blob-exists.txt")" = true ]; then
  az storage blob download --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --file "$EV/results.tar.gz" --only-show-errors >/dev/null
  tar -xzf "$EV/results.tar.gz" -C "$EV"
fi
unset KEY
exit "$TRANSPORT_RC"
