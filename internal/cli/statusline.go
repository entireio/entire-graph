package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
//	◤ entire graph · 17,271 nodes | 86,038 edges · ~184K tok gross · ~12.4K net
//	▸ ctx 42%
//
// Line 1 shows only what the user acts on: the indexed graph's size (symbol nodes and relations,
// as `index` reported them for HEAD's entry, written in full) only when HEAD itself is indexed;
// an index state segment and its remedy only when HEAD has a commit but no index ("index N
// commits behind" when a recent first-parent ancestor has one, else "not indexed"); then the gross
// and net figures once the session has made a graph lookup. Every token figure is an estimated
// count from the stats collector (run in-process, never as a subprocess) over this session's
// transcript and its subagent transcripts. Nothing is priced: the line never shows money. The
// index state is a stat of each sem.HeadIndexEntries entry, HEAD's and then each ancestor's, and
// the size one small read of the counts file beside HEAD's entry (sem.IndexEntryCounts); the
// entries and HEAD's tree are kept under a key read from .git without running git, so a warm
// render spawns no git at all. The line shows no index state when there is no commit yet, when git
// cannot be run on the checkout (metadata validation or the HEAD read fails), or when the index
// state cannot be read; the token figures stay. A status line must never surface an error, so
// every other failure past argument parsing prints nothing and returns nil, and so does an anchor
// outside any checkout.

const (
	statuslineDefaultColumns = 120
	// statuslineMaxStdin bounds the status JSON. Claude Code's payload is a few KB.
	statuslineMaxStdin = 1 << 20
	// statuslineCacheSchema salts the figures key, so a change to what is cached misses.
	statuslineCacheSchema = "statusline-v1"
	// statuslineGitCacheSchema salts the git key the same way.
	statuslineGitCacheSchema = "statusline-git-v2"

	statuslineSeparator = " · "
	statuslineIndigo    = "\x1b[38;2;84;111;255m"
	statuslineMuted     = "\x1b[38;5;244m"
	statuslineReset     = "\x1b[0m"
)

