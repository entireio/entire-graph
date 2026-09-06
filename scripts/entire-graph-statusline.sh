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
#   .session_id             one half of the render-cache key; a digest of the transcript path
#                           is the other half, because session_id is absent in some invocations
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
# Keyed on the session id AND a digest of the transcript path — so two sessions can never share
# an entry — and stamped with the transcript's size+mtime, so an entry invalidates the moment the
# session advances and there is no staleness window to reason about.
#
# A cache MISS serves the previous line and recomputes in the background (stale-while-
# revalidate). A 50 MB orchestrator transcript takes ~450 ms to re-scan, which is far too long
# to hold up a keystroke; the badge is then at most one render behind, which for a
# slowly-accumulating counter is invisible. Only the very first render of a session — when
# there is nothing to serve — computes in line.
# An entry is stored as "<config>\t<stamp>\t<line>":
#   config — everything that changes WHAT is rendered (scope, window, colour, repo), behind a
#            format version that is bumped whenever the badge layout or the key scheme changes,
#            so lines written by an older script are never served. A mismatch here means the
#            cached line answers a different question, so it must never be shown.
#   stamp  — the transcript's size+mtime. A mismatch here means only that the session moved on,
#            which is exactly when serving the previous line is safe and useful.
#
# Eviction policy: last WRITE wins, not last read. An entry is rewritten on every render where
# the transcript advanced, so "untouched for a week" means the session itself has not advanced
# for a week. A session held open but idle that long loses its entry and pays one in-line
# recompute; refreshing the mtime on every cache HIT would instead put a fork on the hottest
# path in this script — one per keystroke — which costs more than the recompute it avoids.
CACHE_DIR=
CACHE_DIR_OK=
CACHE_FILE=
CACHE_CONFIG=
STAMP=
if [ "${ENTIRE_GRAPH_STATUSLINE_CACHE:-1}" != "0" ]; then
	# size-mtime, from whichever stat this platform has. The GNU form is tried FIRST and the
	# result is VALIDATED, because the obvious `bsd || gnu` one-liner is broken on Linux:
	# GNU stat reads -f as --file-system, so `stat -f '%z-%m' file` treats the format as a
	# FILE name, prints multi-line filesystem info for the real file, and only THEN exits
	# non-zero. Inside $( ), the stdout of a failing first command is still captured, so the
	# fallback's correct value arrived appended to a block of "Namelen / Type: ext2/ext3 /
	# Block size" noise. A stamp like that never equals the cached one, so every render missed
	# the cache and re-scanned the transcript (CI: "cache collapses 3 renders to 1 scan",
	# want 1, got 3), and the garbage also reached the badge value itself.
	#
	# BSD stat has no -c, so on macOS the GNU probe fails cleanly with no stdout and the BSD
	# form runs. The case guard is the real defence: anything that is not digits-dash-digits
	# is discarded, so no future stat flavour can leak a multi-line value into the key.
	STAMP=$(stat -c '%s-%Y' "$TRANSCRIPT" 2>/dev/null) || STAMP=
	case $STAMP in
	'' | *[!0-9-]*)
		STAMP=$(stat -f '%z-%m' "$TRANSCRIPT" 2>/dev/null) || STAMP=
		;;
	esac
	case $STAMP in
	'' | *[!0-9-]* | -* | *- | *-*-*)
		STAMP=
		;;
	esac
	if [ -n "$STAMP" ]; then
		# Namespaced per uid: $TMPDIR is shared on some systems, and a cache directory another
		# user can write is a cache another user can dictate the contents of.
		CACHE_DIR=${TMPDIR:-/tmp}/entire-graph-statusline-$(id -u 2>/dev/null || printf 0)
		if [ -L "$CACHE_DIR" ]; then
			# Every read below, and mkdir -p, follow a symlinked DIRECTORY. A file planted in
			# the target is then a regular file: it passes the per-file [ ! -L ] check and its
			# bytes reach the terminal verbatim, terminal escapes included. Refuse it outright —
			# no cache for this render.
			CACHE_DIR=
		else
			# The key must identify the TRANSCRIPT, not just the session id. session_id is
			# absent in some invocations (field() yields "-"), can repeat across worktrees, and
			# is squashed by the sanitising below — every 1-char non-alnum id becomes "_", "a/b"
			# and "a:b" both become "a_b", and anything past 40 chars is cut. Colliding keys
			# overwrite each other's badge on every render, so the digest is ALWAYS appended,
			# not only in the degenerate case.
			SAFE=$(printf '%s' "$SESSION" | tr -c 'A-Za-z0-9._-' '_' | cut -c1-40)
			# cksum is POSIX, so this needs no sha/md5 binary. BOTH of its fields are used —
			# the CRC32 and the byte count — because CRC32 alone collides on ~1.2% of 10k paths.
			DIGEST=$(printf '%s' "$TRANSCRIPT" | cksum 2>/dev/null | awk '{ print $1 "-" $2; exit }')
			case $DIGEST in
			'' | '-' | *[!0-9-]*)
				# No usable cksum. A constant fallback ("0") would put every transcript back
				# onto one key, which is the exact bug this digest exists to fix, so derive it
				# from the path itself instead.
				DIGEST=$(printf '%s' "$TRANSCRIPT" | tr -c 'A-Za-z0-9' '_' | tail -c 40)
				;;
			esac
			CACHE_FILE=$CACHE_DIR/$SAFE-$DIGEST.line
			# v3: the key scheme changed, so entries written by an older script must not be
			# served. Sanitised in one pass because $SCOPE, $SINCE and $REPO all reach a
			# TAB-delimited record, and a tab or newline in any of them shifts the field split.
			CACHE_CONFIG=$(printf 'v3 %s %s color%s %s' \
				"$SCOPE" "$SINCE" "${NO_COLOR:+-off}" "$REPO" | tr '\t\n' '__')
		fi
	fi
