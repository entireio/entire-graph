#!/bin/sh
# entire-graph — status line badge for Claude Code.
#
# Renders one line summarising what the code graph bought you in THIS session:
#
#   [GRAPH] ↗ 6.6K saved · 13 search · 3 impact · 1 nbrs · vs 2.6K explore · 1.5M explore tok ·
#   graph-first ✗ · 2% of locates · 0.2% of session
#
# Segment order is fixed; any segment whose value is missing or zero is dropped rather than
# rendered as a zero. Only WORK verbs are named (locate verbs search/neighbors/impact first,
# then diff/analyze/commit/checkpoint/symbols/edges/snapshot/index). The self-reporting meta
# verbs — stats, version, help, doctor, init-agents, agent-guide, capabilities — replace no
# exploration, so they are struck from the verb split AND from the residual "other" count.
#
# The line is held under 150 visible characters. When it would overflow, whole segments are
# dropped from the right — session %, then explore tok, then explore calls — never truncated.
#
# Claude Code invokes a status line command with the session JSON on stdin and takes stdout as
# the badge (ANSI colour is allowed). The fields consumed here:
#
#   .transcript_path        path to this session's JSONL transcript  (required)
#   .workspace.current_dir  repo to resolve the savings counterfactual against
#   .cwd                    fallback for the above
#   .session_id             cache key
#
# Every number comes from `entire graph stats --transcript <path> --format json`, so the
# accounting is exactly internal/cli/stats.go — command-position-only graph verbs against a
# closed verb allowlist, locate verbs (search/neighbors/impact) alone credited, credit =
# top-hit-file size − returned bytes floored at 0, 4 bytes = 1 token. There is no second
# estimator here to drift out of sync.
#
# Enable it in ~/.claude/settings.json:
#   "statusLine": { "type": "command",
#                   "command": "sh /path/to/scripts/entire-graph-statusline.sh" }
#
# Environment:
#   ENTIRE_GRAPH_BIN               explicit path to the entire-graph binary
#   ENTIRE_GRAPH_STATUSLINE_SCOPE  session (default) | project
#                                  project re-scans the whole ~/.claude/projects/<slug>
#                                  directory — hundreds of MB on a busy project, and far too
#                                  slow to render per keystroke. Opt in knowingly.
#   ENTIRE_GRAPH_STATUSLINE_SINCE  window for scope=project (default 30d)
#   ENTIRE_GRAPH_STATUSLINE_CACHE  0 disables the render cache (always recompute, in-line)
#   NO_COLOR                       set to any value to drop ANSI escapes
#
# Prints NOTHING and exits 0 on every failure: no binary, no transcript, unparseable JSON,
# no sessions. A status line must never surface an error or hang.

set -u

# Never let a stray diagnostic from a child process reach the status line.
exec 2>/dev/null

SCOPE=${ENTIRE_GRAPH_STATUSLINE_SCOPE:-session}
SINCE=${ENTIRE_GRAPH_STATUSLINE_SINCE:-30d}

