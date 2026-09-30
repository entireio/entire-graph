package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// All fixtures here are synthetic. No transcript content from any real session is used.

// dispCall is one tool call plus its result, for building sequential follow-up fixtures.
type dispCall struct {
	name    string
	input   map[string]any
	result  string
	isError bool
}

const graphCommand = `entire graph search --repo . --query "x"`

func graphSearch(result string) dispCall {
	return dispCall{name: "Bash", input: map[string]any{"command": graphCommand}, result: result}
}

func readFile(path, result string) dispCall {
	return dispCall{name: "Read", input: map[string]any{"file_path": path}, result: result}
}

func readSpan(path string, offset, limit int, result string) dispCall {
	return dispCall{name: "Read", input: map[string]any{"file_path": path, "offset": offset, "limit": limit}, result: result}
}

func bashCall(command, result string) dispCall {
	return dispCall{name: "Bash", input: map[string]any{"command": command}, result: result}
}

func grepCall() dispCall {
	return dispCall{name: "Grep", input: map[string]any{"pattern": "x"}, result: "m"}
}

func editCall() dispCall {
	return dispCall{name: "Edit", input: map[string]any{"file_path": "/repo/z.go", "old_string": "a", "new_string": "b"}, result: "ok"}
}

func editPath(path string) dispCall {
	return dispCall{name: "Edit", input: map[string]any{"file_path": path, "old_string": "a", "new_string": "b"}, result: "ok"}
}

func fillers(n int) []dispCall {
	calls := make([]dispCall, n)
	for i := range calls {
		calls[i] = editCall()
	}
	return calls
}

// resultLine is a tool_result record, optionally flagged is_error.
func resultLine(t *testing.T, ts, id, content string, isError bool) string {
	t.Helper()
	block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}
	if isError {
		block["is_error"] = true
	}
	return encodeLine(t, map[string]any{
		"type": "user", "timestamp": ts,
		"message": map[string]any{"role": "user", "content": []any{block}},
	})
}

// batchLine is ONE assistant message carrying several parallel tool_use blocks.
func batchLine(t *testing.T, ts string, uses ...any) string {
	t.Helper()
	return encodeLine(t, map[string]any{
		"type": "assistant", "timestamp": ts,
		"message": map[string]any{"role": "assistant", "content": uses},
	})
}

func dispLines(t *testing.T, name string, calls []dispCall) []string {
	t.Helper()
	ts := statsTime(0)
	lines := make([]string, 0, 2*len(calls))
	for i, call := range calls {
		id := fmt.Sprintf("%s-%d", name, i)
		lines = append(lines, toolUseLine(t, ts, call.name, id, call.input), resultLine(t, ts, id, call.result, call.isError))
	}
	return lines
}

func writeDispSession(t *testing.T, dir, name string, calls ...dispCall) {
	t.Helper()
	writeTranscript(t, dir, name+".jsonl", dispLines(t, name, calls)...)
}

func dispReport(t *testing.T, sessions string) map[string]any {
	t.Helper()
	return runStatsRawJSON(t, "--repo", t.TempDir(), "--sessions-dir", sessions, "--since", "all", "--format", "json")
}

var followUpKeys = map[string]string{
	"none": "graph_locate_no_follow_up", "requery": "graph_locate_requery",
	"overlapping": "graph_locate_follow_up_read_overlapping", "different": "graph_locate_follow_up_read_different_region",
	"unknown": "graph_locate_follow_up_read_unknown_span", "censored": "graph_locate_censored",
	"error": "graph_locate_error", "ineligible": "graph_locate_ineligible",
}

// assertFollowUp checks EVERY class count; classes not named must be zero.
func assertFollowUp(t *testing.T, raw map[string]any, want map[string]int64) {
	t.Helper()
	for class, key := range followUpKeys {
		if got := rawInt(t, raw, key); got != want[class] {
			t.Errorf("%s = %d, want %d", key, got, want[class])
		}
	}
	reads := want["overlapping"] + want["different"] + want["unknown"]
	if got := rawInt(t, raw, "graph_locate_follow_up_read"); got != reads {
		t.Errorf("graph_locate_follow_up_read = %d, want %d", got, reads)
	}
}

func sessionOf(t *testing.T, calls ...dispCall) map[string]any {
	t.Helper()
	dir := t.TempDir()
	writeDispSession(t, dir, "s", calls...)
	return dispReport(t, dir)
}

// namedStats is a locator: it names a file but delivers no source lines.
const namedStats = `{"results":[{"file_path":"internal/cli/stats.go","start_line":770}]}`

