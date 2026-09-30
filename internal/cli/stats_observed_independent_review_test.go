package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests use only authored records in t.TempDir. They exercise the real
// transcript decoder, summary merge, accounting, and default renderer, without
// invoking runStats or discovering any actual session directory.
const reviewObservedGraphBody = `{"format_version":1,"query":"run","repo_root":"/repo","profile":"fast","results":[{"rank":1,"score":1,"file_path":"src/a.go","start_line":10,"end_line":12,"focus_line":10,"snippet_start_line":10,"snippet_end_line":12,"symbol_start_line":10,"symbol_end_line":12,"language":"Go","kind":"function","symbol_name":"run","signature":"func run()","signals":["complete-symbol"],"snippet":"func run() {\n    work()\n}"}],"warnings":[],"partial_failures":[]}`

const reviewObservedReadBody = "10→func run() {\n11→    work()\n12→}"

func reviewObservedUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func reviewObservedGraphUse() map[string]any {
	return reviewObservedUse("g1", "Bash", map[string]any{
		"command": `entire graph query --repo /repo --format json --query "run"`,
	})
}

func reviewObservedReadUse(id, path string) map[string]any {
	return reviewObservedUse(id, "Read", map[string]any{
		"file_path": path, "offset": 10, "limit": 3,
	})
}

// is_error is deliberately encoded into the raw record, rather than through the
// production contentBlock type: the decoder's loss of this field is under test.
func reviewObservedResult(id, text string, isError bool) map[string]any {
	return map[string]any{
		"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isError,
	}
}

func reviewObservedRecord(t *testing.T, tick int, role string, blocks ...map[string]any) string {
	t.Helper()
	stamp := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	encoded, err := json.Marshal(map[string]any{
		"type": role, "timestamp": stamp.Format(time.RFC3339),
		"message": map[string]any{
			"id": fmt.Sprintf("review-message-%d", tick), "role": role, "content": blocks,
		},
	})
	if err != nil {
		t.Fatalf("encode synthetic record: %v", err)
	}
	return string(encoded)
}

func reviewObservedReport(t *testing.T, records ...string) statsResponse {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write synthetic transcript: %v", err)
	}
	summary, ok := summariseTranscript(path)
	if !ok || summary.Malformed != 0 {
		t.Fatalf("synthetic transcript did not decode: ok=%v malformed=%d", ok, summary.Malformed)
	}
	collector := newStatsCollector()
	collector.session("synthetic").merge(summary)
	report := statsResponse{SessionsDirFound: true, Since: "all", DisplacementWindowCalls: 5}
	collector.finish(&report, time.Time{})
	// Every case has exactly one real Graph use and one real Read use. An empty
	// report, a dropped message, or an unrecognised command must not pass as safe.
	if report.Sessions != 1 || report.GraphLocateCalls != 1 || report.ExplorationCalls != 1 {
		t.Fatalf("fixture not observed: sessions=%d locate_calls=%d exploration_calls=%d",
			report.Sessions, report.GraphLocateCalls, report.ExplorationCalls)
	}
	return report
}

func reviewObservedHeadline(report statsResponse) string {
	var out bytes.Buffer
	writeStatsSummary(&out, report)
	return out.String()
}

func reviewObservedNoCredit(t *testing.T, report statsResponse) {
	t.Helper()
	if report.GraphLocateDisplaced != 0 {
		t.Errorf("unproven displacement was credited: displaced=%d", report.GraphLocateDisplaced)
	}
	if report.ObservedNetBytes > 0 || report.ObservedNetTokens > 0 {
		t.Errorf("unproven displacement earned positive observed credit: bytes=%d tokens=%d",
			report.ObservedNetBytes, report.ObservedNetTokens)
	}
	// Exercise the actual default renderer too, without requiring exact wording
	// or prescribing how a future unknown/censored outcome must be represented.
	headline := reviewObservedHeadline(report)
	if strings.TrimSpace(headline) == "" {
		t.Error("default renderer returned no accounting result")
	}
	if strings.Contains(headline, "net +") {
		t.Errorf("default renderer advertises positive observed credit: %.360q", headline)
	}
}

