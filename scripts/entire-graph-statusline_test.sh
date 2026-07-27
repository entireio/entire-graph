#!/bin/sh
# Tests for scripts/entire-graph-statusline.sh.
#
#   sh scripts/entire-graph-statusline_test.sh
#
# Two layers:
#   * integration — the real entire-graph binary over synthetic transcripts, so the rendered
#     numbers are the ones internal/cli/stats.go actually produces;
#   * renderer — a stub binary emitting canned stats JSON, so formatting, degradation and
#     escape-hygiene edge cases can be asserted exactly.
#
# The binary is taken from $ENTIRE_GRAPH_BIN, else ./entire-graph, else `go build`.

set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT=$ROOT/scripts/entire-graph-statusline.sh
WORK=$(mktemp -d) || exit 1
# chflags is how the foreign-cache-directory case below makes chmod fail; clear it unconditionally
# so an aborted run cannot leave an undeletable directory behind (no-op where chflags does not
# exist).
trap 'chflags -R nouchg "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT INT TERM

PASS=0
FAIL=0

fail() {
	FAIL=$((FAIL + 1))
	printf 'FAIL  %s\n' "$1"
	shift
	for detail in "$@"; do printf '        %s\n' "$detail"; done
}
pass() {
	PASS=$((PASS + 1))
	printf 'ok    %s\n' "$1"
}

# assert_has <name> <needle> <haystack>
assert_has() {
	case $3 in
	*"$2"*) pass "$1" ;;
	*) fail "$1" "want substring: $2" "got: $(printf '%s' "$3" | cat -v)" ;;
	esac
}
# assert_lacks <name> <needle> <haystack>
assert_lacks() {
	case $3 in
	*"$2"*) fail "$1" "unwanted substring: $2" "got: $(printf '%s' "$3" | cat -v)" ;;
	*) pass "$1" ;;
	esac
}
# assert_empty <name> <output>
assert_empty() {
	if [ -z "$2" ]; then pass "$1"; else fail "$1" "want no output" "got: $(printf '%s' "$2" | cat -v)"; fi
}
# assert_eq <name> <want> <got>
assert_eq() {
	if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want: $2" "got:  $3"; fi
}
# assert_present <name> <path>
assert_present() {
	if [ -e "$2" ]; then pass "$1"; else fail "$1" "want present: $2"; fi
}
# assert_absent <name> <path>
assert_absent() {
	if [ -e "$2" ]; then fail "$1" "want gone: $2"; else pass "$1"; fi
}
# assert_regular <name> <path>: exists AND is a regular file AND is not a symlink. [ -e ] alone is
# satisfied by a still-planted directory, which is the exact shape these cases are about.
assert_regular() {
	if [ -f "$2" ] && [ ! -L "$2" ]; then
		pass "$1"
	else
		fail "$1" "want a regular file: $2" "got: $(ls -ld "$2" 2>&1)"
	fi
}
# assert_writable <name> <path>
assert_writable() {
	if [ -w "$2" ]; then pass "$1"; else fail "$1" "want writable: $2" "got: $(ls -ld "$2" 2>&1)"; fi
}
# assert_two_files <name> <path-a> <path-b>: distinct paths, both written.
assert_two_files() {
	if [ "$2" = "$3" ]; then
		fail "$1" "want two distinct cache files" "got one: $2"
	elif [ ! -f "$2" ]; then
		fail "$1" "missing cache file: $2"
	elif [ ! -f "$3" ]; then
		fail "$1" "missing cache file: $3"
	else
		pass "$1"
	fi
}
# Visible width of a NO_COLOR line: bytes minus UTF-8 continuation bytes, i.e. characters.
vwidth() { printf '%s' "$1" | LC_ALL=C tr -d '\200-\277' | LC_ALL=C wc -c | tr -d ' '; }
# assert_within <name> <max> <line>
assert_within() {
	got=$(vwidth "$3")
	if [ "$got" -le "$2" ]; then
		pass "$1"
	else
		fail "$1" "want width <= $2" "got width $got: $(printf '%s' "$3" | cat -v)"
	fi
}

# --- binary under test ----------------------------------------------------------------------
BIN=${ENTIRE_GRAPH_BIN:-}
if [ -z "$BIN" ] && [ -x "$ROOT/entire-graph" ]; then BIN=$ROOT/entire-graph; fi
if [ -z "$BIN" ]; then
	printf 'building entire-graph...\n' >&2
	(cd "$ROOT" && go build -o "$WORK/entire-graph" ./cmd/entire-graph) || {
		printf 'cannot build entire-graph\n' >&2
		exit 1
	}
	BIN=$WORK/entire-graph
fi

# --- fixtures --------------------------------------------------------------------------------
ts='2026-07-24T12:00:00Z'

tool_use() { # tool_use <name> <id> <input-json>
	printf '{"type":"assistant","timestamp":"%s","message":{"role":"assistant","content":[{"type":"tool_use","id":"%s","name":"%s","input":%s}]}}\n' \
		"$ts" "$2" "$1" "$3"
}
tool_result() { # tool_result <id> <text>
	printf '{"type":"user","timestamp":"%s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"%s","content":"%s"}]}}\n' \
		"$ts" "$1" "$2"
}
usage() { # usage <input> <cache_write> <cache_read> <output>
	printf '{"type":"assistant","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":%s,"cache_creation_input_tokens":%s,"cache_read_input_tokens":%s,"output_tokens":%s}}}\n' \
		"$ts" "$1" "$2" "$3" "$4"
}

stdin_json() { # stdin_json <session-id> <transcript> <cwd>
	printf '{"session_id":"%s","transcript_path":"%s","cwd":"%s","workspace":{"current_dir":"%s"},"model":{"id":"m","display_name":"M"},"version":"2.1.219"}' \
		"$1" "$2" "$3" "$3"
}
# The same payload with session_id absent, as Claude Code sends it in some invocations.
stdin_json_nosid() { # stdin_json_nosid <transcript> <cwd>
	printf '{"transcript_path":"%s","cwd":"%s","workspace":{"current_dir":"%s"},"model":{"id":"m","display_name":"M"},"version":"2.1.219"}' \
		"$1" "$2" "$2"
}

# The suite must not inherit the developer's own settings for anything the script reads: with
# SCOPE=project exported, every case scans the real ~/.claude/projects tree and five assertions
# fail with a badge full of real data; with NO_COLOR exported, the three colour cases fail because
# the "coloured by default" default is gone. Every invocation therefore goes through clean_env,
# which clears all four; a case that wants one sets it explicitly (its assignment is appended after
# the -u flags and wins).
clean_env() { # clean_env [env assignments...] <command...>
	env -u ENTIRE_GRAPH_STATUSLINE_SCOPE \
		-u ENTIRE_GRAPH_STATUSLINE_SINCE \
		-u ENTIRE_GRAPH_STATUSLINE_CACHE \
		-u NO_COLOR "$@"
}

# run_json <stdin-json> [env assignments...] -> stdout of the status line
run_json() {
	json=$1
	shift
	printf '%s' "$json" |
		clean_env TMPDIR="$WORK/cache" ENTIRE_GRAPH_BIN="$RUN_BIN" "$@" sh "$SCRIPT"
}
# run <session-id> <transcript> <cwd> [env assignments...] -> stdout of the status line
run() {
	sid=$1
	transcript=$2
	cwd=$3
	shift 3
	run_json "$(stdin_json "$sid" "$transcript" "$cwd")" "$@"
}

