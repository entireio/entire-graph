#!/bin/sh
# entire-graph — Claude Code status line, compatibility shim.
#
# The status line is the `entire graph statusline` subcommand (internal/cli/statusline.go).
# `entire graph setup` installs it in .claude/settings.local.json when you choose it (at the
# prompt, or with --statusline), running the subcommand directly. This script remains only so a
# settings file that still names it keeps working:
#
#   "statusLine": { "type": "command", "command": "sh /path/to/scripts/entire-graph-statusline.sh" }
#
# Change that command to `entire graph statusline`.
#
# Claude Code passes the status JSON on stdin; exec hands it, and any arguments, to the first of:
#
#   1. `entire graph statusline`, when `entire` is on PATH. Entire sets ENTIRE_PLUGIN_DATA_DIR, so
#      the line reads the cache that `entire graph index` and `query --head` write.
#   2. `$ENTIRE_GRAPH_BIN statusline`, when ENTIRE_GRAPH_BIN names an executable.
#   3. `entire-graph statusline`, when `entire-graph` is on PATH.
#
# 2 and 3 run the binary without Entire, so it uses its default cache, the one a bare
# `entire-graph index` writes, unless the arguments pass --cache-dir. With none of the three this
# prints nothing and exits 0: a status line must never surface an error.

if command -v entire >/dev/null 2>&1; then
	exec entire graph statusline "$@"
fi
[ -x "${ENTIRE_GRAPH_BIN:-}" ] && exec "$ENTIRE_GRAPH_BIN" statusline "$@"
command -v entire-graph >/dev/null 2>&1 || exit 0
exec entire-graph statusline "$@"
