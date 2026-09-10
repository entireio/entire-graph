#!/bin/bash
set -euo pipefail
RG=rg-entire-graph-advantage-20260905
VM=graph-validation-linux
ACCOUNT=entiregraphadv20260905
CONTAINER=validation
PREFIX=diagnostics-linux-6f23da0a
COMMIT=6f23da0ad4fa704da8c7bac53e1065030c89a4f1
REPO=${GRAPH_ADVANTAGE_REPO_ROOT}
EV="$REPO/docs/implementation/graph-advantage/evidence/$PREFIX"
REMOTE_TEMPLATE="$EV/review/remote-launcher-template.sh"
mkdir -p "$EV"
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
test "$(git -C "$REPO" rev-parse "$COMMIT^{commit}")" = "$COMMIT"
git -C "$REPO" archive --format=tar.gz --output="$EV/source.tar.gz" "$COMMIT" internal cmd scripts go.mod go.sum
sha256sum "$EV/source.tar.gz" > "$EV/source.sha256"
SRC_SHA=$(cut -d' ' -f1 "$EV/source.sha256")
KEY=$(az storage account keys list -g "$RG" -n "$ACCOUNT" --query '[0].value' -o tsv)
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/source.tar.gz" --query exists -o tsv > "$EV/source-blob-exists-before.txt"
az storage blob exists --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --query exists -o tsv > "$EV/result-blob-exists-before.txt"
test "$(cat "$EV/source-blob-exists-before.txt")" = false
test "$(cat "$EV/result-blob-exists-before.txt")" = false
az storage blob upload --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/source.tar.gz" --file "$EV/source.tar.gz" --overwrite false --only-show-errors >/dev/null
EXPIRY=$(date -u -v+2H '+%Y-%m-%dT%H:%MZ')
SRC_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/source.tar.gz" --permissions r --expiry "$EXPIRY" --https-only -o tsv)
RES_SAS=$(az storage blob generate-sas --account-name "$ACCOUNT" --account-key "$KEY" --container-name "$CONTAINER" --name "$PREFIX/results.tar.gz" --permissions cw --expiry "$EXPIRY" --https-only -o tsv)
SRC_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/source.tar.gz?$SRC_SAS"
RES_URL="https://$ACCOUNT.blob.core.windows.net/$CONTAINER/$PREFIX/results.tar.gz?$RES_SAS"
az vm start -g "$RG" -n "$VM" --only-show-errors >/dev/null
SRC_URL_B64=$(printf '%s' "$SRC_URL" | base64 | tr -d '\n')
RES_URL_B64=$(printf '%s' "$RES_URL" | base64 | tr -d '\n')
REMOTE_SCRIPT=$(sed -e "s|__SOURCE_URL_B64__|$SRC_URL_B64|" -e "s|__RESULT_URL_B64__|$RES_URL_B64|" -e "s|__SOURCE_SHA__|$SRC_SHA|" "$REMOTE_TEMPLATE")
unset SRC_SAS RES_SAS SRC_URL RES_URL SRC_URL_B64 RES_URL_B64 KEY
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
