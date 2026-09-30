package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// stats answers the question users actually ask about a code graph: "did this save me
// anything?". It is a local, read-only report over the coding-agent session transcripts that
// already exist on disk (Claude Code writes one JSONL file per session under
// ~/.claude/projects/<path-slug>/). Nothing is uploaded; the only thing written is a per-file
// memo under the cache directory, which --no-cache turns off.
//
// The DEFAULT output is one line: a signed, explicitly counterfactual context model.
// --verbose restores the full report (per-verb/per-kind tables, usage and model text), and
// --format json is a machine contract whose shape only ever grows.

// savingsModelShort/savingsModelText document the counterfactual. Both the text and the JSON
// output carry it verbatim so the number is never quoted without its assumption.
const savingsModelShort = "unvalidated 1:1 substitution assumption using same-session result bytes; not measured savings"

const savingsModelText = "Model (assumption, not a measurement): assume each observed graph locate " +
	"result replaces one exploration result at that session's average exploration bytes/result. " +
	"Subtract observed graph locate bytes and sum signed results, including losses, across sessions " +
	"with both result types. Positive means fewer bytes under this unvalidated 1:1 substitution " +
	"assumption; negative means more. Convert bytes to estimated tokens using the rough 4 bytes = 1 " +
	"token approximation. Unmatched queries, transformed or truncated outputs, and task quality " +
	"are not controlled. Transcripts cannot establish what would have been read instead or causal " +
	"billed-token savings. Legacy JSON savings fields retain their positive-only floor for " +
	"compatibility; the status line uses the signed unfloored fields, and the stats headline uses " +
	"observed displacement instead of this assumption."

// bytesPerToken is the standard rough transcript-accounting conversion. Tool results are raw
// text, so token counts are not recorded per call anywhere; 4 bytes/token is the usual
// approximation and is stated in the output. Actual ratios depend on the content and tokenizer.
const bytesPerToken = 4

// observedModelText documents the headline model. Carried verbatim in JSON and --verbose.
const observedModelText = "Observed displacement (heuristic, not a measurement): each graph locate result " +
	"(query/search/neighbors/impact) is classified by the agent's next K tool calls in the same transcript. " +
	"RE-QUERY: another graph lookup, Grep, Glob or shell search came first - no credit. READ-ANYWAY: the agent " +
	"first read a file whose path overlaps a path the graph output named - no credit. DISPLACED: neither - " +
	"credited with that session's average exploration bytes/result. Every locate result's bytes are subtracted; " +
	"the net is signed, summed over sessions with both result types, and converted at ~4 bytes/token. Limits: " +
	"path overlap is a suffix match on extension-bearing paths, so a read of an extensionless file is missed and " +
	"a same-named file elsewhere can match; K bounds the window, so a re-query or read after K calls still counts " +
	"as displaced; work delegated to a subagent is not visible in the caller's window; displaced does not mean " +
	"the answer was right or that the agent would otherwise have explored."

// substitutionRatio is how many exploration calls one graph locate call is assumed to displace.
// This is an unvalidated counterfactual, not an empirical coefficient for these transcripts.
const substitutionRatio = 1

// graphLocateVerbs are the verbs whose whole purpose is to replace exploration, and therefore
// the only ones credited with savings. Bulk/ingest verbs (snapshot, edges, symbols) and
// change verbs (diff, analyze) replace nothing a human would have read whole.
var graphLocateVerbs = map[string]bool{
	"query":     true,
	"search":    true,
	"neighbors": true,
	"impact":    true,
}

// graphVerbs is the closed set of verbs the plugin dispatches. Matching against it (rather
// than "any word after entire-graph") is what stops a path argument from being read as an
// invocation — e.g. `find /repos/entire-graph -path '*.go'` is exploration, not a graph call.
//
// It MUST list every command in the dispatch switch. A verb missing here is not merely
// uncounted: it falls through BOTH classifications, counting as neither a graph call nor an
// exploration call, so real usage of it is invisible in every report and in the status-line
// badge. That is how def, explain, verify, health and snapshot-query — all shipped, all in
// `entire graph help` — went unmeasured while adoption was being diagnosed from these numbers.
// The drift is the defect, so TestGraphVerbsCoverEveryCommand fails the build on the next one
// rather than trusting this list to be maintained by hand.
var graphVerbs = map[string]bool{
	"query": true, "search": true, "neighbors": true, "impact": true, "diff": true, "commit": true,
	"checkpoint": true, "analyze": true, "doctor": true, "capabilities": true,
	"snapshot": true, "snapshot-query": true, "symbols": true, "edges": true, "index": true,
	"stats": true, "def": true, "explain": true, "verify": true, "health": true,
	"agent-guide": true, "init-agents": true, "version": true, "help": true,
}

// semGraphVerbs are the `entire sem` subcommands that answer the same structural question the
// graph does. They are a subset of graphVerbs (TestSemGraphVerbsAreIndexable), so a sem call lands
// in graph_calls_by_verb under a name the rest of the report already indexes.
var semGraphVerbs = map[string]bool{"edges": true, "symbols": true}

// bashExploreCommands are the shell tools the graph is meant to displace. Matched as the command
// word of a pipeline's LEADING stage — see isExploringShellCommand.
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
	CacheDir    string
	NoCache     bool
	Verbose     bool
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

