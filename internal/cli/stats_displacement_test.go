package cli

import (
	"fmt"
	"strings"
	"testing"
)

// dispCall is one tool call plus its result, for building displacement fixtures.
type dispCall struct {
	name   string
	input  map[string]any
	result string
}

func graphSearch(result string) dispCall {
	return dispCall{"Bash", map[string]any{"command": `entire graph search --repo . --query "x"`}, result}
}

func readFile(path, result string) dispCall {
	return dispCall{"Read", map[string]any{"file_path": path}, result}
}

func bashCall(command, result string) dispCall {
	return dispCall{"Bash", map[string]any{"command": command}, result}
}

func editCall() dispCall {
	return dispCall{"Edit", map[string]any{"file_path": "/repo/z.go", "old_string": "a", "new_string": "b"}, "ok"}
}

func writeDispSession(t *testing.T, dir, name string, calls ...dispCall) {
	t.Helper()
	ts := statsTime(0)
	lines := make([]string, 0, 2*len(calls))
	for i, call := range calls {
		id := fmt.Sprintf("%s-%d", name, i)
		lines = append(lines, toolUseLine(t, ts, call.name, id, call.input), toolResultLine(t, ts, id, call.result))
	}
	writeTranscript(t, dir, name+".jsonl", lines...)
}

func dispReport(t *testing.T, sessions string) map[string]any {
	t.Helper()
	return runStatsRawJSON(t, "--repo", t.TempDir(), "--sessions-dir", sessions, "--since", "all", "--format", "json")
}

func assertDisp(t *testing.T, raw map[string]any, displaced, requery, readAnyway int64) {
	t.Helper()
	got := [3]int64{rawInt(t, raw, "graph_locate_displaced"), rawInt(t, raw, "graph_locate_requery"), rawInt(t, raw, "graph_locate_read_anyway")}
	if want := [3]int64{displaced, requery, readAnyway}; got != want {
		t.Fatalf("displaced/requery/read-anyway = %v, want %v", got, want)
	}
}

const namedStats = `{"results":[{"file_path":"internal/cli/stats.go","start_line":770}]}`

// TestDisplacementWindowIsFive pins K. Changing it changes every published rate, so it must be a
// deliberate, reviewed edit (and the PR must re-run the corpus sensitivity), never a drive-by.
func TestDisplacementWindowIsFive(t *testing.T) {
	t.Parallel()
	if displacementWindow != 5 {
		t.Fatalf("displacementWindow = %d, want 5", displacementWindow)
	}
	raw := dispReport(t, func() string {
		dir := t.TempDir()
		writeDispSession(t, dir, "s", graphSearch("x"), readFile("/repo/a.go", "aaaa"))
		return dir
	}())
	if got := rawInt(t, raw, "displacement_window_calls"); got != 5 {
		t.Fatalf("displacement_window_calls = %d, want 5", got)
	}
}

