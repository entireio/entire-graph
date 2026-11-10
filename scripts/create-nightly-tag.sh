#!/usr/bin/env bash
set -euo pipefail

# Calculate the next nightly tag from the latest stable tag and HEAD.
# Prints the tag to stdout. Exits 2 (not an error) when HEAD already has one.
#
# Tag format: v<major>.<minor>.<patch+1>-nightly.<YYYYMMDDhhmm>.<short-commit>
# Usage: scripts/create-nightly-tag.sh

SHORT_COMMIT=$(git rev-parse --short HEAD)

if git tag -l "v*-nightly.*.${SHORT_COMMIT}" | grep -q .; then
  echo "Nightly tag already exists for commit ${SHORT_COMMIT}, skipping." >&2
  exit 2
fi

LATEST_STABLE=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' 2>/dev/null || true)
if [ -z "$LATEST_STABLE" ]; then
  echo "::error::No stable version tag found" >&2
  exit 1
fi

MAJOR=$(echo "$LATEST_STABLE" | sed 's/^v//' | cut -d. -f1)
MINOR=$(echo "$LATEST_STABLE" | sed 's/^v//' | cut -d. -f2)
PATCH=$(echo "$LATEST_STABLE" | sed 's/^v//' | cut -d. -f3)
NEXT_PATCH=$((PATCH + 1))

DATE=$(TZ=UTC0 date +%Y%m%d%H%M)
TAG="v${MAJOR}.${MINOR}.${NEXT_PATCH}-nightly.${DATE}.${SHORT_COMMIT}"

if git rev-parse "${TAG}" >/dev/null 2>&1; then
  echo "Tag ${TAG} already exists, skipping." >&2
  exit 2
fi

echo "${TAG}"