# The render-cache path the script computes, mirrored here so a change to the key scheme does not
# have to be paid for in test edits (and so a test can never pin the key to a WEAKER scheme).
# Keep in sync with the CACHE_DIR / SAFE / DIGEST block in the script.
cache_dir() { printf '%s/entire-graph-statusline-%s' "$WORK/cache" "$(id -u)"; }
cache_key() { # cache_key <session-id> <transcript> -> "<safe-session>-<digest>"
	ck_safe=$(printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_' | cut -c1-40)
	ck_digest=$(printf '%s' "$2" | cksum | awk '{ print $1 "-" $2; exit }')
	printf '%s-%s' "$ck_safe" "$ck_digest"
}
cache_file() { # cache_file <session-id> <transcript> -> absolute path
	printf '%s/%s.line' "$(cache_dir)" "$(cache_key "$1" "$2")"
}

RUN_BIN=$BIN
mkdir -p "$WORK/cache"

# A repo whose files give the counterfactual something to measure.
REPO=$WORK/repo
mkdir -p "$REPO"
i=0
while [ "$i" -lt 5 ]; do
	awk 'BEGIN{ while (n++ < 200) printf "package main // filler line\n" }' >"$REPO/file$i.go"
	i=$((i + 1))
done

# --- integration: a graph-first session ------------------------------------------------------
T=$WORK/graphfirst.jsonl
{
	tool_use Bash g1 '{"command":"entire graph search --repo . --query \"login bug\""}'
	tool_result g1 "{\\\"results\\\":[{\\\"file_path\\\":\\\"$REPO/file0.go\\\"}]}"
	tool_use Bash g2 '{"command":"entire graph search --repo . --query \"session token\""}'
	tool_result g2 "{\\\"results\\\":[{\\\"file_path\\\":\\\"$REPO/file1.go\\\"}]}"
	tool_use Bash g3 '{"command":"entire graph impact --repo . --symbol Login"}'
	tool_result g3 "{\\\"results\\\":[{\\\"file_path\\\":\\\"$REPO/file2.go\\\"}]}"
	tool_use Read r1 '{"file_path":"/x/a.go"}'
	tool_result r1 'aaaa'
	tool_use Grep gr1 '{"pattern":"Login"}'
	tool_result gr1 'bbbb'
	usage 100 2000 8000 400
} >"$T"

OUT=$(run s-graphfirst "$T" "$REPO")
assert_has 'graph-first session: badge' '[GRAPH]' "$OUT"
assert_has 'graph-first session: saved'  'saved'  "$OUT"
assert_has 'graph-first session: search verb split' '2 search' "$OUT"
assert_has 'graph-first session: impact verb split' '1 impact' "$OUT"
assert_has 'graph-first session: graph-first tick' 'graph-first ✓' "$OUT"
assert_has 'graph-first session: locate share' '60% of locates' "$OUT"
assert_has 'graph-first session: pct of session' 'of session' "$OUT"
assert_has 'graph-first session: exploration calls' 'vs 2 explore' "$OUT"
assert_has 'graph-first session: exploration tokens' 'explore tok' "$OUT"
assert_lacks 'graph-first session: single line' '
' "$(printf '%s' "$OUT")"

# The rendered numbers must equal what `entire graph stats` reports for the same transcript.
STATS=$("$BIN" stats --repo "$REPO" --transcript "$T" --since all --format json)
WANT_SAVED=$(printf '%s' "$STATS" | awk '
	{
		if (!match($0, /"estimated_savings_est_tokens":[0-9]+/)) next
		s = substr($0, RSTART, RLENGTH)
		sub(/^[^:]*:/, "", s)
		v = s + 0
		if (v < 1000) { print v; next }
		if (v < 1000000) { t = sprintf("%.1f", v / 1000); u = "K" }
		else { t = sprintf("%.1f", v / 1000000); u = "M" }
		sub(/\.0$/, "", t)
		print t u
	}')
[ -n "$WANT_SAVED" ] || WANT_SAVED='<no savings field>'
assert_has 'renders the CLI savings number' "$WANT_SAVED saved" "$(run s-cli-number "$T" "$REPO" NO_COLOR=1)"

# --- integration: grep first ------------------------------------------------------------------
T2=$WORK/grepfirst.jsonl
{
	tool_use Grep e1 '{"pattern":"Login"}'
	tool_result e1 'matches'
	tool_use Bash g1 '{"command":"entire graph search --repo . --query \"login\""}'
	tool_result g1 "{\\\"results\\\":[{\\\"file_path\\\":\\\"$REPO/file0.go\\\"}]}"
	usage 10 20 30 40
} >"$T2"
OUT=$(run s-grepfirst "$T2" "$REPO")
assert_has 'grep-first session: cross mark' 'graph-first ✗' "$OUT"

# --- integration: no graph calls ---------------------------------------------------------------
T3=$WORK/nograph.jsonl
{
	tool_use Read r1 '{"file_path":"/x/a.go"}'
	tool_result r1 'aaaa'
	tool_use Grep gr1 '{"pattern":"x"}'
	tool_result gr1 'bbbb'
	usage 10 20 30 40
} >"$T3"
OUT=$(run s-nograph "$T3" "$REPO")
assert_has 'no graph calls: honest message' 'no graph calls yet' "$OUT"
assert_has 'no graph calls: exploration count' '2 explore' "$OUT"
assert_lacks 'no graph calls: no savings claim' 'saved' "$OUT"

# --- degradation ---------------------------------------------------------------------------
OUT=$(run s-missing "$WORK/does-not-exist.jsonl" "$REPO")
assert_empty 'missing transcript prints nothing' "$OUT"

: >"$WORK/empty.jsonl"
OUT=$(run s-empty "$WORK/empty.jsonl" "$REPO")
assert_empty 'empty transcript prints nothing' "$OUT"

printf 'not json at all\n' >"$WORK/garbage.jsonl"
OUT=$(run s-garbage "$WORK/garbage.jsonl" "$REPO")
assert_empty 'transcript of garbage prints nothing' "$OUT"

OUT=$(run_json 'this is not json')
assert_empty 'garbage on stdin prints nothing' "$OUT"

OUT=$(run_json '')
assert_empty 'empty stdin prints nothing' "$OUT"

OUT=$(run_json '{"session_id":"x"}')
assert_empty 'stdin without transcript_path prints nothing' "$OUT"

# Missing binary: an override pointing nowhere, no entire-graph on PATH, no $HOME/go/bin, and
# env -i to clear GOBIN / CLAUDE_PLUGIN_ROOT. PATH must stay REAL: with PATH=/nonexistent the
# script loses awk and dies at the stdin parse, so the empty-BIN guard is never reached and the
# empty output proves nothing about it.
mkdir -p "$WORK/nohome" "$WORK/nobin-tmp"
# env -i needs its assignments as separate WORDS. Holding them in one string and relying on unquoted
# word-splitting is not shell-agnostic: /bin/zsh does not split unquoted parameters, so the whole
# list arrived as one argv entry and the suite died 128/1 under zsh. Positional parameters carry the
# list verbatim in every shell.
set -- "PATH=/usr/bin:/bin" "HOME=$WORK/nohome"
OUT=$(stdin_json s-nobin "$T" "$REPO" |
	env -i "$@" TMPDIR="$WORK/nobin-tmp" ENTIRE_GRAPH_BIN=/nonexistent/entire-graph /bin/sh "$SCRIPT")
assert_empty 'missing binary prints nothing' "$OUT"
# ...and it stops AT `[ -n "$BIN" ] || exit 0`, which the empty output alone does not pin: with that
# guard deleted, an empty $BIN merely 127s inside render() and the output is empty anyway. What the
# guard buys is that NOTHING downstream of it runs — so assert the cache block never ran, i.e. no
# cache directory was created under this case's private TMPDIR.
assert_absent 'missing binary exits before the cache block' \
	"$WORK/nobin-tmp/entire-graph-statusline-$(id -u)"

# Positive control for the case above: the SAME stripped environment with a working binary must
# render. Without it, "prints nothing" is satisfied by any earlier exit.
cat >"$WORK/minstub" <<'STUB'
#!/bin/sh
cat <<'JSON'
{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":100,"estimated_savings_pct_of_session_tokens":1}
JSON
STUB
chmod +x "$WORK/minstub"
set -- "PATH=/usr/bin:/bin" "HOME=$WORK/nohome"
OUT=$(stdin_json s-nobin-ok "$T" "$REPO" |
	env -i "$@" TMPDIR="$WORK/cache" NO_COLOR=1 ENTIRE_GRAPH_BIN="$WORK/minstub" /bin/sh "$SCRIPT")
assert_has 'the stripped environment reaches the binary lookup at all' '100 saved' "$OUT"

# A binary that exits non-zero must not leak an error onto the status line.
cat >"$WORK/broken" <<'STUB'
#!/bin/sh
echo "boom: something went wrong" >&2
exit 3
STUB
chmod +x "$WORK/broken"
RUN_BIN=$WORK/broken
OUT=$(run s-broken "$T" "$REPO")
assert_empty 'failing binary prints nothing' "$OUT"

# A binary that prints non-JSON (e.g. an old build rejecting --transcript).
cat >"$WORK/oldbuild" <<'STUB'
#!/bin/sh
echo "stats received unexpected arguments: --transcript"
exit 1
STUB
chmod +x "$WORK/oldbuild"
RUN_BIN=$WORK/oldbuild
OUT=$(run s-oldbuild "$T" "$REPO")
assert_empty 'binary without --transcript support prints nothing' "$OUT"

# --- renderer edge cases via a stub ----------------------------------------------------------
stub() { # stub <json>
	cat >"$WORK/stub" <<STUB
#!/bin/sh
cat <<'JSON'
$1
JSON
STUB
	chmod +x "$WORK/stub"
	RUN_BIN=$WORK/stub
}

stub '{"sessions":0,"graph_calls":0,"exploration_calls":0}'
OUT=$(run s-zero "$T" "$REPO")
assert_empty 'zero sessions prints nothing' "$OUT"

stub '{"sessions":1,"graph_calls":41,"exploration_calls":14,"exploration_returned_est_tokens":1500000,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":28,"returned_bytes":1},{"name":"impact","calls":9,"returned_bytes":1},{"name":"neighbors","calls":3,"returned_bytes":1},{"name":"diff","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":2100000,"estimated_savings_pct_of_session_tokens":12.4}'
OUT=$(run s-rich "$T" "$REPO" NO_COLOR=1)
assert_eq 'rich line renders exactly' \
	'[GRAPH] ↗ 2.1M saved · 28 search · 9 impact · 3 nbrs · 1 other · vs 14 explore · 1.5M explore tok · graph-first ✓ · 75% of locates · 12% of session' \
	"$OUT"
assert_within 'rich line stays within the width budget' 150 "$OUT"

# --- meta verbs ------------------------------------------------------------------------------
# stats/version/help/doctor/init-agents/agent-guide/capabilities are self-reporting: they
# substitute for no exploration, so they may appear neither in the verb split nor in the
# residual "other". Here 12 stats + 2 doctor + 1 version + 1 help + 1 capabilities +
# 1 init-agents + 1 agent-guide = 19 meta calls out of 41; the work total is 22, of which
# 13 search + 3 impact + 1 neighbors are named, leaving 5 other (4 diff + 1 unknown verb).
stub '{"sessions":1,"graph_calls":41,"exploration_calls":2600,"exploration_returned_est_tokens":1500000,"sessions_with_locate":1,"graph_first_sessions":0,"graph_calls_by_verb":[{"name":"search","calls":13,"returned_bytes":1},{"name":"stats","calls":12,"returned_bytes":1},{"name":"diff","calls":4,"returned_bytes":1},{"name":"impact","calls":3,"returned_bytes":1},{"name":"doctor","calls":2,"returned_bytes":1},{"name":"neighbors","calls":1,"returned_bytes":1},{"name":"version","calls":1,"returned_bytes":1},{"name":"help","calls":1,"returned_bytes":1},{"name":"capabilities","calls":1,"returned_bytes":1},{"name":"init-agents","calls":1,"returned_bytes":1},{"name":"agent-guide","calls":1,"returned_bytes":1},{"name":"newverb","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":6600,"estimated_savings_pct_of_session_tokens":0.2}'
OUT=$(run s-meta "$T" "$REPO" NO_COLOR=1)
assert_eq 'meta verbs are excluded from the split and from "other"' \
	'[GRAPH] ↗ 6.6K saved · 13 search · 3 impact · 1 nbrs · 5 other · vs 2.6K explore · 1.5M explore tok · graph-first ✗ · 2% of locates · 0.20% of session' \
	"$OUT"
for meta in stats version help doctor init-agents agent-guide capabilities; do
	assert_lacks "meta verb '$meta' is never named" " $meta" "$OUT"
done
assert_within 'meta line stays within the width budget' 150 "$OUT"

# A session that only ever asked the graph about itself did no graph work at all.
stub '{"sessions":1,"graph_calls":9,"exploration_calls":4,"exploration_returned_est_tokens":100,"sessions_with_locate":1,"graph_first_sessions":0,"graph_calls_by_verb":[{"name":"stats","calls":7,"returned_bytes":1},{"name":"doctor","calls":2,"returned_bytes":1}],"estimated_savings_est_tokens":0,"estimated_savings_pct_of_session_tokens":0}'
OUT=$(run s-metaonly "$T" "$REPO" NO_COLOR=1)
assert_eq 'a meta-only session claims nothing' '[GRAPH] no graph calls yet · 4 explore' "$OUT"

# Locate verbs rank ahead of bulk/change verbs even when they were called less often.
stub '{"sessions":1,"graph_calls":30,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"symbols","calls":15,"returned_bytes":1},{"name":"edges","calls":9,"returned_bytes":1},{"name":"search","calls":4,"returned_bytes":1},{"name":"impact","calls":2,"returned_bytes":1}],"estimated_savings_est_tokens":800,"estimated_savings_pct_of_session_tokens":1}'
OUT=$(run s-order "$T" "$REPO" NO_COLOR=1)
assert_eq 'locate verbs rank first in the split' \
	'[GRAPH] ↗ 800 saved · 4 search · 2 impact · 15 symbols · 9 other · graph-first ✓ · 100% of locates · 1.0% of session' \
	"$OUT"

# --- new context segments ---------------------------------------------------------------------
# Exploration counters are dropped rather than rendered as zeros.
stub '{"sessions":1,"graph_calls":3,"exploration_calls":0,"exploration_returned_est_tokens":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":3,"returned_bytes":1}],"estimated_savings_est_tokens":700,"estimated_savings_pct_of_session_tokens":2}'
OUT=$(run s-noexplore "$T" "$REPO" NO_COLOR=1)
assert_eq 'zero exploration drops both explore segments' \
	'[GRAPH] ↗ 700 saved · 3 search · graph-first ✓ · 100% of locates · 2.0% of session' "$OUT"

# A savings percentage that rounds to 0.00% is a zero, not a number worth a segment.
stub '{"sessions":1,"graph_calls":1,"exploration_calls":1,"exploration_returned_est_tokens":5,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":40,"estimated_savings_pct_of_session_tokens":0.0004}'
OUT=$(run s-tinypct "$T" "$REPO" NO_COLOR=1)
assert_lacks 'a percentage that rounds to zero is dropped' 'of session' "$OUT"
assert_has 'explore tokens render beside the tiny percentage' '5 explore tok' "$OUT"

# --- width discipline ---------------------------------------------------------------------------
# An over-long line sheds whole segments from the right: session %, then explore tok, then
# explore calls. Nothing is ever truncated mid-segment.
LONG='{"sessions":1,"graph_calls":9999999,"exploration_calls":8888888,"exploration_returned_est_tokens":7777777,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":999999,"returned_bytes":1},{"name":"checkpoint","calls":888888,"returned_bytes":1},{"name":"snapshot","calls":777777,"returned_bytes":1}],"estimated_savings_est_tokens":999999999,"estimated_savings_pct_of_session_tokens":12.4}'
stub "$LONG"
OUT=$(run s-long "$T" "$REPO" NO_COLOR=1)
assert_eq 'over-long line drops session % then explore tok, keeping explore calls' \
	'[GRAPH] ↗ 1000M saved · 999999 search · 888888 checkpoint · 777777 snapshot · 7333335 other · vs 8.9M explore · graph-first ✓ · 53% of locates' \
	"$OUT"
assert_within 'over-long line is brought under the width budget' 150 "$OUT"
assert_lacks 'width discipline drops the session segment whole' 'of session' "$OUT"
assert_lacks 'width discipline drops the explore-token segment whole' 'explore tok' "$OUT"
assert_lacks 'width discipline never leaves a dangling separator' '· ·' "$OUT"

# Only the last segment needs to go when the overflow is small: this one renders at exactly
# 167 visible chars and lands on exactly 150 once the session segment is shed.
stub '{"sessions":1,"graph_calls":19665,"exploration_calls":88888,"exploration_returned_est_tokens":77777,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":9999,"returned_bytes":1},{"name":"checkpoint","calls":8888,"returned_bytes":1},{"name":"snapshot","calls":777,"returned_bytes":1}],"estimated_savings_est_tokens":999999999,"estimated_savings_pct_of_session_tokens":12.4}'
OUT=$(run s-long2 "$T" "$REPO" NO_COLOR=1)
assert_eq 'a small overflow sheds only the session segment' \
	'[GRAPH] ↗ 1000M saved · 9999 search · 8888 checkpoint · 777 snapshot · 1 other · vs 88.9K explore · 77.8K explore tok · graph-first ✓ · 18% of locates' \
	"$OUT"
assert_within 'mildly-long line is brought under the width budget' 150 "$OUT"
assert_eq 'the width budget is used to the last character' 150 "$(vwidth "$OUT")"

# Width is measured in visible characters, so colour must not push segments off the line.
stub "$LONG"
COLOURED=$(run s-longcolor "$T" "$REPO")
assert_has 'coloured over-long line keeps the same last segment' 'of locates' "$COLOURED"
assert_lacks 'coloured over-long line drops the same segments' 'explore tok' "$COLOURED"

# NO_COLOR still applies to every new segment.
stub '{"sessions":1,"graph_calls":5,"exploration_calls":2600,"exploration_returned_est_tokens":1500000,"sessions_with_locate":1,"graph_first_sessions":0,"graph_calls_by_verb":[{"name":"search","calls":5,"returned_bytes":1}],"estimated_savings_est_tokens":6600,"estimated_savings_pct_of_session_tokens":0.2}'
OUT=$(run s-newcolor "$T" "$REPO" NO_COLOR=1)
assert_lacks 'NO_COLOR strips escapes from the extended line' "$(printf '\033')" "$OUT"
assert_eq 'extended line renders exactly under NO_COLOR' \
	'[GRAPH] ↗ 6.6K saved · 5 search · vs 2.6K explore · 1.5M explore tok · graph-first ✗ · <1% of locates · 0.20% of session' \
	"$OUT"
OUT=$(run s-newcolor2 "$T" "$REPO")
assert_has 'the extended line is coloured by default' "$(printf '\033')" "$OUT"
assert_has 'colour does not disturb the new segments' 'vs 2.6K explore' "$OUT"

# 2.0M must print as "2M", not "2\1" — POSIX awk sub() has no backreferences.
stub '{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":2000000,"estimated_savings_pct_of_session_tokens":50}'
OUT=$(run s-round "$T" "$REPO" NO_COLOR=1)
assert_has 'round millions print without a stray backreference' '2M saved' "$OUT"
assert_lacks 'round millions contain no backslash' '\' "$OUT"

stub '{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":12345,"estimated_savings_pct_of_session_tokens":0.04}'
OUT=$(run s-thousands "$T" "$REPO" NO_COLOR=1)
assert_has 'thousands abbreviate' '12.3K saved' "$OUT"
assert_has 'sub-1% keeps two decimals' '0.04% of session' "$OUT"

# Multi-session scope reports graph-first as a rate, matching `entire graph stats`.
stub '{"sessions":7,"graph_calls":10,"exploration_calls":10,"sessions_with_locate":7,"graph_first_sessions":6,"graph_calls_by_verb":[{"name":"search","calls":10,"returned_bytes":1}],"estimated_savings_est_tokens":900,"estimated_savings_pct_of_session_tokens":3}'
OUT=$(run s-multi "$T" "$REPO" NO_COLOR=1 ENTIRE_GRAPH_STATUSLINE_SCOPE=project)
assert_has 'multi-session renders a graph-first rate' 'graph-first 86%' "$OUT"

# ...and scope=project must actually ASK the CLI a project-wide question: the whole session
# directory over the requested window, not this one transcript. An arg-recording stub is the only
# way to assert that; the rendered badge looks the same either way.
cat >"$WORK/argstub" <<STUB
#!/bin/sh
printf '%s\n' "\$*" >"$WORK/args"
cat <<'JSON'
{"sessions":7,"graph_calls":10,"exploration_calls":10,"sessions_with_locate":7,"graph_first_sessions":6,"graph_calls_by_verb":[{"name":"search","calls":10,"returned_bytes":1}],"estimated_savings_est_tokens":900,"estimated_savings_pct_of_session_tokens":3}
JSON
STUB
chmod +x "$WORK/argstub"
RUN_BIN=$WORK/argstub
OUT=$(run s-projectargs "$T" "$REPO" NO_COLOR=1 ENTIRE_GRAPH_STATUSLINE_SCOPE=project ENTIRE_GRAPH_STATUSLINE_SINCE=7d)
ARGS=$(cat "$WORK/args")
assert_has 'scope=project scans the session directory' "--sessions-dir $(dirname "$T")" "$ARGS"
assert_has 'scope=project honours the requested window' '--since 7d' "$ARGS"
assert_lacks 'scope=project does not narrow to one transcript' '--transcript' "$ARGS"
assert_has 'scope=project still renders the rate' 'graph-first 86%' "$OUT"

OUT=$(run s-sessionargs "$T" "$REPO" NO_COLOR=1)
ARGS=$(cat "$WORK/args")
assert_has 'scope=session narrows to this transcript' "--transcript $T" "$ARGS"
assert_has 'scope=session ignores the window' '--since all' "$ARGS"
assert_lacks 'scope=session does not scan the session directory' '--sessions-dir' "$ARGS"

# Escape hygiene: a verb name carrying ANSI/control bytes must never reach the terminal.
stub '{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":100,"estimated_savings_pct_of_session_tokens":1}'
OUT=$(run s-clean "$T" "$REPO" NO_COLOR=1)
assert_lacks 'NO_COLOR strips every escape' "$(printf '\033')" "$OUT"

# Colour belongs to the cache key: a coloured entry must never be served to a NO_COLOR render.
OUT=$(run s-colorcache "$T" "$REPO")
assert_has 'coloured render emits escapes' "$(printf '\033')" "$OUT"
OUT=$(run s-colorcache "$T" "$REPO" NO_COLOR=1)
assert_lacks 'NO_COLOR is not served a cached coloured line' "$(printf '\033')" "$OUT"
OUT=$(run s-colorcache "$T" "$REPO")
assert_has 'colour survives the NO_COLOR render' "$(printf '\033')" "$OUT"

# A verb name outside the closed lowercase set the CLI dispatches is dropped outright and the
# badge falls back to the plain call count. This is the gate that stops a doctored stats payload
# painting arbitrary bytes onto the terminal; clean() behind it is belt-and-braces.
stub '{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"sea[31mrch;rm -rf /","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":100,"estimated_savings_pct_of_session_tokens":1}'
OUT=$(run s-inject "$T" "$REPO" NO_COLOR=1)
assert_eq 'hostile verb name is dropped, not rendered' \
	'[GRAPH] ↗ 100 saved · 1 graph · graph-first ✓ · 100% of locates · 1.0% of session' "$OUT"
assert_lacks 'hostile verb name cannot inject an escape' "$(printf '\033')" "$OUT"
assert_lacks 'hostile verb name cannot inject a shell metacharacter' ';' "$OUT"
assert_lacks 'hostile verb name cannot inject a bracket' '[31m' "$OUT"

# --- cache ------------------------------------------------------------------------------------
# A counting stub proves the second render is served from cache rather than re-scanning.
cat >"$WORK/counter" <<STUB
#!/bin/sh
echo x >>"$WORK/calls"
cat <<'JSON'
{"sessions":1,"graph_calls":2,"exploration_calls":2,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":2,"returned_bytes":1}],"estimated_savings_est_tokens":500,"estimated_savings_pct_of_session_tokens":1}
JSON
STUB
chmod +x "$WORK/counter"
RUN_BIN=$WORK/counter
: >"$WORK/calls"
FIRST=$(run s-cache "$T" "$REPO" NO_COLOR=1)
SECOND=$(run s-cache "$T" "$REPO" NO_COLOR=1)
THIRD=$(run s-cache "$T" "$REPO" NO_COLOR=1)
assert_eq 'cached renders are identical' "$FIRST" "$SECOND"
assert_eq 'cached renders stay identical' "$FIRST" "$THIRD"
assert_eq 'cache collapses 3 renders to 1 scan' 1 "$(wc -l <"$WORK/calls" | tr -d ' ')"

: >"$WORK/calls"
run s-nocache "$T" "$REPO" NO_COLOR=1 ENTIRE_GRAPH_STATUSLINE_CACHE=0 >/dev/null
run s-nocache "$T" "$REPO" NO_COLOR=1 ENTIRE_GRAPH_STATUSLINE_CACHE=0 >/dev/null
assert_eq 'cache can be disabled' 2 "$(wc -l <"$WORK/calls" | tr -d ' ')"

# A stale cache entry (same config, older transcript stamp) is served immediately and refreshed
# behind the render. The entry is produced by a real run, then only its stamp+line are aged, so
# the test cannot drift out of sync with the key format.
CACHE_LINE=$(cache_file s-stale "$T")
run s-stale "$T" "$REPO" NO_COLOR=1 >/dev/null
awk -F'\t' 'BEGIN{OFS="\t"} {print $1, "0-0", "STALE-BADGE"}' "$CACHE_LINE" >"$CACHE_LINE.aged"
mv "$CACHE_LINE.aged" "$CACHE_LINE"
: >"$WORK/calls"
OUT=$(run s-stale "$T" "$REPO" NO_COLOR=1)
assert_eq 'stale cache is served without blocking' 'STALE-BADGE' "$OUT"
tries=0
while [ "$tries" -lt 50 ]; do
	case $(cat "$CACHE_LINE") in
	*STALE-BADGE*) ;;
	*) break ;;
	esac
	tries=$((tries + 1))
	sleep 0.1
done
OUT=$(run s-stale "$T" "$REPO" NO_COLOR=1)
assert_has 'stale cache is refreshed in the background' '500 saved' "$OUT"

# A cache entry written for a DIFFERENT question (other repo/scope/colour) must be recomputed,
# never served — that is what makes the NO_COLOR case above safe.
CACHE_LINE2=$(cache_file s-otherconfig "$T")
run s-otherconfig "$T" "$REPO" NO_COLOR=1 >/dev/null
awk -F'\t' 'BEGIN{OFS="\t"} {print "v1 session 30d color /somewhere/else", $2, "WRONG-QUESTION"}' "$CACHE_LINE2" >"$CACHE_LINE2.x"
mv "$CACHE_LINE2.x" "$CACHE_LINE2"
OUT=$(run s-otherconfig "$T" "$REPO" NO_COLOR=1)
assert_lacks 'cache from a different question is not served' 'WRONG-QUESTION' "$OUT"
assert_has 'cache from a different question is recomputed' '500 saved' "$OUT"

# A symlinked cache file is refused rather than rendered (it could point anywhere). The payload
# must be a VALID entry for exactly this render — right config in the first tab field, right
# stamp in the second — or the config gate rejects it on its own and the test proves nothing
# about the symlink guard. (Pointing the link at /etc/passwd is exactly that vacuous case.)
run s-symsrc "$T" "$REPO" NO_COLOR=1 >/dev/null
SYM_TARGET=$WORK/symlink-target.line
awk -F'\t' 'BEGIN{OFS="\t"} {print $1, $2, "SYMLINKED-BADGE root:x:0:0"}' \
	"$(cache_file s-symsrc "$T")" >"$SYM_TARGET"
ln -sf "$SYM_TARGET" "$(cache_file s-symlink "$T")"
: >"$WORK/calls"
OUT=$(run s-symlink "$T" "$REPO" NO_COLOR=1)
assert_lacks 'a symlinked cache entry is never served' 'SYMLINKED-BADGE' "$OUT"
assert_lacks 'symlinked cache contents never leak' 'root:' "$OUT"
assert_has 'symlinked cache is recomputed instead' '500 saved' "$OUT"

# A symlinked cache DIRECTORY is the same attack one level up: mkdir -p follows it, and a file
# planted in the target is a regular file, so it passes the per-file guard above and its bytes
# would be printed verbatim — terminal escapes included.
mkdir -p "$WORK/evil-tmp" "$WORK/evil-target"
ln -sf "$WORK/evil-target" "$WORK/evil-tmp/entire-graph-statusline-$(id -u)"
run s-dirlink "$T" "$REPO" NO_COLOR=1 >/dev/null
awk -F'\t' 'BEGIN{OFS="\t"} {print $1, $2, "PLANTED-BADGE"}' "$(cache_file s-dirlink "$T")" \
	>"$WORK/evil-target/$(cache_key s-dirlink "$T").line"
OUT=$(run s-dirlink "$T" "$REPO" NO_COLOR=1 TMPDIR="$WORK/evil-tmp")
assert_lacks 'a symlinked cache directory is never read' 'PLANTED-BADGE' "$OUT"
assert_has 'a symlinked cache directory falls back to a real render' '500 saved' "$OUT"
# ...and the WRITE path refuses it too. This needs a session with NO planted record: the case above
# is served straight from the (refused) entry and exits before store() is ever reached, so it says
# nothing about writing. A fresh key forces the miss -> render -> store path, and nothing may then
# appear through the symlink in a directory this user does not control.
run s-dirwrite "$T" "$REPO" NO_COLOR=1 TMPDIR="$WORK/evil-tmp" >/dev/null
assert_absent 'a symlinked cache directory is never written into' \
	"$WORK/evil-target/$(cache_key s-dirwrite "$T").line"

# A cache directory another user owns is a directory that dictates what this script prints. $TMPDIR
# is normally unset, so /tmp/entire-graph-statusline-<uid> is predictable and /tmp is world-writable
# and cleared on boot: another user can win the create race. Per-uid naming does not help (the
# victim's uid is in the name) and neither symlink guard fires (it is a real directory), so the
# refusal has to come from an OWNERSHIP test. chflags uchg is this platform's way of making a
# directory this user owns behave like one it does not — `chmod 700` returns EPERM on it — while
# leaving the planted record readable; skipped where that cannot be done.
FOREIGN_TMP=$WORK/foreign-tmp
FOREIGN_DIR=$FOREIGN_TMP/entire-graph-statusline-$(id -u)
mkdir -p "$FOREIGN_TMP"
run s-foreign "$T" "$REPO" NO_COLOR=1 TMPDIR="$FOREIGN_TMP" >/dev/null
FOREIGN_LINE=$FOREIGN_DIR/$(cache_key s-foreign "$T").line
assert_present 'a usable cache directory is still created and written' "$FOREIGN_LINE"
if [ -f "$FOREIGN_LINE" ]; then
	awk -F'\t' 'BEGIN{OFS="\t"} {print $1, $2, "\033[2J\033]0;OWNED\007HOSTILE-BADGE"}' \
		"$FOREIGN_LINE" >"$FOREIGN_LINE.x"
	mv "$FOREIGN_LINE.x" "$FOREIGN_LINE"
fi

# The PREMISE of the ownership gate, asserted on the platform rather than assumed: chmod(1) exits 0
# on a directory whose mode it cannot change at all, provided the mode already matches — it elides
# the chmod(2) call. That is exactly why "chmod exited 0" was never "we own it" (the other half being
# uid 0, for which chmod succeeds everywhere), and why the gate is now `[ -O ]`.
if chflags uchg "$FOREIGN_DIR" 2>/dev/null; then
	chmod 700 "$FOREIGN_DIR" 2>/dev/null
	ELIDE_RC=$?
	chmod 755 "$FOREIGN_DIR" 2>/dev/null
	ELIDE_DIFF_RC=$?
	chflags nouchg "$FOREIGN_DIR" 2>/dev/null
	assert_eq 'chmod exits 0 on an immutable directory already at the requested mode' 0 "$ELIDE_RC"
	assert_eq 'chmod fails on the same directory once the mode differs' 1 "$ELIDE_DIFF_RC"
else
	printf 'skip  platform has no chflags; chmod(1) mode-match elision unasserted\n'
fi

# 755 first, then immutable: a hostile directory MUST be readable by its victim to be worth planting
# in, so `chmod 700` has real work to do on one and genuinely fails.
chmod 755 "$FOREIGN_DIR" 2>/dev/null
if chflags uchg "$FOREIGN_DIR" 2>/dev/null && ! chmod 700 "$FOREIGN_DIR" 2>/dev/null; then
	FOREIGN_BEFORE=$(cksum <"$FOREIGN_LINE")
	OUT=$(run s-foreign "$T" "$REPO" NO_COLOR=1 TMPDIR="$FOREIGN_TMP")
	FOREIGN_AFTER=$(cksum <"$FOREIGN_LINE")
	chflags nouchg "$FOREIGN_DIR" 2>/dev/null
	assert_lacks 'a cache directory this user cannot chmod is never read' 'HOSTILE-BADGE' "$OUT"
	assert_lacks 'an adopted cache directory cannot inject an escape' "$(printf '\033')" "$OUT"
	assert_has 'an unchmodable cache directory falls back to a real render' '500 saved' "$OUT"
	# The write half. NB: uchg also blocks writes inside the directory, so this assertion cannot
	# distinguish "the gate refused" from "the flag refused" — the non-vacuous version of it is the
	# shimmed-chmod case below, where the directory stays fully writable.
	assert_eq 'an unchmodable cache directory is not rewritten' "$FOREIGN_BEFORE" "$FOREIGN_AFTER"
else
	chflags nouchg "$FOREIGN_DIR" 2>/dev/null
	printf 'skip  platform cannot make chmod fail; foreign cache directory unverified\n'
fi

# --- cache directory: ownership, not "chmod exited 0" -------------------------------------------
# The two cases below are the ones the chflags case above cannot express, because this suite runs as
# ONE uid and cannot create a directory owned by somebody else. Both shim `chmod` on the PATH the
# SCRIPT sees (the suite's own PATH is untouched), which is the only deterministic way to reproduce
# "chmod exits 0 / non-zero without proving anything about ownership" — the two real-world shapes
# being chmod(1)'s mode-match elision (asserted above) and uid 0, for which chmod always succeeds.
mkdir -p "$WORK/shim-chmod-ok" "$WORK/shim-chmod-fail"
printf '#!/bin/sh\nexit 0\n' >"$WORK/shim-chmod-ok/chmod"
printf '#!/bin/sh\nexit 1\n' >"$WORK/shim-chmod-fail/chmod"
chmod +x "$WORK/shim-chmod-ok/chmod" "$WORK/shim-chmod-fail/chmod"

# (A) chmod fails, the way it does with EPERM on a foreign directory — but here the directory stays
# fully readable AND writable, so both halves of the refusal are observable: the planted record must
# not be served, and it must not be overwritten either. This is what pins store()'s own
# `cache_dir_ok || return 0`, and it pins the memo too: CACHE_DIR_OK must not be set on a path that
# returned 1, or the read gate refuses the directory and store() writes into it moments later.
EPERM_TMP=$WORK/eperm-tmp
EPERM_DIR=$EPERM_TMP/entire-graph-statusline-$(id -u)
mkdir -p "$EPERM_TMP"
run s-eperm "$T" "$REPO" NO_COLOR=1 TMPDIR="$EPERM_TMP" >/dev/null
EPERM_LINE=$EPERM_DIR/$(cache_key s-eperm "$T").line
assert_present 'a writable cache directory is still created and written' "$EPERM_LINE"
awk -F'\t' 'BEGIN{OFS="\t"} {print $1, $2, "\033[2J\033]0;OWNED\007EPERM-BADGE"}' \
	"$EPERM_LINE" >"$EPERM_LINE.x"
mv "$EPERM_LINE.x" "$EPERM_LINE"
EPERM_BEFORE=$(cksum <"$EPERM_LINE")
OUT=$(run s-eperm "$T" "$REPO" NO_COLOR=1 TMPDIR="$EPERM_TMP" PATH="$WORK/shim-chmod-fail:$PATH")
assert_lacks 'a cache directory chmod refuses is never read' 'EPERM-BADGE' "$OUT"
assert_lacks 'a chmod-refused cache directory cannot inject an escape' "$(printf '\033')" "$OUT"
assert_has 'a chmod-refused cache directory falls back to a real render' '500 saved' "$OUT"
assert_eq 'a chmod-refused cache directory is never written into' \
	"$EPERM_BEFORE" "$(cksum <"$EPERM_LINE")"
# ...and on a key with no entry at all, which is the only path that reaches store() (the case above
# is refused at the read gate, so a memo set on that failure is what store() would act on).
EPERM_WRITE=$EPERM_DIR/$(cache_key s-epermwrite "$T").line
rm -f "$EPERM_WRITE"
run s-epermwrite "$T" "$REPO" NO_COLOR=1 TMPDIR="$EPERM_TMP" PATH="$WORK/shim-chmod-fail:$PATH" \
	>/dev/null
assert_absent 'a chmod-refused cache directory gets no new entry' "$EPERM_WRITE"

# (B) chmod exits 0 on a directory this user does NOT own — the elision / uid-0 case, and the one
# thing only `[ -O ]` can refuse: the mode bits (1777) are wide open, chmod says 0, and it is not a
# symlink, so every other guard in the script waves it through. It needs a real foreign-owned
# directory that is nevertheless readable and writable, i.e. a world-writable system temp directory
# (drwxrwxrwt, root-owned). CACHE_DIR is ${TMPDIR}/entire-graph-statusline-$(id -u), so a shimmed
# `id` is what retargets the leaf onto it; the surplus ".." components collapse at "/", so the chain
# does not depend on how deep $WORK happens to be. /tmp is skipped on macOS because it is a SYMLINK
# to /private/tmp, and the script's own symlink guard would then refuse the directory for the wrong
# reason, making the case vacuous.
NOTOWNED=
for candidate in /private/tmp /tmp /var/tmp; do
	if [ -d "$candidate" ] && [ ! -L "$candidate" ] && [ ! -O "$candidate" ] && [ -w "$candidate" ]; then
		NOTOWNED=$candidate
		break
	fi
done
if [ -n "$NOTOWNED" ]; then
	NOTOWNED_TMP=$WORK/notowned-tmp
	mkdir -p "$NOTOWNED_TMP/entire-graph-statusline-0" "$WORK/shim-notowned"
	cp "$WORK/shim-chmod-ok/chmod" "$WORK/shim-notowned/chmod"
	printf '#!/bin/sh\nprintf %%s "0/%s%s"\n' \
		'../../../../../../../../../../../../../../../../../../../..' \
		"$NOTOWNED" >"$WORK/shim-notowned/id"
	chmod +x "$WORK/shim-notowned/id"

	# The planted record is produced by a real run in a directory this user DOES own, then copied to
	# the foreign one, so its config and stamp are valid for exactly this render.
	run s-notowned "$T" "$REPO" NO_COLOR=1 >/dev/null
	NOTOWNED_PLANT=$NOTOWNED/$(cache_key s-notowned "$T").line
	awk -F'\t' 'BEGIN{OFS="\t"} {print $1, $2, "\033[2J\033]0;OWNED\007NOTOWNED-BADGE"}' \
		"$(cache_file s-notowned "$T")" >"$NOTOWNED_PLANT"
	NOTOWNED_BEFORE=$(cksum <"$NOTOWNED_PLANT")
	OUT=$(run s-notowned "$T" "$REPO" NO_COLOR=1 \
		TMPDIR="$NOTOWNED_TMP" PATH="$WORK/shim-notowned:$PATH")
	assert_lacks 'a cache directory this user does not own is never read' 'NOTOWNED-BADGE' "$OUT"
	assert_lacks 'an unowned cache directory cannot inject an escape' "$(printf '\033')" "$OUT"
	assert_has 'an unowned cache directory falls back to a real render' '500 saved' "$OUT"
	assert_eq 'an unowned cache directory is never written into' \
		"$NOTOWNED_BEFORE" "$(cksum <"$NOTOWNED_PLANT")"
	rm -f "$NOTOWNED_PLANT"
	NOTOWNED_WRITE=$NOTOWNED/$(cache_key s-notownedwrite "$T").line
	rm -f "$NOTOWNED_WRITE"
	run s-notownedwrite "$T" "$REPO" NO_COLOR=1 \
		TMPDIR="$NOTOWNED_TMP" PATH="$WORK/shim-notowned:$PATH" >/dev/null
	assert_absent 'an unowned cache directory gets no new entry' "$NOTOWNED_WRITE"
	rm -f "$NOTOWNED_WRITE"
else
	printf 'skip  no foreign-owned writable directory available; ownership gate unverified\n'
fi

# --- cache record: field hygiene ---------------------------------------------------------------
# $SCOPE, $SINCE and $REPO all reach a TAB-delimited cache record. A tab in any of them shifts the
# field split, and the "line" served on the next render then starts with the STAMP — the badge
# grows a stray number that was never rendered.
TABREPO=$WORK/tab$(printf '\t')repo
mkdir -p "$TABREPO"
TAB_JSON=$(stdin_json s-tabrepo "$T" "$TABREPO")
TAB_FIRST=$(run_json "$TAB_JSON" NO_COLOR=1)
TAB_SECOND=$(run_json "$TAB_JSON" NO_COLOR=1)
TAB_STAMP=$(stat -f '%z-%m' "$T" 2>/dev/null || stat -c '%s-%Y' "$T" 2>/dev/null)
assert_has 'a tab in the repo path still renders' '500 saved' "$TAB_FIRST"
assert_eq 'a tab in the repo path does not shift the cached record' "$TAB_FIRST" "$TAB_SECOND"
assert_lacks 'the cache stamp never leaks into the served badge' "$TAB_STAMP" "$TAB_SECOND"

# --- cache key: hostile session ids ------------------------------------------------------------
# $SESSION arrives from outside and becomes a FILENAME. Unsanitised, "../../../.." walks the cache
# file clean out of $CACHE_DIR and "/etc/shadow" turns the key into a path; untruncated, an
# unbounded id becomes an unbounded filename. cache_key() mirrors the same tr + cut, so asserting
# the file landed exactly there asserts both.
ESC_TMP=$WORK/escape-tmp
ESC_DIR=$ESC_TMP/entire-graph-statusline-$(id -u)
mkdir -p "$ESC_TMP"
for hostile in '../../../../escaped' '/etc/shadow'; do
	OUT=$(run "$hostile" "$T" "$REPO" NO_COLOR=1 TMPDIR="$ESC_TMP")
	assert_has "hostile session id still renders ($hostile)" '500 saved' "$OUT"
	assert_lacks "hostile session id leaves no separator in the key ($hostile)" \
		'/' "$(cache_key "$hostile" "$T")"
	assert_present "hostile session id cannot leave the cache dir ($hostile)" \
		"$ESC_DIR/$(cache_key "$hostile" "$T").line"
done
LONGSID=0123456789012345678901234567890123456789TAIL
OUT=$(run "$LONGSID" "$T" "$REPO" NO_COLOR=1 TMPDIR="$ESC_TMP")
assert_has 'an over-long session id still renders' '500 saved' "$OUT"
assert_present 'an over-long session id is cut to 40 characters' \
	"$ESC_DIR/$(cache_key "$LONGSID" "$T").line"
assert_lacks 'an over-long session id leaves no untruncated key behind' 'TAIL' "$(ls -a "$ESC_DIR")"

# --- cache key: no session_id ------------------------------------------------------------------
# session_id is absent from some status-line invocations, where the JSON reader yields "-".
# Keying on that alone put every such session on one "-.line": two sessions overwrote each
# other's badge on every render. The stub's numbers depend on the transcript it is pointed at, so
# an entry served for the WRONG transcript is visible in the badge rather than merely suspected.
printf 'a\n' >"$WORK/perA.jsonl"
printf 'bbbb\n' >"$WORK/perB.jsonl"
cat >"$WORK/pertranscript" <<'STUB'
#!/bin/sh
saved=9
for arg in "$@"; do
	case $arg in
	*/perA.jsonl) saved=111 ;;
	*/perB.jsonl) saved=222 ;;
	esac
