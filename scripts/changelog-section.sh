#!/bin/sh
# Print the CHANGELOG.md body for one version, for use as GitHub release notes.
#
#   scripts/changelog-section.sh v0.5.0
#
# Exits 1 without output when CHANGELOG.md has no section for that version, so
# a caller can fall back to auto-generated notes. Nightly prereleases are
# expected to take that path.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
changelog=${CHANGELOG_FILE:-$repo_root/CHANGELOG.md}

version=${1:-}
if [ -z "$version" ]; then
	printf 'usage: %s <version>\n' "$0" >&2
	exit 2
fi
# Accept both `v0.5.0` and `0.5.0`; CHANGELOG headings carry no leading `v`.
version=${version#v}

[ -f "$changelog" ] || exit 1

section=$(awk -v want="$version" '
	# Section headings look like: ## [0.5.0] - 2026-09-20
	/^## \[/ {
		line = $0
		sub(/^## \[/, "", line)
		sub(/\].*$/, "", line)
		if (line == want) { collecting = 1; next }
		if (collecting) { exit }
		next
	}
	# Link reference definitions sit below the last section and are markup, not
	# release notes.
	/^\[[^]]+\]:[ \t]/ { if (collecting) exit; next }
	collecting { print }
' "$changelog")

# Trim leading and trailing blank lines without dropping interior ones.
section=$(printf '%s\n' "$section" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;};/\n$/ba')

[ -n "$section" ] || exit 1
printf '%s\n' "$section"
