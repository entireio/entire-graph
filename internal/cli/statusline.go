package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
)

// `entire graph statusline` is the command Claude Code runs as its status line. It reads the
// status JSON on stdin and prints at most two lines:
//
//	◤ entire graph · ✓ indexed · 23 lookups · ~184K tok gross · ~12.4K net
//	▸ ctx 42%
//
// Every figure is an estimated token count from the stats collector (run in-process, never as a
// subprocess) over this session's transcript and its subagent transcripts. Nothing is priced:
// the line never shows money. The indexed state is sem.HeadIndexed, a few stats after resolving
// HEAD. A status line must never surface an error, so every failure past argument parsing prints
// nothing and returns nil.

const (
	statuslineDefaultColumns = 120
	// statuslineMaxStdin bounds the status JSON. Claude Code's payload is a few KB.
	statuslineMaxStdin = 1 << 20
	// statuslineCacheSchema salts the figures key, so a change to what is cached misses.
	statuslineCacheSchema = "statusline-v1"

	statuslineSeparator = " · "
	statuslineIndigo    = "\x1b[38;2;84;111;255m"
	statuslineMuted     = "\x1b[38;5;244m"
	statuslineReset     = "\x1b[0m"
)

type statuslineInput struct {
	TranscriptPath string `json:"transcript_path"`
	Workspace      struct {
		ProjectDir string `json:"project_dir"`
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	Cwd           string `json:"cwd"`
	ContextWindow *struct {
		UsedPercentage *float64 `json:"used_percentage"`
	} `json:"context_window"`
}

// statuslineFigures is what the cache holds: the computed numbers, never the rendered line, so the
// width and NO_COLOR always apply to the current render.
type statuslineFigures struct {
	Key          string `json:"key"`
	Lookups      int    `json:"lookups"`
	GrossTokens  int64  `json:"gross_covered_est_tokens"`
	NetTokens    int64  `json:"modeled_balance_est_tokens"`
	NetAvailable bool   `json:"net_available"`
}

// statuslinePart is a run of segment text; figure parts are coloured.
type statuslinePart struct {
	text   string
	figure bool
}

type statuslineSegment []statuslinePart

func (s statuslineSegment) width() int {
	total := 0
	for _, part := range s {
		total += utf8.RuneCountInString(part.text)
	}
	return total
}

func runStatusline(ctx context.Context, opts Options, args []string) error {
	var cacheFlag string
	var rest []string
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; {
		case arg == "--cache-dir":
			if index+1 >= len(args) {
				return fmt.Errorf("statusline --cache-dir requires a path")
			}
			index++
			cacheFlag = args[index]
		case strings.HasPrefix(arg, "--cache-dir="):
			cacheFlag = strings.TrimPrefix(arg, "--cache-dir=")
		default:
			rest = append(rest, arg)
		}
	}
	if len(rest) != 0 {
		return unexpectedArgumentsError("statusline", opts.Version, rest)
	}
	lines := renderStatusline(ctx, opts, resolveCacheDir(cacheFlag, opts.Env.PluginDataDir))
	if len(lines) == 0 || opts.Stdout == nil {
		return nil
	}
	_, _ = io.WriteString(opts.Stdout, strings.Join(lines, "\n")+"\n")
	return nil
}