func TestDisplacementClassifiesEachClass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		calls []dispCall
		want  [3]int64 // displaced, requery, read-anyway (of the FIRST locate call; see below)
	}{
		{"displaced-nothing-follows", []dispCall{graphSearch(namedStats), editCall(), readFile("/repo/other.go", "o")}, [3]int64{1, 0, 0}},
		{"requery-grep-tool", []dispCall{graphSearch(namedStats), {"Grep", map[string]any{"pattern": "x"}, "m"}}, [3]int64{0, 1, 0}},
		{"requery-glob-tool", []dispCall{graphSearch(namedStats), {"Glob", map[string]any{"pattern": "*.go"}, "m"}}, [3]int64{0, 1, 0}},
		{"requery-shell-grep", []dispCall{graphSearch(namedStats), bashCall("cd /repo && rg -n foo .", "m")}, [3]int64{0, 1, 0}},
		{"requery-graph-lookup", []dispCall{graphSearch(namedStats), bashCall("entire graph def Foo", "d")}, [3]int64{0, 1, 0}},
		{"read-anyway-read-tool", []dispCall{graphSearch(namedStats), readFile("/abs/repo/internal/cli/stats.go", "body")}, [3]int64{0, 0, 1}},
		{"read-anyway-shell-sed", []dispCall{graphSearch(namedStats), bashCall("sed -n '760,790p' internal/cli/stats.go", "body")}, [3]int64{0, 0, 1}},
		// Graph pointed at X, agent read Y: no overlap, so not read-anyway, and a read is not a
		// re-query either.
		{"read-other-file-is-displaced", []dispCall{graphSearch(namedStats), readFile("/abs/repo/internal/cli/help.go", "body")}, [3]int64{1, 0, 0}},
		{"same-basename-different-dir-is-not-overlap", []dispCall{graphSearch(namedStats), readFile("/abs/repo/other/stats.go", "body")}, [3]int64{1, 0, 0}},
		{"prefix-lookalike-is-not-overlap", []dispCall{graphSearch(namedStats), readFile("/abs/repo/internal/cli/xstats.go", "body")}, [3]int64{1, 0, 0}},
		// First qualifying call decides.
		{"read-then-requery-is-read-anyway", []dispCall{graphSearch(namedStats), readFile("/r/internal/cli/stats.go", "b"), {"Grep", map[string]any{"pattern": "x"}, "m"}}, [3]int64{0, 0, 1}},
		{"requery-then-read-is-requery", []dispCall{graphSearch(namedStats), {"Grep", map[string]any{"pattern": "x"}, "m"}, readFile("/r/internal/cli/stats.go", "b")}, [3]int64{0, 1, 0}},
		// A write is not a read, and a pipe tail is not a search.
		{"write-is-not-read", []dispCall{graphSearch(namedStats), bashCall("cat > internal/cli/stats.go <<'EOF'\nx\nEOF", "")}, [3]int64{1, 0, 0}},
		{"pipe-tail-grep-is-not-requery", []dispCall{graphSearch(namedStats), bashCall("go test ./... 2>&1 | grep FAIL", "ok")}, [3]int64{1, 0, 0}},
		{"meta-graph-verb-is-not-requery", []dispCall{graphSearch(namedStats), bashCall("entire graph stats --repo .", "s")}, [3]int64{1, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// An exploration result so the session has both sides of the comparison.
			writeDispSession(t, dir, "s", tc.calls...)
			raw := dispReport(t, dir)
			// Only count the first call's class: later locate calls in the fixture (graph def
			// is a lookup, not a locate verb) never add a second classified result.
			assertDisp(t, raw, tc.want[0], tc.want[1], tc.want[2])
		})
	}
}

// TestDisplacementWindowBoundary: a re-query at exactly K calls later counts; at K+1 it is outside
// the window and the locate call is displaced.
func TestDisplacementWindowBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fillers int
		want    [3]int64
	}{
		{displacementWindow - 1, [3]int64{0, 1, 0}}, // grep is call K
		{displacementWindow, [3]int64{1, 0, 0}},     // grep is call K+1
	} {
		t.Run(fmt.Sprintf("fillers-%d", tc.fillers), func(t *testing.T) {
			t.Parallel()
			calls := []dispCall{graphSearch(namedStats)}
			for range tc.fillers {
				calls = append(calls, editCall())
			}
			calls = append(calls, dispCall{"Grep", map[string]any{"pattern": "x"}, "m"})
			dir := t.TempDir()
			writeDispSession(t, dir, "s", calls...)
			assertDisp(t, dispReport(t, dir), tc.want[0], tc.want[1], tc.want[2])
		})
	}
	// Same boundary for read-anyway.
	for _, tc := range []struct {
		fillers int
		want    [3]int64
	}{
		{displacementWindow - 1, [3]int64{0, 0, 1}},
		{displacementWindow, [3]int64{1, 0, 0}},
	} {
		calls := []dispCall{graphSearch(namedStats)}
		for range tc.fillers {
			calls = append(calls, editCall())
		}
		calls = append(calls, readFile("/r/internal/cli/stats.go", "b"))
		dir := t.TempDir()
		writeDispSession(t, dir, "s", calls...)
		assertDisp(t, dispReport(t, dir), tc.want[0], tc.want[1], tc.want[2])
	}
}

