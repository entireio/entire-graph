package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixture helpers ---------------------------------------------------------------------

func statsTime(offset time.Duration) string {
	return time.Now().Add(offset).UTC().Format(time.RFC3339)
}

func toolUseLine(t *testing.T, ts, name, id string, input map[string]any) string {
	t.Helper()
	record := map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}},
		},
	}
	return encodeLine(t, record)
}

func toolResultLine(t *testing.T, ts, id string, content any) string {
	t.Helper()
	record := map[string]any{
		"type":      "user",
		"timestamp": ts,
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}},
		},
	}
	return encodeLine(t, record)
}

func usageLine(t *testing.T, ts string, in, cacheWrite, cacheRead, out int64) string {
	t.Helper()
	record := map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "ok"}},
			"usage": map[string]any{
				"input_tokens":                in,
				"cache_creation_input_tokens": cacheWrite,
				"cache_read_input_tokens":     cacheRead,
				"output_tokens":               out,
			},
		},
	}
	return encodeLine(t, record)
}

// usageBlocksLine writes ONE transcript record the way Claude Code actually does it: an
// assistant record carries a `message.id`, one slice of that message's content, and a repeat of
// the whole message's `usage`. Passing id == "" reproduces a record with no id.
func usageBlocksLine(t *testing.T, ts, id string, blocks []any, in, cacheWrite, cacheRead, out int64) string {
	t.Helper()
	message := map[string]any{
		"role":    "assistant",
		"content": blocks,
		"usage": map[string]any{
			"input_tokens":                in,
			"cache_creation_input_tokens": cacheWrite,
			"cache_read_input_tokens":     cacheRead,
			"output_tokens":               out,
		},
	}
	if id != "" {
		message["id"] = id
	}
	return encodeLine(t, map[string]any{"type": "assistant", "timestamp": ts, "message": message})
}

func textBlock(text string) any {
	return map[string]any{"type": "text", "text": text}
}

func toolUseBlock(name, id string, input map[string]any) any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func encodeLine(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeTranscript(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runStatsJSON executes the command and decodes the machine-readable report.
func runStatsJSON(t *testing.T, args ...string) statsResponse {
	t.Helper()
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out}, append([]string{"stats"}, args...)); err != nil {
		t.Fatalf("stats failed: %v\n%s", err, out.String())
	}
	var report statsResponse
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("stats json invalid: %v\n%s", err, out.String())
	}
	return report
}

func findCount(entries []statsCount, name string) (statsCount, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return statsCount{}, false
}

// --- tests -------------------------------------------------------------------------------

