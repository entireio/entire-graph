#!/usr/bin/env bash
set -euo pipefail

readonly PURPOSE_TAG="entire-windows-ci-benchmark"
readonly OWNER_TAG="codex"

usage() {
	cat <<'EOF'
Delete one exact, tagged Azure benchmark resource group.

Usage:
  delete-azure-resources.sh --agent a1 --run-id RUN --yes [options]

Required for deletion:
  --agent AGENT          One of a1, a2, a3, a4, a5, or a6.
  --run-id RUN           Lowercase letters, digits, and hyphens; at most 20 chars.
  --yes                  Confirm deletion after all safety checks pass.

Options:
  --resource-group NAME  Must exactly match the derived benchmark group name.
  --subscription ID      Azure subscription name or ID. The active subscription is
                         used when omitted.
  --no-wait              Submit deletion without waiting for completion.
  --validate-only        Perform read-only existence/tag/scope validation, then stop.
  --dry-run              Perform local validation and print the exact planned deletion;
                         do not contact Azure. --yes is not required.
  -h, --help             Show this help.

The script never accepts a resource-group prefix, wildcard, or arbitrary group.
It derives rg-entire-win-ci-<agent>-<run-id>, verifies the mandatory benchmark
tags on the group and every listed resource, and only then deletes that exact
group. A missing group is an idempotent success.
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
resource_group=""
subscription=""
confirmed=false
no_wait=false
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
	--yes)
		confirmed=true
		shift
		;;
	--no-wait)
		no_wait=true
		shift
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

readonly expected_resource_group="rg-entire-win-ci-${agent}-${run_id}"
if [[ -n "$resource_group" && "$resource_group" != "$expected_resource_group" ]]; then
	die "--resource-group must exactly equal $expected_resource_group"
fi
resource_group=$expected_resource_group

subscription_args=()
if [[ -n "$subscription" ]]; then
	subscription_args=(--subscription "$subscription")
fi

delete_args=(group delete --name "$resource_group" --yes --output none)
if [[ "$no_wait" == true ]]; then
	delete_args+=(--no-wait)
fi

if [[ "$dry_run" == true ]]; then
	printf 'Dry run: would validate mandatory tags and delete only %s.\n' "$resource_group"
	print_command az "${delete_args[@]}" "${subscription_args[@]}" --only-show-errors
	exit 0
fi

command -v az >/dev/null 2>&1 || die 'Azure CLI (az) is required'

az_cli() {
	command az "$@" "${subscription_args[@]}" --only-show-errors
}

account_state=$(az_cli account show --query state --output tsv)
[[ "$account_state" == "Enabled" ]] || die 'the selected Azure subscription is not enabled'
group_exists=$(az_cli group exists --name "$resource_group" --output tsv)
if [[ "$group_exists" == "false" ]]; then
	printf 'Exact resource group does not exist; nothing to delete: %s\n' "$resource_group"
	exit 0
fi
[[ "$group_exists" == "true" ]] || die "unexpected Azure response while checking $resource_group"

group_tags=$(az_cli group show --name "$resource_group" \
	--query "join('|', [tags.purpose, tags.agent, tags.run, tags.expires, tags.owner])" --output tsv) ||
	die 'resource group is missing one or more mandatory tags'
IFS='|' read -r group_purpose group_agent group_run group_expires group_owner <<<"$group_tags"
[[ "$group_purpose" == "$PURPOSE_TAG" ]] || die 'refusing deletion: purpose tag is not the benchmark purpose'
[[ "$group_agent" == "$agent" ]] || die 'refusing deletion: agent tag does not match --agent'
[[ "$group_run" == "$run_id" ]] || die 'refusing deletion: run tag does not match --run-id'
[[ "$group_expires" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
	die 'refusing deletion: expires tag is missing or malformed'
[[ "$group_owner" == "$OWNER_TAG" ]] || die 'refusing deletion: owner tag is not codex'

mismatched_query="[?tags.purpose != '$PURPOSE_TAG' || tags.agent != '$agent' || tags.run != '$run_id' || tags.expires != '$group_expires' || tags.owner != '$OWNER_TAG'] | length(@)"
mismatched_count=$(az_cli resource list --resource-group "$resource_group" \
	--query "$mismatched_query" --output tsv)
[[ "$mismatched_count" == "0" ]] ||
	die "refusing deletion: $mismatched_count listed resources do not have the exact benchmark tag set"

resource_count=$(az_cli resource list --resource-group "$resource_group" --query 'length(@)' --output tsv)
printf 'Validated exact benchmark group %s with %s listed resources; tags expire %s.\n' \
	"$resource_group" "$resource_count" "$group_expires"
if [[ "$validate_only" == true ]]; then
	printf 'Validation only; no Azure resources were deleted.\n'
	exit 0
fi

[[ "$confirmed" == true ]] || die 'deletion requires --yes after the exact target has been selected'
az_cli "${delete_args[@]}"

if [[ "$no_wait" == true ]]; then
	printf 'Submitted deletion for exact resource group %s.\n' "$resource_group"
	exit 0
fi

group_exists=$(az_cli group exists --name "$resource_group" --output tsv)
[[ "$group_exists" == "false" ]] || die "Azure returned from deletion but the group still exists: $resource_group"
printf 'Deleted exact resource group %s. This removes all disposable resources in that group.\n' "$resource_group"
