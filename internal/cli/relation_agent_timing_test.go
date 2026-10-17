package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// relationLatencies is one measured (index, query, total) triple, as neighbors and impact report it.
type relationLatencies struct{ index, query, total int64 }

func (l relationLatencies) String() string {
	return fmt.Sprintf("I=%d Q=%d T=%d", l.index, l.query, l.total)
}

// relationTimings spans the fast path, an ordinary slow run, far past the print ceiling, and values
// a clock step can produce (negative, absurd). The bounded writers must render one answer for all.
var relationTimings = []relationLatencies{
	{1, 1, 1},
	{221, 393, 750},
	{1387, 391, 1905},
	{99999999, 99999999, 99999999},
	{-5, 0, 1 << 40},
}

// maskRelationLatencies masks the digits of every telemetry line, wherever it sits in the payload
// (the compact neighbors form carries it mid-payload), so two payloads are asked whether they chose
// the same rung without being asked to agree on how long the machine took. Every other byte is
// compared exactly.
func maskRelationLatencies(payload string) string {
	lines := strings.SplitAfter(payload, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Index: cache-") || strings.HasPrefix(line, "I:") {
			lines[i] = agentLatencyDigits.ReplaceAllString(line, "N")
		}
	}
	return strings.Join(lines, "")
}

func timingNeighborResponse(l relationLatencies) neighborResponse {
	match := neighborFocus{Symbol: neighborEndpoint{ID: "focus", Name: "Focus", FilePath: "src/focus.go", StartLine: 10, EndLine: 40}}
	for i := 0; i < 12; i++ {
		match.Incoming = append(match.Incoming, neighborEdge{
			Direction: "in", Relation: "CALLS", Resolution: "exact",
			Endpoint: neighborEndpoint{ID: fmt.Sprintf("caller-%d", i), Name: fmt.Sprintf("Caller%d", i),
				FilePath: fmt.Sprintf("src/callers/caller_%d.go", i), StartLine: i + 1},
		})
		match.Outgoing = append(match.Outgoing, neighborEdge{
			Direction: "out", Relation: "CALLS", Resolution: "import_resolved",
			Endpoint: neighborEndpoint{ID: fmt.Sprintf("callee-%d", i), Name: fmt.Sprintf("Callee%d", i),
				FilePath: fmt.Sprintf("src/callees/callee_%d.go", i), StartLine: 3*i + 1},
		})
	}
	return neighborResponse{
		Query: "Focus", Direction: "both", IndexCacheHit: true, FocusMatchesTotal: 1,
		IndexLatencyMS: l.index, QueryLatencyMS: l.query, TotalLatencyMS: l.total,
		Stats:   sem.ProviderStats{Files: 5, ParsedFiles: 5, CompletenessLevel: "complete"},
		Matches: []neighborFocus{match}, PartialFailures: []sem.PartialFailure{},
	}
}

func timingImpactResponse(l relationLatencies) impactResponse {
	symbols := []sem.SymbolRecord{{ID: "focus", Name: "Focus", QualifiedName: "Focus", FilePath: "focus.go", StartLine: 1}}
	var relations []sem.RelationRecord
	for i := 0; i < 24; i++ {
		id := fmt.Sprintf("c%02d", i)
		symbols = append(symbols, sem.SymbolRecord{
			ID: id, Name: "caller_" + id, QualifiedName: "pkg.caller_" + id,
			FilePath: "callers/" + id + ".go", StartLine: i + 1,
		})
		relations = append(relations, sem.RelationRecord{FromID: id, ToID: "focus", Type: "CALLS"})
	}
	response := buildImpactResponseOnDisk(
		sem.ProviderSnapshot{Symbols: symbols, Relations: relations},
		impactFlags{Symbol: "Focus", Depth: 1, Limit: defaultImpactSectionLimit},
	)
	response.IndexLatencyMS, response.QueryLatencyMS, response.TotalLatencyMS = l.index, l.query, l.total
	return response
}

