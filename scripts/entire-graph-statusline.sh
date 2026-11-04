#!/bin/sh
# entire-graph — Claude Code status line, compatibility shim.
#
# The status line is the `entire graph statusline` subcommand (internal/cli/statusline.go).
# `entire graph setup` installs it in .claude/settings.local.json and the plugin manifest runs it
# directly. This script remains only so a settings file that still names it keeps working:
#
#   "statusLine": { "type": "command", "command": "sh /path/to/scripts/entire-graph-statusline.sh" }
#
# Change that command to `entire graph statusline`.
#
# Claude Code passes the status JSON on stdin; exec hands it to the subcommand unchanged. Without
# an `entire` on PATH this prints nothing and exits 0: a status line must never surface an error.

command -v entire >/dev/null 2>&1 || exit 0
exec entire graph statusline
