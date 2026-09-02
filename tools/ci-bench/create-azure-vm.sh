#!/usr/bin/env bash
set -euo pipefail

umask 077

readonly PURPOSE_TAG="entire-windows-ci-benchmark"
readonly OWNER_TAG="codex"
readonly DEFAULT_IMAGE_URN="MicrosoftWindowsServer:WindowsServer:2025-datacenter-azure-edition:latest"
readonly DEFAULT_OS_DISK_SKU="Premium_LRS"

usage() {
	cat <<'EOF'
Create one isolated, non-spot Azure Windows benchmark VM.

Usage:
  create-azure-vm.sh --agent a1 --run-id RUN --location REGION --vm-size SKU [options]

Required:
  --agent AGENT          One of a1, a2, a3, a4, a5, or a6.
  --run-id RUN           Lowercase letters, digits, and hyphens; at most 20 chars.
  --location REGION      Azure region, for example westus3.
  --vm-size SKU          Exact Azure VM SKU. Availability is checked in REGION.

Options:
  --expires TIMESTAMP    UTC expiry tag (YYYY-MM-DDTHH:MM:SSZ). Default: eight hours.
  --image-urn URN        Marketplace image override. Default: Windows Server 2025
                         Azure Edition; :latest is resolved to a concrete version.
  --admin-username NAME  Local Windows administrator name. Default: codexbench.
  --resource-group NAME  Must exactly match the derived benchmark group name.
  --subscription ID      Azure subscription name or ID. The active subscription is
                         used when omitted.
  --validate-only        Perform read-only Azure validation, then stop.
  --dry-run              Perform local validation and print redacted planned commands;
                         do not contact Azure.
  -h, --help             Show this help.

The exact resource group is rg-entire-win-ci-<agent>-<run-id>. The VM has no
public IP, its NSG has no custom rules, its OS disk is Premium SSD, and its
priority is always Regular (never Spot). If AZURE_VM_ADMIN_PASSWORD is unset, a
one-use password is generated with openssl and discarded. Passwords are never
printed.
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

default_expiry() {
	local value
	if value=$(date -u -v+8H '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null); then
		printf '%s\n' "$value"
		return
	fi
	if value=$(date -u -d '+8 hours' '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null); then
		printf '%s\n' "$value"
		return
	fi
	die 'cannot calculate the default expiry; pass --expires explicitly'
}

print_sanitized_command() {
	local argument
	local redact_next=false
	printf '+'
	for argument in "$@"; do
		if [[ "$redact_next" == true ]]; then
			printf ' %q' '<redacted>'
			redact_next=false
			continue
		fi
		printf ' %q' "$argument"
		if [[ "$argument" == "--admin-password" ]]; then
			redact_next=true
		fi
	done
	printf '\n'
}

agent=""
run_id=""
location=""
vm_size=""
expires=""
image_urn=""
admin_username="codexbench"
resource_group=""
subscription=""
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
	--location)
		require_option_value "$1" "${2-}"
		location=$2
		shift 2
		;;
	--vm-size)
		require_option_value "$1" "${2-}"
		vm_size=$2
		shift 2
		;;
	--expires)
		require_option_value "$1" "${2-}"
		expires=$2
		shift 2
		;;
	--image-urn)
		require_option_value "$1" "${2-}"
		image_urn=$2
		shift 2
		;;
	--admin-username)
		require_option_value "$1" "${2-}"
		admin_username=$2
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
[[ "$location" =~ ^[a-z0-9]+$ ]] || die '--location must be a lowercase Azure region name'
[[ "$vm_size" =~ ^Standard_[A-Za-z0-9_-]+$ ]] || die '--vm-size must be an exact Standard_* Azure SKU'
[[ "$admin_username" =~ ^[A-Za-z][A-Za-z0-9_-]{0,18}$ ]] ||
	die '--admin-username must start with a letter and contain at most 19 safe characters'

if [[ -z "$expires" ]]; then
	expires=$(default_expiry)
