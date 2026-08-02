package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestIsTerminalFalseForBuffer(t *testing.T) {
	// A bytes.Buffer is not a *os.File, so it must never be treated as a TTY —
	// this is what keeps progress bars and text summaries out of piped output.
	if isTerminal(&bytes.Buffer{}) {
		t.Fatal("bytes.Buffer reported as a terminal")
	}
}

func TestProgressBarParsePhaseShowsFileBar(t *testing.T) {
	var out bytes.Buffer
	bar := newProgressBar(&out, "indexing")
	bar.update(sem.ProgressEvent{
		Phase: "parse", FilesTotal: 100, FilesDone: 50,
		Symbols: 1200, Relations: 0, Elapsed: 2 * time.Second,
	})
	got := out.String()
	for _, want := range []string{"\r", "indexing", "50/100 files", "1,200 symbols", "█", "░", "\033[K"} {
		if !strings.Contains(got, want) {
			t.Errorf("parse-phase output missing %q:\n%q", want, got)
		}
	}

	out.Reset()
	bar.clear()
	if got := out.String(); !strings.Contains(got, "\033[K") {
		t.Errorf("clear did not erase the line: %q", got)
	}
}

func TestProgressBarRelationsPhaseShowsCountNotFullBar(t *testing.T) {
	// During the relations phase files are already at 100%; a full bar would read
	// as "done" while work continues, so we show the climbing relation count.
	var out bytes.Buffer
	newProgressBar(&out, "indexing").update(sem.ProgressEvent{
		Phase: "relations", FilesTotal: 100, FilesDone: 100,
		Symbols: 1200, Relations: 3400, Elapsed: 4 * time.Second,
	})
	got := out.String()
	for _, want := range []string{"indexing", "linking relations", "3,400 relations"} {
		if !strings.Contains(got, want) {
			t.Errorf("relations-phase output missing %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "100/100 files") {
		t.Errorf("relations phase should not show a full file bar:\n%q", got)
	}
}

func TestProgressBarClearNoopWhenUnrendered(t *testing.T) {
	// A cache hit returns before any progress event, so clear() must write nothing.
	var out bytes.Buffer
	newProgressBar(&out, "indexing").clear()
	if out.Len() != 0 {
		t.Fatalf("clear wrote output for an unrendered bar: %q", out.String())
	}
}

func TestProgressBarUnknownTotalShowsSpinner(t *testing.T) {
	var out bytes.Buffer
	newProgressBar(&out, "indexing").update(sem.ProgressEvent{
		Phase: "start", FilesTotal: 0, Symbols: 0, Relations: 0,
	})
	if got := out.String(); !strings.Contains(got, "parsing") {
		t.Errorf("expected a running indicator before the total is known, got %q", got)
	}
}

func TestWriteIndexTextSummary(t *testing.T) {
	cases := []struct {
		name     string
		response indexResponse
		report   string
		want     []string
		absent   []string
	}{
		{
			name: "cache hit",
			response: indexResponse{
				RepoRoot:       "/repo",
				Commit:         "abcdef1234567890",
				Profile:        "full",
				IndexCacheHit:  true,
				IndexLatencyMS: 42,
				Counts:         sem.ProviderStats{Files: 10, ParsedFiles: 9, Symbols: 100, Relations: 250, CompletenessLevel: "ok"},
			},
			want:   []string{"Indexed /repo", "abcdef123456", "full profile", "cache hit", "9 of 10 files", "100 symbols", "250 relations", "completeness: ok"},
			absent: []string{"warning", "report written"},
		},
		{
			name: "built with warnings and report",
			response: indexResponse{
				RepoRoot:       "/repo",
				Profile:        "fast",
				IndexCacheHit:  false,
				IndexLatencyMS: 1500,
				Counts:         sem.ProviderStats{Files: 3, ParsedFiles: 2, Symbols: 5, Relations: 1, CompletenessLevel: "degraded"},
				Warnings:       []sem.ProviderWarning{{Code: "W_X"}, {Code: "W_Y"}},
			},
			report: "GRAPH_REPORT.md",
			want:   []string{"built in 1.5s", "completeness: degraded", "2 warning(s)", "report written to GRAPH_REPORT.md"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := writeIndexText(&out, tc.response, tc.report); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("summary missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("summary unexpectedly contains %q:\n%s", absent, got)
				}
			}
		})
	}
}
