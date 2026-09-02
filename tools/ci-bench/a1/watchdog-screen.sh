#!/usr/bin/env bash
set -euo pipefail

resource_group=""
vm_name=""
storage_account=""
blob_name=""
timeout_minutes="36"

usage() {
  cat <<'EOF'
Usage: watchdog-screen.sh --resource-group NAME --vm-name NAME \
  --storage-account NAME --blob-name NAME [--timeout-minutes N]

Polls for a private result blob. If it does not appear before the bounded
deadline, deallocates the exact VM so a hung Azure Run Command cannot accrue
unbounded compute cost. This script never deletes resources.
EOF
}

while (($# > 0)); do
  case "$1" in
    --resource-group)
      resource_group="${2:?missing value for --resource-group}"
      shift 2
      ;;
    --vm-name)
      vm_name="${2:?missing value for --vm-name}"
      shift 2
      ;;
    --storage-account)
      storage_account="${2:?missing value for --storage-account}"
      shift 2
      ;;
    --blob-name)
      blob_name="${2:?missing value for --blob-name}"
      shift 2
      ;;
    --timeout-minutes)
      timeout_minutes="${2:?missing value for --timeout-minutes}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$resource_group" || -z "$vm_name" || -z "$storage_account" || -z "$blob_name" ]]; then
  usage >&2
  exit 2
fi
if [[ ! "$timeout_minutes" =~ ^[0-9]+$ ]] || ((timeout_minutes < 31 || timeout_minutes > 60)); then
  echo "--timeout-minutes must be an integer from 31 through 60" >&2
  exit 2
fi
if [[ "$resource_group" != rg-entire-win-ci-a1-* ]]; then
  echo "Refusing non-A1 resource group: $resource_group" >&2
  exit 2
fi
if [[ "$vm_name" != vm-entire-a1-* ]]; then
  echo "Refusing non-A1 VM: $vm_name" >&2
  exit 2
fi

iterations=$((timeout_minutes * 2))
for ((iteration = 1; iteration <= iterations; iteration++)); do
  sleep 30
  blob_exists="$(
    az storage blob exists \
      --account-name "$storage_account" \
      --container-name results \
      --name "$blob_name" \
      --auth-mode login \
      --query exists \
      --output tsv \
      --only-show-errors 2>/dev/null || true
  )"
  if [[ "$blob_exists" == "true" ]]; then
    echo "watchdog result=artifact-present blob=$blob_name iteration=$iteration"
    exit 0
  fi
done

echo "watchdog result=deadline-expired action=deallocate resource_group=$resource_group vm=$vm_name" >&2
az vm deallocate \
  --resource-group "$resource_group" \
  --name "$vm_name" \
  --output none \
  --only-show-errors