fi

# Create $CACHE_DIR, private to this user, refusing a symlinked directory AND any directory this
# user does not own. Any failure means "no cache" — never an error on the status line.
#
# The gate is an OWNERSHIP TEST, not "chmod exited 0". $TMPDIR is usually unset, so the path is
# predictable (/tmp/entire-graph-statusline-<uid>) and /tmp is world-writable and cleared on boot:
# another user can win the create race and then own the directory this script reads its badge out
# of. Per-uid naming does not help — the victim's uid is in the name — and the symlink checks do not
# help either, because a real directory is not a symlink. Requiring `chmod 700` to succeed is NOT
# sufficient on its own, which was the hole this gate had:
#
#   * chmod(1) ELIDES the chmod(2) call when the mode already matches, so a foreign directory that
#     is already at 0700 exits 0 and gets adopted (asserted on the platform by the test suite).
#     Mode 0700 does not make such a directory unreadable to its victim either: a macOS ACL
#     (`chmod +a "<victim> allow list,search,read,add_file"`) is evaluated independently of the
#     mode bits.
#   * For a uid-0 victim chmod succeeds on EVERY directory, so the check degenerates to a no-op.
#
# `[ -O "$CACHE_DIR" ]` compares st_uid with the effective uid, which is the actual question; it is
# also a shell builtin, so it costs no fork, and it is what refuses an attacker-owned directory when
# the victim is root. The chmod stays behind it, still required, but now only as mode REPAIR on a
# directory we already know is ours (one created by an older version of this script under a loose
# umask). Without this gate a planted record's bytes reach the terminal verbatim (verified:
# \033[2J\033]0;OWNED\007 clears the screen and rewrites the window title), and only the guessable
# CONFIG field has to match, not the stamp.
#
# Memoised on success so a render pays at most one chmod fork, not one per call site. The memo is
# set only after BOTH checks pass: setting it any earlier would hand a REFUSED directory to the next
# caller — store() — as though it had been approved.
#
# `mkdir -m 700` rather than `mkdir -p` + chmod: -p publishes the directory at the umask mode first,
# so under `umask 000` there is a window in which it is drwxrwxrwx.
cache_dir_ok() {
	[ -n "$CACHE_DIR" ] || return 1
	[ "$CACHE_DIR_OK" = 1 ] && return 0
	[ ! -L "$CACHE_DIR" ] || return 1
	if [ ! -d "$CACHE_DIR" ]; then
		mkdir -m 700 -p "$CACHE_DIR" 2>/dev/null || return 1
		[ ! -L "$CACHE_DIR" ] || return 1
	fi
	[ -O "$CACHE_DIR" ] || return 1
	chmod 700 "$CACHE_DIR" 2>/dev/null || return 1
	CACHE_DIR_OK=1
	return 0
}

