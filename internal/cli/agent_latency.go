package cli

import "fmt"

// THE RENDERED ANSWER MUST NOT DEPEND ON THE WALL CLOCK.
//
// Every byte-budgeted writer (search, neighbors and impact in agent form) prints a telemetry line
// carrying measured latencies, and charges that line to the same cap as the answer. Charged at its
// ACTUAL width, a slower run hands the answer fewer bytes, so what gets rendered — how many rows,
// whether the full form fits at all — depends on machine load: same cache, same query, different
// answer. The helpers below make every such line a latencyRung: rendered once with the measured
// values saturated at agentLatencyCeilingMS (what is printed) and once with every field AT that
// ceiling (what the fitter is charged). The printed rung is never wider than the reserved one, so
// the cap still holds, and every fitting decision is a function of reserved widths alone.

// agentLatencyCeilingMS is the largest latency a budgeted telemetry line prints. Wider measurements
// saturate to it (about 16.7 minutes), which bounds every rung's width so the fitter can charge a
// constant for it; the exact values stay in the json format. Negative durations print 0.
const agentLatencyCeilingMS = 999999

func agentLatencyField(ms int64) int64 {
	return min(max(ms, 0), agentLatencyCeilingMS)
}

// latencyRung is one telemetry line as printed and as charged. len(reserved) >= len(printed).
type latencyRung struct {
	printed  []byte
	reserved []byte
}

// slack is the bytes the reserved rung would have spent and the printed one did not. A fitter that
// measures an ASSEMBLED payload holding the printed rung adds this to every length it compares
// against the cap, so it decides exactly as it would over the reserved rung.
func (r latencyRung) slack() int {
	return len(r.reserved) - len(r.printed)
}

// renderLatencyRung renders format — one %s for the cache state followed by one %d per latency —
// with the latencies saturated (printed) and all at the ceiling (reserved).
func renderLatencyRung(format, cacheState string, latencies ...int64) latencyRung {
	printed := make([]any, 0, 1+len(latencies))
	reserved := make([]any, 0, 1+len(latencies))
	printed = append(printed, cacheState)
	reserved = append(reserved, cacheState)
	for _, ms := range latencies {
		printed = append(printed, agentLatencyField(ms))
		reserved = append(reserved, int64(agentLatencyCeilingMS))
	}
	return latencyRung{
		printed:  []byte(fmt.Sprintf(format, printed...)),
		reserved: []byte(fmt.Sprintf(format, reserved...)),
	}
}

func indexCacheState(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

// relationLatencyHeader is the first line of the neighbors and impact text/agent answers.
func relationLatencyHeader(cacheHit bool, index, query, total int64) latencyRung {
	return renderLatencyRung("Index: cache-%s (%dms) | Query: %dms | Total: %dms\n",
		indexCacheState(cacheHit), index, query, total)
}