done
printf '{"sessions":1,"graph_calls":1,"exploration_calls":0,"sessions_with_locate":1,"graph_first_sessions":1,"graph_calls_by_verb":[{"name":"search","calls":1,"returned_bytes":1}],"estimated_savings_est_tokens":%s,"estimated_savings_pct_of_session_tokens":1}\n' "$saved"
STUB
chmod +x "$WORK/pertranscript"
RUN_BIN=$WORK/pertranscript

OUT=$(run_json "$(stdin_json_nosid "$WORK/perA.jsonl" "$REPO")" NO_COLOR=1)
assert_has 'no session_id: first transcript renders its own numbers' '111 saved' "$OUT"
OUT=$(run_json "$(stdin_json_nosid "$WORK/perB.jsonl" "$REPO")" NO_COLOR=1)
assert_has 'no session_id: second transcript renders its own numbers' '222 saved' "$OUT"
OUT=$(run_json "$(stdin_json_nosid "$WORK/perA.jsonl" "$REPO")" NO_COLOR=1)
assert_has 'no session_id: the second transcript did not overwrite the first' '111 saved' "$OUT"
assert_two_files 'no session_id: two transcripts get two cache files' \
	"$(cache_file - "$WORK/perA.jsonl")" "$(cache_file - "$WORK/perB.jsonl")"