// searchBody is a complete search response delivering `lines` source lines of path from start.
func searchBody(t *testing.T, path string, start, lines int) string {
	t.Helper()
	snippet := make([]string, lines)
	for i := range snippet {
		snippet[i] = fmt.Sprintf("line %d", start+i)
	}
	raw, err := json.Marshal(map[string]any{"results": []any{map[string]any{
		"file_path": path, "start_line": start, "end_line": start + lines - 1,
		"snippet_start_line": start, "snippet": strings.Join(snippet, "\n"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestFollowUpWindowIsFive pins K. Changing it changes every published rate, so it must be a
// deliberate, reviewed edit (and the PR must re-run the corpus sensitivity), never a drive-by.
func TestFollowUpWindowIsFive(t *testing.T) {
	t.Parallel()
	if followUpWindow != 5 {
		t.Fatalf("followUpWindow = %d, want 5", followUpWindow)
	}
	if got := rawInt(t, sessionOf(t, graphSearch(namedStats)), "follow_up_window_calls"); got != 5 {
		t.Fatalf("follow_up_window_calls = %d, want 5", got)
	}
}

func TestFollowUpClassifiesEachClass(t *testing.T) {
	t.Parallel()
	with := func(calls ...dispCall) []dispCall { return calls }
	for _, tc := range []struct {
		name  string
		calls []dispCall
		want  string
	}{
		{"censored-short-tail", with(graphSearch(namedStats), editCall(), readFile("/repo/other.go", "o")), "censored"},
		{"no-follow-up-full-window", append(with(graphSearch(namedStats)), fillers(5)...), "none"},
		{"requery-grep-tool", with(graphSearch(namedStats), grepCall()), "requery"},
		{"requery-glob-tool", with(graphSearch(namedStats), dispCall{name: "Glob", input: map[string]any{"pattern": "*.go"}, result: "m"}), "requery"},
		{"requery-shell-rg", with(graphSearch(namedStats), bashCall("cd /repo && rg -n foo .", "m")), "requery"},
		{"requery-graph-lookup", with(graphSearch(namedStats), bashCall("entire graph def Foo", "d")), "requery"},
		{"read-tool-of-locator-is-unknown-span", with(graphSearch(namedStats), readFile("/abs/repo/internal/cli/stats.go", "body")), "unknown"},
		{"shell-sed-read-is-unknown-span", with(graphSearch(namedStats), bashCall("sed -n '760,790p' internal/cli/stats.go", "body")), "unknown"},
		// Graph pointed at X, agent read Y: not a follow-up read, and not a re-query.
		{"read-other-file-full-window", append(with(graphSearch(namedStats), readFile("/abs/repo/internal/cli/help.go", "b")), fillers(4)...), "none"},
		{"read-other-file-short-tail", with(graphSearch(namedStats), readFile("/abs/repo/internal/cli/help.go", "b")), "censored"},
		{"same-basename-different-dir", append(with(graphSearch(namedStats), readFile("/abs/repo/other/stats.go", "b")), fillers(4)...), "none"},
		{"prefix-lookalike", append(with(graphSearch(namedStats), readFile("/abs/repo/internal/cli/xstats.go", "b")), fillers(4)...), "none"},
		{"read-then-requery", with(graphSearch(namedStats), readFile("/r/internal/cli/stats.go", "b"), grepCall()), "unknown"},
		{"requery-then-read", with(graphSearch(namedStats), grepCall(), readFile("/r/internal/cli/stats.go", "b")), "requery"},
		{"write-is-not-read", append(with(graphSearch(namedStats), bashCall("cat > internal/cli/stats.go <<'EOF'\nx\nEOF", "")), fillers(4)...), "none"},
		{"pipe-tail-grep-is-not-requery", append(with(graphSearch(namedStats), bashCall("go test ./... 2>&1 | grep FAIL", "ok")), fillers(4)...), "none"},
		{"meta-graph-verb-is-not-requery", append(with(graphSearch(namedStats), bashCall("entire graph stats --repo .", "s")), fillers(4)...), "none"},
		{"error-result", append(with(dispCall{name: "Bash", input: map[string]any{"command": graphCommand}, result: "boom internal/cli/stats.go", isError: true}), fillers(5)...), "error"},
		{"empty-result", append(with(graphSearch("")), fillers(5)...), "ineligible"},
		{"no-path-named", append(with(graphSearch(`{"results":[]}`)), fillers(5)...), "ineligible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertFollowUp(t, sessionOf(t, tc.calls...), map[string]int64{tc.want: 1})
		})
	}
}

// TestFollowUpWindowBoundary: a qualifying call at exactly K calls after delivery counts; at K+1
// it is outside the window and the full window makes the result NO FOLLOW-UP.
func TestFollowUpWindowBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		fillers  int
		last     dispCall
		inWindow string
	}{
		{"requery", followUpWindow - 1, grepCall(), "requery"},
		{"read", followUpWindow - 1, readFile("/r/internal/cli/stats.go", "b"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := append(append([]dispCall{graphSearch(namedStats)}, fillers(tc.fillers)...), tc.last)
			assertFollowUp(t, sessionOf(t, calls...), map[string]int64{tc.inWindow: 1})
			calls = append(append([]dispCall{graphSearch(namedStats)}, fillers(tc.fillers+1)...), tc.last)
			assertFollowUp(t, sessionOf(t, calls...), map[string]int64{"none": 1})
		})
	}
}

// TestFollowUpErrorAtEOFIsNotCredited is the review's D1 falsifier: an earlier 4,000-byte read,
// then a failed 100-byte graph result, then EOF. The previous draft classified it DISPLACED and
// reported +3,900 bytes (+975 est. tokens). It must be ERROR, earn nothing, and cost its bytes.
func TestFollowUpErrorAtEOFIsNotCredited(t *testing.T) {
	t.Parallel()
	failure := strings.Repeat("f", 100)
	raw := sessionOf(t,
		readFile("/r/a.go", strings.Repeat("e", 4000)),
		dispCall{name: "Bash", input: map[string]any{"command": graphCommand}, result: failure, isError: true},
	)
	assertFollowUp(t, raw, map[string]int64{"error": 1})
	if got := rawInt(t, raw, "modeled_balance_bytes"); got != -100 {
		t.Fatalf("modeled_balance_bytes = %d, want -100", got)
	}
	if got := rawInt(t, raw, "modeled_balance_credited_results"); got != 0 {
		t.Fatalf("credited = %d, want 0", got)
	}
	if got := raw["no_follow_up_rate"].(float64); got != 0 {
		t.Fatalf("no_follow_up_rate = %v, want 0", got)
	}
	// An empty result at EOF earns nothing either (it used to take the whole 4,000-byte credit).
	raw = sessionOf(t, readFile("/r/a.go", strings.Repeat("e", 4000)), graphSearch(""))
	assertFollowUp(t, raw, map[string]int64{"ineligible": 1})
	if got := rawInt(t, raw, "modeled_balance_bytes"); got != 0 {
		t.Fatalf("empty result: modeled_balance_bytes = %d, want 0", got)
	}
	// A successful body at immediate EOF has an unobserved tail: censored, not credited.
	body := searchBody(t, "a.go", 1, 3)
	raw = sessionOf(t, readFile("/r/b.go", strings.Repeat("e", 4000)), graphSearch(body))
	assertFollowUp(t, raw, map[string]int64{"censored": 1})
	if got := rawInt(t, raw, "modeled_balance_bytes"); got != -int64(len(body)) {
		t.Fatalf("censored: modeled_balance_bytes = %d, want %d", got, -len(body))
	}
	// Control: the same successful result followed by a matching read is classified as a read, so
	// the fixture is classifying real calls rather than dropping everything.
	raw = sessionOf(t, readFile("/r/b.go", strings.Repeat("e", 4000)), graphSearch(body), readSpan("/r/a.go", 1, 3, "x"))
	assertFollowUp(t, raw, map[string]int64{"overlapping": 1})
}

// TestFollowUpWindowStartsAtDelivery is the review's D2: calls issued in the same parallel batch
// as the graph call, or while it was outstanding, are not reactions to its result.
func TestFollowUpWindowStartsAtDelivery(t *testing.T) {
	t.Parallel()
	ts := statsTime(0)
	named := searchBody(t, "src/a.go", 1, 3)

	t.Run("parallel-batch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeTranscript(t, dir, "s.jsonl",
			batchLine(t, ts,
				toolUseBlock("Bash", "g1", map[string]any{"command": graphCommand}),
				toolUseBlock("Read", "r1", map[string]any{"file_path": "/repo/src/a.go"}),
				toolUseBlock("Grep", "q1", map[string]any{"pattern": "x"})),
			resultLine(t, ts, "r1", "body", false),
			resultLine(t, ts, "q1", "m", false),
			resultLine(t, ts, "g1", named, false),
		)
		// Neither the batched Read nor the batched Grep reacts to g1; nothing follows delivery.
		assertFollowUp(t, dispReport(t, dir), map[string]int64{"censored": 1})
	})

	t.Run("outstanding-then-immediate-read", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		lines := []string{toolUseLine(t, ts, "Bash", "g1", map[string]any{"command": graphCommand})}
		lines = append(lines, dispLines(t, "f", fillers(followUpWindow))...)
		lines = append(lines, resultLine(t, ts, "g1", named, false))
		lines = append(lines, dispLines(t, "r", []dispCall{readFile("/repo/src/a.go", "body")})...)
		writeTranscript(t, dir, "s.jsonl", lines...)
		// The first call after delivery is a matching read. A request-relative window would have
		// missed it and called this DISPLACED. A whole-file read covers the delivered lines.
		assertFollowUp(t, dispReport(t, dir), map[string]int64{"overlapping": 1})
	})
}

