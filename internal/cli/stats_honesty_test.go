package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Exercise the public command, not a fabricated response: absent comparisons must
// remain distinguishable from observed zeroes, even when the legacy sum is positive.
func TestStatsHonestModelIncludesLossesAndComparisonPopulation(t *testing.T) {
	t.Parallel()
	repo, sessions := t.TempDir(), t.TempDir()
	for _, fixture := range []struct {
		name           string
		graph, explore int
	}{
		{"winner", 200, 400},
		{"loser", 1100, 100},
		{"flat", 4, 4},
		{"graph-only", 888, -1},
	} {
		writeHonestyTranscript(t, sessions, fixture.name, fixture.graph, fixture.explore)
	}
	args := []string{"--repo", repo, "--sessions-dir", sessions, "--since", "all"}
	raw := runStatsRawJSON(t, append(args, "--format", "json")...)
	for key, want := range map[string]int64{
		"sessions":                               4,
		"sessions_with_savings_comparison":       3,
		"estimated_savings_bytes":                200,
		"estimated_savings_est_tokens":           50,
		"estimated_savings_bytes_unfloored":      -800,
		"estimated_savings_est_tokens_unfloored": -200,
	} {
		if got := rawInt(t, raw, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	for _, verbose := range []bool{false, true} {
		text := runHonestyText(t, args, verbose)
		// No fixture re-queries or re-reads, so observed displacement equals the 1:1 model here.
		if !strings.Contains(text, "observed displacement: net -200 est. tokens; displaced 100% of 4") {
			t.Errorf("missing signed, qualified observed result:\n%s", text)
		}
		if verbose && !strings.Contains(text, "1:1 context model: -200 est. tokens; not measured savings") {
			t.Errorf("missing signed, qualified 1:1 result:\n%s", text)
		}
		if verbose && !strings.Contains(text, "sessions with both result types: 3 of 4") {
			t.Errorf("missing model population:\n%s", text)
		}
		for _, forbidden := range []string{"tokens saved", "graph-first", "of billed", "conservative", "0.980"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("unsupported human claim %q:\n%s", forbidden, text)
			}
		}
	}
}

func TestStatsHonestModelDistinguishesSignedZeroAndUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		graph, explore int
		want           string
	}{
		{"positive", 4, 8, "+1 est. tokens"},
		{"negative-one", 8, 4, "-1 est. tokens"},
		{"zero", 4, 4, "0 est. tokens"},
		{"graph-only", 4, -1, "unavailable"},
		{"exploration-only", -1, 4, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, sessions := t.TempDir(), t.TempDir()
			writeHonestyTranscript(t, sessions, tc.name, tc.graph, tc.explore)
			args := []string{"--repo", repo, "--sessions-dir", sessions, "--since", "all"}
			observed := "observed displacement: net " + tc.want
			if strings.HasPrefix(tc.want, "unavailable") {
				observed = "unavailable"
			}
			for _, verbose := range []bool{false, true} {
				text := runHonestyText(t, args, verbose)
				if !strings.Contains(text, observed) {
					t.Errorf("missing %q:\n%s", observed, text)
				}
				if verbose && !strings.Contains(text, "1:1 context model: "+tc.want) {
					t.Errorf("missing 1:1 %q:\n%s", tc.want, text)
				}
			}
		})
	}
}

func TestStatsHonestModelCancellationIsAvailableZero(t *testing.T) {
	t.Parallel()
	repo, sessions := t.TempDir(), t.TempDir()
	writeHonestyTranscript(t, sessions, "positive", 4, 8)
	writeHonestyTranscript(t, sessions, "negative", 8, 4)
	text := runHonestyText(t, []string{"--repo", repo, "--sessions-dir", sessions, "--since", "all"}, true)
	if !strings.Contains(text, "1:1 context model: 0 est. tokens") || !strings.Contains(text, "sessions with both result types: 2 of 2") {
		t.Fatalf("cancellation must be available zero:\n%s", text)
	}
}

func TestStatsHonestModelDoesNotCompareDifferentSessions(t *testing.T) {
	t.Parallel()
	repo, sessions := t.TempDir(), t.TempDir()
	writeHonestyTranscript(t, sessions, "graph-only", 4, -1)
	writeHonestyTranscript(t, sessions, "exploration-only", -1, 800)
	args := []string{"--repo", repo, "--sessions-dir", sessions, "--since", "all"}
	if text := runHonestyText(t, args, true); !strings.Contains(text, "1:1 context model: unavailable") || !strings.Contains(text, "sessions with both result types: 0 of 2") {
		t.Fatalf("different sessions must not manufacture a comparison:\n%s", text)
	}
}

func TestStatsHelpDescribesSignedModel(t *testing.T) {
	t.Parallel()
	text := runHonestyText(t, []string{"--help"}, false)
	for _, want := range []string{"not measured savings", "modeled loss", "unavailable"} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "correctly contributes nothing") {
		t.Fatalf("help still endorses dropping losses:\n%s", text)
	}
}

func writeHonestyTranscript(t *testing.T, dir, name string, graph, explore int) {
	t.Helper()
	ts := statsTime(0)
	var lines []string
	if graph >= 0 {
		lines = append(lines,
			toolUseLine(t, ts, "Bash", "g1", map[string]any{"command": `entire graph query "x"`}),
			toolResultLine(t, ts, "g1", strings.Repeat("g", graph)))
	}
	if explore >= 0 {
		lines = append(lines,
			toolUseLine(t, ts, "Read", "e1", map[string]any{"file_path": "example.go"}),
			toolResultLine(t, ts, "e1", strings.Repeat("e", explore)))
	}
	writeTranscript(t, dir, name+".jsonl", lines...)
}

func runHonestyText(t *testing.T, flags []string, verbose bool) string {
	t.Helper()
	args := append([]string{"stats"}, flags...)
	if verbose {
		args = append(args, "--verbose")
	}
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out}, args); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
