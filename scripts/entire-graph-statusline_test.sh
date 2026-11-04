#!/bin/sh
# Tests for scripts/entire-graph-statusline.sh, the compatibility shim for `entire graph statusline`.
#
#   sh scripts/entire-graph-statusline_test.sh
#
# The rendering itself is tested in Go (internal/cli/statusline_test.go). This pins only what the
# shim adds: the status JSON reaches the subcommand unchanged, and a missing `entire` prints nothing.

set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT=$ROOT/scripts/entire-graph-statusline.sh
SH=$(command -v sh)
WORK=$(mktemp -d) || exit 1
trap 'rm -rf "$WORK"' EXIT INT TERM

FAIL=0
check() { # check <name> <want> <got>
	if [ "$2" = "$3" ]; then
		printf 'ok    %s\n' "$1"
	else
		FAIL=$((FAIL + 1))
		printf 'FAIL  %s\n        want %s\n        got  %s\n' "$1" "$2" "$3"
	fi
}

# A fake `entire` that echoes its arguments and its stdin.
mkdir -p "$WORK/bin" "$WORK/empty"
cat >"$WORK/bin/entire" <<'FAKE'
#!/bin/sh
printf '%s|' "$@"
cat
FAKE
chmod +x "$WORK/bin/entire"

OUT=$(printf '{"session_id":"s1"}' | PATH="$WORK/bin:$PATH" "$SH" "$SCRIPT")
check 'runs entire graph statusline with stdin unchanged' 'graph|statusline|{"session_id":"s1"}' "$OUT"

OUT=$(printf '{}' | PATH="$WORK/empty" "$SH" "$SCRIPT" 2>&1)
STATUS=$?
check 'no entire on PATH prints nothing' '' "$OUT"
check 'no entire on PATH exits 0' 0 "$STATUS"

[ "$FAIL" -eq 0 ]