// TestDisplacementTeammateExample: two graph calls, then a Glob, then reads of the files the graph
// named. Under 1:1 both calls are credited; observed, neither is.
func TestDisplacementTeammateExample(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDispSession(t, dir, "s",
		graphSearch(namedStats),
		graphSearch(namedStats),
		dispCall{"Glob", map[string]any{"pattern": "internal/cli/*.go"}, strings.Repeat("p", 400)},
		readFile("/r/internal/cli/stats.go", strings.Repeat("b", 1600)),
	)
	raw := dispReport(t, dir)
	assertDisp(t, raw, 0, 2, 0)
	// 1:1: 2 * mean(400,1600)=2000 - 2*len(namedStats). Observed: 0 credit - 2*len(namedStats).
	graphBytes := int64(2 * len(namedStats))
	if got, want := rawInt(t, raw, "estimated_savings_bytes_unfloored"), 2000-graphBytes; got != want {
		t.Fatalf("1:1 unfloored = %d, want %d", got, want)
	}
	if got := rawInt(t, raw, "observed_net_bytes"); got != -graphBytes {
		t.Fatalf("observed_net_bytes = %d, want %d", got, -graphBytes)
	}
}

// TestDisplacementNetCreditsOnlyDisplaced checks the arithmetic and that windows never cross
// sessions: each session's tail call cannot see the next session's calls.
func TestDisplacementNetCreditsOnlyDisplacedAcrossSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Session a: one displaced locate (8 bytes), exploration mean 100 -> +92.
	writeDispSession(t, dir, "a", graphSearch("gggggggg"), readFile("/r/x.go", strings.Repeat("e", 100)))
	// Session b: locate is the LAST call in its file (displaced: nothing follows in b), plus an
	// earlier locate that is re-queried. Exploration mean 300. Credit 300 - 2*8 = +284.
	writeDispSession(t, dir, "b",
		dispCall{"Grep", map[string]any{"pattern": "q"}, strings.Repeat("e", 300)},
		graphSearch("gggggggg"),
		graphSearch("gggggggg"),
	)
	// Session c sorts after b and starts with a Grep: it must NOT make b's last locate a re-query.
	writeDispSession(t, dir, "c", dispCall{"Grep", map[string]any{"pattern": "q"}, strings.Repeat("e", 50)})
	raw := dispReport(t, dir)
	assertDisp(t, raw, 2, 1, 0)
	if got := rawInt(t, raw, "observed_net_bytes"); got != 92+284 {
		t.Fatalf("observed_net_bytes = %d, want %d", got, 92+284)
	}
	if got := rawInt(t, raw, "observed_net_est_tokens"); got != (92+284)/4 {
		t.Fatalf("observed_net_est_tokens = %d", got)
	}
	if got := raw["displaced_rate"].(float64); got != 0.6667 {
		t.Fatalf("displaced_rate = %v, want 0.6667", got)
	}
	if got := raw["requery_rate"].(float64); got != 0.3333 {
		t.Fatalf("requery_rate = %v, want 0.3333", got)
	}
}