func TestStatsCountsGraphVerbsAndExploration(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --repo . --query "login bug"`}),
		toolResultLine(t, now, "g1", `{"results":[{"rank":1}]}`),
		toolUseLine(t, now, "Bash", "g2", map[string]any{"command": `entire graph impact --repo . --symbol Login`}),
		toolResultLine(t, now, "g2", `impact report`),
		toolUseLine(t, now, "Bash", "g3", map[string]any{"command": `entire-graph neighbors --symbol Login`}),
		toolResultLine(t, now, "g3", `neighbors`),
		toolUseLine(t, now, "Read", "r1", map[string]any{"file_path": "/x/a.go"}),
		toolResultLine(t, now, "r1", strings.Repeat("a", 400)),
		toolUseLine(t, now, "Read", "r2", map[string]any{"file_path": "/x/b.go", "offset": 10, "limit": 40}),
		toolResultLine(t, now, "r2", strings.Repeat("b", 40)),
		toolUseLine(t, now, "Grep", "gr1", map[string]any{"pattern": "Login"}),
		toolResultLine(t, now, "gr1", strings.Repeat("c", 80)),
		toolUseLine(t, now, "Glob", "gl1", map[string]any{"pattern": "**/*.go"}),
		toolResultLine(t, now, "gl1", strings.Repeat("d", 8)),
		toolUseLine(t, now, "Bash", "b1", map[string]any{"command": `rtk grep -rn "Login" src/`}),
		toolResultLine(t, now, "b1", strings.Repeat("e", 120)),
		toolUseLine(t, now, "Bash", "b2", map[string]any{"command": `cat src/app.go | head -50`}),
		toolResultLine(t, now, "b2", strings.Repeat("f", 200)),
		// non-exploring shell command must not be counted at all
		toolUseLine(t, now, "Bash", "b3", map[string]any{"command": `go build ./...`}),
		toolResultLine(t, now, "b3", "ok"),
		// an edit is neither graph nor exploration
		toolUseLine(t, now, "Edit", "e1", map[string]any{"file_path": "/x/a.go"}),
		toolResultLine(t, now, "e1", "done"),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")

	if report.GraphCalls != 3 {
		t.Fatalf("graph calls = %d, want 3 (%+v)", report.GraphCalls, report.GraphByVerb)
	}
	for verb, want := range map[string]int{"search": 1, "impact": 1, "neighbors": 1} {
		entry, ok := findCount(report.GraphByVerb, verb)
		if !ok || entry.Calls != want {
			t.Fatalf("verb %q = %+v, want %d calls", verb, entry, want)
		}
	}
	if report.ExplorationCalls != 6 {
		t.Fatalf("exploration calls = %d, want 6 (%+v)", report.ExplorationCalls, report.ExplorationByKind)
	}
	for kind, want := range map[string]int{
		kindReadWhole: 1, kindReadRange: 1, kindGrep: 1, kindGlob: 1, kindBash: 2,
	} {
		entry, ok := findCount(report.ExplorationByKind, kind)
		if !ok || entry.Calls != want {
			t.Fatalf("exploration kind %q = %+v, want %d calls", kind, entry, want)
		}
	}
	// bytes -> est tokens at 4 bytes/token, attributed via tool_use_id
	if entry, _ := findCount(report.ExplorationByKind, kindReadWhole); entry.ReturnedTokens != 100 {
		t.Fatalf("whole-file Read est tokens = %d, want 100", entry.ReturnedTokens)
	}
	if report.ExplorationReturnedBytes != 400+40+80+8+120+200 {
		t.Fatalf("exploration returned bytes = %d", report.ExplorationReturnedBytes)
	}
	if report.Sessions != 1 {
		t.Fatalf("sessions = %d, want 1", report.Sessions)
	}
}

func TestStatsGraphFirstRate(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	// session 1: graph first, then grep -> graph-first
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "a1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "a1", "hits"),
		toolUseLine(t, now, "Grep", "a2", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "a2", "matches"),
	)
	// session 2: grep first, then graph -> not graph-first
	writeTranscript(t, sessions, "s2.jsonl",
		toolUseLine(t, now, "Grep", "b1", map[string]any{"pattern": "y"}),
		toolResultLine(t, now, "b1", "matches"),
		toolUseLine(t, now, "Bash", "b2", map[string]any{"command": `entire graph search --query "y"`}),
		toolResultLine(t, now, "b2", "hits"),
	)
	// session 3: no locate-ish calls at all -> excluded from the denominator
	writeTranscript(t, sessions, "s3.jsonl",
		usageLine(t, now, 10, 20, 30, 40),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")

	if report.Sessions != 3 {
		t.Fatalf("sessions = %d, want 3", report.Sessions)
	}
	if report.SessionsWithLocate != 2 || report.GraphFirstSessions != 1 {
		t.Fatalf("locate sessions = %d, graph-first = %d, want 2 / 1", report.SessionsWithLocate, report.GraphFirstSessions)
	}
	if report.GraphFirstRate != 0.5 {
		t.Fatalf("graph-first rate = %v, want 0.5", report.GraphFirstRate)
	}
	if report.SessionTokens.Total != 100 {
		t.Fatalf("session tokens total = %d, want 100 (%+v)", report.SessionTokens.Total, report.SessionTokens)
	}
}

// TestStatsDeduplicatesUsageByMessageID pins the billed-token accounting rule. Claude Code
// writes one transcript record per content block of an assistant message and repeats that
// message's whole `usage` block on every one of them, so per-record summing bills a turn once
// per block. Usage must be counted once per unique `message.id` (last record wins, because the
// earlier records carry partial output_tokens), while records with no id keep counting per
// record so their tokens are never silently dropped.
func TestStatsDeduplicatesUsageByMessageID(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(-time.Hour)

	writeTranscript(t, sessions, "s1.jsonl",
		// One message split over three records: identical input/cache figures, output_tokens
		// partial on the first two and complete on the last. Counts ONCE, at 10/100/1000/50.
		usageBlocksLine(t, now, "msg_dup", []any{textBlock("thinking")}, 10, 100, 1000, 5),
		usageBlocksLine(t, now, "msg_dup", []any{textBlock("more")}, 10, 100, 1000, 5),
		usageBlocksLine(t, now, "msg_dup", []any{toolUseBlock("Grep", "t1", map[string]any{"pattern": "x"})}, 10, 100, 1000, 50),
		toolResultLine(t, now, "t1", "hit"),
		// A normal single-record message.
		usageBlocksLine(t, now, "msg_solo", []any{textBlock("done")}, 2, 20, 200, 7),
		// Two records with no id at all: not deduplicable, so both must still be counted.
		usageBlocksLine(t, now, "", []any{textBlock("legacy")}, 1, 10, 100, 3),
		usageBlocksLine(t, now, "", []any{textBlock("legacy")}, 1, 10, 100, 3),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")

	want := statsTokens{
		Input:      10 + 2 + 1 + 1,         // 14
		CacheWrite: 100 + 20 + 10 + 10,     // 140
		CacheRead:  1000 + 200 + 100 + 100, // 1400
		Output:     50 + 7 + 3 + 3,         // 63
		Total:      14 + 140 + 1400 + 63,   // 1617
	}
	if report.SessionTokens != want {
		t.Fatalf("session tokens = %+v, want %+v", report.SessionTokens, want)
	}
	// Guard the regression direction: the naive per-record sum would be materially larger.
	const naiveTotal = (10+100+1000+5)*2 + (10 + 100 + 1000 + 50) + (2 + 20 + 200 + 7) + (1+10+100+3)*2
	if report.SessionTokens.Total >= naiveTotal {
		t.Fatalf("session tokens %d did not fall below the per-record sum %d", report.SessionTokens.Total, naiveTotal)
	}
	// The tool_use blocks live in distinct records, so call counting is per block and unchanged
	// by the dedup: one Grep, counted once.
	if report.ExplorationCalls != 1 {
		t.Fatalf("exploration calls = %d, want 1", report.ExplorationCalls)
	}
	entry, ok := findCount(report.ExplorationByKind, kindGrep)
	if !ok || entry.Calls != 1 || entry.ReturnedBytes != int64(len("hit")) {
		t.Fatalf("grep entry = %+v (found=%v), want 1 call / %d bytes", entry, ok, len("hit"))
	}
}

// TestStatsUsageDedupIsPerSession keeps the dedup scoped to a session: two different sessions
// are separate transcripts and must both be counted, and a session's subagent transcripts fold
// into the owning session without re-billing its parent's messages.
func TestStatsUsageDedupIsPerSession(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(-time.Hour)

	writeTranscript(t, sessions, "s1.jsonl",
		usageBlocksLine(t, now, "msg_a", []any{textBlock("a")}, 1, 10, 100, 1),
		usageBlocksLine(t, now, "msg_a", []any{textBlock("a")}, 1, 10, 100, 4),
	)
	writeTranscript(t, sessions, "s2.jsonl",
		usageBlocksLine(t, now, "msg_b", []any{textBlock("b")}, 1, 10, 100, 4),
	)
	// Subagent transcript of s1: its own message, folded into s1's totals.
	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-1.jsonl",
		usageBlocksLine(t, now, "msg_c", []any{textBlock("c")}, 1, 10, 100, 4),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")

	if report.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", report.Sessions)
	}
	want := statsTokens{Input: 3, CacheWrite: 30, CacheRead: 300, Output: 12, Total: 345}
	if report.SessionTokens != want {
		t.Fatalf("session tokens = %+v, want %+v", report.SessionTokens, want)
	}
}

// TestStatsSavingsModelMath pins the savings model. Each credited graph locate call is priced
// against the exploration cost this very session paid per exploration call — both sides measured
// from the transcript — rather than against a guessed whole-file read.
func TestStatsSavingsModelMath(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	// One locate call costing 100 bytes, and a bulk verb whose 9,000 bytes must neither be
	// credited nor allowed to price the locate call.
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --repo . --query "login"`}),
		toolResultLine(t, now, "g1", strings.Repeat("s", 100)),
		toolUseLine(t, now, "Bash", "g2", map[string]any{"command": `entire graph edges --repo . --format ndjson`}),
		toolResultLine(t, now, "g2", strings.Repeat("e", 9000)),
		toolUseLine(t, now, "Grep", "e1", map[string]any{"pattern": "login"}),
		toolResultLine(t, now, "e1", strings.Repeat("a", 500)),
		toolUseLine(t, now, "Read", "e2", map[string]any{"file_path": "/x/a.go"}),
		toolResultLine(t, now, "e2", strings.Repeat("b", 700)),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")

	// exploration measured at (500+700)/2 = 600 bytes/call; the one locate call cost 100.
	const wantBytes = int64(1*(500+700)/2 - 100)
	if report.EstimatedSavingsBytes != wantBytes {
		t.Fatalf("savings bytes = %d, want %d", report.EstimatedSavingsBytes, wantBytes)
	}
	if report.EstimatedSavingsTokens != wantBytes/4 {
		t.Fatalf("savings tokens = %d, want %d", report.EstimatedSavingsTokens, wantBytes/4)
	}
	if report.GraphBytesPerLocateCall != 100 || report.ExplorationBytesPerCall != 600 {
		t.Fatalf("measured per-call cost = graph %v / exploration %v, want 100 / 600",
			report.GraphBytesPerLocateCall, report.ExplorationBytesPerCall)
	}
	if report.CreditedGraphCalls != 1 || report.GraphCalls != 2 || report.GraphLocateCalls != 1 {
		t.Fatalf("credited = %d, graph = %d, locate = %d; want 1 / 2 / 1",
			report.CreditedGraphCalls, report.GraphCalls, report.GraphLocateCalls)
	}
	if report.GraphLocateReturnedBytes != 100 {
		t.Fatalf("locate returned bytes = %d, want 100 (the bulk verb must not be mixed in)",
			report.GraphLocateReturnedBytes)
	}
	if report.SessionsWithPositiveSavings != 1 {
		t.Fatalf("sessions with positive savings = %d, want 1", report.SessionsWithPositiveSavings)
	}
	if report.SavingsModel == "" || !strings.Contains(report.SavingsModel, "assumption") {
		t.Fatalf("savings model caveat missing: %q", report.SavingsModel)
	}
	if report.MedianTrackedFileBytes != 0 {
		t.Fatalf("median_tracked_file_bytes is retired and must stay 0, got %d", report.MedianTrackedFileBytes)
	}
}