// statsResponse is a machine contract: fields are only ever ADDED, never removed or renamed.
// The statusline reads the signed unfloored estimate plus comparison availability.
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
	// MedianTrackedFileBytes is RETIRED and always 0. The savings model no longer prices a
	// counterfactual from the repository's median tracked-file size, so nothing computes it —
	// but the key stays in the contract, because removing a key is the one change a consumer
	// cannot absorb.
	MedianTrackedFileBytes int64  `json:"median_tracked_file_bytes"`
	SavingsModel           string `json:"savings_model"`

	// --- added fields; older consumers ignore them --------------------------------------

	// TranscriptsConsidered is every `*.jsonl` found in scope; Transcripts is how many were
	// actually opened after the --since window pruned the rest by file mtime.
	TranscriptsConsidered int `json:"transcripts_considered"`
	// TranscriptsFromCache is how many of Transcripts were served from the per-file memo
	// instead of being re-parsed.
	TranscriptsFromCache int `json:"transcripts_from_cache"`
	// GraphLocateCalls / GraphLocateReturnedBytes are the search+neighbors+impact subset —
	// the only calls the savings model prices. CreditedGraphCalls is the subset of those whose
	// tool_result was actually observed, which is what the per-call price divides by.
	GraphLocateCalls         int     `json:"graph_locate_calls"`
	GraphLocateReturnedBytes int64   `json:"graph_locate_returned_bytes"`
	GraphBytesPerLocateCall  float64 `json:"graph_bytes_per_locate_call"`
	ExplorationBytesPerCall  float64 `json:"exploration_bytes_per_call"`
	// SessionsWithPositiveSavings counts positive modeled results, not verified task savings.
	SessionsWithPositiveSavings int     `json:"sessions_with_positive_savings"`
	SubstitutionRatio           float64 `json:"substitution_ratio"`
	// EstimatedSavingsBytesUnfloored / EstimatedSavingsTokensUnfloored are the SAME model with the
	// per-session floor removed, so a session that cost more than it saved subtracts instead of
	// contributing 0. EstimatedSavingsBytes sums max(0, saved) per session, which keeps every win
	// and discards every loss — a positive bias, and the only direct sign bias in this report.
	//
	// The floored fields remain for compatibility. Human output uses the signed model and
	// SessionsWithSavingsComparison to distinguish zero from an unavailable comparison.
	EstimatedSavingsBytesUnfloored  int64 `json:"estimated_savings_bytes_unfloored"`
	EstimatedSavingsTokensUnfloored int64 `json:"estimated_savings_est_tokens_unfloored"`
	// SessionsWithSavingsComparison counts sessions containing at least one observed graph
	// locate result and one observed exploration result. Availability is not causal validity.
	SessionsWithSavingsComparison int `json:"sessions_with_savings_comparison"`

	// --- observed displacement (replaces the assumed 1:1 substitution in the headline) --------

	// DisplacementWindowCalls is K: how many following tool calls decide a locate call's class.
	DisplacementWindowCalls int `json:"displacement_window_calls"`
	// GraphLocateDisplaced / Requery / ReadAnyway partition the observed locate results
	// (CreditedGraphCalls) by what the agent did next. Rates divide by their sum, over every
	// session in the window.
	GraphLocateDisplaced  int     `json:"graph_locate_displaced"`
	GraphLocateRequery    int     `json:"graph_locate_requery"`
	GraphLocateReadAnyway int     `json:"graph_locate_read_anyway"`
	DisplacedRate         float64 `json:"displaced_rate"`
	RequeryRate           float64 `json:"requery_rate"`
	ReadAnywayRate        float64 `json:"read_anyway_rate"`
	// ObservedNetBytes credits only DISPLACED results at the session's exploration price and
	// subtracts every locate result's bytes. Signed, summed over the same comparison population as
	// the unfloored 1:1 fields (SessionsWithSavingsComparison), and unavailable when that is 0.
	ObservedNetBytes          int64  `json:"observed_net_bytes"`
	ObservedNetTokens         int64  `json:"observed_net_est_tokens"`
	ObservedDisplacementModel string `json:"observed_displacement_model"`
}

func runStats(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseStatsFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return unexpectedArgumentsError("stats", opts.Version, rest)
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
		FormatVersion:     1,
		Provider:          sem.ProviderName,
		RepoRoot:          repo,
		SessionsDir:       sessionsDir,
		SessionsDirFound:  found,
		Since:             flags.Since,
		SavingsModel:      savingsModelText,
		SubstitutionRatio: substitutionRatio,
		// K is reported even when nothing was classified, so a reader can always see the window.
		DisplacementWindowCalls:   displacementWindow,
		ObservedDisplacementModel: observedModelText,
		// non-nil so JSON never emits null for the tables
		GraphByVerb:       []statsCount{},
		ExplorationByKind: []statsCount{},
	}

	if found {
		// ONE clock reading drives both halves of the window, so the mtime prune and the
		// per-session filter can never disagree about where the boundary sits.
		cutoff := time.Time{}
		if window > 0 {
			cutoff = time.Now().Add(-window)
		}
		files, err := listTranscriptFiles(sessionsDir, flags.Transcript != "")
		if err != nil {
			return err
		}
		collector := newStatsCollector()
		collector.cache = openStatsCache(statsCacheDir(flags, opts.Env), sessionsDir, opts.Version, files)
		collector.run(files, cutoff)
		collector.finish(&report, cutoff)
	}

	if flags.Format == "json" {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	if flags.Verbose {
		writeStatsText(opts.Stdout, report)
		return nil
	}
	writeStatsSummary(opts.Stdout, report)
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
		case "--cache-dir":
			value, next, err := searchFlagValue(args, index)
			if err != nil {
				return flags, nil, err
			}
			flags.CacheDir, index = value, next
		case "--no-cache":
			flags.NoCache = true
		case "--verbose":
			flags.Verbose = true
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

// --- one transcript file's contribution -------------------------------------------------

// summaryCall is one tool call: which bucket it fell in, and — once its tool_result was seen —
// how many bytes it pulled into context. The API-assigned tool_use id is kept because it is the
// only thing that makes the SAME call identifiable when it appears in two transcripts.
type summaryCall struct {
	ID        string `json:"i,omitempty"`
	Verb      string `json:"v,omitempty"`
	Kind      string `json:"k,omitempty"`
	Bytes     int64  `json:"b,omitempty"`
	HasResult bool   `json:"r,omitempty"`
	// Disp is the observed-displacement class of a graph locate call with a result
	// (displacementDisplaced / displacementRequery / displacementReadAnyway); empty otherwise.
	Disp string `json:"d,omitempty"`
}

// summaryUsage is one assistant message's billed usage, kept per message.id so the same message
// appearing in two transcripts of one session is billed once.
type summaryUsage struct {
	ID     string      `json:"i"`
	Tokens statsTokens `json:"t"`
}

// fileSummary is EVERYTHING one transcript file contributes to the report, and the unit the
// per-file memo caches. Sessions are assembled only from summaries, so the cached path and the
// freshly-parsed path run identical merge code and cannot drift.
//
// Calls and usage are kept per identity rather than pre-aggregated, because aggregates cannot be
// deduplicated afterwards. Claude Code replays a forked subagent's history into the new
// transcript, so ONE tool call really does appear in several of a session's files: measured at
// 215 tool_use ids and 168 usage-bearing message ids across 3,316 real transcripts. Aggregating
// per file and summing would bill every one of those twice.
type fileSummary struct {
	Malformed     int            `json:"malformed,omitempty"`
	FirstUnixNano int64          `json:"first,omitempty"`
	LastUnixNano  int64          `json:"last,omitempty"`
	Calls         []summaryCall  `json:"calls,omitempty"`
	Usage         []summaryUsage `json:"usage,omitempty"`
	// Unkeyed is usage from records carrying no message id. Those cannot be deduplicated, so
	// they accumulate per record: that can only over-count, and silently dropping billed
	// tokens would be the worse failure.
	Unkeyed statsTokens `json:"unkeyed,omitempty"`
}

// fileScanner turns one transcript file into one fileSummary.
type fileScanner struct {
	summary  fileSummary
	pending  map[string]int // tool_use id -> index into summary.Calls
	seenUse  map[string]bool
	usageIdx map[string]int // message id -> index into summary.Usage
	// timeline is every distinct tool call in file order, for the displacement window;
	// locateAt maps a locate call's summary.Calls index to its timeline entry.
	timeline []timelineEntry
	locateAt map[int]int
}

func newFileScanner() *fileScanner {
	return &fileScanner{
		pending:  map[string]int{},
		seenUse:  map[string]bool{},
		usageIdx: map[string]int{},
		locateAt: map[int]int{},
	}
}

func (s *fileScanner) consume(record transcriptRecord) {
	if ts, err := time.Parse(time.RFC3339, record.Timestamp); err == nil {
		nanos := ts.UnixNano()
		if s.summary.FirstUnixNano == 0 || nanos < s.summary.FirstUnixNano {
			s.summary.FirstUnixNano = nanos
		}
		if nanos > s.summary.LastUnixNano {
			s.summary.LastUnixNano = nanos
		}
	}
	if usage := record.Message.Usage; usage != nil {
		s.consumeUsage(record.Message.ID, statsTokens{
			Input:      usage.InputTokens,
			CacheWrite: usage.CacheCreationIn,
			CacheRead:  usage.CacheReadIn,
			Output:     usage.OutputTokens,
		})
	}
	for _, block := range decodeContentBlocks(record.Message.Content) {
		switch block.Type {
		case "tool_use":
			s.consumeToolUse(block)
		case "tool_result":
			s.consumeToolResult(block)
		}
	}
}

// consumeUsage records ONE assistant message's billed usage.
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
func (s *fileScanner) consumeUsage(id string, tokens statsTokens) {
	if id == "" {
		s.summary.Unkeyed.Input += tokens.Input
		s.summary.Unkeyed.CacheWrite += tokens.CacheWrite
		s.summary.Unkeyed.CacheRead += tokens.CacheRead
		s.summary.Unkeyed.Output += tokens.Output
		return
	}
	if index, ok := s.usageIdx[id]; ok {
		s.summary.Usage[index].Tokens = tokens
		return
	}
	s.usageIdx[id] = len(s.summary.Usage)
	s.summary.Usage = append(s.summary.Usage, summaryUsage{ID: id, Tokens: tokens})
}

func (s *fileScanner) consumeToolUse(block contentBlock) {
	// One tool_use id is one API call. A re-serialised turn repeats the block, and counting
	// every occurrence would bill the same call twice; first occurrence wins.
	if block.ID != "" {
		if s.seenUse[block.ID] {
			return
		}
		s.seenUse[block.ID] = true
	}
	entry := timelineFromToolUse(block)
	call, ok := classifyToolUse(block)
	if !ok {
		s.timeline = append(s.timeline, entry)
		return
	}
	call.ID = block.ID
	s.summary.Calls = append(s.summary.Calls, call)
	index := len(s.summary.Calls) - 1
	if block.ID != "" {
		s.pending[block.ID] = index
	}
	if entry.kind == timelineLocate {
		entry.callIndex = index
		s.locateAt[index] = len(s.timeline)
	}
	s.timeline = append(s.timeline, entry)
}

// classifyToolUse puts a tool_use in the graph bucket, the exploration bucket, or neither.
func classifyToolUse(block contentBlock) (summaryCall, bool) {
	if verb, ok := graphVerbFromToolUse(block); ok {
		return summaryCall{Verb: verb}, true
	}
	if kind, ok := explorationKindFromToolUse(block); ok {
		return summaryCall{Kind: kind}, true
	}
	return summaryCall{}, false
}

func (s *fileScanner) consumeToolResult(block contentBlock) {
	index, ok := s.pending[block.ToolUseID]
	if !ok {
		return
	}
	// Dropping the pending entry is what stops a repeated tool_result from being billed twice
	// against the same call: the second one finds nothing pending.
	delete(s.pending, block.ToolUseID)
	text := toolResultText(block.Content)
	s.summary.Calls[index].Bytes = int64(len(text))
	s.summary.Calls[index].HasResult = true
	if at, ok := s.locateAt[index]; ok {
		s.timeline[at].paths = graphOutputPaths(text)
	}
}

func summariseTranscript(path string) (fileSummary, bool) {
	handle, err := os.Open(path) //nolint:gosec // the path comes from walking the caller's own transcript directory
	if err != nil {
		return fileSummary{}, false // skip unreadable transcripts rather than abort the report
	}
	defer func() { _ = handle.Close() }()

	scanner := newFileScanner()
	lines := bufio.NewScanner(handle)
	// Transcript lines routinely exceed bufio's 64KiB default (large tool results inline).
	lines.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" {
			continue
		}
		var record transcriptRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			scanner.summary.Malformed++
			continue
		}
		scanner.consume(record)
	}
	if err := lines.Err(); err != nil {
		// A truncated or over-long line is treated like a malformed line, not a hard failure.
		scanner.summary.Malformed++
	}
	scanner.finishDisplacement()
	return scanner.summary, true
}