const (
	// statuslineNotIndexed is statuslineIndexDistance's answer when no entry exists.
	statuslineNotIndexed = -1
	// statuslineNoHead stands for a HEAD with no tree to index: unborn, git refused or failed, or
	// the index state could not be read. `index` cannot fix that, so the line shows no index state
	// and no remedy.
	statuslineNoHead = -2
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

// statuslinePart is a run of segment text; figure parts are coloured as figures and separator
// parts as the separator between segments.
type statuslinePart struct {
	text      string
	figure    bool
	separator bool
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
	cacheDir := resolveCacheDir(cacheFlag, opts.Env.PluginDataDir)
	if lines := renderStatusline(ctx, opts, cacheDir); len(lines) != 0 && opts.Stdout != nil {
		_, _ = io.WriteString(opts.Stdout, strings.Join(lines, "\n")+"\n")
	}
	// After the line is out, so the sweep never delays it.
	sweepStatuslineCache(cacheDir, time.Now())
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
	// Windows PowerShell 5.1 prefixes text it pipes to a native command with a UTF-8 BOM.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
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

	tree, head, behind, ok := statuslineHeadState(ctx, repo, cacheDir, opts.Version)
	if !ok {
		return nil
	}
	distance, current := statuslineIndexDistance(head, behind)
	if tree == "" {
		distance = statuslineNoHead
	}
	// The size is read on every render, hits included, and only for HEAD's own entry: beside a
	// stale state it would read as the current code's size.
	var size statuslineGraphSize
	if distance == 0 {
		size.symbols, size.relations, size.ok = sem.IndexEntryCounts(current)
	}
	figures, ok := statuslineFiguresFor(input.TranscriptPath, repo, tree, cacheDir, opts.Version)
	if !ok || ctx.Err() != nil {
		return nil
	}

	columns := statuslineColumns()
	color := os.Getenv("NO_COLOR") == ""
	if line := renderStatuslineLine(fitStatuslineSegments(statuslineMainSegments(distance, size, figures), columns), color); line != "" {
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
// walked up to its checkout root the same way stats resolves a call's cwd. "" when there is no anchor
// or it is in no checkout: the status line then prints nothing rather than "not indexed".
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
	return ""
}

// statuslineGitRecord is what a full-path render learnt from git, kept under the git key: HEAD's
// tree, the entries whose presence means it is indexed, and the same for each first-parent ancestor
// within sem.MaxStoreContextAncestorDistance, nearest first. It holds no figures or rendered text,
// and no indexed answer: the entries are stat'ed on every render, so a later `index` shows at once.
type statuslineGitRecord struct {
	Key     string             `json:"key"`
	Tree    string             `json:"tree"`
	Entries []sem.IndexEntry   `json:"entries"`
	Behind  [][]sem.IndexEntry `json:"behind"`
}

// statuslineHeadState returns HEAD's tree ("" when unborn, unresolvable, or when the index state
// cannot be read), its index entries and its ancestors' (nearest first). When the git key
// (sem.HeadKeyWithoutGit plus the repo, the cache dir and this binary) matches the stored record,
// it runs no git and skips the metadata validation, which guards git subprocesses and has none to
// guard. Otherwise it takes the full path (HEAD read once, then sem.HeadIndexEntries) and stores a
// record, but only when the validation passed, HEAD resolved (or is unborn), the ancestor walk
// succeeded and ctx was not cancelled, so a transient failure never pins an answer under an
// unchanged key. An index read error returns no tree, keeps nothing and still reports ok, so only
// the index state is dropped. ok is false when the render must print nothing.
func statuslineHeadState(ctx context.Context, repo, cacheDir, version string) (tree string, head []sem.IndexEntry, behind [][]sem.IndexEntry, ok bool) {
	key, headKey, unborn, keyed := "", "", false, false
	path := ""
	if cacheDir != "" {
		if headKey, unborn, keyed = sem.HeadKeyWithoutGit(repo); keyed {
			key = statsCacheKey(statuslineGitCacheSchema, statsCacheSalt(version), repo, cacheDir, headKey)
			path = filepath.Join(cacheDir, "statusline", "git-"+statsCacheKey(repo)+".json")
			var record statuslineGitRecord
			if loadStatuslineJSON(path, &record) && record.Key == key {
				return record.Tree, record.Entries, record.Behind, true
			}
		}
	}

	// One metadata validation receipt serves the HEAD read below and every git call
	// HeadIndexEntries makes, and HEAD is read once: HeadIndexEntries takes the commit and tree.
	// The tree keys the figures cache; with no tree the line shows no index state.
	gitCtx, validationErr := sem.WithGitMetadataValidationForSetup(ctx, repo)
	resolved := false
	if validationErr == nil {
		if commit, headTree, err := gitutil.HeadCommitAndTree(gitCtx, repo); err != nil {
			resolved = unborn
		} else {
			tree, resolved = headTree, true
			// The walk is an extra: when it fails the render still shows HEAD's own state, unkept.
			var walkErr error
			head, behind, walkErr, err = sem.HeadIndexEntries(gitCtx, repo, version, cacheDir, commit, tree,
				sem.MaxStoreContextAncestorDistance)
			if err != nil {
				// Drop only the index state: the token figures come from the transcript.
				return "", nil, nil, true
			}
			if walkErr != nil {
				resolved = false
			}
		}
	}
	// The key was read before the git calls. A checkout landing in between would store the new HEAD's
	// answer under the old HEAD's key, served again once the old bytes return, and could pair a tree
	// and entries from two HEADs. So save only when a second read finds the same key: a racy render
	// still shows its answer, it just does not keep it.
	if path != "" && resolved && ctx.Err() == nil {
		if again, _, stable := sem.HeadKeyWithoutGit(repo); stable && again == headKey {
			saveStatuslineJSON(path, statuslineGitRecord{Key: key, Tree: tree, Entries: head, Behind: behind})
		}
	}
	return tree, head, behind, true
}

// statuslineIndexDistance stats HEAD's entries, then each ancestor's in order, and returns 0 when
// HEAD is indexed, n when the nearest indexed ancestor is n commits back, and statuslineNotIndexed
// when none is. When HEAD is indexed, current is its first existing entry in sem.HeadIndexEntries
// order (full before fast), the one whose size is shown. It runs no git, so an `index` since the
// last render shows at once.
func statuslineIndexDistance(head []sem.IndexEntry, behind [][]sem.IndexEntry) (distance int, current sem.IndexEntry) {
	for _, entry := range head {
		if sem.AnyIndexEntryExists([]sem.IndexEntry{entry}) {
			return 0, entry
		}
	}
	for index, entries := range behind {
		if sem.AnyIndexEntryExists(entries) {
			return index + 1, sem.IndexEntry{}
		}
	}
	return statuslineNotIndexed, sem.IndexEntry{}
}

// statuslineGraphSize is the indexed graph's size as `index` reported it: symbol nodes and
// relations. ok is false when there is no size to show.
type statuslineGraphSize struct {
	symbols, relations int
	ok                 bool
}

// statuslineFiguresFor serves the figures from the cache when every in-scope transcript file, HEAD's
// tree and this binary are unchanged, and otherwise runs the stats collector and caches the result.
// The collector resumes a transcript that only grew from where the last render stopped
// (transcriptResume), so it parses only the appended lines.
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
		var cached statuslineFigures
		if loadStatuslineJSON(path, &cached) && cached.Key == key {
			return cached, true
		}
	}

	collector := newStatsCollector()
	memoDir := ""
	if cacheDir != "" {
		// Under statusline/, so the sweep removes a finished session's memo with its figures.
		memoDir = filepath.Join(cacheDir, "statusline")
	}
	collector.cache = openStatsCache(memoDir, transcript, version, files)
	collector.resume = newTranscriptResume(cacheDir, version)
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
		saveStatuslineJSON(path, figures)
	}
	return figures, true
}