fi
[[ "$expires" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
	die '--expires must use UTC format YYYY-MM-DDTHH:MM:SSZ'
now_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
[[ "$expires" > "$now_utc" ]] || die '--expires must be in the future'

readonly expected_resource_group="rg-entire-win-ci-${agent}-${run_id}"
if [[ -n "$resource_group" && "$resource_group" != "$expected_resource_group" ]]; then
	die "--resource-group must exactly equal $expected_resource_group"
fi
resource_group=$expected_resource_group

readonly vm_name="vm-entire-${agent}-${run_id}"
readonly vnet_name="vnet-entire-${agent}-${run_id}"
readonly subnet_name="snet-benchmark"
readonly nsg_name="nsg-entire-${agent}-${run_id}"
readonly nic_name="nic-entire-${agent}-${run_id}"
readonly os_disk_name="osdisk-entire-${agent}-${run_id}"
run_compact=${run_id//-/}
readonly computer_name="ew${agent}${run_compact:0:10}"
readonly -a tags=(
	"purpose=${PURPOSE_TAG}"
	"agent=${agent}"
	"run=${run_id}"
	"expires=${expires}"
	"owner=${OWNER_TAG}"
)

subscription_args=()
if [[ -n "$subscription" ]]; then
	subscription_args=(--subscription "$subscription")
fi

print_az() {
	print_sanitized_command az "$@" "${subscription_args[@]}" --only-show-errors
}

if [[ "$dry_run" == true ]]; then
	planned_image=${image_urn:-$DEFAULT_IMAGE_URN}
	printf 'Dry run: exact resource group %s\n' "$resource_group"
	printf 'Planned image: %s (validated/resolved during a real run)\n' "$planned_image"
	printf 'Planned VM: %s, SKU %s, location %s, disk %s, priority Regular\n' \
		"$vm_name" "$vm_size" "$location" "$DEFAULT_OS_DISK_SKU"
	print_az account show --query state --output tsv
	print_az account list-locations --query "[?name == '$location'].name | [0]" --output tsv
	print_az vm list-skus --location "$location" --resource-type virtualMachines --size "$vm_size" --output json
	print_az vm image show --location "$location" --urn "$planned_image" --output none
	print_az group create --name "$resource_group" --location "$location" --tags "${tags[@]}" --output none
	print_az network nsg create --resource-group "$resource_group" --name "$nsg_name" --location "$location" --tags "${tags[@]}" --output none
	print_az network vnet create --resource-group "$resource_group" --name "$vnet_name" --location "$location" \
		--address-prefixes 10.42.0.0/16 --subnet-name "$subnet_name" --subnet-prefixes 10.42.0.0/24 \
		--tags "${tags[@]}" --output none
	print_az network nic create --resource-group "$resource_group" --name "$nic_name" --location "$location" \
		--vnet-name "$vnet_name" --subnet "$subnet_name" --network-security-group "$nsg_name" \
		--tags "${tags[@]}" --output none
	print_az vm create --resource-group "$resource_group" --name "$vm_name" --location "$location" \
		--computer-name "$computer_name" --nics "$nic_name" --image "$planned_image" --size "$vm_size" \
		--priority Regular --storage-sku "$DEFAULT_OS_DISK_SKU" --os-disk-name "$os_disk_name" \
		--admin-username "$admin_username" --authentication-type password --admin-password '<generated>' \
		--enable-agent true --tags "${tags[@]}" --output none
	print_az resource tag --resource-group "$resource_group" --name "$os_disk_name" \
		--resource-type Microsoft.Compute/disks --tags "${tags[@]}" --output none
	exit 0
fi

command -v az >/dev/null 2>&1 || die 'Azure CLI (az) is required'

az_cli() {
	command az "$@" "${subscription_args[@]}" --only-show-errors
}

account_state=$(az_cli account show --query state --output tsv)
[[ "$account_state" == "Enabled" ]] || die 'the selected Azure subscription is not enabled'
resolved_location=$(az_cli account list-locations --query "[?name == '$location'].name | [0]" --output tsv)
[[ "$resolved_location" == "$location" ]] || die "Azure region is unavailable in this subscription: $location"

sku_query="[?name == '$vm_size' && length(restrictions) == \`0\`] | [0].name"
resolved_vm_size=$(az_cli vm list-skus --location "$location" --resource-type virtualMachines \
	--size "$vm_size" --query "$sku_query" --output tsv)
[[ "$resolved_vm_size" == "$vm_size" ]] ||
	die "VM SKU $vm_size is unavailable or restricted in $location; discover choices with: az vm list-skus --location $location --resource-type virtualMachines --output table"

premium_query="[?name == '$vm_size'] | [0].capabilities[?name == 'PremiumIO'] | [0].value"
premium_io=$(az_cli vm list-skus --location "$location" --resource-type virtualMachines \
	--size "$vm_size" --query "$premium_query" --output tsv)
[[ "$premium_io" == "True" ]] || die "VM SKU $vm_size does not support the required Premium SSD OS disk"

requested_image=${image_urn:-$DEFAULT_IMAGE_URN}
IFS=: read -r image_publisher image_offer image_sku image_version image_extra <<<"$requested_image"
[[ -n "$image_publisher" && -n "$image_offer" && -n "$image_sku" && -n "$image_version" && -z "$image_extra" ]] ||
	die '--image-urn must be a Marketplace URN: publisher:offer:sku:version'
for image_component in "$image_publisher" "$image_offer" "$image_sku" "$image_version"; do
	[[ "$image_component" =~ ^[A-Za-z0-9._-]+$ ]] || die "unsafe Marketplace image component: $image_component"
done

if [[ "$image_version" == "latest" ]]; then
	image_query="sort_by([?sku == '$image_sku'], &version)[-1].urn"
	resolved_image=$(az_cli vm image list --location "$location" --publisher "$image_publisher" \
		--offer "$image_offer" --sku "$image_sku" --architecture x64 --all \
		--query "$image_query" --output tsv)
	[[ -n "$resolved_image" ]] || die "no matching x64 image is available in $location for $requested_image"
else
	resolved_image=$requested_image
fi

image_os=$(az_cli vm image show --location "$location" --urn "$resolved_image" \
	--query osDiskImage.operatingSystem --output tsv)
[[ "$image_os" == "Windows" ]] || die "resolved image is not Windows: $resolved_image"

group_exists=$(az_cli group exists --name "$resource_group" --output tsv)
[[ "$group_exists" == "false" ]] ||
	die "resource group already exists; refusing to adopt or overwrite it: $resource_group"

printf 'Validated image %s and VM SKU %s in %s.\n' "$resolved_image" "$vm_size" "$location"
if [[ "$validate_only" == true ]]; then
	printf 'Validation only; no Azure resources were created. Exact resource group: %s\n' "$resource_group"
	exit 0
fi

printf 'Creating disposable benchmark resource group %s.\n' "$resource_group"
printf 'If creation is interrupted, remove only this group with delete-azure-resources.sh --agent %s --run-id %s --yes.\n' \
	"$agent" "$run_id"

az_cli group create --name "$resource_group" --location "$location" --tags "${tags[@]}" --output none
az_cli network nsg create --resource-group "$resource_group" --name "$nsg_name" --location "$location" \
	--tags "${tags[@]}" --output none
# Azure subnets do not support tags; the taggable parent VNet carries the mandatory tag set.
az_cli network vnet create --resource-group "$resource_group" --name "$vnet_name" --location "$location" \
	--address-prefixes 10.42.0.0/16 --subnet-name "$subnet_name" --subnet-prefixes 10.42.0.0/24 \
	--tags "${tags[@]}" --output none
az_cli network nic create --resource-group "$resource_group" --name "$nic_name" --location "$location" \
	--vnet-name "$vnet_name" --subnet "$subnet_name" --network-security-group "$nsg_name" \
	--tags "${tags[@]}" --output none

custom_rule_count=$(az_cli network nsg rule list --resource-group "$resource_group" --nsg-name "$nsg_name" \
	--query 'length(@)' --output tsv)
[[ "$custom_rule_count" == "0" ]] || die 'NSG unexpectedly contains custom rules; refusing to create the VM'
public_ip_count=$(az_cli network nic show --resource-group "$resource_group" --name "$nic_name" \
	--query 'length(ipConfigurations[?publicIPAddress != `null`])' --output tsv)
[[ "$public_ip_count" == "0" ]] || die 'NIC unexpectedly has a public IP; refusing to create the VM'

admin_password=${AZURE_VM_ADMIN_PASSWORD-}
if [[ -z "$admin_password" ]]; then
	command -v openssl >/dev/null 2>&1 ||
		die 'openssl is required to generate the one-use VM password; alternatively set AZURE_VM_ADMIN_PASSWORD'
	admin_password="Aa1!$(openssl rand -hex 24)"
fi

az_cli vm create --resource-group "$resource_group" --name "$vm_name" --location "$location" \
	--computer-name "$computer_name" --nics "$nic_name" --image "$resolved_image" --size "$vm_size" \
	--priority Regular --storage-sku "$DEFAULT_OS_DISK_SKU" --os-disk-name "$os_disk_name" \
	--admin-username "$admin_username" --authentication-type password --admin-password "$admin_password" \
	--enable-agent true --tags "${tags[@]}" --output none
unset admin_password

az_cli resource tag --resource-group "$resource_group" --name "$os_disk_name" \
	--resource-type Microsoft.Compute/disks --tags "${tags[@]}" --output none

vm_priority=$(az_cli vm show --resource-group "$resource_group" --name "$vm_name" --query priority --output tsv)
[[ "$vm_priority" == "Regular" ]] || die "VM priority is not Regular: $vm_priority"
disk_sku=$(az_cli disk show --resource-group "$resource_group" --name "$os_disk_name" --query sku.name --output tsv)
[[ "$disk_sku" == "$DEFAULT_OS_DISK_SKU" ]] || die "OS disk SKU is not $DEFAULT_OS_DISK_SKU: $disk_sku"
untagged_count=$(az_cli resource list --resource-group "$resource_group" \
	--query "[?tags.purpose != '$PURPOSE_TAG' || tags.agent != '$agent' || tags.run != '$run_id' || tags.expires != '$expires' || tags.owner != '$OWNER_TAG'] | length(@)" \
	--output tsv)
[[ "$untagged_count" == "0" ]] || die "$untagged_count taggable resources are missing mandatory benchmark tags"

printf 'Created VM %s in exact resource group %s.\n' "$vm_name" "$resource_group"
printf 'Image: %s\nVM SKU: %s\nOS disk: %s\nPublic IPs: 0\nCustom NSG rules: 0\n' \
	"$resolved_image" "$vm_size" "$DEFAULT_OS_DISK_SKU"