// --- session assembly -------------------------------------------------------------------

type sessionAcc struct {
	firstNano, lastNano int64
	firstLocate         string // "graph" | "explore"
	verbCalls           map[string]int
	verbResults         map[string]int
	verbBytes           map[string]int64
	kindCalls           map[string]int
	kindResults         map[string]int
	kindBytes           map[string]int64
	seenCalls           map[string]summaryCall
	usageByID           map[string]statsTokens
	unkeyed             statsTokens
	// displacement counts observed-displacement classes of locate calls with results.
	displacement map[string]int
}

func newSessionAcc() *sessionAcc {
	return &sessionAcc{
		verbCalls:    map[string]int{},
		verbResults:  map[string]int{},
		verbBytes:    map[string]int64{},
		kindCalls:    map[string]int{},
		kindResults:  map[string]int{},
		kindBytes:    map[string]int64{},
		seenCalls:    map[string]summaryCall{},
		usageByID:    map[string]statsTokens{},
		displacement: map[string]int{},
	}
}

// merge folds one transcript's summary into its owning session. Replayed calls count once,
// while later copies can supply missing results or more complete usage. Files are merged in
// sorted path order, so firstLocate does not depend on what was cached.
func (a *sessionAcc) merge(summary fileSummary) {
	if summary.FirstUnixNano != 0 && (a.firstNano == 0 || summary.FirstUnixNano < a.firstNano) {
		a.firstNano = summary.FirstUnixNano
	}
	if summary.LastUnixNano > a.lastNano {
		a.lastNano = summary.LastUnixNano
	}
	for _, call := range summary.Calls {
		if call.ID != "" {
			if previous, seen := a.seenCalls[call.ID]; seen {
				// A replay can complete a call whose first copy had no result.
				// Keep its original classification and count the result only once.
				if !previous.HasResult && call.HasResult {
					previous.HasResult, previous.Bytes, previous.Disp = true, call.Bytes, call.Disp
					a.seenCalls[call.ID] = previous
					if previous.Verb != "" {
						a.verbResults[previous.Verb]++
						a.verbBytes[previous.Verb] += call.Bytes
						a.countDisplacement(previous)
					} else {
						a.kindResults[previous.Kind]++
						a.kindBytes[previous.Kind] += call.Bytes
					}
				}
				continue
			}
			a.seenCalls[call.ID] = call
		}
		if call.Verb != "" {
			a.verbCalls[call.Verb]++
			if call.HasResult {
				a.verbResults[call.Verb]++
				a.verbBytes[call.Verb] += call.Bytes
				a.countDisplacement(call)
			}
			if a.firstLocate == "" {
				a.firstLocate = "graph"
			}
			continue
		}
		a.kindCalls[call.Kind]++
		if call.HasResult {
			a.kindResults[call.Kind]++
			a.kindBytes[call.Kind] += call.Bytes
		}
		if a.firstLocate == "" {
			a.firstLocate = "explore"
		}
	}
	for _, usage := range summary.Usage {
		// Usage grows as a message streams. Filename order does not identify
		// the final record, so a partial replay must not lower any counter.
		previous := a.usageByID[usage.ID]
		a.usageByID[usage.ID] = statsTokens{
			Input:      max(previous.Input, usage.Tokens.Input),
			CacheWrite: max(previous.CacheWrite, usage.Tokens.CacheWrite),
			CacheRead:  max(previous.CacheRead, usage.Tokens.CacheRead),
			Output:     max(previous.Output, usage.Tokens.Output),
		}
	}
	a.unkeyed.Input += summary.Unkeyed.Input
	a.unkeyed.CacheWrite += summary.Unkeyed.CacheWrite
	a.unkeyed.CacheRead += summary.Unkeyed.CacheRead
	a.unkeyed.Output += summary.Unkeyed.Output
}

