package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// timingDeterminismResponse is a ten-result ranking with multi-line bodies, so the byte fitter has
// real choices to make (how many rows, how much snippet each keeps) at every budget below.
func timingDeterminismResponse() sem.SearchResponse {
	results := make([]sem.SearchResult, 0, 10)
	for i := 1; i <= 10; i++ {
		start := 10 * i
		var snippet strings.Builder
		for line := 0; line < 8; line++ {
			fmt.Fprintf(&snippet, "\tstep%d_%d := compute(input, %d) // body line\n", i, line, line)
		}
		results = append(results, sem.SearchResult{
			Rank: i, Score: float64(40 - i), FilePath: fmt.Sprintf("internal/pkg%d/file%d.go", i, i),
			StartLine: start, EndLine: start + 9, FocusLine: start + 2,
			SnippetStartLine: start + 1, SnippetEndLine: start + 8,
			SymbolName: fmt.Sprintf("Handler%d", i), Signals: []string{"body"},
			Snippet: snippet.String(),
		})
	}
	// A suffix block too, so the suffix fitter's decision is under test as well as the ranking's.
	hits := make([]sem.SearchLiteralHit, 0, 6)
	for i := 1; i <= 6; i++ {
		hits = append(hits, sem.SearchLiteralHit{
			FilePath: fmt.Sprintf("internal/pkg%d/file%d.go", i, i), Line: 10*i + 3,
			Symbol: fmt.Sprintf("Handler%d", i), Role: sem.SearchLiteralRoleConsumer,
		})
	}
	return sem.SearchResponse{
		Query: "handler compute input", Profile: "full", Results: results,
		LiteralCluster: &sem.SearchLiteralCluster{Literal: "compute", HitsTotal: 6, FilesTotal: 6, Hits: hits},
		Warnings:       []sem.ProviderWarning{}, PartialFailures: []sem.PartialFailure{},
	}
}

var agentLatencyDigits = regexp.MustCompile(`[0-9]+`)

// splitAgentHeader separates the telemetry line (always the first line of an honest payload) from
// the body, and masks the header's numbers so two payloads can be asked whether they chose the same
// header RUNG without being asked to agree on how long the machine took.
func latencies(stats sem.SearchStats) string {
	return fmt.Sprintf("I=%d Q=%d P=%d T=%d", stats.IndexLatencyMS, stats.QueryLatencyMS, stats.PreselectLatencyMS, stats.TotalLatencyMS)
}

func splitAgentHeader(payload string) (string, string) {
	header, body, _ := strings.Cut(payload, "\n")
	return agentLatencyDigits.ReplaceAllString(header, "N"), body
}

