package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
)

// stats answers the question users actually ask about a code graph: "did this save me
// anything?". It is a local, read-only report over the coding-agent session transcripts that
// already exist on disk (Claude Code writes one JSONL file per session under
// ~/.claude/projects/<path-slug>/). Nothing is uploaded and nothing is written.
//
// It reports three things the graph cannot otherwise show:
//   - how often the graph was used vs. how often the session fell back to grep/find/whole-file
//     reads (the behaviour the graph is supposed to replace),
//   - how many bytes/tokens each of those two paths pulled into context,
//   - an ESTIMATED token saving, computed from a single explicitly documented assumption
//     (see savingsModelText) rather than presented as a measurement.

// savingsModelShort/savingsModelText document the counterfactual. Both the text and the JSON
// output carry it verbatim so the number is never quoted without its assumption.
const savingsModelShort = "each graph locate call is credited with the one whole-file read it replaced"

const savingsModelText = "Model (assumption, not a measurement): each graph locate call " +
	"(search/neighbors/impact) is credited with the ONE whole-file read it replaced. " +
	"Counterfactual = on-disk size of the top-hit file that call pointed at (the repo's median " +
	"tracked-file size when the path cannot be resolved), minus the bytes the graph call " +
	"actually returned, floored at 0, converted at 4 bytes = 1 token. Real savings depend on " +
	"what you would actually have read instead; treat this as an estimate, not ground truth."

// bytesPerToken is the standard rough transcript-accounting conversion. Tool results are raw
// text, so token counts are not recorded per call anywhere; 4 bytes/token is the usual
// approximation and is stated in the output.
const bytesPerToken = 4

// graphLocateVerbs are the verbs whose whole purpose is to replace exploration, and therefore
// the only ones credited with savings. Bulk/ingest verbs (snapshot, edges, symbols) and
// change verbs (diff, analyze) replace nothing a human would have read whole.
var graphLocateVerbs = map[string]bool{
	"search":    true,
	"neighbors": true,
	"impact":    true,
}

// graphVerbs is the closed set of verbs the plugin dispatches. Matching against it (rather
// than "any word after entire-graph") is what stops a path argument from being read as an
// invocation — e.g. `find /repos/entire-graph -path '*.go'` is exploration, not a graph call.
var graphVerbs = map[string]bool{
	"search": true, "neighbors": true, "impact": true, "diff": true, "commit": true,
	"checkpoint": true, "analyze": true, "doctor": true, "capabilities": true,
	"snapshot": true, "symbols": true, "edges": true, "index": true, "stats": true,
	"agent-guide": true, "init-agents": true, "version": true, "help": true,
}

// bashExploreCommands are the shell tools the graph is meant to displace. Matched as the first
// word of any pipeline/`&&`/`;` segment.
var bashExploreCommands = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"find": true, "cat": true, "head": true, "tail": true, "sed": true, "awk": true,
	"less": true, "more": true,
}

// commandPrefixes are wrappers that precede the real command word (`rtk grep ...`,
// `sudo find ...`, `xargs cat`). Skipped when identifying a segment's command.
var commandPrefixes = map[string]bool{
	"rtk": true, "sudo": true, "command": true, "time": true, "nice": true, "xargs": true, "env": true,
}

var filePathJSONPattern = regexp.MustCompile(`"(?:file_path|path|file)"\s*:\s*"((?:[^"\\]|\\.)*)"`)

var pathTokenPattern = regexp.MustCompile(`[A-Za-z0-9_./+-]*[A-Za-z0-9_-]\.[A-Za-z0-9_]{1,12}`)

const (
	kindReadRange = "Read (line range)"
	kindReadWhole = "Read (whole file)"
	kindGrep      = "Grep"
	kindGlob      = "Glob"
	kindBash      = "Bash grep/find/read"
)

// explorationKindOrder fixes report ordering so runs are diffable.
var explorationKindOrder = []string{kindReadWhole, kindReadRange, kindGrep, kindGlob, kindBash}

type statsFlags struct {
	Repo        string
	Since       string
	Format      string
	SessionsDir string
	Transcript  string
}

type statsCount struct {
	Name           string `json:"name"`
	Calls          int    `json:"calls"`
	ReturnedBytes  int64  `json:"returned_bytes"`
	ReturnedTokens int64  `json:"returned_est_tokens"`
}