// renderStatusline returns the rendered lines, or nil on any failure.
func renderStatusline(ctx context.Context, opts Options, cacheDir string) (lines []string) {
	defer func() {
		if recover() != nil {
			lines = nil
		}
	}()
	if opts.Stdin == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(opts.Stdin, statuslineMaxStdin+1))
	if err != nil || len(data) > statuslineMaxStdin {
		return nil
	}
	var input statuslineInput
	if json.Unmarshal(data, &input) != nil || input.TranscriptPath == "" {
		return nil
	}
	if info, err := os.Stat(input.TranscriptPath); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	repo := statuslineAnchorRepo(input)
	if repo == "" {
		return nil
	}

	// One metadata validation receipt serves the HEAD read below and every git call HeadIndexed makes.
	// The tree keys the figures cache; an unresolvable HEAD is the "not indexed" state.
	gitCtx, validationErr := sem.WithGitMetadataValidationForSetup(ctx, repo)
	tree := ""
	if validationErr == nil {
		if _, headTree, err := gitutil.HeadCommitAndTree(gitCtx, repo); err == nil {
			tree = headTree
		}
	}
	indexed := false
	if tree != "" {
		if indexed, err = sem.HeadIndexed(gitCtx, repo, opts.Version, cacheDir); err != nil {
			return nil
		}
	}
	figures, ok := statuslineFiguresFor(input.TranscriptPath, repo, tree, cacheDir, opts.Version)
	if !ok || ctx.Err() != nil {
		return nil
	}

	columns := statuslineColumns()
	color := os.Getenv("NO_COLOR") == ""
	if line := renderStatuslineLine(fitStatuslineSegments(statuslineMainSegments(indexed, figures), columns), color); line != "" {
		lines = append(lines, line)
	}
	if input.ContextWindow != nil && input.ContextWindow.UsedPercentage != nil {
		percent := int64(math.Round(*input.ContextWindow.UsedPercentage))
		usage := statuslineSegment{{text: "▸ ctx "}, {text: strconv.FormatInt(percent, 10) + "%", figure: true}}
		if line := renderStatuslineLine(fitStatuslineSegments([]statuslineSegment{usage}, columns), color); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// statuslineAnchorRepo is the session's repository: the project dir, else the current dir, else the cwd,
// walked up to its checkout root the same way stats resolves a call's cwd.
func statuslineAnchorRepo(input statuslineInput) string {
	anchor := input.Workspace.ProjectDir
	if anchor == "" {
		anchor = input.Workspace.CurrentDir
	}
	if anchor == "" {
		anchor = input.Cwd
	}
	if anchor == "" {
		return ""
	}
	if top, ok := discoverImplicitCheckoutRoot(anchor); ok {
		return top
	}
	abs, err := filepath.Abs(anchor)
	if err != nil {
		return ""
	}
	return abs
}

// statuslineFiguresFor serves the figures from the cache when every in-scope transcript file, HEAD's
// tree and this binary are unchanged, and otherwise runs the stats collector and caches the result.
func statuslineFiguresFor(transcript, repo, tree, cacheDir, version string) (statuslineFigures, bool) {
	files, err := listTranscriptFiles(transcript, true)
	if err != nil || len(files) == 0 {
		return statuslineFigures{}, false
	}
	keyParts := []string{statuslineCacheSchema, statsCacheSalt(version), repo, tree}
	for _, file := range files {
		identity := file.identity
		if identity == "" {
			identity = file.path
		}
		keyParts = append(keyParts, identity,
			strconv.FormatInt(file.size, 10), strconv.FormatInt(file.modTime.UnixNano(), 10))
	}
	key := statsCacheKey(keyParts...)
	path := ""
	if cacheDir != "" {
		path = filepath.Join(cacheDir, "statusline", statsCacheKey(transcript)+".json")
		if cached, ok := loadStatuslineFigures(path); ok && cached.Key == key {
			return cached, true
		}
	}

	collector := newStatsCollector()
	collector.cache = openStatsCache(cacheDir, transcript, version, files)
	collector.run(files, time.Time{})
	report := statsResponse{RepoRoot: repo}
	collector.finish(&report, time.Time{})
	figures := statuslineFigures{
		Key:          key,
		Lookups:      report.GraphLocateCalls,
		GrossTokens:  report.GrossCoveredEstTokens,
		NetTokens:    report.ObservedNetTokens,
		NetAvailable: report.SessionsWithSavingsComparison > 0,
	}
	if path != "" {
		saveStatuslineFigures(path, figures)
	}
	return figures, true
}

// loadStatuslineFigures treats every read or decode failure as a miss.
func loadStatuslineFigures(path string) (statuslineFigures, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the resolved cache directory
	if err != nil {
		return statuslineFigures{}, false
	}
	var figures statuslineFigures
	if json.Unmarshal(data, &figures) != nil {
		return statuslineFigures{}, false
	}
	return figures, true
}

// saveStatuslineFigures writes atomically and best effort: Claude Code cancels an in-flight render
// when a newer update arrives, so a killed write must never leave a torn file in place.
func saveStatuslineFigures(path string, figures statuslineFigures) {
	data, err := json.Marshal(figures)
	if err != nil {
		return
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return
	}
	temporary, err := os.CreateTemp(directory, ".statusline-*.tmp")
	if err != nil {
		return
	}
	name := temporary.Name()
	_, writeErr := temporary.Write(data)
	if err := temporary.Close(); err != nil || writeErr != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
	}
}