# --- cache key: duplicate session_id -----------------------------------------------------------
# The same session_id can arrive with two different transcripts (a resumed session, a second
# worktree, or two ids the sanitising squashes onto one key). Keying on the id alone made the two
# flip-flop each other's badge on every render.
OUT=$(run s-dup "$WORK/perA.jsonl" "$REPO" NO_COLOR=1)
assert_has 'duplicate session_id: first transcript renders its own numbers' '111 saved' "$OUT"
OUT=$(run s-dup "$WORK/perB.jsonl" "$REPO" NO_COLOR=1)
assert_has 'duplicate session_id: second transcript renders its own numbers' '222 saved' "$OUT"
OUT=$(run s-dup "$WORK/perA.jsonl" "$REPO" NO_COLOR=1)
assert_has 'duplicate session_id: no flip-flop between the two transcripts' '111 saved' "$OUT"
assert_two_files 'duplicate session_id: two transcripts get two cache files' \
	"$(cache_file s-dup "$WORK/perA.jsonl")" "$(cache_file s-dup "$WORK/perB.jsonl")"

# --- cache key: no cksum -----------------------------------------------------------------------
# cksum is POSIX but a shim/stripped image can still take it away. The fallback must derive the
# digest FROM THE PATH: a constant would put every transcript back onto one key — the exact
# collision the digest exists to prevent — and the two sessions above would resume overwriting each
# other's badge. It must also be STABLE, or every render writes a new file and nothing is cached.
mkdir -p "$WORK/shim"
printf '#!/bin/sh\nexit 127\n' >"$WORK/shim/cksum"
chmod +x "$WORK/shim/cksum"
NOCK_TMP=$WORK/nocksum-tmp
NOCK_DIR=$NOCK_TMP/entire-graph-statusline-$(id -u)
mkdir -p "$NOCK_TMP"
nocksum_lines() { ls -a "$NOCK_DIR" 2>/dev/null | grep -c '\.line$'; }
OUT=$(run_json "$(stdin_json_nosid "$WORK/perA.jsonl" "$REPO")" NO_COLOR=1 \
	TMPDIR="$NOCK_TMP" PATH="$WORK/shim:$PATH")