# --- stdin ---------------------------------------------------------------------------------
# One awk pass pulls the three fields out of the session JSON. Always three lines, "-" for a
# field that is absent, so word splitting downstream can never shift the fields.
META=$(
	awk '
		function field(key,   pat, raw) {
			pat = "\"" key "\"[ \t]*:[ \t]*\"[^\"]*\""
			if (!match(blob, pat)) return "-"
			raw = substr(blob, RSTART, RLENGTH)
			sub(/^[^:]*:[ \t]*"/, "", raw)
			sub(/"$/, "", raw)
			if (raw == "") return "-"
			return raw
		}
		{ blob = blob $0 }
		END {
			dir = field("current_dir")
			if (dir == "-") dir = field("cwd")
			print field("transcript_path")
			print dir
			print field("session_id")
		}
	'
) || exit 0

IFS='
'
# shellcheck disable=SC2086 # deliberate: split META on newlines only
set -- $META
unset IFS
[ "$#" -eq 3 ] || exit 0
TRANSCRIPT=$1
REPO=$2
SESSION=$3

[ "$TRANSCRIPT" = "-" ] && exit 0
[ -f "$TRANSCRIPT" ] || exit 0
[ -s "$TRANSCRIPT" ] || exit 0
[ "$REPO" = "-" ] && REPO=$PWD
[ -d "$REPO" ] || REPO=$PWD

# --- binary --------------------------------------------------------------------------------
BIN=
for candidate in \
	"${ENTIRE_GRAPH_BIN:-}" \
	"${CLAUDE_PLUGIN_ROOT:-}${CLAUDE_PLUGIN_ROOT:+/entire-graph}" \
	"${GOBIN:-}${GOBIN:+/entire-graph}" \
	"${HOME}/go/bin/entire-graph"; do
	if [ -n "$candidate" ] && [ -x "$candidate" ]; then
		BIN=$candidate
		break
	fi
done
if [ -z "$BIN" ]; then
	BIN=$(command -v entire-graph) || BIN=
fi
[ -n "$BIN" ] || exit 0

# --- measure + render ----------------------------------------------------------------------
render() {
	if [ "$SCOPE" = "project" ]; then
		report=$("$BIN" stats --repo "$REPO" --sessions-dir "$(dirname "$TRANSCRIPT")" \
			--since "$SINCE" --format json 2>/dev/null) || return 1
	else
		report=$("$BIN" stats --repo "$REPO" --transcript "$TRANSCRIPT" \
			--since all --format json 2>/dev/null) || return 1
	fi
	case $report in
	'{'*) ;;
	*) return 1 ;;
	esac

	color=1
	[ -n "${NO_COLOR:-}" ] && color=0

	printf '%s' "$report" | awk -v color="$color" '
		function number(key,   pat, raw) {
			pat = "\"" key "\"[ \t]*:[ \t]*-?[0-9][0-9.eE+-]*"
			if (!match(blob, pat)) return -1
			raw = substr(blob, RSTART, RLENGTH)
			sub(/^[^:]*:[ \t]*/, "", raw)
			return raw + 0
		}
		# 532 -> "532", 12345 -> "12.3K", 2000000 -> "2M", 2100000 -> "2.1M".
		# NB: no backreference in the sub() — POSIX awk only understands "&", so the unit is
		# appended after trimming rather than captured.
		function human(value,   text, unit) {
			if (value < 1000) return sprintf("%d", value)
			if (value < 1000000) { text = sprintf("%.1f", value / 1000); unit = "K" }
			else { text = sprintf("%.1f", value / 1000000); unit = "M" }
			sub(/\.0$/, "", text)
			return text unit
		}
		function pct(value) {
			if (value >= 10) return sprintf("%d%%", value + 0.5)
			if (value >= 1) return sprintf("%.1f%%", value)
			return sprintf("%.2f%%", value)
		}
		function paint(text, code) {
			if (!color) return text
			return "\033[" code "m" text "\033[0m"
		}
		function sep() { return paint(" \302\267 ", "2") }
		# Verb names are rendered into the terminal, so keep them to the closed lowercase set
		# the CLI dispatches and cap the length: a status line must never echo arbitrary bytes.
		function clean(name) {
			gsub(/[^a-z-]/, "", name)
			return substr(name, 1, 16)
		}
		# Self-reporting verbs: they answer questions ABOUT the graph and replace no
		# exploration, so they must never be named in the badge nor counted as work.
		function isMeta(n) {
			return index(" stats version help doctor init-agents agent-guide capabilities ", " " n " ") > 0
		}
		# Verbs that do work on the codebase. Locate verbs rank first in the split.
		function isWork(n) {
			return index(" search neighbors impact diff analyze commit checkpoint symbols edges snapshot index ", " " n " ") > 0
		}
		function isLocate(n) { return index(" search neighbors impact ", " " n " ") > 0 }
		# Segments are accumulated with their VISIBLE width (colour codes and multi-byte
		# glyphs excluded, counted by the caller) and a drop rank: 0 never drops, 1 drops
		# first. Overflow is handled by dropping whole segments, never by truncating one.
		function addseg(text, width, rank) {
			nseg++
			stext[nseg] = text
			swidth[nseg] = width
			srank[nseg] = rank
			wtotal += width
		}
		# A plain (ASCII) segment preceded by the " \302\267 " separator: 3 visible chars.
		function addplain(text, rank) { addseg(sep() text, 3 + length(text), rank) }
		{ blob = blob $0 }
		END {
			maxw = 150
			sessions = number("sessions")
			graph    = number("graph_calls")
			explore  = number("exploration_calls")
			if (sessions <= 0) exit 0

			label = paint("[GRAPH]", "38;5;45")

			# Verb split. graph_calls_by_verb is already ordered calls-desc by the CLI.
			# Meta verbs are subtracted from the call total outright; names outside the
			# closed set are never rendered but still count as work, so a verb this script
			# has not learned about yet degrades to "other" rather than vanishing.
			nverb = 0
			meta = 0
			if (match(blob, /"graph_calls_by_verb"[ \t]*:[ \t]*\[[^]]*\]/)) {
				verbs = substr(blob, RSTART, RLENGTH)
				while (match(verbs, /"name"[ \t]*:[ \t]*"[a-z-]+"[ \t]*,[ \t]*"calls"[ \t]*:[ \t]*[0-9]+/)) {
					entry = substr(verbs, RSTART, RLENGTH)
					verbs = substr(verbs, RSTART + RLENGTH)
					name = entry
					sub(/^"name"[ \t]*:[ \t]*"/, "", name)
					sub(/".*$/, "", name)
					calls = entry
					sub(/^.*"calls"[ \t]*:[ \t]*/, "", calls)
					calls = calls + 0
					name = clean(name)
					if (name == "" || calls <= 0) continue
					if (isMeta(name)) { meta += calls; continue }
					if (!isWork(name)) continue
					nverb++
					vname[nverb] = name
					vcalls[nverb] = calls
				}
			}
			work = graph - meta

			if (work <= 0) {
				out = label " " paint("no graph calls yet", "2")
				if (explore > 0) out = out sep() paint(human(explore) " explore", "2")
				print out
				exit 0
			}

			saved    = number("estimated_savings_est_tokens")
			savedPct = number("estimated_savings_pct_of_session_tokens")
			expTok   = number("exploration_returned_est_tokens")
			gfHit    = number("graph_first_sessions")
			gfTotal  = number("sessions_with_locate")

			if (saved > 0) {
				text = human(saved)
				# "[GRAPH] " = 8, "\342\206\227 " = 2, " saved" = 6.
				addseg(label " " paint("\342\206\227 " text " saved", "38;5;78"), 16 + length(text), 0)
			} else {
				addseg(label, 7, 0)
			}

			# Locate verbs first, then the remaining work verbs; calls-desc within each.
			shown = 0
			counted = 0
			for (pass = 1; pass <= 2; pass++) {
				for (i = 1; i <= nverb; i++) {
					if (shown >= 3) break
					if (pass == 1 && !isLocate(vname[i])) continue
					if (pass == 2 && isLocate(vname[i])) continue
					disp = vname[i]
					if (disp == "neighbors") disp = "nbrs"
					addplain(vcalls[i] " " disp, 0)
					counted += vcalls[i]
					shown++
				}
			}
			if (shown == 0) addplain(work " graph", 0)
			else if (counted < work) addplain((work - counted) " other", 0)

			# What the graph was measured against: the exploration it did not replace.
			if (explore > 0) addplain("vs " human(explore) " explore", 3)
			if (expTok > 0) addplain(human(expTok) " explore tok", 2)

			# graph-first: did the session OPEN with the graph rather than grep/read. One
			# session is a yes/no; a multi-session scope is a rate, as `entire graph stats`
			# reports it.
			if (gfTotal > 0) {
				if (sessions == 1)
					# "graph-first " = 12 plus the one-glyph tick.
					addseg(sep() "graph-first " (gfHit > 0 ? "\342\234\223" : "\342\234\227"), 16, 0)
				else
					addplain(sprintf("graph-first %d%%", (gfHit * 100 / gfTotal) + 0.5), 0)
			}

			# Share of all locate-ish calls that went to the graph instead of grep/read.
			# A share that rounds to 0% would read as "no graph calls", which is false
			# whenever we got this far — say "<1%" instead.
			locates = graph + (explore > 0 ? explore : 0)
			if (locates > 0) {
				share = graph * 100 / locates
				if (share < 0.5) addplain("<1% of locates", 0)
				else addplain(sprintf("%d%% of locates", share + 0.5), 0)
			}

			# Below 0.005% every format rounds to "0.00%", which is a zero — drop it.
			if (savedPct >= 0.005) addplain(pct(savedPct) " of session", 1)

			for (rank = 1; rank <= 3 && wtotal > maxw; rank++) {
				for (i = 1; i <= nseg; i++) {
					if (srank[i] != rank || gone[i]) continue
					gone[i] = 1
					wtotal -= swidth[i]
				}
			}

			out = ""
			for (i = 1; i <= nseg; i++) if (!gone[i]) out = out stext[i]
			print out
		}
	'
}

# --- cache ---------------------------------------------------------------------------------
# Keyed on the transcript's size+mtime, so it invalidates the moment the session advances and
# there is no staleness window to reason about.
#
# A cache MISS serves the previous line and recomputes in the background (stale-while-
# revalidate). A 50 MB orchestrator transcript takes ~450 ms to re-scan, which is far too long
# to hold up a keystroke; the badge is then at most one render behind, which for a
# slowly-accumulating counter is invisible. Only the very first render of a session — when
# there is nothing to serve — computes in line.
# The key has two halves, stored as "<config>\t<stamp>\t<line>":
#   config — everything that changes WHAT is rendered (scope, window, colour, repo), behind a
#            format version that is bumped whenever the badge layout changes so lines written by
#            an older script are never served. A mismatch here means the cached line answers a
#            different question, so it must never be shown.
#   stamp  — the transcript's size+mtime. A mismatch here means only that the session moved on,
#            which is exactly when serving the previous line is safe and useful.
CACHE_FILE=
CACHE_CONFIG=
STAMP=
if [ "${ENTIRE_GRAPH_STATUSLINE_CACHE:-1}" != "0" ]; then
	STAMP=$(stat -f '%z-%m' "$TRANSCRIPT" 2>/dev/null || stat -c '%s-%Y' "$TRANSCRIPT" 2>/dev/null) || STAMP=
	if [ -n "$STAMP" ]; then
		CACHE_DIR=${TMPDIR:-/tmp}/entire-graph-statusline
		# The key must identify the TRANSCRIPT, not just the session id. session_id is absent in
		# some invocations and then degenerates to "-", so keying on it alone made every such
		# session share one cache file and overwrite each other's badge (observed in the wild:
		# a single "-.line" plus 1-char keys). Appending a digest of the transcript path makes the
		# key unique even when session_id is missing, empty or truncated. cksum is POSIX, so this
		# needs no sha/md5 binary.
		SAFE=$(printf '%s' "$SESSION" | tr -c 'A-Za-z0-9._-' '_' | cut -c1-40)
		DIGEST=$(printf '%s' "$TRANSCRIPT" | cksum | awk '{print $1}') || DIGEST=
		[ -n "$DIGEST" ] || DIGEST=0
		CACHE_FILE=$CACHE_DIR/$SAFE-$DIGEST.line
		# v3: cache-key scheme changed, so lines written by an older script must not be served.
		CACHE_CONFIG="v3 $SCOPE $SINCE color${NO_COLOR:+-off} $REPO"
	fi
fi

store() { # store <line>
	[ -n "$CACHE_FILE" ] || return 0
	mkdir -p "$CACHE_DIR" 2>/dev/null || return 0
	# One cache file per transcript accumulates forever otherwise; drop week-old lines.
	find "$CACHE_DIR" -name '*.line' -mtime +7 -delete 2>/dev/null
	tmp=$CACHE_FILE.$$
	if printf '%s\t%s\t%s\n' "$CACHE_CONFIG" "$STAMP" "$1" >"$tmp" 2>/dev/null; then
		mv -f "$tmp" "$CACHE_FILE" 2>/dev/null || rm -f "$tmp" 2>/dev/null
	else
		rm -f "$tmp" 2>/dev/null
	fi
}

if [ -n "$CACHE_FILE" ] && [ -f "$CACHE_FILE" ] && [ ! -L "$CACHE_FILE" ]; then
	CACHED=$(head -c 4096 "$CACHE_FILE" 2>/dev/null)
	CACHED_LINE=${CACHED#*	}    # drop config
	CACHED_LINE=${CACHED_LINE#*	} # drop stamp
	case $CACHED in
	"$CACHE_CONFIG	$STAMP	"*)
		printf '%s\n' "$CACHED_LINE"
		exit 0
		;;
	"$CACHE_CONFIG	"*)
		# Same question, older answer. Serve it now, refresh behind the user's back. mkdir
		# is the atomic test-and-set that stops a burst of renders spawning a herd of scans.
		LOCK=$CACHE_FILE.lock
		if [ -d "$LOCK" ]; then
			# Reap a lock orphaned by a killed refresh so the badge cannot freeze forever.
			find "$LOCK" -maxdepth 0 -mmin +2 -exec rmdir {} \; 2>/dev/null
		fi
		if mkdir -p "$CACHE_DIR" 2>/dev/null && mkdir "$LOCK" 2>/dev/null; then
			(
				line=$(render) && [ -n "$line" ] && store "$line"
				rmdir "$LOCK" 2>/dev/null
			) >/dev/null 2>&1 &
		fi
		printf '%s\n' "$CACHED_LINE"
		exit 0
		;;
	esac
fi

LINE=$(render) || exit 0
[ -n "$LINE" ] || exit 0
store "$LINE"
printf '%s\n' "$LINE"