func statuslineMainSegments(indexed bool, figures statuslineFigures) []statuslineSegment {
	segments := []statuslineSegment{{{text: "◤", figure: true}, {text: " entire graph"}}}
	if indexed {
		segments = append(segments, statuslineSegment{{text: "✓ indexed"}})
	} else {
		segments = append(segments, statuslineSegment{{text: "not indexed"}}, statuslineSegment{{text: "run entire graph index"}})
	}
	if figures.Lookups == 0 {
		return segments
	}
	segments = append(segments,
		statuslineSegment{{text: strconv.Itoa(figures.Lookups), figure: true}, {text: " lookups"}},
		statuslineSegment{{text: formatStatuslineTokens(figures.GrossTokens), figure: true}, {text: " tok gross"}},
	)
	if figures.NetAvailable {
		segments = append(segments, statuslineSegment{{text: formatStatuslineTokens(figures.NetTokens), figure: true}, {text: " net"}})
	} else {
		segments = append(segments, statuslineSegment{{text: "net n/a"}})
	}
	return segments
}

// fitStatuslineSegments drops whole segments from the right until the line fits columns.
func fitStatuslineSegments(segments []statuslineSegment, columns int) []statuslineSegment {
	for len(segments) > 0 {
		width := utf8.RuneCountInString(statuslineSeparator) * (len(segments) - 1)
		for _, segment := range segments {
			width += segment.width()
		}
		if width <= columns {
			break
		}
		segments = segments[:len(segments)-1]
	}
	return segments
}

func renderStatuslineLine(segments []statuslineSegment, color bool) string {
	var line strings.Builder
	for index, segment := range segments {
		if index > 0 {
			if color {
				line.WriteString(statuslineMuted + statuslineSeparator + statuslineReset)
			} else {
				line.WriteString(statuslineSeparator)
			}
		}
		for _, part := range segment {
			if color && part.figure {
				line.WriteString(statuslineIndigo + part.text + statuslineReset)
			} else {
				line.WriteString(part.text)
			}
		}
	}
	return line.String()
}

// statuslineColumns is COLUMNS, which Claude Code sets, or the default when it is unset or invalid.
func statuslineColumns() int {
	columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
	if err != nil || columns <= 0 {
		return statuslineDefaultColumns
	}
	return columns
}

// formatStatuslineTokens renders an estimated token count: ~N below 1,000, then ~N.NK and ~N.NM
// (a trailing .0 dropped). A negative count is spelled with U+2212, as in "−7.3K".
func formatStatuslineTokens(value int64) string {
	if value < 0 {
		return "−" + statuslineMagnitude(uint64(-(value+1))+1)
	}
	return "~" + statuslineMagnitude(uint64(value))
}

func statuslineMagnitude(value uint64) string {
	if value < 1000 {
		return strconv.FormatUint(value, 10)
	}
	scaled, unit := float64(value)/1e3, "K"
	if math.Round(scaled*10)/10 >= 1000 {
		scaled, unit = float64(value)/1e6, "M"
	}
	text := strconv.FormatFloat(math.Round(scaled*10)/10, 'f', 1, 64)
	return strings.TrimSuffix(text, ".0") + unit
}
