package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestWarnfIsPlainOnAPipe(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	warnf(&out, "cache unavailable: %v", "boom")
	if got, want := out.String(), "graph: cache unavailable: boom\n"; got != want {
		t.Fatalf("pipe output must be byte-identical to a plain Fprintf:\n got %q\nwant %q", got, want)
	}
}

func TestWarnfPaintsPrefixWhenColorForced(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	warnf(&out, "cache unavailable: %v", "boom")
	if got, want := out.String(), "\x1b[33mgraph:\x1b[0m cache unavailable: boom\n"; got != want {
		t.Fatalf("colored output should paint only the prefix in palette warning yellow:\n got %q\nwant %q", got, want)
	}
}

func TestWarnfNeutralizesHostileMessageBytes(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	warnf(&out, "path %s missing", "evil\x1b[2K")
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("message bytes must be neutralized before the terminal:\n%q", out.String())
	}
}

func TestWarnPrefixPaintsOnlyUnderColor(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	if got := warnPrefix(&out, "warning:"); got != "warning:" {
		t.Fatalf("pipe prefix must stay plain, got %q", got)
	}

	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	if got := warnPrefix(&out, "warning:"); got != "\x1b[33mwarning:\x1b[0m" {
		t.Fatalf("colored prefix should be palette warning yellow, got %q", got)
	}
}

func TestWarnIfPartialPaintsHeadlineWhenColorForced(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")

	summary := &sem.SnapshotSummary{}
	summary.Stats.CompletenessLevel = "degraded"
	var out bytes.Buffer
	warnIfPartial(&out, false, summary)
	if !strings.Contains(out.String(), "\x1b[93m") {
		t.Fatalf("degraded banner headline should use the loud warning tier (bright yellow):\n%q", out.String())
	}
}

func TestWarnIfPartialPlainOnAPipe(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	summary := &sem.SnapshotSummary{}
	summary.Stats.CompletenessLevel = "degraded"
	var out bytes.Buffer
	warnIfPartial(&out, false, summary)
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("banner on a pipe must carry no ANSI escapes:\n%q", out.String())
	}
}