// --- the audit contract's nine fixtures -------------------------------------------------

// 1. A complete delivered body followed by a disjoint read is not a covered reread.
func TestFollowUpContract1DisjointReadIsDifferentRegion(t *testing.T) {
	t.Parallel()
	raw := sessionOf(t, graphSearch(searchBody(t, "a.go", 10, 3)), readSpan("/r/a.go", 100, 10, "x"))
	assertFollowUp(t, raw, map[string]int64{"different": 1})
}

// 2. Clipped output: a surviving header is not delivered source.
func TestFollowUpContract2ClippedBodyIsNotCoverage(t *testing.T) {
	t.Parallel()
	full := searchBody(t, "a.go", 10, 20)
	clipped := full[:strings.Index(full, `"snippet":`)+len(`"snippet":"line 10`)] // header + one partial line
	raw := sessionOf(t, graphSearch(clipped), readSpan("/r/a.go", 10, 3, "x"))
	assertFollowUp(t, raw, map[string]int64{"unknown": 1})
	// A parseable response whose snippet delivered only 2 lines covers only those 2, whatever
	// end_line claims.
	short := `{"results":[{"file_path":"a.go","start_line":10,"end_line":30,"snippet_start_line":10,"snippet":"l10\nl11"}]}`
	raw = sessionOf(t, graphSearch(short), readSpan("/r/a.go", 20, 5, "x"))
	assertFollowUp(t, raw, map[string]int64{"different": 1})
}

