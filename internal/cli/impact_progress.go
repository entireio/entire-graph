package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/entireio/cli/cmd/entire/cli/palette"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

type impactProgressReporter func(stage, detail string)

func (r impactProgressReporter) update(stage, detail string) {
	if r != nil {
		r(stage, detail)
	}
}

func (r impactProgressReporter) snapshot(event sem.ProgressEvent) {
	switch event.Phase {
	case sem.BuildPhaseInventory:
		r.update("Discovering source files", fmt.Sprintf("%d files found", event.FilesTotal))
	case sem.BuildPhaseParse:
		r.update("Parsing source files", fmt.Sprintf("%d/%d files · %s symbols", event.FilesDone, event.FilesTotal, humanInt(int64(event.Symbols))))
	case sem.BuildPhaseRelations:
		r.update("Linking code relationships", fmt.Sprintf("%s relationships", humanInt(int64(event.Relations))))
	case sem.BuildPhaseFinalize:
		r.update("Finalizing graph", "Preparing the graph for impact analysis")
	}
}

type impactStageMsg struct{ stage, detail string }
type impactProgressDone struct{ err error }

type impactProgressModel struct {
	query    string
	spinner  spinner.Model
	stages   []impactStageMsg
	started  time.Time
	width    int
	finished bool
	err      error
}

// Match Entire CLI's dispatch TUI: MiniDot, base16 semantic palette, default
// body foreground, and muted secondary text. Import the palette itself so
// Graph does not maintain a competing set of brand colors.
func newImpactProgressModel(query string) impactProgressModel {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Accent)).Bold(true)
	return impactProgressModel{
		query: termsafe.Line(query), spinner: sp, started: time.Now(), width: 80,
		stages: []impactStageMsg{{stage: "Opening repository"}},
	}
}

func (m impactProgressModel) Init() tea.Cmd { return m.spinner.Tick }

func (m impactProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(20, msg.Width)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case impactStageMsg:
		last := len(m.stages) - 1
		if last >= 0 && m.stages[last].stage == msg.stage {
			m.stages[last] = msg
		} else {
			m.stages = append(m.stages, msg)
		}
	case impactProgressDone:
		m.finished, m.err = true, msg.err
		return m, tea.Quit
	}
	return m, nil
}

func (m impactProgressModel) View() tea.View {
	if m.finished && m.err == nil {
		return tea.NewView("") // Bubble Tea clears the inline view before results.
	}
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Accent)).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Muted))
	success := lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Success))
	failure := lipgloss.NewStyle().Foreground(lipgloss.Color(palette.Error))
	lines := []string{accent.Render("Analyzing impact") + " · " + m.query, ""}
	for i, stage := range m.stages {
		marker := success.Render("✓")
		if i == len(m.stages)-1 {
			marker = m.spinner.View()
			if m.err != nil {
				marker = failure.Render("✗")
			}
		}
		line := marker + " " + termsafe.Line(stage.stage)
		if stage.detail != "" {
			line += muted.Render(" — " + termsafe.Line(stage.detail))
		}
		lines = append(lines, line)
	}
	footer := fmt.Sprintf("Elapsed %s", time.Since(m.started).Round(time.Second))
	if m.finished {
		footer += " · Analysis failed"
	} else {
		footer += " · Ctrl+C to cancel"
	}
	lines = append(lines, "", muted.Render(footer))
	return tea.NewView("\n" + lipgloss.NewStyle().Width(m.width).Render(strings.Join(lines, "\n")) + "\n")
}

func runImpactProgress(ctx context.Context, out io.Writer, query string, work func(context.Context, impactProgressReporter) (impactResponse, error)) (impactResponse, error) {
	if !isTerminal(out) {
		return work(ctx, nil)
	}
	// Follow the CLI's accessibility/NO_COLOR/legacy-terminal conventions.
	// These terminals get stage announcements without cursor movement.
	if os.Getenv("ACCESSIBLE") != "" || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || os.Getenv("TERM") == "cygwin" {
		fmt.Fprintln(out)
		lastStage := ""
		return work(ctx, func(stage, detail string) {
			if stage != lastStage {
				fmt.Fprintln(out, termsafe.Line(stage)+"...")
				lastStage = stage
			}
		})
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// No input reader or terminal capability queries: progress must not consume
	// stdin or leave terminal response bytes in the user's next shell prompt.
	program := tea.NewProgram(newImpactProgressModel(query), tea.WithOutput(out), tea.WithInput(nil), tea.WithContext(ctx))
	done := make(chan struct{})
	var uiErr error
	go func() {
		_, uiErr = program.Run()
		cancel() // A signal or renderer failure must also stop the analysis.
		close(done)
	}()
	response, err := work(ctx, func(stage, detail string) { program.Send(impactStageMsg{stage, detail}) })
	program.Send(impactProgressDone{err: err})
	<-done
	if err != nil {
		return response, err
	}
	if uiErr != nil {
		return response, fmt.Errorf("impact progress: %w", uiErr)
	}
	return response, nil
}