// countDisplacement records a locate result's class. A class-less locate result (which the scanner
// never produces) is left out of every class and therefore never credited.
func (a *sessionAcc) countDisplacement(call summaryCall) {
	if graphLocateVerbs[call.Verb] && call.HasResult && call.Disp != "" {
		a.displacement[call.Disp]++
	}
}

// tokens folds the per-message usage into the session's billed total. Integer addition is
// order-independent, so a map walk is deterministic here.
func (a *sessionAcc) tokens() statsTokens {
	total := a.unkeyed
	for _, usage := range a.usageByID {
		total.Input += usage.Input
		total.CacheWrite += usage.CacheWrite
		total.CacheRead += usage.CacheRead
		total.Output += usage.Output
	}
	total.Total = total.Input + total.CacheWrite + total.CacheRead + total.Output
	return total
}

func addCounts(into, from map[string]int) {
	for key, value := range from {
		into[key] += value
	}
}

func addBytes(into, from map[string]int64) {
	for key, value := range from {
		into[key] += value
	}
}

func sumCounts(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func sumBytes(values map[string]int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}

// locateTotals is the search+neighbors+impact subset: calls made, results observed, and the
// bytes those results returned. Only a result carries bytes, so only results can price a call.
func (a *sessionAcc) locateTotals() (calls, results int, bytes int64) {
	for verb := range graphLocateVerbs {
		calls += a.verbCalls[verb]
		results += a.verbResults[verb]
		bytes += a.verbBytes[verb]
	}
	return calls, results, bytes
}

// savingsBytes implements the model documented in savingsModelText, in integers so two runs over
// unchanged input cannot differ by a float ulp:
//
//	saved = locateResults * (exploreBytes / exploreResults) - locateBytes
//
// The subtracted term is exactly locateResults * (locateBytes / locateResults), which is why the
// graph-side division cancels and no per-call rounding accumulates.
//
// The result is SIGNED: larger graph results produce a modeled loss. Legacy JSON keeps
// its per-session floor, but human output uses the signed sum and a separate availability count.
//
// The 0 returned when either side is missing is a DIFFERENT zero: it means "one side of the
// comparison is absent", not "the comparison came out flat". It stays, and must not be conflated
// with a measured loss.
func (a *sessionAcc) savingsBytes() int64 {
	_, locateResults, locateBytes := a.locateTotals()
	exploreResults := sumCounts(a.kindResults)
	if locateResults == 0 || exploreResults == 0 {
		return 0
	}
	displaced := mulDiv(int64(locateResults)*substitutionRatio, sumBytes(a.kindBytes), int64(exploreResults))
	return displaced - locateBytes
}

// mulDiv computes a*b/c, truncating, without overflowing the a*b product — which on real
// transcript volumes already reaches ~10^13 and on adversarial input would silently wrap int64.
func mulDiv(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(c) {
		return math.MaxInt64 // the quotient does not fit in 64 bits; saturate rather than wrap
	}
	quotient, _ := bits.Div64(hi, lo, uint64(c))
	if quotient > uint64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(quotient)
}

// --- collector --------------------------------------------------------------------------

type transcriptFile struct {
	path     string
	identity string // absolute resolved path, including a symlink target
	session  string
	size     int64
	modTime  time.Time
}

type statsCollector struct {
	sessions    map[string]*sessionAcc
	cache       *statsCache
	considered  int
	transcripts int
	fromCache   int
	malformed   int
}

func newStatsCollector() *statsCollector {
	return &statsCollector{sessions: map[string]*sessionAcc{}}
}

func (c *statsCollector) session(key string) *sessionAcc {
	acc, ok := c.sessions[key]
	if !ok {
		acc = newSessionAcc()
		c.sessions[key] = acc
	}
	return acc
}

// run parses the candidate transcripts and folds them into sessions.
//
// The --since window is applied HERE, by file mtime, before anything is opened. That is the
// whole performance story: a project's transcript directory reaches gigabytes (2.2 GB over 3,313
// files was the report that prompted this), and `--since 1d` used to JSON-parse every byte of it
// before throwing the result away in the per-session filter.
//
// The prune is sound in ONE direction only. A file whose mtime precedes the window start cannot
// contain a record inside the window, because records are written by appending — dropping it is
// safe. The converse does not hold (a file touched today can carry months of older records), so
// a file that survives is still parsed in full and still filtered per session afterwards.
// Pruning is decided per SESSION, not per file, so a subagent transcript can never be separated
// from the transcript that owns it.
func (c *statsCollector) run(files []transcriptFile, cutoff time.Time) {
	c.considered = len(files)
	c.cache.retain(files)
	keep := map[string]bool{}
	for _, file := range files {
		if cutoff.IsZero() || !file.modTime.Before(cutoff) {
			keep[file.session] = true
		}
	}
	for _, file := range files {
		if !keep[file.session] {
			continue
		}
		summary, ok := c.summarise(file)
		if !ok {
			continue
		}
		c.transcripts++
		c.malformed += summary.Malformed
		c.session(file.session).merge(summary)
	}
	c.cache.save()
}

func (c *statsCollector) summarise(file transcriptFile) (fileSummary, bool) {
	if summary, ok := c.cache.lookup(file); ok {
		c.fromCache++
		return summary, true
	}
	// Read the same target whose identity was captured during enumeration.
	// Retargeting an alias must not store another file's data under this key.
	path := file.identity
	if path == "" {
		path = file.path
	}
	summary, ok := summariseTranscript(path)
	if !ok {
		return fileSummary{}, false
	}
	c.cache.store(file, summary)
	return summary, true
}

// listTranscriptFiles enumerates the `*.jsonl` in scope with the size and mtime the memo keys
// on. Top-level `<session>.jsonl` files are sessions; subagent transcripts live under
// `<session>/subagents/*.jsonl` and are folded into the owning session so delegated exploration
// is not invisible.
//
// When single is set, root names ONE transcript file rather than a directory: that file plus the
// `<session>/` sibling directory holding its subagent transcripts. Both key to the same session,
// so the accounting is byte-for-byte what the directory walk would produce for that session. It
// exists because a project's transcript directory can reach gigabytes, which a per-render caller
// (status line) cannot afford to walk.
func listTranscriptFiles(root string, single bool) ([]transcriptFile, error) {
	if !single {
		return walkTranscripts(root, root, true)
	}
	base := filepath.Dir(root)
	files := []transcriptFile{}
	if file, ok := transcriptFileAt(root, sessionKeyForPath(base, root)); ok {
		files = append(files, file)
	}
	subagents := strings.TrimSuffix(root, ".jsonl")
	if subagents == root {
		return files, nil
	}
	if info, err := os.Stat(subagents); err != nil || !info.IsDir() {
		return files, nil //nolint:nilerr // a session without subagent transcripts is the common case
	}
	nested, err := walkTranscripts(subagents, base, false)
	if err != nil {
		return nil, err
	}
	files = append(files, nested...)
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func walkTranscripts(dir, keyRoot string, reportRootError bool) ([]transcriptFile, error) {
	var files []transcriptFile
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if reportRootError && path == dir {
				return err
			}
			return nil //nolint:nilerr // an unreadable subtree must not fail the whole report
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if file, ok := transcriptFileAt(path, sessionKeyForPath(keyRoot, path)); ok {
			files = append(files, file)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

// transcriptFileAt follows transcript symlinks while keeping the alias's session grouping.
// Cache identity and window pruning use the resolved target, not the symlink's metadata.
func transcriptFileAt(path, session string) (transcriptFile, bool) {
	identity, err := filepath.EvalSymlinks(path)
	if err != nil {
		return transcriptFile{}, false
	}
	identity, err = filepath.Abs(identity)
	if err != nil {
		return transcriptFile{}, false
	}
	info, err := os.Stat(identity)
	if err != nil || !info.Mode().IsRegular() {
		return transcriptFile{}, false
	}
	return transcriptFile{path: path, identity: identity, session: session, size: info.Size(), modTime: info.ModTime()}, true
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
// (`entire graph query`) and the direct binary form (`/path/to/entire-graph query`) count,
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
			if base == "entire" && len(rest) > 0 && shellWord(rest[0]) == "sem" {
				if verb, ok := firstSemVerb(rest[1:]); ok {
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

// firstSemVerb matches the `entire sem <verb>` form. The CLI's own agent instructions recommend
// `entire sem edges` and `entire sem symbols` by name as the deterministic callers/definitions
// lookup, and they were counted in NEITHER arm: not a graph call, and not exploration either — so
// a session driven entirely by them read as a session that used no tool at all. They are counted
// as graph calls; the savings model still prices only graphLocateVerbs, so nothing here is
// credited with a displacement it did not make.
func firstSemVerb(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	verb := strings.TrimLeft(shellWord(fields[0]), "-")
	if semGraphVerbs[verb] {
		return verb, true
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

// isExploringShellCommand reports whether a Bash call's purpose was to LOCATE code in the
// repository. Two rules decide it, and both were absent before:
//
//   - Only the LEADING stage of each pipeline is classified. Everything downstream of a `|`
//     reshapes text that is already in context; it reads nothing. Matching any pipe segment whose
//     first word was grep/rg/head/tail/sed/awk turned `go test ./... 2>&1 | tail -40` into a locate
//     call.
//   - A WRITE is not a locate, however grep-shaped it looks. No redirection was parsed at all, so
//     `cat > file`, `cat <<'EOF' > file`, `tee` and `sed -i` all counted as exploration — and a
//     here-document's BODY was read as if its lines were commands.
//
// Measured over a real 2,690-transcript corpus, against the classifier this replaces: the
// pipeline-head rule alone removes 32,303 of 100,273 exploration calls (32.2%), write detection
// removes a further 4,502 on top of it, 36,805 in total (36.7%). The calls removed averaged 1,016
// returned bytes against 3,117 for the ones kept, so the measured exploration price per call — the
// thing the savings model divides by — rose 32.8%.
func isExploringShellCommand(command string) bool {
	if command == "" {
		return false
	}
	for _, stage := range pipelineHeadStages(command) {
		if isWritingShellStage(stage) {
			continue
		}
		if bashExploreCommands[shellCommandWord(stage)] {
			return true
		}
	}
	return false
}

// shellCommandWord returns the command a stage runs — wrappers (`rtk`, `sudo`, `xargs`) and
// leading VAR=value assignments skipped — or "" when the stage runs nothing.
func shellCommandWord(stage string) string {
	for _, field := range strings.Fields(stage) {
		word := shellWord(field)
		if word == "" || strings.Contains(word, "=") {
			continue
		}
		word = filepath.Base(word)
		if commandPrefixes[word] {
			continue
		}
		return word
	}
	return ""
}

// pipelineHeadStages splits a command into its STATEMENTS (`;`, `&&`, `||`, a background `&`, a
// newline) and returns the leading stage of each — the text up to that statement's first `|`.
//
// Per statement, not per command: `cd internal && grep -rn foo .` is a real search, so every
// statement's head is classified, not just the first one in the string.
//
// This deliberately does NOT reuse splitShellSegments. That splitter is shared with
// graphVerbFromCommand, which legitimately needs non-first segments (`cd /repo && entire graph
// neighbors --symbol Foo`), so narrowing it in place would stop the graph half of the report from
// finding its own calls. Two readers, two rules, two splitters.
// Quoting is honoured throughout: `git commit -m "fix: a; grep the logs"` is ONE commit, not two
// statements the second of which searches.
func pipelineHeadStages(command string) []string {
	runes := []rune(stripHeredocBodies(command))
	literal := shellQuoteMask(runes)
	var stages []string
	var current []rune
	capturing := true
	endStatement := func() {
		if capturing {
			stages = append(stages, string(current))
		}
		current = current[:0]
		capturing = true
	}
	for index := 0; index < len(runes); index++ {
		character := runes[index]
		switch {
		case literal[index]:
			// Inside quotes nothing is an operator; it is just text the command carries.
			if capturing {
				current = append(current, character)
			}
		case character == '\n' || character == ';':
			endStatement()
		case character == '&':
			switch {
			case index+1 < len(runes) && runes[index+1] == '&' && !literal[index+1]:
				index++
				endStatement()
			case index+1 < len(runes) && runes[index+1] == '>' && !literal[index+1]:
				// `&>` / `&>>` send both streams to a file. It is a redirect, and splitting the
				// statement there hid the redirect from the write check that runs on the head.
				if capturing {
					current = append(current, character)
				}
			case previousNonSpaceIsRedirect(runes, literal, index):
				// `2>&1` duplicates a file descriptor; it is not a statement boundary.
				if capturing {
					current = append(current, character)
				}
			default:
				endStatement() // backgrounding operator
			}
		case character == '|':
			if index+1 < len(runes) && runes[index+1] == '|' && !literal[index+1] {
				index++
				endStatement()
				continue
			}
			// The head of this pipeline is complete; the rest of the statement only reshapes it.
			if capturing {
				stages = append(stages, string(current))
				current = current[:0]
			}
			capturing = false
		default:
			if capturing {
				current = append(current, character)
			}
		}
	}
	endStatement()
	return stages
}

// shellQuoteMask marks the runes of a command that are inside single or double quotes, or are a
// backslash escape and the character it escapes. Every scanner below consults it, because none of
// the operators they look for — `|`, `;`, `&`, `>`, `<<` — is an operator inside quotes:
// `git commit -m "fix: a; grep the logs"` is one commit, and `cat "odd>name.txt"` is a read.
//
// A command whose quotes do not balance gets an all-false mask: masking to end-of-string on an
// unterminated quote would hide every later statement, and falling back to the unquoted reading is
// this file's pre-existing behaviour, so the failure stays bounded.
func shellQuoteMask(runes []rune) []bool {
	masked := make([]bool, len(runes))
	var quote rune
	for index := 0; index < len(runes); index++ {
		character := runes[index]
		switch {
		case character == '\\' && quote != '\'':
			masked[index] = true
			if index+1 < len(runes) {
				masked[index+1] = true
				index++
			}
		case quote != 0:
			masked[index] = true
			if character == quote {
				quote = 0
			}
		case character == '\'' || character == '"':
			quote = character
			masked[index] = true
		}
	}
	if quote != 0 {
		return make([]bool, len(runes))
	}
	return masked
}

func previousNonSpaceIsRedirect(runes []rune, literal []bool, index int) bool {
	for cursor := index - 1; cursor >= 0; cursor-- {
		if literal[cursor] {
			return false
		}
		switch runes[cursor] {
		case ' ', '\t':
			continue
		case '>':
			return true
		default:
			return false
		}
	}
	return false
}

// stripHeredocBodies removes here-document BODIES before anything is classified. A body is data
// being written into a file, not a sequence of commands: `cat > run.sh <<'EOF'` followed by a line
// reading `grep -rn foo .` is ONE write, and treating the payload's lines as statements counted the
// file's contents as locate calls. The opening line survives — it is a real command — and so does
// everything after the terminator.
func stripHeredocBodies(command string) string {
	if !strings.Contains(command, "<<") {
		return command
	}
	lines := strings.Split(command, "\n")
	kept := make([]string, 0, len(lines))
	for index := 0; index < len(lines); index++ {
		kept = append(kept, lines[index])
		delimiter := heredocDelimiter(lines[index])
		if delimiter == "" {
			continue
		}
		// Strip only a body that is actually CLOSED. An unterminated here-document is far more
		// likely to be a misread `<<` than a real one, and swallowing the rest of the command
		// would silently drop whatever locate calls its later lines make — the failure mode this
		// whole function exists to prevent, pointed the other way.
		terminator := -1
		for cursor := index + 1; cursor < len(lines); cursor++ {
			if strings.TrimSpace(lines[cursor]) == delimiter {
				terminator = cursor
				break
			}
		}
		if terminator < 0 {
			continue
		}
		index = terminator
	}
	return strings.Join(kept, "\n")
}

// heredocDelimiter returns the terminator word a line's here-document opens with, or "" when it
// opens none.
//
// It is deliberately conservative, because a false positive costs real commands: stripHeredocBodies
// would discard the lines that follow. Three guards here, and stripHeredocBodies adds a fourth by
// refusing to strip an unterminated body:
//
//   - the `<<` must be OUTSIDE quotes, so `echo "count << 2"` opens nothing;
//   - `<<<` is a here-STRING, whose data is on the same line;
//   - the word must be quoted (`<<'EOF'`) or an unquoted SHOUTING identifier (EOF, PY, PYEOF) —
//     the shapes real here-documents actually use. That is what rejects `std::cout << x` and
//     `n << 2`, whose operands are a lowercase word and a number.
//
// A lowercase unquoted delimiter (`<<eof`) is therefore missed, and the command falls back to
// being classified line by line — the behaviour that predates this function, so the failure is
// bounded rather than destructive.
func heredocDelimiter(line string) string {
	runes := []rune(line)
	literal := shellQuoteMask(runes)
	for index := 0; index < len(runes); index++ {
		if literal[index] || runes[index] != '<' {
			continue
		}
		if index+1 >= len(runes) || runes[index+1] != '<' || literal[index+1] {
			continue
		}
		position := index + 2
		if position < len(runes) && runes[position] == '<' && !literal[position] {
			index = position // here-string
			continue
		}
		if position < len(runes) && runes[position] == '-' {
			position++
		}
		for position < len(runes) && (runes[position] == ' ' || runes[position] == '\t') {
			position++
		}
		word, quoted := heredocWord(runes[position:])
		if word != "" && (quoted || isShoutingWord(word)) {
			return word
		}
		index = position - 1
	}
	return ""
}

// heredocWord reads the delimiter token after a `<<`, reporting whether it was quoted. A quoted
// delimiter is unambiguous, so it is accepted whatever it spells.
func heredocWord(runes []rune) (string, bool) {
	if len(runes) == 0 {
		return "", false
	}
	if runes[0] == '\'' || runes[0] == '"' {
		for cursor := 1; cursor < len(runes); cursor++ {
			if runes[cursor] == runes[0] {
				return string(runes[1:cursor]), true
			}
		}
		return "", false
	}
	end := 0
	for end < len(runes) && !strings.ContainsRune(" \t|;&<>()'\"", runes[end]) {
		end++
	}
	return string(runes[:end]), false
}

// isShoutingWord reports whether a word is an all-caps identifier — EOF, PY, PYEOF, SQL2. It is
// the shape an unquoted here-document delimiter takes in practice, and the cheapest way to tell
// one from the right-hand side of a shift or a stream insertion.
func isShoutingWord(word string) bool {
	hasLetter := false
	for _, character := range word {
		switch {
		case character >= 'A' && character <= 'Z':
			hasLetter = true
		case character >= '0' && character <= '9' || character == '_':
		default:
			return false
		}
	}
	return hasLetter
}

// isWritingShellStage reports whether a stage's purpose is to write rather than to read. The
// command word alone cannot tell: `cat x.go` is a read and `cat > x.go` is a file being created,
// and the difference is entirely in the redirection nobody was parsing.
func isWritingShellStage(stage string) bool {
	switch shellCommandWord(stage) {
	case "tee":
		// tee always names a file to write, with or without a redirect.
		return true
	case "cat":
		return hasStdoutRedirect(stage) || heredocDelimiter(stage) != ""
	case "sed":
		return hasInPlaceFlag(stage)
	}
	return false
}

// hasStdoutRedirect reports whether a stage sends STDOUT to a file. `2>` is excluded on purpose:
// silencing stderr is something a read does too, and treating `cat x.go 2>/dev/null` as a write
// would drop a real read. A `>` inside quotes is not a redirect either — `cat "odd>name.txt"` reads
// a file whose name contains the character.
func hasStdoutRedirect(stage string) bool {
	runes := []rune(stage)
	literal := shellQuoteMask(runes)
	for index, character := range runes {
		if character != '>' || literal[index] {
			continue
		}
		if index+1 < len(runes) && runes[index+1] == '&' {
			continue // `>&2` duplicates a descriptor; it does not name a file
		}
		if index > 0 && runes[index-1] == '<' {
			continue // `<>` opens for read-write
		}
		if index > 0 && runes[index-1] >= '0' && runes[index-1] <= '9' && runes[index-1] != '1' {
			continue // 2>, 3>, … are not stdout
		}
		return true
	}
	return false
}

// hasInPlaceFlag reports whether a `sed` stage edits its input in place. GNU takes an optional
// suffix (`-i.bak`), BSD a separate one (`-i ”`), and short flags bundle (`-ni`).
func hasInPlaceFlag(stage string) bool {
	for _, field := range strings.Fields(stage) {
		word := shellWord(field)
		if word == "--in-place" || strings.HasPrefix(word, "--in-place=") {
			return true
		}
		if len(word) < 2 || word[0] != '-' || word[1] == '-' {
			continue
		}
		flags := word[1:]
		if cut := strings.IndexAny(flags, ".='\""); cut >= 0 {
			flags = flags[:cut]
		}
		if strings.ContainsRune(flags, 'i') {
			return true
		}
	}
	return false
}

// splitShellSegments is the GRAPH side's splitter: every `|`/`;`/`&`/newline segment, because an
// `entire graph` invocation is a real call wherever in the command it sits. The exploration side
// uses pipelineHeadStages instead — see the note there before narrowing this.
//
// The two sides differ in WHICH segments they classify, not in what counts as a segment boundary.
// Both honour quoting and both ignore here-document bodies, because an instrument that discounts a
// quoted `grep` while still counting a quoted `entire graph query` is biased, not merely imprecise:
// `git commit -m "risky; entire graph query foo"` is a commit, and the body of `cat > run.sh
// <<'EOF'` is a file being written whichever tool its lines name.
func splitShellSegments(command string) []string {
	runes := []rune(stripHeredocBodies(command))
	literal := shellQuoteMask(runes)
	var segments []string
	var current []rune
	for index, character := range runes {
		if !literal[index] && (character == '|' || character == ';' || character == '&' || character == '\n') {
			if len(current) > 0 {
				segments = append(segments, string(current))
				current = current[:0]
			}
			continue
		}
		current = append(current, character)
	}
	if len(current) > 0 {
		segments = append(segments, string(current))
	}
	return segments
}

// --- aggregation and rendering ----------------------------------------------------------

func (c *statsCollector) finish(report *statsResponse, cutoff time.Time) {
	report.Transcripts = c.transcripts
	report.TranscriptsConsidered = c.considered
	report.TranscriptsFromCache = c.fromCache
	report.MalformedLinesSkipped = c.malformed

	var cutoffNano int64
	if !cutoff.IsZero() {
		cutoffNano = cutoff.UnixNano()
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

	var windowStart, windowEnd int64
	var exploreResults int
	for _, key := range keys {
		acc := c.sessions[key]
		if cutoffNano != 0 && acc.lastNano != 0 && acc.lastNano < cutoffNano {
			continue
		}
		tokens := acc.tokens()
		graphCalls := sumCounts(acc.verbCalls)
		exploreCalls := sumCounts(acc.kindCalls)
		if graphCalls == 0 && exploreCalls == 0 && tokens.Total == 0 {
			continue
		}
		report.Sessions++
		if acc.firstNano != 0 && (windowStart == 0 || acc.firstNano < windowStart) {
			windowStart = acc.firstNano
		}
		if acc.lastNano > windowEnd {
			windowEnd = acc.lastNano
		}
		report.GraphCalls += graphCalls
		report.ExplorationCalls += exploreCalls
		switch acc.firstLocate {
		case "graph":
			report.SessionsWithLocate++
			report.GraphFirstSessions++
		case "explore":
			report.SessionsWithLocate++
		}
		addCounts(verbCalls, acc.verbCalls)
		addBytes(verbBytes, acc.verbBytes)
		addCounts(kindCalls, acc.kindCalls)
		addBytes(kindBytes, acc.kindBytes)
		report.SessionTokens.Input += tokens.Input
		report.SessionTokens.CacheWrite += tokens.CacheWrite
		report.SessionTokens.CacheRead += tokens.CacheRead
		report.SessionTokens.Output += tokens.Output

		locateCalls, locateResults, locateBytes := acc.locateTotals()
		report.GraphLocateCalls += locateCalls
		report.CreditedGraphCalls += locateResults
		report.GraphLocateReturnedBytes += locateBytes
		sessionExploreResults := sumCounts(acc.kindResults)
		exploreResults += sessionExploreResults
		if locateResults > 0 && sessionExploreResults > 0 {
			report.SessionsWithSavingsComparison++
		}
		// Preserve the legacy positive-only sum; human output uses the signed model.
		saved := acc.savingsBytes()
		report.EstimatedSavingsBytesUnfloored += saved
		report.ObservedNetBytes += acc.observedSavingsBytes()
		report.GraphLocateDisplaced += acc.displacement[displacementDisplaced]
		report.GraphLocateRequery += acc.displacement[displacementRequery]
		report.GraphLocateReadAnyway += acc.displacement[displacementReadAnyway]
		if saved > 0 {
			report.EstimatedSavingsBytes += saved
			report.SessionsWithPositiveSavings++
		}
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
	report.EstimatedSavingsTokensUnfloored = report.EstimatedSavingsBytesUnfloored / bytesPerToken
	report.ObservedNetTokens = report.ObservedNetBytes / bytesPerToken
	if classified := report.GraphLocateDisplaced + report.GraphLocateRequery + report.GraphLocateReadAnyway; classified > 0 {
		report.DisplacedRate = roundTo(float64(report.GraphLocateDisplaced)/float64(classified), 4)
		report.RequeryRate = roundTo(float64(report.GraphLocateRequery)/float64(classified), 4)
		report.ReadAnywayRate = roundTo(float64(report.GraphLocateReadAnyway)/float64(classified), 4)
	}
	if report.CreditedGraphCalls > 0 {
		report.GraphBytesPerLocateCall = roundTo(
			float64(report.GraphLocateReturnedBytes)/float64(report.CreditedGraphCalls), 2)
	}
	if exploreResults > 0 {
		report.ExplorationBytesPerCall = roundTo(
			float64(report.ExplorationReturnedBytes)/float64(exploreResults), 2)
	}
	if report.SessionTokens.Total > 0 {
		report.EstimatedSavingsPct = roundTo(float64(report.EstimatedSavingsTokens)*100/float64(report.SessionTokens.Total), 2)
	}
	if report.SessionsWithLocate > 0 {
		report.GraphFirstRate = roundTo(float64(report.GraphFirstSessions)/float64(report.SessionsWithLocate), 4)
	}
	if windowStart != 0 {
		report.WindowStart = time.Unix(0, windowStart).UTC().Format(time.RFC3339)
	}
	if windowEnd != 0 {
		report.WindowEnd = time.Unix(0, windowEnd).UTC().Format(time.RFC3339)
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

// roundTo rounds half away from zero. The previous int64-truncating implementation rounded
// negatives the wrong way and was undefined for values outside int64 range; math.Round has
// neither problem and is identical on the non-negative values this report produces.
func roundTo(value float64, places int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	factor := math.Pow(10, float64(places))
	return math.Round(value*factor) / factor
}

const statsPrefix = "[entire-graph]"

// writeStatsSummary is one line with the sign, assumption and availability at the point of use.
func writeStatsSummary(out io.Writer, report statsResponse) {
	if !report.SessionsDirFound {
		fmt.Fprintf(out, "%s no coding-agent session transcripts for this repo yet (looked in %s)\n",
			statsPrefix, termsafe.Line(report.SessionsDir))
		return
	}
	if report.Sessions == 0 {
		fmt.Fprintf(out, "%s no sessions in this window (--since %s); widen it with --since all\n",
			statsPrefix, termsafe.Line(report.Since))
		return
	}
	fmt.Fprintf(out, "%s %s\n", statsPrefix, observedModelSummary(report))
}

// observedModelSummary is the headline: the signed observed-displacement net, and ALWAYS the
// displaced rate beside it, so the number is never quoted without how often the graph actually
// ended a search.
func observedModelSummary(report statsResponse) string {
	classified := report.GraphLocateDisplaced + report.GraphLocateRequery + report.GraphLocateReadAnyway
	rates := fmt.Sprintf("displaced %s of %s graph locate results (re-query %s, read-anyway %s; next %d calls)",
		percent(report.DisplacedRate), humanInt(int64(classified)),
		percent(report.RequeryRate), percent(report.ReadAnywayRate), report.DisplacementWindowCalls)
	if report.SessionsWithSavingsComparison == 0 {
		if classified == 0 {
			return "observed displacement: unavailable (no session has both result types)"
		}
		return "observed displacement: net unavailable (no session has both result types); " + rates
	}
	signed := humanInt(report.ObservedNetTokens)
	if report.ObservedNetTokens > 0 {
		signed = "+" + signed
	}
	return "observed displacement: net " + signed + " est. tokens; " + rates + "; not measured savings"
}

func statsModelSummary(report statsResponse) string {
	if report.SessionsWithSavingsComparison == 0 {
		return "1:1 context model: unavailable (no session has both result types)"
	}
	signed := humanInt(report.EstimatedSavingsTokensUnfloored)
	if report.EstimatedSavingsTokensUnfloored > 0 {
		signed = "+" + signed
	}
	return "1:1 context model: " + signed + " est. tokens; not measured savings; assumes 100% displaced"
}

// statsGreen styles the headline for a terminal and degrades to plain text everywhere else,
// using the same NO_COLOR / FORCE_COLOR / TERM / character-device rules as the rest of the
// binary, so a pipe or a redirect never receives raw escape bytes.
func statsGreen(out io.Writer, value string) string {
	value = termsafe.Line(value)
	if value == "" || !sem.ShouldUseColor(out) {
		return value
	}
	return "\x1b[32m" + value + "\x1b[0m"
}

func writeStatsText(out io.Writer, report statsResponse) {
	fmt.Fprintln(out, "entire graph stats")
	fmt.Fprintf(out, "  repo:     %s\n", valueOrUnset(report.RepoRoot))
	if !report.SessionsDirFound {
		fmt.Fprintf(out, "  sessions: not found (looked in %s)\n", report.SessionsDir)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No coding-agent session transcripts exist for this repo yet, so there is")
		fmt.Fprintln(out, "nothing to compare. Run some work with `entire graph query` and re-run this.")
		return
	}
	fmt.Fprintf(out, "  sessions: %s\n", report.SessionsDir)
	fmt.Fprintf(out, "  window:   --since %s · %d session(s), %d of %d transcript file(s) parsed\n",
		report.Since, report.Sessions, report.Transcripts, report.TranscriptsConsidered)
	if report.WindowStart != "" {
		fmt.Fprintf(out, "  covering: %s → %s\n", report.WindowStart, report.WindowEnd)
	}
	fmt.Fprintln(out)

	if report.Sessions == 0 {
		fmt.Fprintln(out, "No sessions in this window. Widen it with --since all.")
		return
	}

	fmt.Fprintf(out, "graph calls: %s · exploration calls: %s\n",
		humanInt(int64(report.GraphCalls)),
		humanInt(int64(report.ExplorationCalls)))
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
		fmt.Fprintln(out, "observed exploration calls")
		fmt.Fprintf(out, "  %-22s %8s %14s\n", "kind", "calls", "est. tokens")
		for _, entry := range report.ExplorationByKind {
			fmt.Fprintf(out, "  %-22s %8s %14s\n", entry.Name, humanInt(int64(entry.Calls)), humanInt(entry.ReturnedTokens))
		}
		fmt.Fprintf(out, "  %-22s %8s %14s\n", "TOTAL", humanInt(int64(report.ExplorationCalls)), humanInt(report.ExplorationReturnedTokens))
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "session usage tokens (reported by transcripts)")
	fmt.Fprintf(out, "  input %s · cache write %s · cache read %s · output %s · total %s\n",
		humanInt(report.SessionTokens.Input), humanInt(report.SessionTokens.CacheWrite),
		humanInt(report.SessionTokens.CacheRead), humanInt(report.SessionTokens.Output),
		humanInt(report.SessionTokens.Total))
	fmt.Fprintln(out)

	fmt.Fprintln(out, "observed bytes per result (not monetary cost)")
	fmt.Fprintf(out, "  graph locate %s bytes/call · exploration %s bytes/call\n",
		strconv.FormatFloat(report.GraphBytesPerLocateCall, 'f', -1, 64),
		strconv.FormatFloat(report.ExplorationBytesPerCall, 'f', -1, 64))
	fmt.Fprintln(out)

	fmt.Fprintln(out, observedModelSummary(report))
	fmt.Fprintf(out, "  displaced %s · re-query %s · read-anyway %s (window: next %d tool calls)\n",
		humanInt(int64(report.GraphLocateDisplaced)), humanInt(int64(report.GraphLocateRequery)),
		humanInt(int64(report.GraphLocateReadAnyway)), report.DisplacementWindowCalls)
	for _, line := range wrapText(observedModelText, 88) {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, statsModelSummary(report))
	fmt.Fprintf(out, "  observed graph locate results: %d of %d graph calls (query/search/neighbors/impact only)\n",
		report.CreditedGraphCalls, report.GraphCalls)
	fmt.Fprintf(out, "  sessions with both result types: %d of %d\n",
		report.SessionsWithSavingsComparison, report.Sessions)
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