// 3. Two graph calls then one read: the read is attributed once.
func TestFollowUpContract3NoDoubleAttribution(t *testing.T) {
	t.Parallel()
	body := searchBody(t, "a.go", 1, 3)
	// Sequential: the second call is a re-query of the first; the read belongs to the second.
	raw := sessionOf(t, graphSearch(body), graphSearch(body), readFile("/r/a.go", "x"))
	assertFollowUp(t, raw, map[string]int64{"requery": 1, "overlapping": 1})
	// Parallel: both delivered before the read. Only the latest delivery gets it.
	ts := statsTime(0)
	dir := t.TempDir()
	lines := []string{
		batchLine(t, ts,
			toolUseBlock("Bash", "g1", map[string]any{"command": graphCommand}),
			toolUseBlock("Bash", "g2", map[string]any{"command": graphCommand})),
		resultLine(t, ts, "g1", body, false),
		resultLine(t, ts, "g2", body, false),
	}
	lines = append(lines, dispLines(t, "r", []dispCall{readFile("/r/a.go", "x")})...)
	writeTranscript(t, dir, "s.jsonl", lines...)
	assertFollowUp(t, dispReport(t, dir), map[string]int64{"overlapping": 1, "censored": 1})

	// Which one gets it: the LATEST delivery. g1 delivered lines 1-3, g2 (delivered last) lines
	// 50-52 of the same file; a read of lines 1-3 attributed to g2 is a different-region read.
	dir = t.TempDir()
	lines = []string{
		batchLine(t, ts,
			toolUseBlock("Bash", "g1", map[string]any{"command": graphCommand}),
			toolUseBlock("Bash", "g2", map[string]any{"command": graphCommand})),
		resultLine(t, ts, "g1", searchBody(t, "a.go", 1, 3), false),
		resultLine(t, ts, "g2", searchBody(t, "a.go", 50, 3), false),
	}
	lines = append(lines, dispLines(t, "r", []dispCall{readSpan("/r/a.go", 1, 3, "x")})...)
	writeTranscript(t, dir, "s.jsonl", lines...)
	assertFollowUp(t, dispReport(t, dir), map[string]int64{"different": 1, "censored": 1})
}

// 4. Out-of-order completion binds by tool_use id; a subagent's calls never enter the parent's
// window, whatever their timestamps.
func TestFollowUpContract4OutOfOrderAndSubagentBinding(t *testing.T) {
	t.Parallel()
	ts := statsTime(0)
	dir := t.TempDir()
	lines := []string{
		batchLine(t, ts,
			toolUseBlock("Bash", "g1", map[string]any{"command": graphCommand}),
			toolUseBlock("Bash", "g2", map[string]any{"command": graphCommand})),
		resultLine(t, ts, "g2", searchBody(t, "b.go", 1, 3), false),
		resultLine(t, ts, "g1", searchBody(t, "a.go", 1, 3), false),
	}
	lines = append(lines, dispLines(t, "p", append([]dispCall{readFile("/r/b.go", "x")}, fillers(4)...))...)
	writeTranscript(t, dir, "s.jsonl", lines...)
	// The subagent, with EARLIER timestamps, reads a.go immediately.
	early := statsTime(-3600e9)
	writeTranscript(t, dir, "s/subagents/agent-1.jsonl",
		toolUseLine(t, early, "Read", "sub-r1", map[string]any{"file_path": "/r/a.go"}),
		resultLine(t, early, "sub-r1", "x", false))
	assertFollowUp(t, dispReport(t, dir), map[string]int64{"overlapping": 1, "none": 1})
}

// 5. A failed or empty graph result followed by grep is never credited.
func TestFollowUpContract5FailedOrEmptyThenGrep(t *testing.T) {
	t.Parallel()
	raw := sessionOf(t,
		readFile("/r/z.go", strings.Repeat("e", 400)),
		dispCall{name: "Bash", input: map[string]any{"command": graphCommand}, result: "error: index missing for a.go", isError: true},
		grepCall())
	assertFollowUp(t, raw, map[string]int64{"error": 1})
	if got := rawInt(t, raw, "modeled_balance_credited_results"); got != 0 {
		t.Fatalf("credited = %d", got)
	}
	raw = sessionOf(t, readFile("/r/z.go", strings.Repeat("e", 400)), graphSearch(""), grepCall())
	assertFollowUp(t, raw, map[string]int64{"ineligible": 1})
}

// 6. A session that ends right after a graph call is censored, not a success.
func TestFollowUpContract6SessionEndIsCensored(t *testing.T) {
	t.Parallel()
	body := searchBody(t, "a.go", 1, 3)
	raw := sessionOf(t, readFile("/r/z.go", strings.Repeat("e", 4000)), graphSearch(body))
	assertFollowUp(t, raw, map[string]int64{"censored": 1})
	if raw["censored_rate"].(float64) != 1 || raw["no_follow_up_rate"].(float64) != 0 {
		t.Fatalf("rates: censored %v no-follow-up %v", raw["censored_rate"], raw["no_follow_up_rate"])
	}
}