type statsTokens struct {
	Input      int64 `json:"input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type statsResponse struct {
	FormatVersion             int          `json:"format_version"`
	Provider                  string       `json:"provider"`
	RepoRoot                  string       `json:"repo_root"`
	SessionsDir               string       `json:"sessions_dir"`
	SessionsDirFound          bool         `json:"sessions_dir_found"`
	Since                     string       `json:"since"`
	WindowStart               string       `json:"window_start"`
	WindowEnd                 string       `json:"window_end"`
	Sessions                  int          `json:"sessions"`
	Transcripts               int          `json:"transcripts"`
	MalformedLinesSkipped     int          `json:"malformed_lines_skipped"`
	GraphCalls                int          `json:"graph_calls"`
	ExplorationCalls          int          `json:"exploration_calls"`
	SessionsWithLocate        int          `json:"sessions_with_locate"`
	GraphFirstSessions        int          `json:"graph_first_sessions"`
	GraphFirstRate            float64      `json:"graph_first_rate"`
	GraphByVerb               []statsCount `json:"graph_calls_by_verb"`
	ExplorationByKind         []statsCount `json:"exploration_by_kind"`
	GraphReturnedBytes        int64        `json:"graph_returned_bytes"`
	GraphReturnedTokens       int64        `json:"graph_returned_est_tokens"`
	ExplorationReturnedBytes  int64        `json:"exploration_returned_bytes"`
	ExplorationReturnedTokens int64        `json:"exploration_returned_est_tokens"`
	SessionTokens             statsTokens  `json:"session_tokens"`
	EstimatedSavingsBytes     int64        `json:"estimated_savings_bytes"`
	EstimatedSavingsTokens    int64        `json:"estimated_savings_est_tokens"`
	EstimatedSavingsPct       float64      `json:"estimated_savings_pct_of_session_tokens"`
	CreditedGraphCalls        int          `json:"credited_graph_calls"`
	MedianTrackedFileBytes    int64        `json:"median_tracked_file_bytes"`
	SavingsModel              string       `json:"savings_model"`
}

func runStats(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseStatsFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("stats received unexpected arguments: %s", strings.Join(rest, " "))
	}
	switch flags.Format {
	case "text", "json":
	default:
		return fmt.Errorf("stats --format must be text or json, got %q", flags.Format)
	}
	if flags.Transcript != "" && flags.SessionsDir != "" {
		return fmt.Errorf("stats --transcript and --sessions-dir are mutually exclusive")
	}
	window, err := parseSince(flags.Since)
	if err != nil {
		return err
	}

	repo, repoErr := resolveRepo(ctx, opts.Env, flags.Repo)
	if repoErr != nil && flags.SessionsDir == "" && flags.Transcript == "" {
		return repoErr
	}
	if repo != "" {
		if abs, err := filepath.Abs(repo); err == nil {
			repo = abs
		}
	}

	sessionsDir := flags.SessionsDir
	found := true
	switch {
	case flags.Transcript != "":
		// Single-session scope: the caller already knows exactly which transcript it wants
		// (a status line rendering the live session, say). Reported as sessions_dir so the
		// JSON shape stays identical.
		sessionsDir = flags.Transcript
		if info, err := os.Stat(sessionsDir); err != nil || !info.Mode().IsRegular() {
			found = false
		}
	case sessionsDir == "":
		sessionsDir, found = resolveSessionsDir(repo)
	default:
		if info, err := os.Stat(sessionsDir); err != nil || !info.IsDir() {
			found = false
		}
	}

	report := statsResponse{
		FormatVersion:    1,
		Provider:         sem.ProviderName,
		RepoRoot:         repo,
		SessionsDir:      sessionsDir,
		SessionsDirFound: found,
		Since:            flags.Since,
		SavingsModel:     savingsModelText,
		// non-nil so JSON never emits null for the tables
		GraphByVerb:       []statsCount{},
		ExplorationByKind: []statsCount{},
	}

	if found {
		collector := newStatsCollector(ctx, repo)
		scan := collector.scan
		if flags.Transcript != "" {
			scan = collector.scanTranscript
		}
		if err := scan(sessionsDir); err != nil {
			return err
		}
		collector.finish(&report, window)
	}

	if flags.Format == "json" {
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	writeStatsText(opts.Stdout, report)
	return nil
}

func parseStatsFlags(args []string) (statsFlags, []string, error) {
	flags := statsFlags{Since: "30d", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--repo":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Repo, index = value, next
		case "--since":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Since, index = value, next
		case "--format":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Format, index = value, next
		case "--sessions-dir":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.SessionsDir, index = value, next
		case "--transcript":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.Transcript, index = value, next
		default:
			rest = append(rest, args[index])
		}
	}
	return flags, rest, nil
}

// parseSince returns the lookback window. A zero duration means "all history".
func parseSince(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" || trimmed == "all" {
		return 0, nil
	}
	unit := trimmed[len(trimmed)-1]
	var scale time.Duration
	switch unit {
	case 'h':
		scale = time.Hour
	case 'd':
		scale = 24 * time.Hour
	case 'w':
		scale = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("stats --since must be all or <n>h|<n>d|<n>w, got %q", value)
	}
	amount, err := strconv.Atoi(trimmed[:len(trimmed)-1])
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("stats --since must be all or <n>h|<n>d|<n>w, got %q", value)
	}
	return time.Duration(amount) * scale, nil
}

// resolveSessionsDir maps a repo path to the Claude Code transcript directory. Claude Code
// slugifies the absolute project path into a single directory name; historically that is a
// straight `/` -> `-` substitution, but non-alphanumerics are also folded, so both candidates
// are probed and the first that exists wins.
func resolveSessionsDir(repo string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || repo == "" {
		return "", false
	}
	root := filepath.Join(home, ".claude", "projects")
	for _, slug := range sessionSlugCandidates(repo) {
		candidate := filepath.Join(root, slug)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, true
		}
	}
	return filepath.Join(root, sessionSlugCandidates(repo)[0]), false
}

func sessionSlugCandidates(repo string) []string {
	slashOnly := strings.ReplaceAll(repo, string(filepath.Separator), "-")
	folded := []rune(slashOnly)
	for i, r := range folded {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			folded[i] = '-'
		}
	}
	candidates := []string{string(folded)}
	if slashOnly != string(folded) {
		candidates = append(candidates, slashOnly)
	}
	return candidates
}

// --- transcript model -------------------------------------------------------------------

type transcriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Message   struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens     int64 `json:"input_tokens"`
			CacheCreationIn int64 `json:"cache_creation_input_tokens"`
			CacheReadIn     int64 `json:"cache_read_input_tokens"`
			OutputTokens    int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

// pendingCall remembers what a tool_use was, so the matching tool_result can be attributed to
// the graph bucket or the exploration bucket.
type pendingCall struct {
	session string
	graph   bool
	verb    string
	kind    string
}

type sessionAcc struct {
	key           string
	first, last   time.Time
	graphCalls    int
	exploreCalls  int
	firstLocate   string // "graph" | "explore"
	verbCalls     map[string]int
	verbBytes     map[string]int64
	exploreCalls2 map[string]int
	exploreBytes  map[string]int64
	usageByID     map[string]statsTokens // one entry per assistant message; see addUsage
	unkeyedTokens statsTokens            // usage from records carrying no message id
	savingsBytes  int64
	creditedCalls int
}

func newSessionAcc(key string) *sessionAcc {
	return &sessionAcc{
		key:           key,
		verbCalls:     map[string]int{},
		verbBytes:     map[string]int64{},
		exploreCalls2: map[string]int{},
		exploreBytes:  map[string]int64{},
		usageByID:     map[string]statsTokens{},
	}
}

// addUsage records ONE assistant message's billed usage.
//
// Claude Code does not write one transcript record per assistant message: it writes one record
// per content block (text, each tool_use, …), and every one of those records repeats the SAME
// `message.id` and the SAME `usage` block. Summing usage per RECORD therefore bills a turn once
// per block — measured at 2.3x-3.0x the true totals on real sessions. Usage is keyed by
// `message.id` instead, so a message counts exactly once however many records it spans.
//
// Last write wins: across a message's records the input/cache figures are identical while
// output_tokens is partial on the earlier records and complete on the final one (verified over
// ~7,500 usage-bearing records in ~400 transcripts: the last record was never below any earlier
// record on any field, and last-per-id reproduced the run's own `claude -p --output-format json`
// usage exactly in 250/250 benchmark sessions).
//
// Records with no id cannot be deduplicated, so they accumulate per record. That can only ever
// over-count, never drop tokens — silently losing usage would be the worse failure.
func (a *sessionAcc) addUsage(id string, usage statsTokens) {
	if id == "" {
		a.unkeyedTokens.Input += usage.Input
		a.unkeyedTokens.CacheWrite += usage.CacheWrite
		a.unkeyedTokens.CacheRead += usage.CacheRead
		a.unkeyedTokens.Output += usage.Output
		return
	}
	a.usageByID[id] = usage
}

// tokens folds the per-message usage into the session's billed total.
func (a *sessionAcc) tokens() statsTokens {
	total := a.unkeyedTokens
	for _, usage := range a.usageByID {
		total.Input += usage.Input
		total.CacheWrite += usage.CacheWrite
		total.CacheRead += usage.CacheRead
		total.Output += usage.Output
	}
	total.Total = total.Input + total.CacheWrite + total.CacheRead + total.Output
	return total
}

type statsCollector struct {
	ctx         context.Context
	repo        string
	sessions    map[string]*sessionAcc
	pending     map[string]pendingCall
	sizeCache   map[string]int64
	medianBytes int64
	medianDone  bool
	transcripts int
	malformed   int
}

func newStatsCollector(ctx context.Context, repo string) *statsCollector {
	return &statsCollector{
		ctx:       ctx,
		repo:      repo,
		sessions:  map[string]*sessionAcc{},
		pending:   map[string]pendingCall{},
		sizeCache: map[string]int64{},
	}
}

// scan walks the transcript directory. Top-level `<session>.jsonl` files are sessions;
// subagent transcripts live under `<session>/subagents/*.jsonl` and are folded into the
// owning session so delegated exploration is not invisible.
func (c *statsCollector) scan(dir string) error {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil //nolint:nilerr // an unreadable subtree must not fail the whole report
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, path := range files {
		if err := c.scanFile(dir, path); err != nil {
			return err
		}
	}
	return nil
}

// scanTranscript is scan narrowed to ONE session: the named `<session>.jsonl` plus the
// `<session>/` sibling directory holding its subagent transcripts. Both are keyed to the same
// session by sessionKeyForPath, so the accounting is byte-for-byte what scan would produce for
// that session. It exists because a project's transcript directory can reach hundreds of MB,
// which a per-render caller (status line) cannot afford to walk.
func (c *statsCollector) scanTranscript(path string) error {
	root := filepath.Dir(path)
	if err := c.scanFile(root, path); err != nil {
		return err
	}
	subagents := strings.TrimSuffix(path, ".jsonl")
	if subagents == path {
		return nil
	}
	if info, err := os.Stat(subagents); err != nil || !info.IsDir() {
		return nil //nolint:nilerr // a session without subagent transcripts is the common case
	}
	var files []string
	err := filepath.WalkDir(subagents, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree must not fail the whole report
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		files = append(files, current)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, file := range files {
		if err := c.scanFile(root, file); err != nil {
			return err
		}
	}
	return nil
}

func (c *statsCollector) scanFile(root, path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return nil //nolint:nilerr // skip unreadable transcripts rather than abort the report
	}
	defer func() { _ = handle.Close() }()

	c.transcripts++
	sessionKey := sessionKeyForPath(root, path)
	scanner := bufio.NewScanner(handle)
	// Transcript lines routinely exceed bufio's 64KiB default (large tool results inline).
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record transcriptRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			c.malformed++
			continue
		}
		c.consume(sessionKey, record)
	}
	if err := scanner.Err(); err != nil {
		// A truncated or over-long line is treated like a malformed line, not a hard failure.
		c.malformed++
	}
	return nil
}

// sessionKeyForPath folds `<session>/subagents/agent-x.jsonl` back onto `<session>`.
func sessionKeyForPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 1 {
		return parts[0]
	}
	return strings.TrimSuffix(parts[0], ".jsonl")
}

func (c *statsCollector) session(key string) *sessionAcc {
	acc, ok := c.sessions[key]
	if !ok {
		acc = newSessionAcc(key)
		c.sessions[key] = acc
	}
	return acc
}

func (c *statsCollector) consume(sessionKey string, record transcriptRecord) {
	acc := c.session(sessionKey)
	if ts, err := time.Parse(time.RFC3339, record.Timestamp); err == nil {
		if acc.first.IsZero() || ts.Before(acc.first) {
			acc.first = ts
		}
		if ts.After(acc.last) {
			acc.last = ts
		}
	}
	if usage := record.Message.Usage; usage != nil {
		acc.addUsage(record.Message.ID, statsTokens{
			Input:      usage.InputTokens,
			CacheWrite: usage.CacheCreationIn,
			CacheRead:  usage.CacheReadIn,
			Output:     usage.OutputTokens,
		})
	}
	for _, block := range decodeContentBlocks(record.Message.Content) {
		switch block.Type {
		case "tool_use":
			c.consumeToolUse(acc, block)
		case "tool_result":
			c.consumeToolResult(block)
		}
	}
}

func decodeContentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

func (c *statsCollector) consumeToolUse(acc *sessionAcc, block contentBlock) {
	if verb, ok := graphVerbFromToolUse(block); ok {
		acc.graphCalls++
		acc.verbCalls[verb]++
		if acc.firstLocate == "" {
			acc.firstLocate = "graph"
		}
		if block.ID != "" {
			c.pending[block.ID] = pendingCall{session: acc.key, graph: true, verb: verb}
		}
		return
	}
	kind, ok := explorationKindFromToolUse(block)
	if !ok {
		return
	}
	acc.exploreCalls++
	acc.exploreCalls2[kind]++
	if acc.firstLocate == "" {
		acc.firstLocate = "explore"
	}
	if block.ID != "" {
		c.pending[block.ID] = pendingCall{session: acc.key, kind: kind}
	}
}

func (c *statsCollector) consumeToolResult(block contentBlock) {
	call, ok := c.pending[block.ToolUseID]
	if !ok {
		return
	}
	delete(c.pending, block.ToolUseID)
	text := toolResultText(block.Content)
	size := int64(len(text))
	acc := c.session(call.session)
	if call.graph {
		acc.verbBytes[call.verb] += size
		if graphLocateVerbs[call.verb] {
			acc.savingsBytes += c.creditFor(text, size)
			acc.creditedCalls++
		}
		return
	}
	acc.exploreBytes[call.kind] += size
}

// creditFor implements the documented counterfactual: the whole-file read this graph call
// replaced, minus what the call itself cost, floored at 0.
func (c *statsCollector) creditFor(text string, returned int64) int64 {
	counterfactual := c.topHitFileSize(text)
	if counterfactual <= 0 {
		counterfactual = c.medianTrackedFileSize()
	}
	if counterfactual <= returned {
		return 0
	}
	return counterfactual - returned
}

// topHitFileSize resolves the first file the graph call pointed at and measures it on disk.
func (c *statsCollector) topHitFileSize(text string) int64 {
	for _, match := range filePathJSONPattern.FindAllStringSubmatch(text, 8) {
		var unquoted string
		if err := json.Unmarshal([]byte(`"`+match[1]+`"`), &unquoted); err != nil {
			unquoted = match[1]
		}
		if size := c.fileSize(unquoted); size > 0 {
			return size
		}
	}
	for _, candidate := range pathTokenPattern.FindAllString(text, 64) {
		if size := c.fileSize(candidate); size > 0 {
			return size
		}
	}
	return 0
}

func (c *statsCollector) fileSize(path string) int64 {
	if path == "" || c.repo == "" {
		return 0
	}
	if cached, ok := c.sizeCache[path]; ok {
		return cached
	}
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(c.repo, resolved)
	}
	var size int64
	if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
		size = info.Size()
	}
	c.sizeCache[path] = size
	return size
}

// medianTrackedFileSize is the fallback counterfactual when the top hit cannot be resolved on
// disk (file since deleted/renamed, or a different worktree). Tracked files first; a bounded
// filesystem walk if the directory is not a git repo.
func (c *statsCollector) medianTrackedFileSize() int64 {
	if c.medianDone {
		return c.medianBytes
	}
	c.medianDone = true
	if c.repo == "" {
		return 0
	}
	var sizes []int64
	if files, err := gitutil.ListIndexFiles(c.ctx, c.repo); err == nil {
		for _, name := range files {
			if info, err := os.Stat(filepath.Join(c.repo, name)); err == nil && info.Mode().IsRegular() {
				sizes = append(sizes, info.Size())
			}
			if len(sizes) >= 5000 {
				break
			}
		}
	}
	if len(sizes) == 0 {
		_ = filepath.WalkDir(c.repo, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // best-effort fallback measurement
			}
			if entry.IsDir() {
				if entry.Name() != "." && strings.HasPrefix(entry.Name(), ".") && path != c.repo {
					return filepath.SkipDir
				}
				return nil
			}
			if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
				sizes = append(sizes, info.Size())
			}
			if len(sizes) >= 5000 {
				return filepath.SkipAll
			}
			return nil
		})
	}
	if len(sizes) == 0 {
		return 0
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
	c.medianBytes = sizes[len(sizes)/2]
	return c.medianBytes
}

// toolResultText normalises the two shapes a tool_result carries: a plain string, or an array
// of content blocks.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var builder strings.Builder
		for _, block := range blocks {
			builder.WriteString(block.Text)
		}
		return builder.String()
	}
	return string(raw)
}

// graphVerbFromToolUse detects a graph invocation inside a Bash command. Both the plugin form
// (`entire graph search`) and the direct binary form (`/path/to/entire-graph search`) count,
// but only in COMMAND position of a pipeline segment and only when followed by a known verb —
// otherwise a repo path that merely ends in "entire-graph" would be mistaken for a call.
func graphVerbFromToolUse(block contentBlock) (string, bool) {
	if block.Name != "Bash" {
		return "", false
	}
	command, _ := block.Input["command"].(string)
	if command == "" {
		return "", false
	}
	return graphVerbFromCommand(command)
}

func graphVerbFromCommand(command string) (string, bool) {
	for _, segment := range splitShellSegments(command) {
		fields := strings.Fields(segment)
		for index := 0; index < len(fields); index++ {
			word := shellWord(fields[index])
			if word == "" || strings.Contains(word, "=") {
				continue
			}
			base := filepath.Base(word)
			if commandPrefixes[base] {
				continue
			}
			rest := fields[index+1:]
			if base == "entire" && len(rest) > 0 && shellWord(rest[0]) == "graph" {
				if verb, ok := firstGraphVerb(rest[1:]); ok {
					return verb, true
				}
			}
			if base == "entire-graph" {
				if verb, ok := firstGraphVerb(rest); ok {
					return verb, true
				}
			}
			break // this segment's command word is something else
		}
	}
	return "", false
}

func firstGraphVerb(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	verb := strings.TrimLeft(shellWord(fields[0]), "-")
	switch verb {
	case "v":
		verb = "version"
	case "h":
		verb = "help"
	}
	if graphVerbs[verb] {
		return verb, true
	}
	return "", false
}

func shellWord(field string) string {
	return strings.Trim(field, "\"'`()")
}

// explorationKindFromToolUse classifies the tool calls the graph is meant to displace.
func explorationKindFromToolUse(block contentBlock) (string, bool) {
	switch block.Name {
	case "Read":
		if _, ok := block.Input["offset"]; ok {
			return kindReadRange, true
		}
		if _, ok := block.Input["limit"]; ok {
			return kindReadRange, true
		}
		return kindReadWhole, true
	case "Grep":
		return kindGrep, true
	case "Glob":
		return kindGlob, true
	case "Bash":
		command, _ := block.Input["command"].(string)
		if isExploringShellCommand(command) {
			return kindBash, true
		}
	}
	return "", false
}

func isExploringShellCommand(command string) bool {
	if command == "" {
		return false
	}
	for _, segment := range splitShellSegments(command) {
		for _, field := range strings.Fields(segment) {
			word := shellWord(field)
			if word == "" || strings.Contains(word, "=") {
				continue
			}
			word = filepath.Base(word)
			if commandPrefixes[word] {
				continue
			}
			if bashExploreCommands[word] {
				return true
			}
			break
		}
	}
	return false
}

func splitShellSegments(command string) []string {
	return strings.FieldsFunc(command, func(r rune) bool {
		return r == '|' || r == ';' || r == '&' || r == '\n'
	})
}

// --- aggregation and rendering ----------------------------------------------------------

func (c *statsCollector) finish(report *statsResponse, window time.Duration) {
	report.Transcripts = c.transcripts
	report.MalformedLinesSkipped = c.malformed

	cutoff := time.Time{}
	if window > 0 {
		cutoff = time.Now().Add(-window)
	}

	verbCalls := map[string]int{}
	verbBytes := map[string]int64{}
	kindCalls := map[string]int{}
	kindBytes := map[string]int64{}

	keys := make([]string, 0, len(c.sessions))
	for key := range c.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var windowStart, windowEnd time.Time
	for _, key := range keys {
		acc := c.sessions[key]
		if !cutoff.IsZero() && !acc.last.IsZero() && acc.last.Before(cutoff) {
			continue
		}
		tokens := acc.tokens()
		if acc.graphCalls == 0 && acc.exploreCalls == 0 && tokens.Total == 0 {
			continue
		}
		report.Sessions++
		if !acc.first.IsZero() && (windowStart.IsZero() || acc.first.Before(windowStart)) {
			windowStart = acc.first
		}
		if acc.last.After(windowEnd) {
			windowEnd = acc.last
		}
		report.GraphCalls += acc.graphCalls
		report.ExplorationCalls += acc.exploreCalls
		switch acc.firstLocate {
		case "graph":
			report.SessionsWithLocate++
			report.GraphFirstSessions++
		case "explore":
			report.SessionsWithLocate++
		}
		for verb, count := range acc.verbCalls {
			verbCalls[verb] += count
		}
		for verb, size := range acc.verbBytes {
			verbBytes[verb] += size
		}
		for kind, count := range acc.exploreCalls2 {
			kindCalls[kind] += count
		}
		for kind, size := range acc.exploreBytes {
			kindBytes[kind] += size
		}
		report.SessionTokens.Input += tokens.Input
		report.SessionTokens.CacheWrite += tokens.CacheWrite
		report.SessionTokens.CacheRead += tokens.CacheRead
		report.SessionTokens.Output += tokens.Output
		report.EstimatedSavingsBytes += acc.savingsBytes
		report.CreditedGraphCalls += acc.creditedCalls
	}

	report.SessionTokens.Total = report.SessionTokens.Input + report.SessionTokens.CacheWrite +
		report.SessionTokens.CacheRead + report.SessionTokens.Output

	report.GraphByVerb = sortedCounts(verbCalls, verbBytes, nil)
	report.ExplorationByKind = sortedCounts(kindCalls, kindBytes, explorationKindOrder)
	for _, entry := range report.GraphByVerb {
		report.GraphReturnedBytes += entry.ReturnedBytes
	}
	for _, entry := range report.ExplorationByKind {
		report.ExplorationReturnedBytes += entry.ReturnedBytes
	}
	report.GraphReturnedTokens = report.GraphReturnedBytes / bytesPerToken
	report.ExplorationReturnedTokens = report.ExplorationReturnedBytes / bytesPerToken
	report.EstimatedSavingsTokens = report.EstimatedSavingsBytes / bytesPerToken
	if report.SessionTokens.Total > 0 {
		report.EstimatedSavingsPct = roundTo(float64(report.EstimatedSavingsTokens)*100/float64(report.SessionTokens.Total), 2)
	}
	if report.SessionsWithLocate > 0 {
		report.GraphFirstRate = roundTo(float64(report.GraphFirstSessions)/float64(report.SessionsWithLocate), 4)
	}
	report.MedianTrackedFileBytes = c.medianBytes
	if !windowStart.IsZero() {
		report.WindowStart = windowStart.UTC().Format(time.RFC3339)
	}
	if !windowEnd.IsZero() {
		report.WindowEnd = windowEnd.UTC().Format(time.RFC3339)
	}
}

func sortedCounts(calls map[string]int, bytes map[string]int64, order []string) []statsCount {
	out := make([]statsCount, 0, len(calls))
	seen := map[string]bool{}
	appendEntry := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, statsCount{
			Name:           name,
			Calls:          calls[name],
			ReturnedBytes:  bytes[name],
			ReturnedTokens: bytes[name] / bytesPerToken,
		})
	}
	for _, name := range order {
		if calls[name] > 0 {
			appendEntry(name)
		}
	}
	remaining := make([]string, 0, len(calls))
	for name := range calls {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		if calls[remaining[i]] != calls[remaining[j]] {
			return calls[remaining[i]] > calls[remaining[j]]
		}
		return remaining[i] < remaining[j]
	})
	for _, name := range remaining {
		appendEntry(name)
	}
	return out
}

func roundTo(value float64, places int) float64 {
	factor := 1.0
	for i := 0; i < places; i++ {
		factor *= 10
	}
	rounded := float64(int64(value*factor+0.5)) / factor
	return rounded
}

func writeStatsText(out io.Writer, report statsResponse) {
	fmt.Fprintln(out, "entire graph stats")
	fmt.Fprintf(out, "  repo:     %s\n", valueOrUnset(report.RepoRoot))
	if !report.SessionsDirFound {
		fmt.Fprintf(out, "  sessions: not found (looked in %s)\n", report.SessionsDir)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No coding-agent session transcripts exist for this repo yet, so there is")
		fmt.Fprintln(out, "nothing to compare. Run some work with `entire graph search` and re-run this.")
		return
	}
	fmt.Fprintf(out, "  sessions: %s\n", report.SessionsDir)
	fmt.Fprintf(out, "  window:   --since %s · %d session(s), %d transcript file(s)\n",
		report.Since, report.Sessions, report.Transcripts)
	if report.WindowStart != "" {
		fmt.Fprintf(out, "  covering: %s → %s\n", report.WindowStart, report.WindowEnd)
	}
	fmt.Fprintln(out)

	if report.Sessions == 0 {
		fmt.Fprintln(out, "No sessions in this window. Widen it with --since all.")
		return
	}

	fmt.Fprintf(out, "graph calls: %s · exploration calls: %s · graph-first rate: %s (%d/%d sessions)\n",
		humanInt(int64(report.GraphCalls)),
		humanInt(int64(report.ExplorationCalls)),
		percent(report.GraphFirstRate),
		report.GraphFirstSessions, report.SessionsWithLocate)
	fmt.Fprintln(out)

	if len(report.GraphByVerb) > 0 {
		fmt.Fprintln(out, "graph calls by verb")
		fmt.Fprintf(out, "  %-22s %8s %14s\n", "verb", "calls", "est. tokens")
		for _, entry := range report.GraphByVerb {
			fmt.Fprintf(out, "  %-22s %8s %14s\n", entry.Name, humanInt(int64(entry.Calls)), humanInt(entry.ReturnedTokens))
		}
		fmt.Fprintf(out, "  %-22s %8s %14s\n", "TOTAL", humanInt(int64(report.GraphCalls)), humanInt(report.GraphReturnedTokens))
		fmt.Fprintln(out)
	}

	if len(report.ExplorationByKind) > 0 {
		fmt.Fprintln(out, "exploration calls (what the graph is meant to replace)")
		fmt.Fprintf(out, "  %-22s %8s %14s\n", "kind", "calls", "est. tokens")
		for _, entry := range report.ExplorationByKind {
			fmt.Fprintf(out, "  %-22s %8s %14s\n", entry.Name, humanInt(int64(entry.Calls)), humanInt(entry.ReturnedTokens))
		}
		fmt.Fprintf(out, "  %-22s %8s %14s\n", "TOTAL", humanInt(int64(report.ExplorationCalls)), humanInt(report.ExplorationReturnedTokens))
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "session tokens (billed, read from transcript usage)")
	fmt.Fprintf(out, "  input %s · cache write %s · cache read %s · output %s · total %s\n",
		humanInt(report.SessionTokens.Input), humanInt(report.SessionTokens.CacheWrite),
		humanInt(report.SessionTokens.CacheRead), humanInt(report.SessionTokens.Output),
		humanInt(report.SessionTokens.Total))
	fmt.Fprintln(out)

	fmt.Fprintf(out, "ESTIMATED SAVINGS  ~%s tokens", humanInt(report.EstimatedSavingsTokens))
	if report.EstimatedSavingsPct > 0 {
		fmt.Fprintf(out, "  (~%.2f%% of billed session tokens)", report.EstimatedSavingsPct)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  credited graph calls: %d of %d (search/neighbors/impact only)\n",
		report.CreditedGraphCalls, report.GraphCalls)
	for _, line := range wrapText(savingsModelText, 88) {
		fmt.Fprintf(out, "  %s\n", line)
	}
}

func percent(rate float64) string {
	return fmt.Sprintf("%.0f%%", rate*100)
}

func humanInt(value int64) string {
	text := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	var parts []string
	for len(text) > 3 {
		parts = append([]string{text[len(text)-3:]}, parts...)
		text = text[:len(text)-3]
	}
	parts = append([]string{text}, parts...)
	joined := strings.Join(parts, ",")
	if negative {
		return "-" + joined
	}
	return joined
}

func wrapText(text string, width int) []string {
	words := strings.Fields(text)
	var lines []string
	var current string
	for _, word := range words {
		switch {
		case current == "":
			current = word
		case len(current)+1+len(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
