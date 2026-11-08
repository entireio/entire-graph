#!/usr/bin/env bash
# tui-census.sh — capture every command's human-mode output for an eyeball pass.
#
# Adapted from entire-brain's scripts/tui-census.sh. Graph's CLI is hand-rolled
# dispatch (no cobra __complete), so commands are enumerated from the root help
# listing instead of the completion tree. Each safe command runs in a small
# scratch repo under a fake TTY so colors are captured, one file per command:
#
#   scripts/tui-census.sh -b /path/to/old/entire-graph before
#   scripts/tui-census.sh after
#   # then: for f in .context/tui-census/after/*.txt; do less -R "$f"; done
#
# The scratch repo lives under the output root and is shared between runs, so
# before/after differ only by rendering. The index cache is isolated via
# ENTIRE_PLUGIN_DATA_DIR; the provider itself is no-egress by contract, so no
# network toggle is needed. Commands that are mutating outside the scratch repo
# or interactive are not executed; their file records the skip reason plus
# --help output. A command missing from the table below is flagged UNMAPPED so
# new commands surface in the census instead of silently vanishing.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_ROOT="$ROOT/.context/tui-census"
BIN=""
LABEL="current"
TIMEOUT_SECS=90

while [ $# -gt 0 ]; do
  case "$1" in
    -b) BIN="$2"; shift 2 ;;
    -o) OUT_ROOT="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) LABEL="$1"; shift ;;
  esac
done

CAPTURE_DIR="$OUT_ROOT/$LABEL"
SCRATCH="$OUT_ROOT/scratch"
PLUGIN_DATA="$OUT_ROOT/plugin-data"
SESSIONS_DIR="$OUT_ROOT/sessions"
mkdir -p "$CAPTURE_DIR" "$PLUGIN_DATA" "$SESSIONS_DIR"

if [ -z "$BIN" ]; then
  BIN="$OUT_ROOT/entire-graph-$LABEL"
  echo "building $BIN"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/entire-graph)
fi

# Environment for every invocation: colors on, isolated cache.
census_env() {
  env -u NO_COLOR -u ENTIRE_GRAPH_FORCE_COLOR -u FORCE_COLOR \
    TERM=xterm-256color \
    ENTIRE_PLUGIN_DATA_DIR="$PLUGIN_DATA" \
    "$@"
}

# ---------------------------------------------------------------- scratch repo
if [ ! -f "$SCRATCH/.census-ready" ]; then
  echo "setting up scratch repo at $SCRATCH"
  rm -rf "$SCRATCH"
  mkdir -p "$SCRATCH"
  (
    cd "$SCRATCH"
    git init -q -b main
    git config user.email census@example.invalid
    git config user.name "TUI Census"
    cat > main.go <<'GO'
package main

import "fmt"

func greet(name string) string { return "hello, " + name }

func main() { fmt.Println(greet("census")) }
GO
    git add main.go
    git commit -qm "scratch: seed greeter"
    cat >> main.go <<'GO'

func shout(name string) string { return greet(name) + "!" }
GO
    git add main.go
    git commit -qm "scratch: add shout so diff/commit have entity changes"
  )
  touch "$SCRATCH/.census-ready"
fi

# ------------------------------------------------------------- command census
# run:<args>  — execute with these args (empty string means no args)
# skip:<why>  — do not execute; record the reason and --help instead
cmd_plan() {
  case "$1" in
    help|version|doctor|capabilities|health|index|store|agent-guide) echo "run:" ;;
    diff|analyze) echo "run:--base HEAD~1 --head HEAD" ;;
    commit)       echo "run:HEAD" ;;
    checkpoint)   echo "run:HEAD" ;;  # read-only; scratch has no trailer, captures the error surface
    query)        echo "run:--query 'greet by name' --format text" ;;
    def)          echo "run:greet --format text" ;;
    explain)      echo "run:--format text" ;;  # stdin is starved: captures the no-input surface
    neighbors)    echo "run:--symbol greet --format text" ;;
    impact)       echo "run:--symbol greet --format text" ;;
    verify)       echo "run:" ;;
    context)      echo "run:--task 'greet by name'" ;;
    snapshot|symbols|edges) echo "run:" ;;
    stats)        echo "run:--sessions-dir '$SESSIONS_DIR'" ;;
    setup)        echo "run:--no-claude-hook" ;;  # writes only inside the scratch repo
    hook)         echo "skip:Claude Code hook plumbing; expects hook JSON on stdin" ;;
    snapshot-query) echo "skip:needs a pregenerated compact snapshot (--input); output is machine NDJSON" ;;
    *) echo "unmapped:" ;;
  esac
}

# Graph has no completion tree; the root help listing (two-space-indented
# command rows inside the group sections) is the command inventory. Command
# rows pad the name to a shared column, so at least two spaces follow the
# name — which excludes the one-space "entire graph <command>" usage line.
list_commands() {
  census_env "$BIN" help | awk '/^  [a-z][a-z-]*  +/ {print $1}'
}

# Capture one command under a fake TTY so color/caps paths are exercised.
capture() {
  local outfile="$1"; shift
  (
    cd "$SCRATCH"
    # script(1) forwards its stdin to the pty; starve it or it will eat the
    # caller's command list.
    census_env script -q "$outfile.raw" "$ROOT/scripts/tui-census-run.sh" "$TIMEOUT_SECS" "$@" >/dev/null 2>&1 < /dev/null || true
  )
  # script(1) records CRLF, any terminal-capability probes (OSC 11 background,
  # CSI 6n cursor) and the EOT from the starved stdin. Strip those; keep the
  # SGR color sequences, they are the census.
  LC_ALL=C sed -e $'s/\\^D\x08\x08//g' -e $'s/\x1b\\]11;?\x1b\\\\//g' -e $'s/\x1b\\]11;?\x07//g' -e $'s/\x1b\\[6n//g' "$outfile.raw" | tr -d $'\r\x04' >> "$outfile"
  rm -f "$outfile.raw"
}

COMMANDS_FILE="$CAPTURE_DIR/commands.txt"
list_commands | sort > "$COMMANDS_FILE"
echo "census: $(wc -l < "$COMMANDS_FILE" | tr -d ' ') commands -> $CAPTURE_DIR"

while IFS= read -r cmd; do
  slug="$(echo "$cmd" | tr ' ' '-')"
  outfile="$CAPTURE_DIR/$slug.txt"
  plan="$(cmd_plan "$cmd")"
  kind="${plan%%:*}"
  args="${plan#*:}"
  case "$kind" in
    run)
      printf '$ entire graph %s %s\n\n' "$cmd" "$args" > "$outfile"
      # The table quotes multi-word arguments; eval re-splits them faithfully.
      eval "set -- $args"
      # shellcheck disable=SC2086
      capture "$outfile" "$BIN" $cmd "$@"
      ;;
    skip)
      printf 'skipped: %s\n\n$ entire graph %s --help\n\n' "$args" "$cmd" > "$outfile"
      # shellcheck disable=SC2086
      capture "$outfile" "$BIN" $cmd --help
      ;;
    unmapped)
      printf 'UNMAPPED: add %q to cmd_plan in scripts/tui-census.sh\n\n$ entire graph %s --help\n\n' "$cmd" "$cmd" > "$outfile"
      # shellcheck disable=SC2086
      capture "$outfile" "$BIN" $cmd --help
      ;;
  esac
  echo "  $slug"
done < "$COMMANDS_FILE"

echo "done: page through with  for f in $CAPTURE_DIR/*.txt; do less -R \"\$f\"; done"
