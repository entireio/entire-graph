#!/usr/bin/env bash
set -euo pipefail

umask 077

readonly PURPOSE_TAG="entire-windows-ci-benchmark"
readonly OWNER_TAG="codex"

usage() {
	cat <<'EOF'
Execute a local PowerShell script on one exact benchmark VM with Azure Run Command.

Usage:
  run-on-azure-vm.sh --agent a1 --run-id RUN --script FILE [options]

Required:
  --agent AGENT          One of a1, a2, a3, a4, a5, or a6.
  --run-id RUN           Lowercase letters, digits, and hyphens; at most 20 chars.
  --script FILE          Local PowerShell script sent to RunPowerShellScript.

Options:
  --resource-group NAME  Must exactly match the derived benchmark group name.
  --subscription ID      Azure subscription name or ID. The active subscription is
                         used when omitted.
  --output-file FILE     Write the Azure Run Command JSON response to a new file.
  --validate-only        Validate the group, tags, VM, and private networking, then stop.
  --dry-run              Perform local validation and print the planned command without
                         contacting Azure.
  -h, --help             Show this help.

This wrapper deliberately does not accept inline arguments or protected values:
benchmark input belongs in the reviewed PowerShell script, and secrets must not
be sent through Run Command arguments or printed by that script.
EOF
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

require_option_value() {
	local option=$1
	local value=${2-}
	[[ -n "$value" ]] || die "$option requires a non-empty value"
}

print_command() {
	local argument
	printf '+'
	for argument in "$@"; do
		printf ' %q' "$argument"
	done
	printf '\n'
}

agent=""
run_id=""
script_path=""
resource_group=""
subscription=""
output_file=""
dry_run=false
validate_only=false

while (($# > 0)); do
	case "$1" in
	--agent)
		require_option_value "$1" "${2-}"
		agent=$2
		shift 2
		;;
	--run-id)
		require_option_value "$1" "${2-}"
		run_id=$2
		shift 2
		;;
	--script)
		require_option_value "$1" "${2-}"
		script_path=$2
		shift 2
		;;
	--resource-group)
		require_option_value "$1" "${2-}"
		resource_group=$2
		shift 2
		;;
	--subscription)
		require_option_value "$1" "${2-}"
		subscription=$2
		shift 2
		;;
	--output-file)
		require_option_value "$1" "${2-}"
		output_file=$2
		shift 2
		;;
	--dry-run)
		dry_run=true
		shift
		;;
	--validate-only)
		validate_only=true
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
done

[[ "$agent" =~ ^a[1-6]$ ]] || die '--agent must be one of a1 through a6'
[[ "$run_id" =~ ^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$ ]] ||
	die '--run-id must be 1-20 lowercase letters/digits/hyphens and cannot end in a hyphen'
[[ -n "$script_path" ]] || die '--script is required'
[[ -f "$script_path" && -r "$script_path" ]] || die "PowerShell script is not a readable file: $script_path"
case "$script_path" in
*.ps1) ;;
*) die '--script must name a .ps1 file' ;;
esac

script_directory=$(CDPATH= cd -- "$(dirname -- "$script_path")" && pwd)
script_path="$script_directory/$(basename -- "$script_path")"

if [[ -n "$output_file" ]]; then
	output_parent=$(dirname -- "$output_file")
	[[ -d "$output_parent" ]] || die "output directory does not exist: $output_parent"
	[[ ! -e "$output_file" ]] || die "refusing to overwrite output file: $output_file"
	output_parent=$(CDPATH= cd -- "$output_parent" && pwd)
	output_file="$output_parent/$(basename -- "$output_file")"
fi

readonly expected_resource_group="rg-entire-win-ci-${agent}-${run_id}"
if [[ -n "$resource_group" && "$resource_group" != "$expected_resource_group" ]]; then
	die "--resource-group must exactly equal $expected_resource_group"
fi
resource_group=$expected_resource_group
readonly vm_name="vm-entire-${agent}-${run_id}"

subscription_args=()
if [[ -n "$subscription" ]]; then
	subscription_args=(--subscription "$subscription")
fi

if [[ "$dry_run" == true ]]; then
	printf 'Dry run: would validate and execute %s on %s/%s.\n' "$script_path" "$resource_group" "$vm_name"
	print_command az vm run-command invoke --resource-group "$resource_group" --name "$vm_name" \
		--command-id RunPowerShellScript --scripts "@$script_path" --output json \
		"${subscription_args[@]}" --only-show-errors
	exit 0