assert_has 'no cksum: the badge still renders' '111 saved' "$OUT"
OUT=$(run_json "$(stdin_json_nosid "$WORK/perA.jsonl" "$REPO")" NO_COLOR=1 \
	TMPDIR="$NOCK_TMP" PATH="$WORK/shim:$PATH")
assert_has 'no cksum: one transcript keeps one key across renders' '111 saved' "$OUT"
assert_eq 'no cksum: one transcript, one cache file' 1 "$(nocksum_lines)"
OUT=$(run_json "$(stdin_json_nosid "$WORK/perB.jsonl" "$REPO")" NO_COLOR=1 \
	TMPDIR="$NOCK_TMP" PATH="$WORK/shim:$PATH")
assert_has 'no cksum: a second transcript renders its own numbers' '222 saved' "$OUT"
assert_eq 'no cksum: two transcripts, two cache files' 2 "$(nocksum_lines)"

# --- prune -------------------------------------------------------------------------------------
# A store drops the garbage the script leaks — week-old lines, tmp files abandoned by a killed
# write, lock DIRECTORIES orphaned by a killed refresh — and touches nothing that is live.
RUN_BIN=$WORK/counter
CDIR=$(cache_dir)
OLD=202001010000
rm -f "$CDIR/.pruned"
: >"$CDIR/gone-0-0.line"
: >"$CDIR/gone-0-0.line.4242"
mkdir -p "$CDIR/gone-0-0.line.lock"
touch -t "$OLD" "$CDIR/gone-0-0.line" "$CDIR/gone-0-0.line.4242" "$CDIR/gone-0-0.line.lock"
: >"$CDIR/kept-0-0.line"
: >"$CDIR/kept-0-0.line.9999"
mkdir -p "$CDIR/kept-0-0.line.lock"
run s-prune "$T" "$REPO" NO_COLOR=1 >/dev/null
assert_absent 'prune drops a week-old cache line' "$CDIR/gone-0-0.line"
assert_absent 'prune drops an abandoned tmp write' "$CDIR/gone-0-0.line.4242"
assert_absent 'prune drops an orphaned lock directory' "$CDIR/gone-0-0.line.lock"
assert_present 'prune keeps a fresh cache line' "$CDIR/kept-0-0.line"
assert_present 'prune keeps an in-flight tmp write' "$CDIR/kept-0-0.line.9999"
assert_present 'prune keeps a live lock directory' "$CDIR/kept-0-0.line.lock"
assert_present 'prune leaves its throttle sentinel' "$CDIR/.pruned"