// 7. A read of the same path after an edit cannot reuse the delivered lines.
func TestFollowUpContract7ReadAfterEditIsStale(t *testing.T) {
	t.Parallel()
	body := searchBody(t, "a.go", 10, 3)
	raw := sessionOf(t, graphSearch(body), editPath("/r/a.go"), readSpan("/r/a.go", 10, 3, "x"))
	assertFollowUp(t, raw, map[string]int64{"unknown": 1})
	// Control: without the edit the same read covers delivered lines.
	raw = sessionOf(t, graphSearch(body), editCall(), readSpan("/r/a.go", 10, 3, "x"))
	assertFollowUp(t, raw, map[string]int64{"overlapping": 1})
}

// 8. A read partly inside and partly outside the delivered span is overlapping, and no read earns
// any byte credit, so there is no all-or-nothing credit to get wrong.
func TestFollowUpContract8PartialOverlap(t *testing.T) {
	t.Parallel()
	body := searchBody(t, "a.go", 10, 11) // lines 10-20
	raw := sessionOf(t, readFile("/r/z.go", strings.Repeat("e", 1000)), graphSearch(body), readSpan("/r/a.go", 15, 20, "x"))
	assertFollowUp(t, raw, map[string]int64{"overlapping": 1})
	if got := rawInt(t, raw, "modeled_balance_bytes"); got != -int64(len(body)) {
		t.Fatalf("modeled_balance_bytes = %d, want %d", got, -len(body))
	}
}

// 9. Negative, exact zero, absent comparison and missing usage stay distinct, and #279's signed and
// legacy JSON contract is preserved.
func TestFollowUpContract9SignedAccountingPreserved(t *testing.T) {
	t.Parallel()
	body := searchBody(t, "a.go", 1, 3)
	n := len(body)
	text := func(dir string) string {
		return runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, false)
	}

	negative := t.TempDir()
	writeDispSession(t, negative, "s", append([]dispCall{readFile("/r/z.go", "eeeeeeeeee"), graphSearch(body)}, fillers(5)...)...)
	raw := dispReport(t, negative)
	if got := rawInt(t, raw, "modeled_balance_bytes"); got != int64(10-n) {
		t.Fatalf("negative: modeled_balance_bytes = %d, want %d", got, 10-n)
	}
	if rawInt(t, raw, "estimated_savings_bytes") != 0 || rawInt(t, raw, "estimated_savings_bytes_unfloored") != int64(10-n) {
		t.Fatalf("negative: legacy floored/unfloored = %d/%d", rawInt(t, raw, "estimated_savings_bytes"), rawInt(t, raw, "estimated_savings_bytes_unfloored"))
	}
	if !strings.Contains(text(negative), fmt.Sprintf("modeled balance %s est. tokens", humanInt(int64(10-n)/4))) {
		t.Fatalf("negative headline:\n%s", text(negative))
	}

	zero := t.TempDir()
	writeDispSession(t, zero, "s", append([]dispCall{readFile("/r/z.go", strings.Repeat("e", n)), graphSearch(body)}, fillers(5)...)...)
	raw = dispReport(t, zero)
	if rawInt(t, raw, "modeled_balance_bytes") != 0 || rawInt(t, raw, "sessions_with_savings_comparison") != 1 {
		t.Fatalf("zero must be an AVAILABLE zero: %v", raw)
	}
	if !strings.Contains(text(zero), "modeled balance 0 est. tokens") {
		t.Fatalf("zero headline:\n%s", text(zero))
	}

	absent := t.TempDir()
	writeDispSession(t, absent, "s", append([]dispCall{graphSearch(body)}, fillers(5)...)...)
	raw = dispReport(t, absent)
	if rawInt(t, raw, "sessions_with_savings_comparison") != 0 || rawInt(t, raw, "modeled_balance_bytes") != 0 {
		t.Fatalf("absent: %v", raw)
	}
	if got := text(absent); !strings.Contains(got, "modeled balance unavailable") || strings.Contains(got, "est. tokens") {
		t.Fatalf("absent headline:\n%s", got)
	}

	// None of these fixtures carries usage: the report still renders and says so as zero usage,
	// without changing any modeled figure.
	if got := raw["session_tokens"].(map[string]any)["total_tokens"].(float64); got != 0 {
		t.Fatalf("missing usage: total_tokens = %v", got)
	}
}

// --- attribution, rendering and contract details -----------------------------------------

