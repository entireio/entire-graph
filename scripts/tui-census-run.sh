#!/usr/bin/env bash
# Helper for tui-census.sh: run one command under the fake TTY that script(1)
# provides, with a hard timeout and stdin closed so nothing blocks on input.
set -u
secs="$1"; shift
exec perl -e 'alarm shift; exec @ARGV or die "exec: $!"' "$secs" "$@" < /dev/null
