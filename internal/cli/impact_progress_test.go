package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestImpactProgressKeepsCompletedStagesAndAnimatesBetweenEvents(t *testing.T) {
	t.Parallel()
	model := newImpactProgressModel("Target")
	for _, msg := range []impactStageMsg{
		{"Discovering source files", "10 files found"},
		{"Parsing source files", "1/10 files"},
		{"Parsing source files", "5/10 files"},
	} {
		updated, _ := model.Update(msg)
		model = updated.(impactProgressModel)
	}
	if len(model.stages) != 3 {
		t.Fatalf("counter updates added stages: %#v", model.stages)
	}
	view := model.View().Content
	for _, want := range []string{"Analyzing impact", "Opening repository", "Discovering source files", "5/10 files", "Elapsed"} {
		if !strings.Contains(view, want) {
			t.Fatalf("progress missing %q:\n%s", want, view)
		}
	}
	if !strings.HasPrefix(view, "\n") || strings.Count(view, "✓") != 2 {
		t.Fatalf("missing leading separation or completed markers:\n%s", view)
	}
	before := model.spinner.View()
	updated, nextTick := model.Update(model.Init()())
	model = updated.(impactProgressModel)
	if nextTick == nil || model.spinner.View() == before {
		t.Fatal("spinner must animate and schedule another tick without a provider event")
	}
	updated, _ = model.Update(impactProgressDone{})
	if view := updated.(impactProgressModel).View().Content; view != "" {
		t.Fatalf("successful completion did not clear the inline view: %q", view)
	}
}

func TestImpactProgressFailureAndUntrustedNames(t *testing.T) {
	t.Parallel()
	model := newImpactProgressModel("Target\nFORGED\x1b[2J")
	updated, _ := model.Update(impactProgressDone{err: errors.New("failed")})
	view := updated.(impactProgressModel).View().Content
	if !strings.Contains(view, "✗") || strings.Contains(view, "\nFORGED") || strings.Contains(view, "\x1b[2J") {
		t.Fatalf("failure status or name escaping incorrect: %q", view)
	}
}

func TestImpactProgressNonTerminalIsSilent(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	wantErr := errors.New("analysis failed")
	_, err := runImpactProgress(t.Context(), &out, "Target", func(_ context.Context, report impactProgressReporter) (impactResponse, error) {
		if report != nil {
			t.Fatal("redirected output must not enable provider progress overhead")
		}
		report.update("Opening repository", "")
		return impactResponse{}, wantErr
	})
	if !errors.Is(err, wantErr) || out.Len() != 0 {
		t.Fatalf("non-terminal run: error=%v, output=%q", err, out.String())
	}
}

func TestImpactProgressMatchesColdAndCachedAnalysis(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "app.go", "package app\nfunc Target() {}\nfunc Caller() { Target() }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	flags := impactFlags{Repo: repo, Symbol: "Target", Profile: "full", Format: "text", Depth: 2, Limit: 15, CacheDir: t.TempDir()}
	for _, cached := range []bool{false, true} {
		var stages []impactStageMsg
		report := impactProgressReporter(func(stage, detail string) { stages = append(stages, impactStageMsg{stage, detail}) })
		response, err := analyzeImpact(t.Context(), Options{Version: "test", Stderr: io.Discard}, flags, report)
		if err != nil {
			t.Fatal(err)
		}
		if response.IndexCacheHit != cached || response.Callers.Total != 1 {
			t.Fatalf("unexpected analysis result: hit=%v callers=%d", response.IndexCacheHit, response.Callers.Total)
		}
		sawParse, sawCache := false, false
		for _, stage := range stages {
			sawParse = sawParse || stage.stage == "Parsing source files"
			sawCache = sawCache || strings.Contains(stage.detail, "parsing and linking skipped")
		}
		if sawParse == cached || sawCache != cached {
			t.Fatalf("progress does not match cache behavior (cached=%v): %#v", cached, stages)
		}
		if stages[0].stage != "Opening repository" || stages[len(stages)-1].stage != "Preparing results" {
			t.Fatalf("incomplete lifecycle: %#v", stages)
		}
	}
}

func TestImpactProgressMapsProviderFinalizeAccurately(t *testing.T) {
	t.Parallel()
	var got impactStageMsg
	report := impactProgressReporter(func(stage, detail string) { got = impactStageMsg{stage, detail} })
	report.snapshot(sem.ProgressEvent{Phase: sem.BuildPhaseFinalize, FilesTotal: 10, FilesDone: 10})
	if got.stage != "Finalizing graph" || strings.Contains(got.detail, "parsing") {
		t.Fatalf("finalization reported as parsing: %#v", got)
	}
}