func TestFollowUpNetCreditsOnlyNoFollowUpAcrossSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := searchBody(t, "a.go", 1, 1)
	b := int64(len(body))
	// Session a: one no-follow-up result, exploration mean 100 -> 100 - b.
	writeDispSession(t, dir, "a", append([]dispCall{readFile("/r/x.go", strings.Repeat("e", 100)), graphSearch(body)}, fillers(5)...)...)
	// Session b: exploration mean 300; a re-queried result then a censored tail -> 0 - 2b.
	writeDispSession(t, dir, "b", dispCall{name: "Grep", input: map[string]any{"pattern": "q"}, result: strings.Repeat("e", 300)},
		graphSearch(body), graphSearch(body))
	// Session c sorts after b and starts with a Grep: it must NOT reach into b's window.
	writeDispSession(t, dir, "c", grepCall())
	raw := dispReport(t, dir)
	assertFollowUp(t, raw, map[string]int64{"none": 1, "requery": 1, "censored": 1})
	if got, want := rawInt(t, raw, "modeled_balance_bytes"), 100-3*b; got != want {
		t.Fatalf("modeled_balance_bytes = %d, want %d", got, want)
	}
	if rawInt(t, raw, "modeled_balance_credited_results") != 1 || rawInt(t, raw, "comparable_graph_locate_results") != 3 {
		t.Fatalf("credited/comparable = %d/%d", rawInt(t, raw, "modeled_balance_credited_results"), rawInt(t, raw, "comparable_graph_locate_results"))
	}
	if got := raw["no_follow_up_rate"].(float64); got != 0.3333 {
		t.Fatalf("no_follow_up_rate = %v", got)
	}
}

func TestFollowUpReplayCountsOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	parent := dispLines(t, "p", append([]dispCall{graphSearch(namedStats)}, fillers(5)...))
	writeTranscript(t, dir, "s.jsonl", parent...)
	writeTranscript(t, dir, "s/subagents/agent-1.jsonl", append(parent, dispLines(t, "sub", []dispCall{grepCall()})...)...)
	assertFollowUp(t, dispReport(t, dir), map[string]int64{"none": 1})
}

// When the first copy of a locate call has no result, the replay that carries the result also
// carries the class.
func TestFollowUpReplayCanCompleteAClass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := statsTime(0)
	use := toolUseLine(t, ts, "Bash", "g1", map[string]any{"command": graphCommand})
	writeTranscript(t, dir, "s.jsonl", use,
		toolUseLine(t, ts, "Read", "e0", map[string]any{"file_path": "/r/b.go"}),
		resultLine(t, ts, "e0", "eeee", false))
	writeTranscript(t, dir, "s/subagents/agent-1.jsonl", use,
		resultLine(t, ts, "g1", `{"file_path":"a.go"}`, false),
		toolUseLine(t, ts, "Read", "e1", map[string]any{"file_path": "/r/a.go"}),
		resultLine(t, ts, "e1", "eeee", false))
	assertFollowUp(t, dispReport(t, dir), map[string]int64{"unknown": 1})
}

func TestFollowUpHeadlineIsObservationalAndNeverASavingsClaim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := searchBody(t, "a.go", 1, 1)
	writeDispSession(t, dir, "s",
		append([]dispCall{graphSearch(body), {name: "Grep", input: map[string]any{"pattern": "x"}, result: strings.Repeat("e", 400)}, graphSearch(body)},
			fillers(5)...)...)
	text := runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, false)
	balance := (400 - 2*int64(len(body))) / 4
	want := fmt.Sprintf("[entire-graph] observed follow-up of 2 graph locate results (next 5 calls after delivery): "+
		"re-query 50%%, read named file 0%%, none 50%%, censored 0%%, error/ineligible 0%%; modeled balance +%d est. tokens "+
		"if each of 1 no-follow-up results (of 2 in 1 sessions with both result types) replaced one average exploration "+
		"result (assumed; not measured savings)\n", balance)
	if text != want {
		t.Fatalf("headline =\n%q\nwant\n%q", text, want)
	}
	for _, forbidden := range []string{"displaced", "saved", "savings:"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("headline carries %q", forbidden)
		}
	}
	verbose := runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, true)
	for _, line := range strings.Split(verbose, "\n") {
		if strings.Contains(line, "est. tokens") && (strings.Contains(line, "balance") || strings.Contains(line, "model:")) &&
			!strings.Contains(line, "%") {
			t.Errorf("a modeled number is printed without a rate: %q", line)
		}
	}
}

func TestFollowUpNoLocateResults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDispSession(t, dir, "s", readFile("/r/a.go", "e"))
	text := runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, false)
	if !strings.Contains(text, "observed follow-up: no graph locate results") {
		t.Fatalf("headline:\n%s", text)
	}
}

func TestGraphDeliveredSpans(t *testing.T) {
	t.Parallel()
	spans := graphDeliveredSpans(searchBody(t, "./a.go", 10, 3))
	if len(spans["a.go"]) != 1 || spans["a.go"][0] != (lineSpan{10, 12}) {
		t.Fatalf("spans = %v", spans)
	}
	if got := graphDeliveredSpans(`{"results":[{"file_path":"a.go","snippet_start_line":5,"snippet":""}]}`); len(got["a.go"]) != 0 {
		t.Fatalf("a locator delivers no lines: %v", got)
	}
	if got := graphDeliveredSpans(`{"results":[{"file_path":"a.go","snippet_start_line":5,"snippet":"x`); got != nil {
		t.Fatalf("clipped output must yield no spans: %v", got)
	}
	if got := readToolSpan(map[string]any{}); got == nil || *got != (lineSpan{1, -1}) {
		t.Fatalf("whole-file read span = %v", got)
	}
	if got := readToolSpan(map[string]any{"offset": float64(15), "limit": float64(20)}); got == nil || *got != (lineSpan{15, 34}) {
		t.Fatalf("offset/limit span = %v", got)
	}
}