// loadStatuslineJSON decodes a status line cache file into value. Every read or decode failure is
// a miss.
func loadStatuslineJSON(path string, value any) bool {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the resolved cache directory
	if err != nil {
		return false
	}
	return json.Unmarshal(data, value) == nil
}

// saveStatuslineJSON writes atomically and best effort: Claude Code cancels an in-flight render
// when a newer update arrives, so a killed write must never leave a torn file in place.
func saveStatuslineJSON(path string, value any) {
	data, err := json.Marshal(value)
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

const (
	// statuslineSweepEvery throttles the sweep to once a day, as the old script's prune was.
	statuslineSweepEvery = 24 * time.Hour
	// statuslineSweepAge is how long a status line cache file may go unwritten before it is swept.
	statuslineSweepAge = 7 * 24 * time.Hour
	// statuslineSweepTempAge is the age past which a temporary file is an orphan of a killed render.
	// A render writes and renames its temporary file within its own lifetime, which Claude Code
	// keeps far below this, so a file this old is never one a render is still writing.
	statuslineSweepTempAge = time.Hour
	// statuslineSweepStamp is the file whose mtime records the last sweep.
	statuslineSweepStamp = ".swept"
)

// sweepStatuslineCache removes what renders leave under <cache>/statusline: temporary files older
// than statuslineSweepTempAge, and every other file not written for statuslineSweepAge (a finished
// session's figures, checkpoint and memo, a repository's git record). Files a live render reads or
// writes are never that old: each write lands as a fresh temporary file renamed into place, and a
// file removed just before a read is a miss. It runs at most once per statuslineSweepEvery, gated by
// the stamp's mtime, which it rewrites before sweeping, so concurrent renders mostly skip and two
// that both sweep only race to remove the same files. Every error is ignored: a status line must
// never surface one. Reads stay inside the directory through os.Root and never follow a link.
func sweepStatuslineCache(cacheDir string, now time.Time) {
	if cacheDir == "" {
		return
	}
	root, err := os.OpenRoot(filepath.Join(cacheDir, "statusline"))
	if err != nil {
		return
	}
	defer root.Close()
	if info, err := root.Lstat(statuslineSweepStamp); err == nil && info.Mode().IsRegular() &&
		now.Sub(info.ModTime()) < statuslineSweepEvery {
		return
	}
	if root.WriteFile(statuslineSweepStamp, nil, 0o600) != nil {
		return
	}
	_ = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || path == statuslineSweepStamp {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		limit := statuslineSweepAge
		if strings.HasSuffix(entry.Name(), ".tmp") {
			limit = statuslineSweepTempAge
		}
		if now.Sub(info.ModTime()) > limit {
			_ = root.Remove(filepath.FromSlash(path))
		}
		return nil
	})
}

// statuslineMainSegments builds line 1. distance is statuslineIndexDistance's answer: the graph's
// size appears only when HEAD is indexed and its counts were read, the index state and its remedy
// only when HEAD has a commit but no index, and the figures only after a lookup.
func statuslineMainSegments(distance int, size statuslineGraphSize, figures statuslineFigures) []statuslineSegment {
	segments := []statuslineSegment{{{text: "◤", figure: true}, {text: " entire graph"}}}
	if distance == 0 && size.ok {
		segments = append(segments, statuslineSegment{
			{text: formatStatuslineCount(size.symbols), figure: true}, {text: " nodes"},
			{text: " | ", separator: true},
			{text: formatStatuslineCount(size.relations), figure: true}, {text: " edges"},
		})
	}
	switch {
	case distance == 1:
		segments = append(segments, statuslineSegment{{text: "index 1 commit behind"}})
	case distance > 1:
		segments = append(segments, statuslineSegment{{text: "index " + strconv.Itoa(distance) + " commits behind"}})
	case distance == statuslineNotIndexed:
		segments = append(segments, statuslineSegment{{text: "not indexed"}})
	}
	if distance > 0 || distance == statuslineNotIndexed {
		segments = append(segments, statuslineSegment{{text: "run entire graph index"}})
	}
	if figures.Lookups == 0 {
		return segments
	}
	segments = append(segments, statuslineSegment{{text: formatStatuslineTokens(figures.GrossTokens), figure: true}, {text: " tok gross"}})
	if figures.NetAvailable {
		segments = append(segments, statuslineSegment{{text: formatStatuslineTokens(figures.NetTokens), figure: true}, {text: " net"}})
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
			switch {
			case color && part.figure:
				line.WriteString(statuslineIndigo + part.text + statuslineReset)
			case color && part.separator:
				line.WriteString(statuslineMuted + part.text + statuslineReset)
			default:
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

// formatStatuslineCount writes a count in full with comma thousands separators: 17,271. Counts are
// never negative (sem.IndexEntryCounts refuses one).
func formatStatuslineCount(value int) string {
	digits := strconv.Itoa(value)
	for cut := len(digits) - 3; cut > 0; cut -= 3 {
		digits = digits[:cut] + "," + digits[cut:]
	}
	return digits
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