# ...and it does not re-scan on the next store: the sentinel is the throttle.
: >"$CDIR/throttled-0-0.line"
touch -t "$OLD" "$CDIR/throttled-0-0.line"
run s-prune2 "$T" "$REPO" NO_COLOR=1 >/dev/null
assert_present 'prune is throttled behind its sentinel' "$CDIR/throttled-0-0.line"

# A sentinel that is not a regular file — a planted DIRECTORY — used to disable prune permanently:
# [ -f ] is false, so the throttle never short-circuits, but ": >" then fails on the directory and
# the function returns before scanning. Self-heal instead. The sentinel must come back as a REGULAR
# FILE, not merely exist: [ -e ] is satisfied by the planted directory that was never cleared.
rm -rf "$CDIR/.pruned"
mkdir -p "$CDIR/.pruned"
: >"$CDIR/wedged-0-0.line"
touch -t "$OLD" "$CDIR/wedged-0-0.line"
run s-prune3 "$T" "$REPO" NO_COLOR=1 >/dev/null
assert_absent 'a directory sentinel does not wedge prune forever' "$CDIR/wedged-0-0.line"
assert_regular 'a directory sentinel is replaced by a real sentinel' "$CDIR/.pruned"

# The same wedge with a NON-EMPTY directory. `rmdir` only removes empty directories, so this shape
# survived every store: prune never ran again and the cache grew without bound.
rm -rf "$CDIR/.pruned"
mkdir -p "$CDIR/.pruned/child"
: >"$CDIR/.pruned/child/junk"
: >"$CDIR/wedged2-0-0.line"
touch -t "$OLD" "$CDIR/wedged2-0-0.line"
run s-prune4 "$T" "$REPO" NO_COLOR=1 >/dev/null
assert_absent 'a NON-EMPTY directory sentinel does not wedge prune forever' "$CDIR/wedged2-0-0.line"
assert_regular 'a non-empty directory sentinel is replaced by a real sentinel' "$CDIR/.pruned"