func TestPathsOverlapIsWholeComponentSuffix(t *testing.T) {
	t.Parallel()
	named := graphOutputPaths(`{"file_path":"internal/cli/stats.go","symbol_id":"x:Go:internal/cli/stats.go:function:F"} calls fmt.Errorf and store.List at ./main.go:3`)
	for _, want := range []string{"internal/cli/stats.go", "main.go"} {
		if !pathsOverlap(named, []string{want}) {
			t.Errorf("named %v should contain %q", named, want)
		}
	}
	for _, path := range named {
		if strings.Contains(path, "Errorf") || strings.Contains(path, "List") {
			t.Errorf("Go selector read as a path: %q", path)
		}
	}
	for _, tc := range []struct {
		read string
		want bool
	}{
		{"/abs/repo/internal/cli/stats.go", true},
		{"internal/cli/stats.go", true},
		{"./internal/cli/stats.go", true},
		{"cli/stats.go", true},
		{"/abs/repo/internal/cli/xstats.go", false},
		{"/abs/repo/other/stats.go", false},
		{"/abs/repo/internal/cli/stats.go.bak", false},
	} {
		if got := pathsOverlap([]string{"internal/cli/stats.go"}, []string{tc.read}); got != tc.want {
			t.Errorf("pathsOverlap(stats.go, %q) = %v, want %v", tc.read, got, tc.want)
		}
	}
	if pathsOverlap([]string{"stats.go"}, []string{"/abs/repo/xstats.go"}) {
		t.Error("named stats.go must not overlap a read of xstats.go")
	}
	if pathsOverlap([]string{"/abs/repo/xstats.go"}, []string{"stats.go"}) {
		t.Error("named xstats.go must not overlap a relative read of stats.go")
	}
	if !pathsOverlap([]string{"stats.go"}, []string{"/abs/repo/internal/cli/stats.go"}) {
		t.Error("a bare basename the graph named must overlap that file's absolute path")
	}
}

// TestShellDisplacementCommandsPartitionExploration keeps "search" and "read" an exact partition
// of the shell exploration commands, so a newly added explore command must be assigned a class.
func TestShellDisplacementCommandsPartitionExploration(t *testing.T) {
	t.Parallel()
	for word := range bashExploreCommands {
		if shellSearchCommands[word] == shellReadCommands[word] {
			t.Errorf("%q must be in exactly one of shellSearchCommands/shellReadCommands", word)
		}
	}
	for _, set := range []map[string]bool{shellSearchCommands, shellReadCommands} {
		for word := range set {
			if !bashExploreCommands[word] {
				t.Errorf("%q is not an exploration command", word)
			}
		}
	}
	for verb := range graphLocateVerbs {
		if !graphLookupVerbs[verb] {
			t.Errorf("locate verb %q must also be a lookup verb", verb)
		}
	}
	for verb := range graphLookupVerbs {
		if !graphVerbs[verb] {
			t.Errorf("lookup verb %q is not a graph verb", verb)
		}
	}
}

