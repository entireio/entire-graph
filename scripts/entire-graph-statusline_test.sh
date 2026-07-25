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
trap 'rm -rf "$WORK"' EXIT INT TERM

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

# run <session-id> <transcript> <cwd> [env assignments...] -> stdout of the status line
run() {
	sid=$1
	transcript=$2
	cwd=$3
	shift 3
	stdin_json "$sid" "$transcript" "$cwd" |
		env TMPDIR="$WORK/cache" ENTIRE_GRAPH_BIN="$RUN_BIN" "$@" sh "$SCRIPT"
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

OUT=$(printf 'this is not json' | env TMPDIR="$WORK/cache" ENTIRE_GRAPH_BIN="$BIN" sh "$SCRIPT")
assert_empty 'garbage on stdin prints nothing' "$OUT"

OUT=$(printf '' | env TMPDIR="$WORK/cache" ENTIRE_GRAPH_BIN="$BIN" sh "$SCRIPT")
assert_empty 'empty stdin prints nothing' "$OUT"

OUT=$(printf '{"session_id":"x"}' | env TMPDIR="$WORK/cache" ENTIRE_GRAPH_BIN="$BIN" sh "$SCRIPT")
assert_empty 'stdin without transcript_path prints nothing' "$OUT"

# Missing binary: no override, nothing on PATH, no ~/go/bin.
OUT=$(stdin_json s-nobin "$T" "$REPO" |
	env -i HOME="$WORK/nohome" PATH=/nonexistent TMPDIR="$WORK/cache" \
		ENTIRE_GRAPH_BIN=/nonexistent/entire-graph /bin/sh "$SCRIPT")
assert_empty 'missing binary prints nothing' "$OUT"

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
CACHE_LINE=$WORK/cache/entire-graph-statusline/s-stale.line
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
CACHE_LINE2=$WORK/cache/entire-graph-statusline/s-otherconfig.line
run s-otherconfig "$T" "$REPO" NO_COLOR=1 >/dev/null
awk -F'\t' 'BEGIN{OFS="\t"} {print "v1 session 30d color /somewhere/else", $2, "WRONG-QUESTION"}' "$CACHE_LINE2" >"$CACHE_LINE2.x"
mv "$CACHE_LINE2.x" "$CACHE_LINE2"
OUT=$(run s-otherconfig "$T" "$REPO" NO_COLOR=1)
assert_lacks 'cache from a different question is not served' 'WRONG-QUESTION' "$OUT"
assert_has 'cache from a different question is recomputed' '500 saved' "$OUT"

# A symlinked cache file is refused rather than rendered (it could point anywhere).
ln -sf /etc/passwd "$WORK/cache/entire-graph-statusline/s-symlink.line"
: >"$WORK/calls"
OUT=$(run s-symlink "$T" "$REPO" NO_COLOR=1)
assert_has 'symlinked cache is ignored, not rendered' '500 saved' "$OUT"
assert_lacks 'symlinked cache contents never leak' 'root:' "$OUT"

# --- result -----------------------------------------------------------------------------------
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
