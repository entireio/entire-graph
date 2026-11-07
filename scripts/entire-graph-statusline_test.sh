#!/bin/sh
# Tests for scripts/entire-graph-statusline.sh, the compatibility shim for `entire graph statusline`.
#
#   sh scripts/entire-graph-statusline_test.sh
#
# The rendering itself is tested in Go (internal/cli/statusline_test.go). This pins only what the
# shim adds: which binary runs, in which order, with the status JSON and the arguments unchanged,
# and that finding none prints nothing.

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

# A fake that prints its label, its arguments and its stdin with shell built-ins only, so each
# case's PATH can hold nothing but the fakes it wants found: a real `entire` elsewhere on PATH
# would otherwise win.
fake() { # fake <path> <label>
	mkdir -p "$(dirname -- "$1")"
	cat >"$1" <<FAKE
#!/bin/sh
printf '%s|' '$2' "\$@"
while IFS= read -r line || [ -n "\$line" ]; do printf '%s' "\$line"; done
FAKE
	chmod +x "$1"
}
fake "$WORK/entire/entire" entire
fake "$WORK/graph/entire-graph" entire-graph
fake "$WORK/override/graph-dev" dev
mkdir -p "$WORK/empty"

OUT=$(printf '{"session_id":"s1"}' | ENTIRE_GRAPH_BIN= PATH="$WORK/entire" "$SH" "$SCRIPT")
check 'runs entire graph statusline with stdin unchanged' 'entire|graph|statusline|{"session_id":"s1"}' "$OUT"

# Finding #8: the PATH fallback dropped the shim's own arguments.
OUT=$(printf '{"session_id":"s2"}' | ENTIRE_GRAPH_BIN= PATH="$WORK/entire" "$SH" "$SCRIPT" --cache-dir /tmp/c)
check 'passes the arguments through' 'entire|graph|statusline|--cache-dir|/tmp/c|{"session_id":"s2"}' "$OUT"

# ENTIRE_GRAPH_BIN runs the binary without Entire's ENTIRE_PLUGIN_DATA_DIR, so it would read another
# cache than `entire graph index` writes: entire wins when both are there.
OUT=$(printf '{"session_id":"s3"}' | ENTIRE_GRAPH_BIN="$WORK/override/graph-dev" PATH="$WORK/entire:$WORK/graph" "$SH" "$SCRIPT")
check 'entire outranks ENTIRE_GRAPH_BIN' 'entire|graph|statusline|{"session_id":"s3"}' "$OUT"

OUT=$(printf '{"session_id":"s4"}' | ENTIRE_GRAPH_BIN="$WORK/override/graph-dev" PATH="$WORK/graph" "$SH" "$SCRIPT")
check 'without entire, ENTIRE_GRAPH_BIN outranks entire-graph' 'dev|statusline|{"session_id":"s4"}' "$OUT"

OUT=$(printf '{"session_id":"s5"}' | ENTIRE_GRAPH_BIN= PATH="$WORK/graph" "$SH" "$SCRIPT" --cache-dir /tmp/c)
check 'then entire-graph on PATH runs as entire-graph statusline' 'entire-graph|statusline|--cache-dir|/tmp/c|{"session_id":"s5"}' "$OUT"

OUT=$(printf '{}' | ENTIRE_GRAPH_BIN= PATH="$WORK/empty" "$SH" "$SCRIPT" 2>&1)
STATUS=$?
check 'nothing found prints nothing' '' "$OUT"
check 'nothing found exits 0' 0 "$STATUS"

[ "$FAIL" -eq 0 ]