// THE RENDERED ANSWER MUST NOT DEPEND ON THE WALL CLOCK. The header carries measured latencies, so
// its width varies with machine load; when that width was charged to the byte fitter, a query that
// took 1905ms instead of 750ms lost a ranked row's header at --max-context-bytes 4096 (observed:
// row 10 rendered as a bare locator on the slow run, a full `10. path:27-30 sym (covers)` header on
// the fast one, same cache, same query). The body and the header rung must be identical for any
// latencies, and the payload must stay within the cap for all of them.
func TestAgentSearchBodyIsIndependentOfLatencies(t *testing.T) {
	budgets := []int{1, 2, 8, 12, 16, 24, 32, 40, 48, 64, 80, 96, 128, 160, 200, 256, 300, 400, 512, 700, 1024, 1500, 2048, 3000, 4096}
	timings := []sem.SearchStats{
		{IndexCacheHit: true, IndexLatencyMS: 1, QueryLatencyMS: 1, PreselectLatencyMS: 1, TotalLatencyMS: 1},
		{IndexCacheHit: true, IndexLatencyMS: 221, QueryLatencyMS: 393, PreselectLatencyMS: 135, TotalLatencyMS: 750},
		{IndexCacheHit: true, IndexLatencyMS: 1387, QueryLatencyMS: 391, PreselectLatencyMS: 126, TotalLatencyMS: 1905},
		{IndexCacheHit: true, IndexLatencyMS: 99999999, QueryLatencyMS: 99999999, PreselectLatencyMS: 99999999, TotalLatencyMS: 99999999},
		{IndexCacheHit: true, IndexLatencyMS: -5, QueryLatencyMS: 0, PreselectLatencyMS: 1 << 40, TotalLatencyMS: 7},
	}
	// Every budget up to the roomiest bounded size, too: whether the suffix block rides along is
	// decided in a window only a few bytes wide, so the suffix fitter being charged the reserved
	// header width is only under test at a budget inside it. A sparse list missed it.
	slowest := timingDeterminismResponse()
	slowest.Stats = timings[3]
	var roomy bytes.Buffer
	if err := writeAgentSearch(&roomy, slowest, 1<<20); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(roomy.String(), "SAME-CONCEPT LITERAL") {
		t.Fatalf("fixture lost its suffix block; the sweep below would not test the suffix fitter:\n%s", roomy.String())
	}
	sweep := make([]int, 0, roomy.Len())
	for budget := 1; budget <= roomy.Len(); budget++ {
		sweep = append(sweep, budget)
	}
	check := func(budgets []int, timings []sem.SearchStats) {
		t.Helper()
		for _, budget := range budgets {
			checkAgentSearchTimingDeterminism(t, budget, timings)
		}
	}
	check(budgets, timings)
	check(sweep, []sem.SearchStats{timings[0], timings[3]})
}

func checkAgentSearchTimingDeterminism(t *testing.T, budget int, timings []sem.SearchStats) {
	t.Helper()
	var wantHeader, wantBody string
	for i, stats := range timings {
		response := timingDeterminismResponse()
		response.Stats = stats
		var out bytes.Buffer
		if err := writeAgentSearch(&out, response, budget); err != nil {
			t.Fatal(err)
		}
		if out.Len() > budget {
			t.Fatalf("budget %d, %s: payload is %d bytes:\n%s", budget, latencies(stats), out.Len(), out.String())
		}
		header, body := splitAgentHeader(out.String())
		if i == 0 {
			wantHeader, wantBody = header, body
			continue
		}
		if header != wantHeader {
			t.Errorf("budget %d: header rung depends on latency:\n  %q (%s)\n  %q (%s)",
				budget, wantHeader, latencies(timings[0]), header, latencies(stats))
		}
		if body != wantBody {
			t.Errorf("budget %d: rendered body depends on latency (%s vs %s):\n--- fast ---\n%s\n--- slow ---\n%s",
				budget, latencies(timings[0]), latencies(stats), wantBody, body)
		}
	}
}

// The header's latency fields saturate at the reserved width rather than overflowing it, so the cap
// holds for any measured value; ordinary values print exactly.
func TestAgentSearchHeaderLatencyIsExactBelowCeilingAndSaturatesAbove(t *testing.T) {
	response := timingDeterminismResponse()
	response.Stats = sem.SearchStats{IndexLatencyMS: 1387, QueryLatencyMS: 391, PreselectLatencyMS: 126, TotalLatencyMS: 1905}
	var out bytes.Buffer
	if err := writeAgentSearch(&out, response, 4096); err != nil {
		t.Fatal(err)
	}
	if want := "Index: cache-miss (1387ms) | Query: 391ms | Preselect: 126ms | Total: 1905ms\n"; !strings.HasPrefix(out.String(), want) {
		t.Fatalf("header changed for ordinary latencies:\n%s", out.String())
	}
	response.Stats = sem.SearchStats{IndexLatencyMS: 99999999, QueryLatencyMS: -1, TotalLatencyMS: 1 << 40}
	out.Reset()
	if err := writeAgentSearch(&out, response, 4096); err != nil {
		t.Fatal(err)
	}
	ceiling := agentLatencyCeilingMS
	want := fmt.Sprintf("Index: cache-miss (%dms) | Query: 0ms | Preselect: 0ms | Total: %dms\n", ceiling, ceiling)
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("header did not saturate:\n%s", out.String())
	}
}
