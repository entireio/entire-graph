#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

if ! command -v entire >/dev/null 2>&1; then
	printf 'entire CLI is required for plugin installation\n' >&2
	exit 1
fi

version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || printf 'dev')}
go build -trimpath -ldflags "-X main.version=$version" -o entire-sem ./cmd/entire-sem
entire plugin install ./entire-sem --force
entire sem version