# Drop the garbage this script leaks: week-old lines, tmp files abandoned by a killed write, and
# lock DIRECTORIES orphaned by a killed refresh (find -delete cannot remove a directory, so they
# would otherwise survive forever and freeze the badge). Throttled behind a sentinel — a full
# scan of the directory costs 2.84 ms at 200 entries and 21.58 ms at 20000, which is not a price
# to pay on every store.
prune() {
	[ -n "$CACHE_DIR" ] || return 0
	sentinel=$CACHE_DIR/.pruned
	if [ -f "$sentinel" ] && [ ! -L "$sentinel" ]; then
		[ -z "$(find "$sentinel" -maxdepth 0 -mmin +1440 2>/dev/null)" ] && return 0
	elif [ -e "$sentinel" ] || [ -L "$sentinel" ]; then
		# Anything but a regular file here — a directory (empty or NOT), a symlink, a fifo — would
		# make the truncation below fail on every store, disabling prune permanently and letting the cache
		# grow forever. `rm -rf` clears every one of those shapes; `rmdir` covered only EMPTY
		# directories, so a planted directory with anything inside it wedged prune for good. On a
		# symlink `rm -rf` still removes the link, never what it points at.
		rm -rf "$sentinel" 2>/dev/null
	fi
	# `true >file`, never `: >file`: ":" is a POSIX SPECIAL built-in, and a redirection error on a
	# special built-in terminates a non-interactive shell outright (XCU 2.8.1) — dash does exactly
	# that, exiting 2 mid-store, and /bin/sh IS dash on most Linux distributions. "true" is a regular
	# built-in, so a failed redirection merely fails the command and the self-heal below can run.
	if ! true >"$sentinel" 2>/dev/null; then
		# The shape check above cannot catch an unwritable REGULAR file (mode 444, left behind by a
		# killed run or by an older uid): [ -f ] is true, so the throttle arm is taken, and the write
		# then fails with EACCES on every store — prune wedged just as permanently. Replace the file
		# once rather than wedge; give up quietly if even that fails.
		rm -f "$sentinel" 2>/dev/null
		true >"$sentinel" 2>/dev/null || return 0
	fi
	find "$CACHE_DIR" -maxdepth 1 -type f -name '*.line' -mtime +7 -delete 2>/dev/null
	# Abandoned "<key>.line.<pid>" writes. Two minutes is orders of magnitude longer than a
	# write takes, so an in-flight tmp file is never in range.
	find "$CACHE_DIR" -maxdepth 1 -type f -name '*.line.*' -mmin +2 -delete 2>/dev/null
	# Orphaned "<key>.line.lock" directories, on the same 2-minute deadline as the lock reaper
	# below. ACCEPTED BEHAVIOUR, not an oversight: a refresh that itself runs longer than two
	# minutes has its own lock reaped and a second refresh may then start concurrently. Both write
	# via "<tmp> then mv -f", which is atomic, so the outcome is last-write-wins on a
	# monotonically-growing counter — never a torn entry — and the deliberate alternative (probing
	# whether the holder is still alive) would need the pid in the lock plus a kill(0) per render,
	# which costs more than the duplicated scan it would prevent.
	find "$CACHE_DIR" -maxdepth 1 -type d -name '*.line.lock' -mmin +2 -exec rmdir {} \; 2>/dev/null
	return 0
}

store() { # store <line>
	[ -n "$CACHE_FILE" ] || return 0
	cache_dir_ok || return 0
	tmp=$CACHE_FILE.$$
	if printf '%s\t%s\t%s\n' "$CACHE_CONFIG" "$STAMP" "$1" >"$tmp" 2>/dev/null; then
		mv -f "$tmp" "$CACHE_FILE" 2>/dev/null || rm -f "$tmp" 2>/dev/null
	else
		rm -f "$tmp" 2>/dev/null
	fi
	# After the write, never before: the entry this render paid for must not wait on maintenance.
	prune
}

# cache_dir_ok comes BEFORE the file tests on purpose: it is the ownership gate, so it has to run
# before anything inside the directory is read, not only before a write.
if [ -n "$CACHE_FILE" ] && cache_dir_ok && [ -f "$CACHE_FILE" ] && [ ! -L "$CACHE_FILE" ]; then
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
			# Reap a lock orphaned by a killed refresh so the badge cannot freeze forever. Same
			# accepted trade as in prune(): a refresh slower than two minutes can be unlocked
			# underneath itself, and the worst case is two concurrent scans whose atomic `mv -f`
			# resolves to last-write-wins.
			find "$LOCK" -maxdepth 0 -mmin +2 -exec rmdir {} \; 2>/dev/null
		fi
		# cache_dir_ok here is defence in depth, not a second gate: this arm is only reachable
		# because the gate at the top of this block already passed, and the result is memoised, so
		# the call is a builtin no-op. It stays so that a future edit which reorders this block
		# cannot create a lock inside a directory that was never vetted.
		if cache_dir_ok && mkdir "$LOCK" 2>/dev/null; then
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
# Print BEFORE storing: this is the first render of a session, already paying the full in-line
# scan, and store() may run the prune. The badge must not wait on cache maintenance.
printf '%s\n' "$LINE"
store "$LINE"