// TestStatsSavingsFlooredAtZeroWhenGraphCostsMore is the answer the tool must be willing to
// give: a session whose graph calls returned MORE per call than the exploration they displaced
// saved nothing. This repo's own paired A/B benchmark found graph output larger per call than
// the baseline's, so a model that could not report 0 would be contradicting our own evidence.
func TestStatsSavingsFlooredAtZeroWhenGraphCostsMore(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "tiny"`}),
		toolResultLine(t, now, "g1", strings.Repeat("p", 2000)),
		toolUseLine(t, now, "Grep", "e1", map[string]any{"pattern": "tiny"}),
		toolResultLine(t, now, "e1", strings.Repeat("m", 100)),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.EstimatedSavingsTokens != 0 || report.EstimatedSavingsBytes != 0 {
		t.Fatalf("savings should floor at 0, got %d bytes / %d tokens",
			report.EstimatedSavingsBytes, report.EstimatedSavingsTokens)
	}
	if report.SessionsWithPositiveSavings != 0 {
		t.Fatalf("sessions with positive savings = %d, want 0", report.SessionsWithPositiveSavings)
	}
	// The measurement itself is still reported, so the 0 is auditable rather than mysterious.
	if report.GraphBytesPerLocateCall != 2000 || report.ExplorationBytesPerCall != 100 {
		t.Fatalf("per-call cost = graph %v / exploration %v, want 2000 / 100",
			report.GraphBytesPerLocateCall, report.ExplorationBytesPerCall)
	}
}

// TestStatsSavingsNeedsBothSides: with nothing to compare against, the honest answer is 0, not
// an invented counterfactual.
func TestStatsSavingsNeedsBothSides(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", strings.Repeat("s", 10)),
	)
	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.EstimatedSavingsBytes != 0 {
		t.Fatalf("a session with no exploration to price against must save 0, got %d",
			report.EstimatedSavingsBytes)
	}
}

func TestStatsHandlesMissingSessionsDirGracefully(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"stats", "--repo", repo, "--sessions-dir", missing})
	if err != nil {
		t.Fatalf("stats must exit 0 when no transcripts exist, got %v", err)
	}
	if !strings.Contains(out.String(), "no coding-agent session transcripts for this repo yet") {
		t.Fatalf("missing graceful message:\n%s", out.String())
	}

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", missing, "--format", "json")
	if report.SessionsDirFound || report.Sessions != 0 || report.GraphCalls != 0 {
		t.Fatalf("unexpected report for missing dir: %+v", report)
	}
}

func TestStatsHandlesEmptySessionsDir(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	empty := t.TempDir()

	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"stats", "--repo", repo, "--sessions-dir", empty}); err != nil {
		t.Fatalf("stats must exit 0 on an empty sessions dir, got %v", err)
	}
	if !strings.Contains(out.String(), "no sessions in this window") {
		t.Fatalf("missing empty-window message:\n%s", out.String())
	}
}

func TestStatsSkipsMalformedLines(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	writeTranscript(t, sessions, "s1.jsonl",
		"{not json at all",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		`{"type":"assistant","message":{"content":"a plain string, not blocks"}}`,
		"",
		`[1,2,3]`,
		toolResultLine(t, now, "g1", "hits"),
		toolUseLine(t, now, "Grep", "r1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "r1", "matches"),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.MalformedLinesSkipped != 2 {
		t.Fatalf("malformed lines skipped = %d, want 2", report.MalformedLinesSkipped)
	}
	if report.GraphCalls != 1 || report.ExplorationCalls != 1 {
		t.Fatalf("valid records lost: graph=%d exploration=%d", report.GraphCalls, report.ExplorationCalls)
	}
}

func TestStatsSinceWindowFiltersOldSessions(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	recent := statsTime(-2 * time.Hour)
	old := statsTime(-60 * 24 * time.Hour)

	writeTranscript(t, sessions, "recent.jsonl",
		toolUseLine(t, recent, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, recent, "g1", "hits"),
	)
	writeTranscript(t, sessions, "old.jsonl",
		toolUseLine(t, old, "Bash", "g2", map[string]any{"command": `entire graph search --query "y"`}),
		toolResultLine(t, old, "g2", "hits"),
	)

	windowed := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "7d")
	if windowed.Sessions != 1 || windowed.GraphCalls != 1 {
		t.Fatalf("--since 7d = %d sessions / %d graph calls, want 1 / 1", windowed.Sessions, windowed.GraphCalls)
	}
	all := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if all.Sessions != 2 || all.GraphCalls != 2 {
		t.Fatalf("--since all = %d sessions / %d graph calls, want 2 / 2", all.Sessions, all.GraphCalls)
	}
}

func TestStatsFoldsSubagentTranscriptsIntoOwningSession(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", "hits"),
	)
	writeTranscript(t, sessions, filepath.Join("s1", "subagents", "agent-abc.jsonl"),
		toolUseLine(t, now, "Grep", "sa1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "sa1", "matches"),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.Sessions != 1 {
		t.Fatalf("sessions = %d, want 1 (subagent transcript must fold into its parent)", report.Sessions)
	}
	if report.Transcripts != 2 {
		t.Fatalf("transcripts = %d, want 2", report.Transcripts)
	}
	if report.GraphCalls != 1 || report.ExplorationCalls != 1 {
		t.Fatalf("graph=%d exploration=%d, want 1 / 1", report.GraphCalls, report.ExplorationCalls)
	}
	// the session still counts as graph-first: the graph call came first in the parent
	if report.GraphFirstSessions != 1 {
		t.Fatalf("graph-first sessions = %d, want 1", report.GraphFirstSessions)
	}
}

func TestStatsHandlesBlockShapedToolResults(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Grep", "r1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "r1", []any{
			map[string]any{"type": "text", "text": strings.Repeat("m", 40)},
			map[string]any{"type": "text", "text": strings.Repeat("n", 40)},
		}),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.ExplorationReturnedBytes != 80 || report.ExplorationReturnedTokens != 20 {
		t.Fatalf("block-shaped tool_result = %d bytes / %d tokens, want 80 / 20",
			report.ExplorationReturnedBytes, report.ExplorationReturnedTokens)
	}
}

// TestStatsTextOutputCarriesRatioAndCaveat covers --verbose, which is where the full report
// moved when the default became the single headline line.
func TestStatsTextOutputCarriesRatioAndCaveat(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", "hits"),
		toolUseLine(t, now, "Grep", "r1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "r1", "matches"),
	)

	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"stats", "--repo", repo, "--sessions-dir", sessions, "--since", "all", "--verbose"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"graph calls: 1",
		"exploration calls: 1",
		"graph-first rate: 100%",
		"graph calls by verb",
		"exploration calls (what the graph is meant to replace)",
		"session tokens (billed",
		"ESTIMATED SAVINGS",
		"assumption, not a measurement",
		"measured per-call cost",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestStatsRejectsBadFlags(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	cases := [][]string{
		{"stats", "--repo", repo, "--sessions-dir", sessions, "--format", "ndjson"},
		{"stats", "--repo", repo, "--sessions-dir", sessions, "--since", "yesterday"},
		{"stats", "--repo", repo, "--sessions-dir", sessions, "extra-arg"},
		{"stats", "--since"},
	}
	for _, args := range cases {
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Stdout: &out, Stderr: &out}, args); err == nil {
			t.Fatalf("expected error for %v, got success:\n%s", args, out.String())
		}
	}
}

func TestParseSinceAcceptsHoursDaysWeeksAndAll(t *testing.T) {
	t.Parallel()
	cases := map[string]time.Duration{
		"all": 0, "": 0, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour,
	}
	for input, want := range cases {
		got, err := parseSince(input)
		if err != nil {
			t.Fatalf("parseSince(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parseSince(%q) = %v, want %v", input, got, want)
		}
	}
	for _, bad := range []string{"0d", "-3d", "d", "7y", "seven days"} {
		if _, err := parseSince(bad); err == nil {
			t.Fatalf("parseSince(%q) should have failed", bad)
		}
	}
}

func TestSessionSlugCandidatesMatchClaudeProjectLayout(t *testing.T) {
	t.Parallel()
	got := sessionSlugCandidates(string(filepath.Separator) + filepath.Join("private", "tmp", "pfprod"))
	if got[0] != "-private-tmp-pfprod" {
		t.Fatalf("slug = %q, want -private-tmp-pfprod", got[0])
	}
	dotted := sessionSlugCandidates(string(filepath.Separator) + filepath.Join("Users", "x", "entire.io"))
	if dotted[0] != "-Users-x-entire-io" {
		t.Fatalf("dotted slug = %q, want -Users-x-entire-io", dotted[0])
	}
	if len(dotted) < 2 || dotted[1] != "-Users-x-entire.io" {
		t.Fatalf("expected a raw slash-only fallback candidate, got %v", dotted)
	}
}

func TestIsExploringShellCommandClassification(t *testing.T) {
	t.Parallel()
	explore := []string{
		`grep -rn "foo" .`,
		`rtk grep -n foo bar.go`,
		`sudo find / -name "*.go"`,
		`cat src/app.go`,
		`ls -la | grep foo`,
		`sed -n '1,80p' file.go`,
		"cat a.go\nhead -5 b.go",
	}
	for _, command := range explore {
		if !isExploringShellCommand(command) {
			t.Fatalf("expected %q to count as exploration", command)
		}
	}
	notExplore := []string{
		`go build ./...`,
		`git commit -m "grep"`,
		``,
		`npm test`,
		`FOO=grep make build`,
	}
	for _, command := range notExplore {
		if isExploringShellCommand(command) {
			t.Fatalf("did not expect %q to count as exploration", command)
		}
	}
}

// TestGraphVerbFromCommandIgnoresPathsThatEndInEntireGraph pins a real false positive found
// against live transcripts: a repo checked out at .../entire-graph made every `find` over it
// look like a graph invocation with verb "-path".
func TestGraphVerbFromCommandIgnoresPathsThatEndInEntireGraph(t *testing.T) {
	t.Parallel()
	graphCalls := map[string]string{
		`entire graph search --repo . --query "x"`:            "search",
		`rtk entire graph impact --symbol Foo`:                "impact",
		`cd /repo && entire graph neighbors --symbol Foo`:     "neighbors",
		`/tmp/build/entire-graph stats --repo .`:              "stats",
		`entire graph search --query "x" 2>/dev/null | head`:  "search",
		`entire graph --version`:                              "version",
		`ENTIRE_REPO_ROOT=/r entire graph edges --repo .`:     "edges",
		`./entire-graph index --repo . --cache-dir /tmp/c`:    "index",
		`entire graph diff --base main --head HEAD --json`:    "diff",
		"entire graph search \\\n  --query \"multi line\"":    "search",
		`entire graph symbols --repo . --format ndjson | wc`:  "symbols",
		`entire graph snapshot --repo . > /tmp/snapshot.json`: "snapshot",
	}
	for command, want := range graphCalls {
		got, ok := graphVerbFromCommand(command)
		if !ok || got != want {
			t.Fatalf("graphVerbFromCommand(%q) = %q,%v; want %q,true", command, got, ok, want)
		}
	}

	notGraphCalls := []string{
		`find /private/tmp/scratch/entire-graph -path '*internal/sem/search.go'`,
		`ls -la /repos/entire-graph`,
		`go build -o /tmp/entire-graph ./cmd/entire-graph`,
		`git -C /repos/entire-graph status`,
		`grep -rn "entire graph search" docs/`,
		`echo "run entire graph search first"`,
		`cat /repos/entire-graph/AGENTS.md`,
	}
	for _, command := range notGraphCalls {
		if got, ok := graphVerbFromCommand(command); ok {
			t.Fatalf("graphVerbFromCommand(%q) = %q,true; want no match", command, got)
		}
	}
}

// TestStatsClassifiesPathLookalikeAsExploration is the end-to-end half of the regression: the
// `find` above must land in the exploration bucket, not the graph bucket.
func TestStatsClassifiesPathLookalikeAsExploration(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "f1", map[string]any{
			"command": `find /private/tmp/scratch/entire-graph -path '*internal/sem/search.go'`,
		}),
		toolResultLine(t, now, "f1", "internal/sem/search.go"),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.GraphCalls != 0 {
		t.Fatalf("graph calls = %d, want 0 (%+v)", report.GraphCalls, report.GraphByVerb)
	}
	if report.ExplorationCalls != 1 {
		t.Fatalf("exploration calls = %d, want 1", report.ExplorationCalls)
	}
	if report.GraphFirstSessions != 0 || report.SessionsWithLocate != 1 {
		t.Fatalf("graph-first = %d of %d, want 0 of 1", report.GraphFirstSessions, report.SessionsWithLocate)
	}
}

func TestStatsIsAdvertisedInHelp(t *testing.T) {
	t.Parallel()
	// The root listing advertises the stats command...
	var root bytes.Buffer
	if err := Run(t.Context(), Options{Stdout: &root, Stderr: &root}, []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(root.String(), "\n  stats ") {
		t.Fatalf("root help does not advertise stats:\n%s", root.String())
	}
	// ...and `stats --help` shows its usage line.
	var detail bytes.Buffer
	if err := Run(t.Context(), Options{Stdout: &detail, Stderr: &detail}, []string{"stats", "--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.String(), "entire graph stats") {
		t.Fatalf("stats --help does not show its usage:\n%s", detail.String())
	}
}

func TestHumanIntGroupsThousands(t *testing.T) {
	t.Parallel()
	for input, want := range map[int64]string{0: "0", 42: "42", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := humanInt(input); got != want {
			t.Fatalf("humanInt(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestStatsTranscriptScopesToOneSession(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	// The session we want.
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", "hits"),
		usageLine(t, now, 10, 20, 30, 40),
	)
	// A sibling session in the same directory that must NOT be counted.
	writeTranscript(t, sessions, "s2.jsonl",
		toolUseLine(t, now, "Bash", "g2", map[string]any{"command": `entire graph impact --symbol Y`}),
		toolResultLine(t, now, "g2", "blast radius"),
		usageLine(t, now, 1000, 1000, 1000, 1000),
	)

	report := runStatsJSON(t, "--repo", repo,
		"--transcript", filepath.Join(sessions, "s1.jsonl"), "--format", "json", "--since", "all")

	if report.Sessions != 1 || report.Transcripts != 1 {
		t.Fatalf("sessions/transcripts = %d/%d, want 1/1", report.Sessions, report.Transcripts)
	}
	if report.GraphCalls != 1 {
		t.Fatalf("graph calls = %d, want 1 (%+v)", report.GraphCalls, report.GraphByVerb)
	}
	if _, ok := findCount(report.GraphByVerb, "impact"); ok {
		t.Fatalf("sibling session leaked into the report: %+v", report.GraphByVerb)
	}
	if report.SessionTokens.Total != 10+20+30+40 {
		t.Fatalf("session tokens = %d, want 100", report.SessionTokens.Total)
	}
	if report.SessionsDir != filepath.Join(sessions, "s1.jsonl") || !report.SessionsDirFound {
		t.Fatalf("sessions_dir = %q found=%v", report.SessionsDir, report.SessionsDirFound)
	}
}

func TestStatsTranscriptFoldsSubagents(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", "hits"),
	)
	// Delegated work lives under <session>/subagents/ and belongs to the same session.
	writeTranscript(t, sessions, filepath.Join("s1", "subagents", "agent-1.jsonl"),
		toolUseLine(t, now, "Grep", "e1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "e1", "matches"),
	)

	report := runStatsJSON(t, "--repo", repo,
		"--transcript", filepath.Join(sessions, "s1.jsonl"), "--format", "json", "--since", "all")

	if report.Sessions != 1 {
		t.Fatalf("sessions = %d, want 1 (subagent must fold into its owner)", report.Sessions)
	}
	if report.Transcripts != 2 {
		t.Fatalf("transcripts = %d, want 2", report.Transcripts)
	}
	if report.GraphCalls != 1 || report.ExplorationCalls != 1 {
		t.Fatalf("graph/exploration = %d/%d, want 1/1", report.GraphCalls, report.ExplorationCalls)
	}
	// Graph call came first in the owner, so the folded session is still graph-first.
	if report.GraphFirstSessions != 1 || report.SessionsWithLocate != 1 {
		t.Fatalf("graph-first = %d/%d, want 1/1", report.GraphFirstSessions, report.SessionsWithLocate)
	}
}

func TestStatsTranscriptMissingReportsNotFound(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nope.jsonl")

	report := runStatsJSON(t, "--repo", repo, "--transcript", missing, "--format", "json")
	if report.SessionsDirFound {
		t.Fatal("missing transcript must report sessions_dir_found=false")
	}
	if report.Sessions != 0 || report.GraphCalls != 0 {
		t.Fatalf("missing transcript produced data: %+v", report)
	}
}

func TestStatsTranscriptRejectsSessionsDir(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"stats", "--transcript", "/tmp/a.jsonl", "--sessions-dir", "/tmp"})
	if err == nil {
		t.Fatal("--transcript with --sessions-dir must be rejected")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStatsTranscriptDirectoryIsNotATranscript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	report := runStatsJSON(t, "--repo", t.TempDir(), "--transcript", dir, "--format", "json")
	if report.SessionsDirFound {
		t.Fatal("a directory passed to --transcript must report sessions_dir_found=false")
	}
}

// --- default output shape ----------------------------------------------------------------

// TestStatsDefaultOutputIsOneHeadlineLine pins the reported problem: the default is the number,
// not a report. Everything the old default printed is still one --verbose away.
func TestStatsDefaultOutputIsOneHeadlineLine(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", strings.Repeat("s", 40)),
		toolUseLine(t, now, "Grep", "e1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "e1", strings.Repeat("m", 840)),
	)

	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"stats", "--repo", repo, "--sessions-dir", sessions, "--since", "all"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("default output must be exactly one line, got %d:\n%s", len(lines), text)
	}
	// (840-40) bytes saved -> 200 tokens
	if lines[0] != "[entire-graph] ~200 tokens saved" {
		t.Fatalf("headline = %q, want %q", lines[0], "[entire-graph] ~200 tokens saved")
	}
	for _, unwanted := range []string{"graph calls by verb", "session tokens (billed", "ESTIMATED SAVINGS", "assumption"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("default output leaked the verbose report (%q):\n%s", unwanted, text)
		}
	}

	var verbose bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &verbose, Stderr: &verbose},
		[]string{"stats", "--repo", repo, "--sessions-dir", sessions, "--since", "all", "--verbose"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(verbose.String(), "ESTIMATED SAVINGS  ~200 tokens") {
		t.Fatalf("--verbose must restore the full report:\n%s", verbose.String())
	}
}

// TestStatsHeadlineIsPlainTextWhenOutputIsNotATerminal keeps ANSI bytes out of pipes and files.
func TestStatsHeadlineIsPlainTextWhenOutputIsNotATerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	var out bytes.Buffer
	if got := statsGreen(&out, "[entire-graph] ~1 tokens saved"); strings.Contains(got, "\x1b[") {
		t.Fatalf("a non-terminal writer must not receive escapes, got %q", got)
	}
	t.Setenv("FORCE_COLOR", "1")
	if got := statsGreen(&out, "x"); got != "\x1b[32mx\x1b[0m" {
		t.Fatalf("forced colour = %q, want a green wrap", got)
	}
	t.Setenv("NO_COLOR", "1")
	if got := statsGreen(&out, "x"); got != "x" {
		t.Fatalf("NO_COLOR must win, got %q", got)
	}
}

// --- windowing / performance --------------------------------------------------------------

// TestStatsPrunesTranscriptsOutsideTheWindowByMtime is the fix for the reported CPU burn: a
// narrow --since must stop the scan from OPENING transcripts it is going to discard anyway. The
// prune is by file mtime, which is sound in one direction only — a file last written before the
// window opened cannot hold a record inside it — so this asserts both directions.
func TestStatsPrunesTranscriptsOutsideTheWindowByMtime(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	recent := statsTime(-2 * time.Hour)
	old := statsTime(-60 * 24 * time.Hour)

	writeTranscript(t, sessions, "recent.jsonl",
		toolUseLine(t, recent, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, recent, "g1", "hits"),
	)
	writeTranscript(t, sessions, "old.jsonl",
		toolUseLine(t, old, "Bash", "g2", map[string]any{"command": `entire graph search --query "y"`}),
		toolResultLine(t, old, "g2", "hits"),
	)
	stale := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(sessions, "old.jsonl"), stale, stale); err != nil {
		t.Fatal(err)
	}

	windowed := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "7d")
	if windowed.TranscriptsConsidered != 2 {
		t.Fatalf("transcripts considered = %d, want 2", windowed.TranscriptsConsidered)
	}
	if windowed.Transcripts != 1 {
		t.Fatalf("--since 7d parsed %d transcript(s); the file whose mtime is 60 days old must never be opened",
			windowed.Transcripts)
	}
	if windowed.Sessions != 1 || windowed.GraphCalls != 1 {
		t.Fatalf("--since 7d = %d sessions / %d graph calls, want 1 / 1", windowed.Sessions, windowed.GraphCalls)
	}

	all := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if all.Transcripts != 2 || all.Sessions != 2 || all.GraphCalls != 2 {
		t.Fatalf("--since all = %d transcripts / %d sessions / %d graph calls, want 2 / 2 / 2",
			all.Transcripts, all.Sessions, all.GraphCalls)
	}
}

// TestStatsKeepsAWholeSessionWhenAnyOfItsTranscriptsIsRecent: pruning decides per SESSION, so a
// freshly-written subagent transcript cannot be separated from the parent that owns it.
func TestStatsKeepsAWholeSessionWhenAnyOfItsTranscriptsIsRecent(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	recent := statsTime(-time.Hour)

	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, recent, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, recent, "g1", "hits"),
	)
	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-1.jsonl",
		toolUseLine(t, recent, "Grep", "e1", map[string]any{"pattern": "x"}),
		toolResultLine(t, recent, "e1", "matches"),
	)
	// The parent's own mtime is stale; only the subagent transcript was written recently.
	stale := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(sessions, "s1.jsonl"), stale, stale); err != nil {
		t.Fatal(err)
	}

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "7d")
	if report.Transcripts != 2 {
		t.Fatalf("transcripts = %d, want 2 (a recent subagent keeps its owner in scope)", report.Transcripts)
	}
	if report.GraphCalls != 1 || report.ExplorationCalls != 1 {
		t.Fatalf("graph/exploration = %d/%d, want 1/1", report.GraphCalls, report.ExplorationCalls)
	}
}

// --- double counting ------------------------------------------------------------------------

// TestStatsCountsARepeatedToolUseOnceWithinASession pins a measured defect. Claude Code replays
// a forked subagent's history into the new transcript, so ONE tool call really does appear in
// several of a session's files: 215 tool_use ids across 3,316 real transcripts on the machine
// that reported this. It is one API call and must be billed once.
func TestStatsCountsARepeatedToolUseOnceWithinASession(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	shared := strings.Repeat("m", 100)

	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-1.jsonl",
		toolUseLine(t, now, "Grep", "shared", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "shared", shared),
	)
	// The forked sibling replays the same call verbatim.
	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-2.jsonl",
		toolUseLine(t, now, "Grep", "shared", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "shared", shared),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.ExplorationCalls != 1 {
		t.Fatalf("exploration calls = %d, want 1 (one API call replayed into two transcripts)",
			report.ExplorationCalls)
	}
	if report.ExplorationReturnedBytes != int64(len(shared)) {
		t.Fatalf("exploration bytes = %d, want %d", report.ExplorationReturnedBytes, len(shared))
	}
}

// TestStatsBillsARepeatedMessageOnceWithinASession is the usage half of the same replay: 168
// usage-bearing message ids appeared in more than one transcript of their own session.
func TestStatsBillsARepeatedMessageOnceWithinASession(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)

	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-1.jsonl",
		usageBlocksLine(t, now, "msg_shared", []any{textBlock("a")}, 1, 10, 100, 5),
	)
	writeTranscript(t, filepath.Join(sessions, "s1", "subagents"), "agent-2.jsonl",
		usageBlocksLine(t, now, "msg_shared", []any{textBlock("a")}, 1, 10, 100, 5),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	want := statsTokens{Input: 1, CacheWrite: 10, CacheRead: 100, Output: 5, Total: 116}
	if report.SessionTokens != want {
		t.Fatalf("session tokens = %+v, want %+v (one message replayed into two transcripts)",
			report.SessionTokens, want)
	}
}

// --- determinism ------------------------------------------------------------------------

// TestStatsOutputIsByteIdenticalAcrossRuns: nothing in the report may depend on map iteration
// order, float accumulation order, or which files happened to be cached.
func TestStatsOutputIsByteIdenticalAcrossRuns(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	for index := 0; index < 6; index++ {
		name := fmt.Sprintf("s%d.jsonl", index)
		writeTranscript(t, sessions, name,
			toolUseLine(t, now, "Bash", fmt.Sprintf("g%d", index), map[string]any{"command": `entire graph search --query "x"`}),
			toolResultLine(t, now, fmt.Sprintf("g%d", index), strings.Repeat("s", 10+index)),
			toolUseLine(t, now, "Bash", fmt.Sprintf("n%d", index), map[string]any{"command": `entire graph neighbors --symbol X`}),
			toolResultLine(t, now, fmt.Sprintf("n%d", index), strings.Repeat("n", 20+index)),
			toolUseLine(t, now, "Grep", fmt.Sprintf("e%d", index), map[string]any{"pattern": "x"}),
			toolResultLine(t, now, fmt.Sprintf("e%d", index), strings.Repeat("m", 300+index)),
			toolUseLine(t, now, "Read", fmt.Sprintf("r%d", index), map[string]any{"file_path": "/x/a.go"}),
			toolResultLine(t, now, fmt.Sprintf("r%d", index), strings.Repeat("r", 700+index)),
			usageBlocksLine(t, now, fmt.Sprintf("msg%d", index), []any{textBlock("done")}, 11, 22, 33, 44),
		)
	}

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		full := append([]string{"stats", "--repo", repo, "--sessions-dir", sessions, "--since", "all"}, args...)
		if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out}, full); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	for _, args := range [][]string{nil, {"--verbose"}, {"--format", "json"}} {
		first, second := run(args...), run(args...)
		if first != second {
			t.Fatalf("stats %v was not byte-identical across two runs:\n--- first ---\n%s\n--- second ---\n%s",
				args, first, second)
		}
	}
}

// --- arithmetic ---------------------------------------------------------------------------

func TestMulDivIsExactAndDoesNotOverflow(t *testing.T) {
	t.Parallel()
	if got := mulDiv(3, 1200, 2); got != 1800 {
		t.Fatalf("mulDiv(3,1200,2) = %d, want 1800", got)
	}
	// The a*b product overflows int64 here; the quotient does not. A naive a*b/c wraps.
	const wide = int64(1) << 40
	if got := mulDiv(wide, wide, wide); got != wide {
		t.Fatalf("mulDiv(2^40,2^40,2^40) = %d, want %d", got, wide)
	}
	// The exact boundary of the overflow guard. bits.Div64 PANICS when the high word is >= the
	// divisor, so `hi >= c` has to be the test and not `hi > c`: here hi and c are both 1.
	if got := mulDiv(1<<32, 1<<32, 1); got != math.MaxInt64 {
		t.Fatalf("mulDiv(2^32,2^32,1) = %d, want MaxInt64", got)
	}
	// Quotient itself out of range: saturate, never wrap negative.
	if got := mulDiv(math.MaxInt64, 4, 2); got != math.MaxInt64 {
		t.Fatalf("mulDiv saturation = %d, want MaxInt64", got)
	}
	if got := mulDiv(math.MaxInt64, math.MaxInt64, 1); got != math.MaxInt64 {
		t.Fatalf("mulDiv saturation = %d, want MaxInt64", got)
	}
	for _, args := range [][3]int64{{0, 5, 1}, {5, 0, 1}, {5, 5, 0}, {-1, 5, 1}} {
		if got := mulDiv(args[0], args[1], args[2]); got != 0 {
			t.Fatalf("mulDiv%v = %d, want 0", args, got)
		}
	}
}

func TestRoundToRoundsHalfAwayFromZero(t *testing.T) {
	t.Parallel()
	// 0.125 and 0.375 are exact in binary, so these pin the tie-breaking rule itself.
	if got := roundTo(0.125, 2); got != 0.13 {
		t.Fatalf("roundTo(0.125,2) = %v, want 0.13", got)
	}
	if got := roundTo(-0.125, 2); got != -0.13 {
		t.Fatalf("roundTo(-0.125,2) = %v, want -0.13 (half away from zero, not toward it)", got)
	}
	if got := roundTo(0.5, 4); got != 0.5 {
		t.Fatalf("roundTo(0.5,4) = %v, want 0.5", got)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := roundTo(bad, 2); got != 0 {
			t.Fatalf("roundTo(%v,2) = %v, want 0", bad, got)
		}
	}
}

// --- the JSON contract ----------------------------------------------------------------------

// TestStatsJSONContractKeepsEveryPublishedKey guards the machine contract. Callers exist —
// scripts/entire-graph-statusline.sh reads two of these on every prompt render — so keys may be
// added but never removed or renamed.
func TestStatsJSONContractKeepsEveryPublishedKey(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(statsResponse{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	published := []string{
		"format_version", "provider", "repo_root", "sessions_dir", "sessions_dir_found", "since",
		"window_start", "window_end", "sessions", "transcripts", "malformed_lines_skipped",
		"graph_calls", "exploration_calls", "sessions_with_locate", "graph_first_sessions",
		"graph_first_rate", "graph_calls_by_verb", "exploration_by_kind", "graph_returned_bytes",
		"graph_returned_est_tokens", "exploration_returned_bytes", "exploration_returned_est_tokens",
		"session_tokens", "estimated_savings_bytes", "estimated_savings_est_tokens",
		"estimated_savings_pct_of_session_tokens", "credited_graph_calls",
		"median_tracked_file_bytes", "savings_model",
	}
	for _, key := range published {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("stats JSON dropped published key %q; the shape may only ever grow", key)
		}
	}
	// The tables must serialise as arrays, never null, for a report with no data.
	report := statsResponse{GraphByVerb: []statsCount{}, ExplorationByKind: []statsCount{}}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"graph_calls_by_verb":[]`) {
		t.Fatalf("empty tables must encode as [], got %s", encoded)
	}
}

