#!/bin/bash
set -euo pipefail
RG=rg-entire-graph-advantage-20260905 VM=graph-validation-linux ACCOUNT=entiregraphadv20260905 CONTAINER=validation PREFIX=check-effa358f-linux-full-01
REPO=${GRAPH_ADVANTAGE_REPO_ROOT}
EV="$REPO/docs/implementation/graph-advantage/evidence/check-effa358f-linux-full-01"
INPUT=/tmp/check-effa358f-linux-full-01.2obFb2/input.tar.gz
TEMPLATE="$EV/review/remote-template.sh"
finish(){
  for vm_name in graph-validation-linux graph-p1-worker-2 graph-p1-worker-3; do
    az vm deallocate -g "$RG" -n "$vm_name" --no-wait >/dev/null 2>&1 || true
  done
  for _ in $(seq 1 120); do
    all_deallocated=true
    for vm_name in graph-validation-linux graph-p1-worker-2 graph-p1-worker-3; do
      state=$(az vm get-instance-view -g "$RG" -n "$vm_name" --query "instanceView.statuses[?starts_with(code, 'PowerState/')].code | [0]" -o tsv 2>/dev/null || true)
      [ "$state" = PowerState/deallocated ] || all_deallocated=false
    done
    [ "$all_deallocated" = true ] && break
    sleep 5
  done
  az vm get-instance-view -g "$RG" -n "$VM" -o json >"$EV/vm-terminal.json" 2>/dev/null || true
  az vm list -g "$RG" -d --query '[].{name:name,powerState:powerState}' -o json >"$EV/all-vms-terminal.json" 2>/dev/null || true
}
trap finish EXIT
KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
echo '61b4baaafd23ea350e05bd856cacc617bf9603990abbcf5aa829b04260783ce8  '"$INPUT" | sha256sum -c - >/dev/null
for blob in input.tar.gz results.tar.gz; do az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/$blob" --query exists -o tsv >"$EV/${blob%.tar.gz}-blob-exists-before.txt"; test "$(cat "$EV/${blob%.tar.gz}-blob-exists-before.txt")" = false; done
az storage blob upload --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/input.tar.gz" --file "$INPUT" --overwrite false --only-show-errors >/dev/null
EXPIRY=$(date -u -v+3H '+%Y-%m-%dT%H:%MZ')
IN_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/input.tar.gz" --permissions r --expiry "$EXPIRY" --https-only -o tsv)
OUT_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --permissions cw --expiry "$EXPIRY" --https-only -o tsv)
IN_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/input.tar.gz?$IN_SAS" OUT_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/results.tar.gz?$OUT_SAS"
IN_B64=$(printf %s "$IN_URL"|base64|tr -d '\n') OUT_B64=$(printf %s "$OUT_URL"|base64|tr -d '\n')
SCRIPT=$(sed -e "s|__INPUT_URL_B64__|$IN_B64|" -e "s|__RESULT_URL_B64__|$OUT_B64|" "$TEMPLATE")
unset IN_SAS OUT_SAS IN_URL OUT_URL IN_B64 OUT_B64 KEY
az vm start -g "$RG" -n "$VM" --only-show-errors >/dev/null
set +e
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts "$SCRIPT" -o json >"$EV/transport.json" 2>"$EV/transport.stderr.txt"
RC=$?
set -e
unset SCRIPT
printf '%s\n' "$RC" >"$EV/transport.exit.txt"
KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv >"$EV/result-blob-exists.txt"
if [ "$(cat "$EV/result-blob-exists.txt")" = true ]; then az storage blob download --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --file "$EV/results.tar.gz" --only-show-errors >/dev/null; tar -xzf "$EV/results.tar.gz" -C "$EV"; fi
unset KEY
exit "$RC"