func TestStatsJSONFollowUpKeysAreAdditive(t *testing.T) {
	t.Parallel()
	raw := sessionOf(t, graphSearch(namedStats), readFile("/r/a.go", "eeee"))
	keys := []string{
		"follow_up_window_calls", "graph_locate_follow_up_read", "no_follow_up_rate", "requery_rate",
		"follow_up_read_rate", "censored_rate", "error_rate", "ineligible_rate", "modeled_balance_bytes",
		"modeled_balance_est_tokens", "modeled_balance_credited_results", "comparable_graph_locate_results", "follow_up_model",
		// and every legacy key the statusline and older consumers read
		"estimated_savings_bytes", "estimated_savings_est_tokens_unfloored", "sessions_with_savings_comparison", "substitution_ratio",
	}
	for _, key := range followUpKeys {
		keys = append(keys, key)
	}
	for _, key := range keys {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	if got := raw["substitution_ratio"].(float64); got != 1 {
		t.Errorf("legacy substitution_ratio changed: %v", got)
	}
}

// TestFollowUpCacheNeverMixesClassifierVersions: a memo written by an older classifier carries
// classes with different semantics (a v2 "q" was request-relative). Bumping the schema must make
// such a memo miss entirely; the control proves the memo IS served when the schema matches, so
// the poisoned class would have been visible.
func TestFollowUpCacheNeverMixesClassifierVersions(t *testing.T) {
	t.Parallel()
	if statsCacheSchema != "v3" {
		t.Fatalf("statsCacheSchema = %q: a classifier change needs a schema bump and this test updated", statsCacheSchema)
	}
	ts := statsTime(0)
	const count = 10
	body := searchBody(t, "src/a.go", 1, 3)
	for run := range 2 {
		schema := []string{"v2", statsCacheSchema}[run]
		t.Run(schema, func(t *testing.T) {
			t.Parallel()
			sessions, cacheDir := t.TempDir(), t.TempDir()
			for i := range count {
				// D2 shape: the matching read shares the graph call's batch, so it is not a
				// follow-up; the result's tail is censored under the current classifier.
				writeTranscript(t, sessions, fmt.Sprintf("s%02d.jsonl", i),
					batchLine(t, ts,
						toolUseBlock("Bash", "g1", map[string]any{"command": graphCommand}),
						toolUseBlock("Read", "r1", map[string]any{"file_path": "/repo/src/a.go"})),
					resultLine(t, ts, "r1", "body", false),
					resultLine(t, ts, "g1", body, false))
			}
			args := []string{"--repo", t.TempDir(), "--sessions-dir", sessions, "--since", "all", "--format", "json", "--cache-dir", cacheDir}
			runStatsRawJSON(t, args...)
			artifact := statsCacheArtifact(t, cacheDir)
			raw, err := os.ReadFile(artifact)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			var document statsCacheDocument
			if err := json.NewDecoder(reader).Decode(&document); err != nil {
				t.Fatal(err)
			}
			poisoned := 0
			for key, summary := range document.Entries {
				for i := range summary.Calls {
					if summary.Calls[i].Disp != "" {
						summary.Calls[i].Disp = followUpRequery
						poisoned++
					}
				}
				document.Entries[key] = summary
			}
			if poisoned != count {
				t.Fatalf("precondition: poisoned %d classes, want %d", poisoned, count)
			}
			document.Schema = schema
			var rewritten bytes.Buffer
			writer := gzip.NewWriter(&rewritten)
			if err := json.NewEncoder(writer).Encode(document); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifact, rewritten.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			report := runStatsRawJSON(t, args...)
			if schema == statsCacheSchema {
				// Control: the current-schema memo is served, poison and all.
				if rawInt(t, report, "transcripts_from_cache") != count || rawInt(t, report, "graph_locate_requery") != count {
					t.Fatalf("control: from_cache=%d requery=%d, want %d/%d", rawInt(t, report, "transcripts_from_cache"),
						rawInt(t, report, "graph_locate_requery"), count, count)
				}
				return
			}
			if rawInt(t, report, "transcripts_from_cache") != 0 {
				t.Fatalf("an old-schema memo served %d entries", rawInt(t, report, "transcripts_from_cache"))
			}
			assertFollowUp(t, report, map[string]int64{"censored": count})
		})
	}
}

// Passages attached to a result (--single-resolution keeps them on the parent) are delivered
// source too: a read of a delivered passage line is overlapping, not a different region.
func TestFollowUpDeliveredPassagesCount(t *testing.T) {
	t.Parallel()
	body := `{"results":[{"file_path":"notes.md","snippet_start_line":1,"snippet":"primary","passages":[{"start_line":100,"end_line":100,"focus_line":100,"snippet":"delivered answer"}]}]}`
	for _, tc := range []struct {
		name   string
		offset int
		edit   bool
		want   string
	}{
		{"passage-line", 100, false, "overlapping"},
		{"primary-line", 1, false, "overlapping"},
		{"undelivered-line", 50, false, "different"},
		{"passage-after-edit", 100, true, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := []dispCall{graphSearch(body)}
			if tc.edit {
				calls = append(calls, editPath("/r/notes.md"))
			}
			calls = append(calls, readSpan("/r/notes.md", tc.offset, 1, "x"))
			assertFollowUp(t, sessionOf(t, calls...), map[string]int64{tc.want: 1})
		})
	}
}

// Delivered trailing blank lines keep their coordinates.
func TestFollowUpDeliveredTrailingBlankLines(t *testing.T) {
	t.Parallel()
	body := `{"results":[{"file_path":"a.go","snippet_start_line":10,"snippet_end_line":12,"snippet":"x\n\n"}]}`
	if got := graphDeliveredSpans(body)["a.go"]; len(got) != 1 || got[0] != (lineSpan{10, 12}) {
		t.Fatalf("spans = %v, want [10-12]", got)
	}
	for _, tc := range []struct {
		offset int
		want   string
	}{{10, "overlapping"}, {11, "overlapping"}, {12, "overlapping"}, {13, "different"}} {
		assertFollowUp(t, sessionOf(t, graphSearch(body), readSpan("/r/a.go", tc.offset, 1, "x")), map[string]int64{tc.want: 1})
	}
	// A stated end line caps a presentation terminator; an end line beyond the text is not trusted.
	if got := graphDeliveredSpans(`{"results":[{"file_path":"a.go","snippet_start_line":10,"snippet_end_line":10,"snippet":"x\n"}]}`)["a.go"]; got[0] != (lineSpan{10, 10}) {
		t.Fatalf("terminator not capped: %v", got)
	}
	if got := graphDeliveredSpans(`{"results":[{"file_path":"a.go","snippet_start_line":10,"snippet_end_line":30,"snippet":"x\ny"}]}`)["a.go"]; got[0] != (lineSpan{10, 11}) {
		t.Fatalf("end line beyond the text trusted: %v", got)
	}
}