// TestStatsJSONIgnoresVerbose: --verbose is a text-rendering choice and must not reshape the
// machine output.
func TestStatsJSONIgnoresVerbose(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", "hits"),
	)
	render := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		full := append([]string{"stats", "--repo", repo, "--sessions-dir", sessions,
			"--since", "all", "--format", "json"}, args...)
		if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out}, full); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if plain, verbose := render(), render("--verbose"); plain != verbose {
		t.Fatalf("--verbose changed the JSON:\n%s\n%s", plain, verbose)
	}
}

// TestStatsUnmatchedCallsDoNotDeflateThePrice: bytes are only observable on a tool_result, so
// the per-call prices must divide by RESULTS seen, not by calls issued. Dividing by calls would
// silently halve a price whenever a call's result was never written to the transcript.
func TestStatsUnmatchedCallsDoNotDeflateThePrice(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, now, "g1", strings.Repeat("s", 100)),
		// a second locate call whose result never landed
		toolUseLine(t, now, "Bash", "g2", map[string]any{"command": `entire graph search --query "y"`}),
		toolUseLine(t, now, "Grep", "e1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "e1", strings.Repeat("m", 800)),
		// likewise an exploration call with no result
		toolUseLine(t, now, "Grep", "e2", map[string]any{"pattern": "y"}),
	)

	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.GraphCalls != 2 || report.ExplorationCalls != 2 {
		t.Fatalf("calls = %d graph / %d exploration, want 2 / 2", report.GraphCalls, report.ExplorationCalls)
	}
	if report.CreditedGraphCalls != 1 {
		t.Fatalf("credited = %d, want 1 (only the call whose result was observed)", report.CreditedGraphCalls)
	}
	if report.GraphBytesPerLocateCall != 100 || report.ExplorationBytesPerCall != 800 {
		t.Fatalf("per-call cost = graph %v / exploration %v, want 100 / 800",
			report.GraphBytesPerLocateCall, report.ExplorationBytesPerCall)
	}
	if report.EstimatedSavingsBytes != 700 {
		t.Fatalf("savings bytes = %d, want 700 (1 credited call x 800 - 100)", report.EstimatedSavingsBytes)
	}
}

