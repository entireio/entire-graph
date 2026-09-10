#!/bin/bash
set -euo pipefail
RG=rg-entire-graph-advantage-20260905
VM=graph-validation-linux
ACCOUNT=entiregraphadv20260905
CONTAINER=validation
PREFIX=check-6f23da0a-linux-cache-fixtures-r2
REPO=${GRAPH_ADVANTAGE_REPO_ROOT}
EV="$REPO/docs/implementation/graph-advantage/evidence/$PREFIX"
REMOTE_TEMPLATE="$EV/review/remote-template.sh"
terminal_capture() {
  az vm deallocate -g "$RG" -n "$VM" --no-wait >/dev/null 2>&1 || true
  for _ in $(seq 1 120); do
    state=$(az vm get-instance-view -g "$RG" -n "$VM" --query "instanceView.statuses[?starts_with(code, 'PowerState/')].code | [0]" -o tsv 2>/dev/null || true)
    [ "$state" = PowerState/deallocated ] && break
    sleep 5
  done
  az vm get-instance-view -g "$RG" -n "$VM" -o json > "$EV/vm-terminal.json" 2>/dev/null || true
  az vm list -g "$RG" -d --query '[].{name:name,powerState:powerState}' -o json > "$EV/all-vms-terminal.json" 2>/dev/null || true
}
trap terminal_capture EXIT
KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
SOURCE_ARCHIVE="$REPO/docs/implementation/graph-advantage/evidence/diagnostics-linux-6f23da0a/source.tar.gz"
echo 'e1a42113a94d043c79833ea9d16ae568965bdb50efdd7a5ccc5f3186c883032b  '"$SOURCE_ARCHIVE" | sha256sum -c - >/dev/null
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/source.tar.gz" --query exists -o tsv > "$EV/source-blob-exists-before.txt"
test "$(cat "$EV/source-blob-exists-before.txt")" = false
az storage blob upload --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/source.tar.gz" --file "$SOURCE_ARCHIVE" --overwrite false --only-show-errors >/dev/null
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv > "$EV/result-blob-exists-before.txt"
test "$(cat "$EV/result-blob-exists-before.txt")" = false
EXPIRY=$(date -u -v+2H '+%Y-%m-%dT%H:%MZ')
RESULT_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --permissions cw --expiry "$EXPIRY" --https-only -o tsv)
SOURCE_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/source.tar.gz" --permissions r --expiry "$EXPIRY" --https-only -o tsv)
RESULT_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/results.tar.gz?$RESULT_SAS"
SOURCE_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/source.tar.gz?$SOURCE_SAS"
RESULT_URL_B64=$(printf '%s' "$RESULT_URL" | base64 | tr -d '\n')
SOURCE_URL_B64=$(printf '%s' "$SOURCE_URL" | base64 | tr -d '\n')
REMOTE_SCRIPT=$(sed -e "s|__RESULT_URL_B64__|$RESULT_URL_B64|" -e "s|__SOURCE_URL_B64__|$SOURCE_URL_B64|" "$REMOTE_TEMPLATE")
unset RESULT_SAS SOURCE_SAS RESULT_URL SOURCE_URL RESULT_URL_B64 SOURCE_URL_B64 KEY
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