# ...and a sentinel that IS a regular file but is unwritable (mode 444 — a killed run, an older uid,
# a botched restore). The shape branch cannot catch it: [ -f ] is true, so the throttle arm is taken,
# the file is stale so prune proceeds, and ": >" then fails with EACCES on every store — wedged just
# as permanently. The write self-heals: remove and recreate once.
rm -rf "$CDIR/.pruned"
: >"$CDIR/.pruned"
touch -t "$OLD" "$CDIR/.pruned"
chmod 444 "$CDIR/.pruned"
: >"$CDIR/wedged3-0-0.line"
touch -t "$OLD" "$CDIR/wedged3-0-0.line"
run s-prune5 "$T" "$REPO" NO_COLOR=1 >/dev/null
assert_absent 'a read-only sentinel does not wedge prune forever' "$CDIR/wedged3-0-0.line"
assert_regular 'a read-only sentinel is replaced by a real sentinel' "$CDIR/.pruned"
assert_writable 'the replacement sentinel is writable, so the next store is not wedged either' \
	"$CDIR/.pruned"

# The same case with dash EXECUTING the script, which is not a formality: ":" is a POSIX SPECIAL
# built-in, and a redirection error on a special built-in terminates a non-interactive shell (XCU
# 2.8.1). dash does exactly that — `: >unwritable-sentinel` exits 2 in the middle of store(), so no
# self-heal can run — while bash merely fails the command, which is why `sh` on macOS hides the bug
# entirely. /bin/sh IS dash on most Linux distributions, i.e. on CI. The script therefore truncates
# with the regular built-in `true`, and this case is what pins it.
if [ -x /bin/dash ]; then
	rm -rf "$CDIR/.pruned"
	: >"$CDIR/.pruned"
	touch -t "$OLD" "$CDIR/.pruned"
	chmod 444 "$CDIR/.pruned"
	: >"$CDIR/wedged4-0-0.line"
	touch -t "$OLD" "$CDIR/wedged4-0-0.line"
	stdin_json s-prune6 "$T" "$REPO" |
		clean_env TMPDIR="$WORK/cache" ENTIRE_GRAPH_BIN="$RUN_BIN" NO_COLOR=1 \
			/bin/dash "$SCRIPT" >/dev/null
	assert_absent 'the sentinel self-heal survives dash, not just bash' "$CDIR/wedged4-0-0.line"
	assert_regular 'dash leaves a real sentinel behind' "$CDIR/.pruned"
else
	printf 'skip  no /bin/dash; the special-built-in redirection trap is unpinned\n'
fi

# --- result -----------------------------------------------------------------------------------
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