// TestDisplacementReplayCountsOnce: a subagent transcript replaying the parent's history must not
// classify the same locate call twice.
func TestDisplacementReplayCountsOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := statsTime(0)
	parent := []string{
		toolUseLine(t, ts, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`}),
		toolResultLine(t, ts, "g1", "gggg"),
		toolUseLine(t, ts, "Read", "e1", map[string]any{"file_path": "/r/a.go"}),
		toolResultLine(t, ts, "e1", "eeee"),
	}
	writeTranscript(t, dir, "s.jsonl", parent...)
	writeTranscript(t, dir, "s/subagents/agent-1.jsonl", append(parent,
		toolUseLine(t, ts, "Grep", "e2", map[string]any{"pattern": "x"}),
		toolResultLine(t, ts, "e2", "m"))...)
	raw := dispReport(t, dir)
	assertDisp(t, raw, 1, 0, 0)
}

func TestDisplacementHeadlineAlwaysCarriesTheRate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDispSession(t, dir, "s",
		graphSearch("gggg"),
		dispCall{"Grep", map[string]any{"pattern": "x"}, strings.Repeat("e", 40)},
		graphSearch("gggg"),
	)
	text := runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, false)
	// net: 1 displaced * 40 - 8 = 32 bytes = +8 tokens; 1 of 2 displaced.
	want := "[entire-graph] observed displacement: net +8 est. tokens; displaced 50% of 2 graph locate results (re-query 50%, read-anyway 0%; next 5 calls); not measured savings\n"
	if text != want {
		t.Fatalf("headline =\n%q\nwant\n%q", text, want)
	}
	verbose := runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, true)
	for _, line := range strings.Split(verbose, "\n") {
		if strings.Contains(line, "est. tokens;") && !strings.Contains(line, "displaced") {
			t.Errorf("a savings number is printed without its rate: %q", line)
		}
	}
}

func TestDisplacementUnavailableKeepsRates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDispSession(t, dir, "s", graphSearch("gggg"), graphSearch("gggg"))
	text := runHonestyText(t, []string{"--repo", t.TempDir(), "--sessions-dir", dir, "--since", "all"}, false)
	if !strings.Contains(text, "net unavailable") || !strings.Contains(text, "displaced 50% of 2") || strings.Contains(text, "est. tokens") {
		t.Fatalf("unavailable net must show rates and no number:\n%s", text)
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
		{"cli/stats.go", true}, // relative read from a subdirectory
		{"/abs/repo/internal/cli/xstats.go", false},
		{"/abs/repo/other/stats.go", false},
		{"/abs/repo/internal/cli/stats.go.bak", false},
	} {
		if got := pathsOverlap([]string{"internal/cli/stats.go"}, []string{tc.read}); got != tc.want {
			t.Errorf("pathsOverlap(stats.go, %q) = %v, want %v", tc.read, got, tc.want)
		}
	}
	// Component boundary in both directions: a bare basename the graph named must not match a
	// longer name that merely ends with it, and vice versa.
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

func TestStatsJSONObservedDisplacementKeysAreAdditive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDispSession(t, dir, "s", graphSearch("gggg"), readFile("/r/a.go", "eeee"))
	raw := dispReport(t, dir)
	for _, key := range []string{
		"displacement_window_calls", "graph_locate_displaced", "graph_locate_requery", "graph_locate_read_anyway",
		"displaced_rate", "requery_rate", "read_anyway_rate", "observed_net_bytes", "observed_net_est_tokens",
		"observed_displacement_model",
		// and every legacy key the statusline and older consumers read
		"estimated_savings_bytes", "estimated_savings_est_tokens_unfloored", "sessions_with_savings_comparison", "substitution_ratio",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	if got := raw["substitution_ratio"].(float64); got != 1 {
		t.Errorf("legacy substitution_ratio changed: %v", got)
	}
}

// TestDisplacementReplayCanCompleteAClass: when the first copy of a locate call has no result (the
// parent transcript was cut before it), the replay that carries the result also carries the class.
func TestDisplacementReplayCanCompleteAClass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := statsTime(0)
	use := toolUseLine(t, ts, "Bash", "g1", map[string]any{"command": `entire graph search --query "x"`})
	writeTranscript(t, dir, "s.jsonl", use,
		toolUseLine(t, ts, "Read", "e0", map[string]any{"file_path": "/r/b.go"}),
		toolResultLine(t, ts, "e0", "eeee"))
	writeTranscript(t, dir, "s/subagents/agent-1.jsonl", use,
		toolResultLine(t, ts, "g1", `{"file_path":"a.go"}`),
		toolUseLine(t, ts, "Read", "e1", map[string]any{"file_path": "/r/a.go"}),
		toolResultLine(t, ts, "e1", "eeee"))
	assertDisp(t, dispReport(t, dir), 0, 0, 1)
}