fi

command -v az >/dev/null 2>&1 || die 'Azure CLI (az) is required'

az_cli() {
	command az "$@" "${subscription_args[@]}" --only-show-errors
}

account_state=$(az_cli account show --query state --output tsv)
[[ "$account_state" == "Enabled" ]] || die 'the selected Azure subscription is not enabled'
group_exists=$(az_cli group exists --name "$resource_group" --output tsv)
[[ "$group_exists" == "true" ]] || die "exact resource group does not exist: $resource_group"

group_tags=$(az_cli group show --name "$resource_group" \
	--query "join('|', [tags.purpose, tags.agent, tags.run, tags.expires, tags.owner])" --output tsv) ||
	die 'resource group is missing one or more mandatory tags'
IFS='|' read -r group_purpose group_agent group_run group_expires group_owner <<<"$group_tags"
[[ "$group_purpose" == "$PURPOSE_TAG" ]] || die 'resource group purpose tag is not the benchmark purpose'
[[ "$group_agent" == "$agent" ]] || die 'resource group agent tag does not match --agent'
[[ "$group_run" == "$run_id" ]] || die 'resource group run tag does not match --run-id'
[[ "$group_expires" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
	die 'resource group expires tag is missing or malformed'
[[ "$group_owner" == "$OWNER_TAG" ]] || die 'resource group owner tag is not codex'

vm_tags=$(az_cli vm show --resource-group "$resource_group" --name "$vm_name" \
	--query "join('|', [tags.purpose, tags.agent, tags.run, tags.expires, tags.owner])" --output tsv) ||
	die "exact benchmark VM does not exist or is missing mandatory tags: $vm_name"
[[ "$vm_tags" == "$group_tags" ]] || die 'VM tags do not exactly match the resource-group benchmark tags'

vm_state=$(az_cli vm show --resource-group "$resource_group" --name "$vm_name" --query provisioningState --output tsv)
[[ "$vm_state" == "Succeeded" ]] || die "VM provisioning state is not Succeeded: $vm_state"
vm_priority=$(az_cli vm show --resource-group "$resource_group" --name "$vm_name" --query priority --output tsv)
[[ "$vm_priority" == "Regular" ]] || die "refusing to run on non-Regular VM priority: $vm_priority"
vm_os=$(az_cli vm show --resource-group "$resource_group" --name "$vm_name" \
	--query storageProfile.osDisk.osType --output tsv)
[[ "$vm_os" == "Windows" ]] || die "refusing to run on a non-Windows VM: $vm_os"

nic_ids=$(az_cli vm show --resource-group "$resource_group" --name "$vm_name" \
	--query 'networkProfile.networkInterfaces[].id' --output tsv)
[[ -n "$nic_ids" ]] || die 'VM has no network interface'
while IFS= read -r nic_id; do
	[[ -n "$nic_id" ]] || continue
	public_ip_count=$(az_cli network nic show --ids "$nic_id" \
		--query 'length(ipConfigurations[?publicIPAddress != `null`])' --output tsv)
	[[ "$public_ip_count" == "0" ]] || die "refusing to run on a VM with a public IP: $nic_id"
done <<<"$nic_ids"

printf 'Validated private, tagged, non-spot Windows VM %s/%s.\n' "$resource_group" "$vm_name"
if [[ "$validate_only" == true ]]; then
	printf 'Validation only; Run Command was not invoked.\n'
	exit 0
fi

run_command=(
	vm run-command invoke
	--resource-group "$resource_group"
	--name "$vm_name"
	--command-id RunPowerShellScript
	--scripts "@$script_path"
	--output json
)

if [[ -z "$output_file" ]]; then
	az_cli "${run_command[@]}"
	printf 'Run Command completed for %s/%s.\n' "$resource_group" "$vm_name" >&2
	exit 0
fi

temporary_output=$(mktemp "${output_file}.tmp.XXXXXX")
remove_temporary_output() {
	rm -f -- "$temporary_output"
}
trap remove_temporary_output EXIT HUP INT TERM
az_cli "${run_command[@]}" >"$temporary_output"
mv -- "$temporary_output" "$output_file"
trap - EXIT HUP INT TERM
printf 'Run Command completed for %s/%s; response written to %s.\n' \
	"$resource_group" "$vm_name" "$output_file" >&2