// checkRelationTimingDeterminism sweeps EVERY budget from 1 to past the roomiest render: the
// previous fix in this class needed a dense sweep to catch a window about 20 bytes wide. At each
// budget the payload must stay within the cap and, latency digits masked, be byte-identical across
// all timings. It returns how many distinct payloads the sweep saw, for the non-vacuity guard.
func checkRelationTimingDeterminism(t *testing.T, name string, render func(relationLatencies, int) string) int {
	t.Helper()
	roomiest := len(render(relationTimings[3], 0))
	distinct := map[string]bool{}
	failures := 0
	for budget := 1; budget <= roomiest+64; budget++ {
		want := ""
		for i, timing := range relationTimings {
			payload := render(timing, budget)
			if len(payload) > budget {
				t.Errorf("%s budget %d, %s: payload is %d bytes:\n%s", name, budget, timing, len(payload), payload)
				failures++
			}
			masked := maskRelationLatencies(payload)
			if i == 0 {
				want = masked
				distinct[masked] = true
				continue
			}
			if masked != want {
				t.Errorf("%s budget %d: rendered answer depends on latency (%s vs %s):\n--- fast ---\n%s\n--- slow ---\n%s",
					name, budget, relationTimings[0], timing, want, masked)
				failures++
			}
		}
		if failures > 6 {
			t.Fatalf("%s: stopping after %d failures", name, failures)
		}
	}
	return len(distinct)
}

func TestAgentNeighborsBoundedIsIndependentOfLatencies(t *testing.T) {
	render := func(l relationLatencies, budget int) string {
		var out bytes.Buffer
		if err := writeAgentNeighborsBounded(&out, timingNeighborResponse(l), budget); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	// Non-vacuity: the timings must actually produce headers of different widths, and the sweep
	// must cross both the full render and the compact one (where the "I:" line sits mid-payload).
	fast, slow := render(relationTimings[0], 0), render(relationTimings[3], 0)
	if fastHeader, _, _ := strings.Cut(fast, "\n"); len(fastHeader)+10 > len(strings.SplitN(slow, "\n", 2)[0]) {
		t.Fatalf("fixture timings do not change the header width:\n%s\n%s", fast, slow)
	}
	if compact := render(relationTimings[0], len(fast)/2); !strings.Contains(compact, "!output-truncated") ||
		!strings.Contains(compact, "\nI:hit/") {
		t.Fatalf("fixture never reaches the compact form's latency line:\n%s", compact)
	}
	if distinct := checkRelationTimingDeterminism(t, "neighbors", render); distinct < 20 {
		t.Fatalf("budget sweep saw only %d distinct payloads; it is not exercising the fitter", distinct)
	}
}

func TestImpactBoundedIsIndependentOfLatencies(t *testing.T) {
	render := func(l relationLatencies, budget int) string {
		var out bytes.Buffer
		if err := writeImpactBounded(&out, timingImpactResponse(l), budget); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	fast, slow := render(relationTimings[0], 0), render(relationTimings[3], 0)
	if fastHeader, _, _ := strings.Cut(fast, "\n"); len(fastHeader)+10 > len(strings.SplitN(slow, "\n", 2)[0]) {
		t.Fatalf("fixture timings do not change the header width:\n%s\n%s", fast, slow)
	}
	// All three decisions the writer makes (full, a reduced section cap, the hard cut) are crossed.
	if !strings.Contains(fast, "- ... +") {
		t.Fatalf("fixture has no section cap to reduce:\n%s", fast)
	}
	if cut := render(relationTimings[0], 200); !strings.Contains(cut, "!output-truncated") {
		t.Fatalf("fixture never reaches the hard cut:\n%s", cut)
	}
	if distinct := checkRelationTimingDeterminism(t, "impact", render); distinct < 10 {
		t.Fatalf("budget sweep saw only %d distinct payloads; it is not exercising the fitter", distinct)
	}
}

// Ordinary latencies print exactly; out-of-range ones saturate to the reserved width rather than
// overflowing it, and negative ones print 0.
func TestRelationHeadersPrintExactLatenciesAndSaturate(t *testing.T) {
	var out bytes.Buffer
	writeImpactText(&out, timingImpactResponse(relationLatencies{1387, 391, 1905}))
	if want := "Index: cache-miss (1387ms) | Query: 391ms | Total: 1905ms\n"; !strings.HasPrefix(out.String(), want) {
		t.Fatalf("impact header changed for ordinary latencies:\n%s", out.String())
	}
	out.Reset()
	if err := writeAgentNeighborsFull(&out, timingNeighborResponse(relationLatencies{99999999, -1, 1 << 40})); err != nil {
		t.Fatal(err)
	}
	ceiling := agentLatencyCeilingMS
	if want := fmt.Sprintf("Index: cache-hit (%dms) | Query: 0ms | Total: %dms\n", ceiling, ceiling); !strings.HasPrefix(out.String(), want) {
		t.Fatalf("neighbors header did not saturate:\n%s", out.String())
	}
}