// D1: an error or empty result immediately before EOF is not evidence of an
// avoided read. Zero/unavailable or a retained negative cost are both acceptable.
func TestReviewStatsObservedErrorAndEmptyTailCannotEarnCredit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		isError bool
	}{
		{name: "error_at_eof", body: strings.Repeat("E", 100), isError: true},
		{name: "empty_result_at_eof", body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := reviewObservedReport(t,
				reviewObservedRecord(t, 0, "assistant", reviewObservedReadUse("r0", "/repo/prior.go")),
				reviewObservedRecord(t, 1, "user", reviewObservedResult("r0", strings.Repeat("x", 4000), false)),
				reviewObservedRecord(t, 2, "assistant", reviewObservedGraphUse()),
				reviewObservedRecord(t, 3, "user", reviewObservedResult("g1", tc.body, tc.isError)),
			)
			if report.ExplorationReturnedBytes != 4000 || report.GraphReturnedBytes != int64(len(tc.body)) {
				t.Fatalf("result-byte premise lost: exploration=%d graph=%d",
					report.ExplorationReturnedBytes, report.GraphReturnedBytes)
			}
			reviewObservedNoCredit(t, report)
		})
	}
}

// Positive control: rejecting every follow-up as unknown cannot make this file
// pass. The matching Read was issued only after the successful body arrived.
func TestReviewStatsObservedDeliveredBodyThenMatchingReadControl(t *testing.T) {
	report := reviewObservedReport(t,
		reviewObservedRecord(t, 0, "assistant", reviewObservedGraphUse()),
		reviewObservedRecord(t, 1, "user", reviewObservedResult("g1", reviewObservedGraphBody, false)),
		reviewObservedRecord(t, 2, "assistant", reviewObservedReadUse("r1", "/repo/src/a.go")),
		reviewObservedRecord(t, 3, "user", reviewObservedResult("r1", reviewObservedReadBody, false)),
	)
	if report.GraphLocateReadAnyway != 1 || report.GraphLocateRequery != 0 {
		t.Errorf("unambiguous post-delivery read not recognized: read_anyway=%d requery=%d",
			report.GraphLocateReadAnyway, report.GraphLocateRequery)
	}
	if report.ObservedNetBytes >= 0 {
		t.Errorf("control must retain the nonempty Graph result's cost, got %d bytes", report.ObservedNetBytes)
	}
	reviewObservedNoCredit(t, report)
}

// D2a: both uses are in ONE assistant turn. The read even completes before the
// Graph result, so it cannot be a response to evidence that has not arrived.
func TestReviewStatsObservedParallelReadIsNotPostDeliveryFollowup(t *testing.T) {
	report := reviewObservedReport(t,
		reviewObservedRecord(t, 0, "assistant", reviewObservedGraphUse(), reviewObservedReadUse("r1", "/repo/src/a.go")),
		reviewObservedRecord(t, 1, "user", reviewObservedResult("r1", reviewObservedReadBody, false)),
		reviewObservedRecord(t, 2, "user", reviewObservedResult("g1", reviewObservedGraphBody, false)),
	)
	if report.GraphLocateReadAnyway != 0 || report.GraphLocateRequery != 0 {
		t.Errorf("pre-delivery parallel work counted as follow-up: read_anyway=%d requery=%d",
			report.GraphLocateReadAnyway, report.GraphLocateRequery)
	}
	// Its remaining tail is censored. Unknown is allowed; no new class is named.
	reviewObservedNoCredit(t, report)
}

// D2b: five unrelated requests share the initial turn with Graph. They must not
// exhaust a window intended to measure what happens AFTER Graph delivers code.
func TestReviewStatsObservedPreDeliveryBatchCannotHideFirstFollowup(t *testing.T) {
	batch := []map[string]any{reviewObservedGraphUse()}
	for i := 0; i < 5; i++ {
		batch = append(batch, reviewObservedUse(fmt.Sprintf("noop-%d", i), "Bash", map[string]any{"command": "true"}))
	}
	records := []string{reviewObservedRecord(t, 0, "assistant", batch...)}
	for i := 0; i < 5; i++ {
		records = append(records, reviewObservedRecord(t, i+1, "user", reviewObservedResult(fmt.Sprintf("noop-%d", i), "ok", false)))
	}
	records = append(records,
		reviewObservedRecord(t, 6, "user", reviewObservedResult("g1", reviewObservedGraphBody, false)),
		reviewObservedRecord(t, 7, "assistant", reviewObservedReadUse("r1", "/repo/src/a.go")),
		reviewObservedRecord(t, 8, "user", reviewObservedResult("r1", reviewObservedReadBody, false)),
	)
	report := reviewObservedReport(t, records...)
	reviewObservedNoCredit(t, report)
}