// TestStatsIdlessToolUsesAreAllCounted: a block with no tool_use id cannot be recognised as a
// replay of anything, so every one of them must still be counted. Deduplicating on the empty
// string would silently collapse them into one.
func TestStatsIdlessToolUsesAreAllCounted(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Grep", "", map[string]any{"pattern": "x"}),
		toolUseLine(t, now, "Grep", "", map[string]any{"pattern": "y"}),
		toolUseLine(t, now, "Grep", "", map[string]any{"pattern": "z"}),
	)
	report := runStatsJSON(t, "--repo", repo, "--sessions-dir", sessions, "--format", "json", "--since", "all")
	if report.ExplorationCalls != 3 {
		t.Fatalf("exploration calls = %d, want 3 (id-less calls are not deduplicable)",
			report.ExplorationCalls)
	}
}

// TestStatsPruneKeepsAFileWhoseMtimeIsExactlyTheCutoff pins the boundary of the mtime prune.
// The window is inclusive at its lower edge: a file last written at exactly the cutoff instant
// is inside it. Driving the collector directly is what makes the tie reachable at all — a cutoff
// taken from the wall clock can never be hit by a test that only writes files.
func TestStatsPruneKeepsAFileWhoseMtimeIsExactlyTheCutoff(t *testing.T) {
	t.Parallel()
	sessions := t.TempDir()
	now := statsTime(0)
	writeTranscript(t, sessions, "s1.jsonl",
		toolUseLine(t, now, "Grep", "e1", map[string]any{"pattern": "x"}),
		toolResultLine(t, now, "e1", "matches"),
	)
	files, err := listTranscriptFiles(sessions, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 candidate transcript, got %d", len(files))
	}

	atCutoff := newStatsCollector()
	atCutoff.run(files, files[0].modTime)
	if atCutoff.transcripts != 1 {
		t.Fatalf("a transcript whose mtime equals the cutoff was pruned (%d parsed, want 1)",
			atCutoff.transcripts)
	}

	justAfter := newStatsCollector()
	justAfter.run(files, files[0].modTime.Add(time.Nanosecond))
	if justAfter.transcripts != 0 {
		t.Fatalf("a transcript one nanosecond older than the cutoff was parsed (%d, want 0)",
			justAfter.transcripts)
	}
}
