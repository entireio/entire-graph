package cli

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Observed follow-up.
//
// The 1:1 model credits every graph locate result with replacing exactly one exploration result.
// That is an assumption. What a transcript CAN show is what the agent did after a graph result was
// delivered: whether it asked again, whether it opened a file the result named, or neither. Those
// are observations of follow-up behaviour, NOT measurements of calls avoided: an absent follow-up
// may mean the task ended, failed, changed direction or used another source.
//
// Every observed graph locate result lands in exactly one class:
//
//   - ERROR: the tool_result carried is_error. Nothing was located.
//   - INELIGIBLE: the result was empty or named no file path. Nothing was located.
//   - RE-QUERY: within the next followUpWindow tool calls issued AFTER the result was delivered,
//     another lookup came first — a graph lookup verb, Grep, Glob, or a shell search.
//   - FOLLOW-UP READ: first, the agent read a file whose path overlaps a path the result named.
//     Sub-bucketed by line span: OVERLAPPING (the read covers lines the result actually delivered),
//     DIFFERENT REGION (both spans known, disjoint) or UNKNOWN SPAN (a span is unknown, or the file
//     was edited in between so delivered lines may be stale).
//   - NO FOLLOW-UP: a full window of followUpWindow calls was observed after delivery and none of
//     them qualified.
//   - CENSORED: fewer than followUpWindow calls followed delivery before the transcript ended, and
//     none qualified. The tail is unobserved, which is not evidence of anything.
//
// Only NO FOLLOW-UP results enter the modeled balance, and even that credit is an assumption stated
// at the point of use, never a measurement.

// followUpWindow is K, how many tool calls issued after a graph result's delivery are inspected.
//
// Why 5: a delivered result is followed by its consequence almost immediately — the agent either
// opens the answer or asks again. Parallel tool calls in one assistant turn land as consecutive
// calls, and Claude Code commonly issues 2-4 of them per turn, so K must span at least one full
// parallel batch plus the next turn's first call. Much beyond that the calls belong to a different
// subtask, and a later grep is no longer evidence about THIS graph answer. The PR that introduced
// this constant reports the rates' sensitivity to K on a real corpus.
const followUpWindow = 5

// Class codes, persisted in the stats memo (summaryCall.Disp).
const (
	followUpNone            = "n" // full window observed, nothing qualified (the only credited class)
	followUpRequery         = "q"
	followUpReadOverlapping = "o"
	followUpReadDifferent   = "g"
	followUpReadUnknownSpan = "u"
	followUpCensored        = "c"
	followUpError           = "e"
	followUpIneligible      = "x"
)

// followUpReadClasses are the three FOLLOW-UP READ sub-buckets.
var followUpReadClasses = []string{followUpReadOverlapping, followUpReadDifferent, followUpReadUnknownSpan}

// graphLookupVerbs are graph verbs that answer "where is X / what touches X". Any of them after a
// delivered locate result is a re-query. Meta verbs (stats, index, doctor, help, ...) are not.
var graphLookupVerbs = map[string]bool{
	"query": true, "search": true, "neighbors": true, "impact": true, "def": true, "explain": true,
	"symbols": true, "edges": true, "snapshot-query": true,
}

// shellSearchCommands / shellReadCommands split bashExploreCommands into "asks again" and "opens a
// file". TestShellDisplacementCommandsPartitionExploration keeps them a partition of it.
var shellSearchCommands = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true, "find": true,
}

var shellReadCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "sed": true, "awk": true, "less": true, "more": true,
}

// editTools write a file; a read of that file afterwards cannot reuse lines delivered before it.
var editTools = map[string]string{
	"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path",
}

type timelineKind uint8

const (
	timelineOther timelineKind = iota
	timelineLocate
	timelineRequery
	timelineRead
	timelineEdit
)

// lineSpan is an inclusive 1-based line range; end < 0 means "to end of file".
type lineSpan struct{ start, end int }

func (s lineSpan) overlaps(o lineSpan) bool {
	return (s.end < 0 || o.start <= s.end) && (o.end < 0 || s.start <= o.end)
}

// timelineEntry is one tool call in transcript order. It lives only while a file is scanned; what
// is cached is the resulting class on the locate call's summaryCall.
type timelineEntry struct {
	kind  timelineKind
	paths []string // read/edit: the file(s) touched; locate: the paths its output named
	// read: the requested span, when known (Read tool). locate: delivered spans per named path,
	// only when the output parsed as a complete search response.
	readSpan  *lineSpan
	delivered map[string][]lineSpan
	callIndex int // locate: index into fileSummary.Calls
	// locate, set when its result arrives: deliveredAt is the number of timeline entries that
	// existed at delivery (the window starts there); deliverSeq orders deliveries.
	hasResult   bool
	deliveredAt int
	deliverSeq  int
	isError     bool
}

// timelineFromToolUse classifies a tool_use for the follow-up window. It is deliberately
// independent of the exploration classifier's buckets: an Edit or `go test` still occupies a slot.
func timelineFromToolUse(block contentBlock) timelineEntry {
	if key, ok := editTools[block.Name]; ok {
		path, _ := block.Input[key].(string)
		return timelineEntry{kind: timelineEdit, paths: []string{path}}
	}
	switch block.Name {
	case "Grep", "Glob":
		return timelineEntry{kind: timelineRequery}
	case "Read":
		path, _ := block.Input["file_path"].(string)
		if path == "" {
			return timelineEntry{kind: timelineOther}
		}
		return timelineEntry{kind: timelineRead, paths: []string{path}, readSpan: readToolSpan(block.Input)}
	case "Bash":
		command, _ := block.Input["command"].(string)
		if verb, ok := graphVerbFromCommand(command); ok {
			switch {
			case graphLocateVerbs[verb]:
				return timelineEntry{kind: timelineLocate}
			case graphLookupVerbs[verb]:
				return timelineEntry{kind: timelineRequery}
			}
			return timelineEntry{kind: timelineOther}
		}
		return shellTimelineEntry(command)
	}
	return timelineEntry{kind: timelineOther}
}

// readToolSpan is the Read tool's requested range: offset is the 1-based first line, limit the line
// count. No offset and no limit is a whole-file read. A malformed value is an unknown span.
func readToolSpan(input map[string]any) *lineSpan {
	offset, hasOffset := input["offset"]
	limit, hasLimit := input["limit"]
	start, end := 1, -1
	if hasOffset {
		value, ok := offset.(float64)
		if !ok || value < 0 {
			return nil
		}
		start = max(1, int(value))
	}
	if hasLimit {
		value, ok := limit.(float64)
		if !ok || value < 1 {
			return nil
		}
		end = start + int(value) - 1
	}
	return &lineSpan{start, end}
}

// shellTimelineEntry applies the exploration classifier's rules (leading pipeline stage only,
// writes excluded) and then splits a locate command into search vs read. A search anywhere in the
// command makes the whole call a re-query. Shell read spans are not parsed: unknown span.
func shellTimelineEntry(command string) timelineEntry {
	if command == "" {
		return timelineEntry{kind: timelineOther}
	}
	var reads []string
	for _, stage := range pipelineHeadStages(command) {
		if isWritingShellStage(stage) {
			continue
		}
		word := shellCommandWord(stage)
		switch {
		case shellSearchCommands[word]:
			return timelineEntry{kind: timelineRequery}
		case shellReadCommands[word]:
			reads = append(reads, shellStageOperands(stage)...)
		}
	}
	if len(reads) == 0 {
		return timelineEntry{kind: timelineOther}
	}
	return timelineEntry{kind: timelineRead, paths: reads}
}

// shellStageOperands returns every non-flag word after the command word. Non-path operands (a sed
// script, a line count) are harmless: they cannot overlap a path the graph named.
func shellStageOperands(stage string) []string {
	var operands []string
	seenCommand := false
	for _, field := range strings.Fields(stage) {
		word := shellWord(field)
		if word == "" {
			continue
		}
		if !seenCommand {
			base := filepath.Base(word)
			if strings.Contains(word, "=") || commandPrefixes[base] {
				continue
			}
			seenCommand = true
			continue
		}
		if strings.HasPrefix(word, "-") {
			continue
		}
		operands = append(operands, word)
	}
	return operands
}

// graphOutputPathPattern matches file-path-shaped tokens in graph output, text or JSON: optional
// directories, then a name with a lowercase extension (`internal/cli/stats.go`, `main.go`). The
// lowercase-extension rule keeps Go selectors like `fmt.Errorf` or `store.List` from being read
// as paths. Extensionless files (Makefile, Dockerfile) are NOT matched — see the help text.
var graphOutputPathPattern = regexp.MustCompile(`[A-Za-z0-9_@+~./-]*[A-Za-z0-9_-]\.[a-z][a-z0-9]{0,7}\b`)

// maxGraphOutputPaths bounds per-call memory on adversarially large outputs.
const maxGraphOutputPaths = 512

func graphOutputPaths(text string) []string {
	matches := graphOutputPathPattern.FindAllString(text, -1)
	seen := map[string]bool{}
	var paths []string
	for _, match := range matches {
		path := normaliseOverlapPath(match)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
		if len(paths) == maxGraphOutputPaths {
			break
		}
	}
	return paths
}

// graphDeliveredSpans returns, per file, the source lines a search response actually delivered:
// snippet_start_line plus the number of lines in the snippet text that survived rendering. It
// returns nil unless the WHOLE output parses as a search response, so clipped output (a `| head`,
// a truncated body) never yields spans — a header that survived clipping is not delivered source.
// A result whose snippet is empty (a locator) delivers no lines.
func graphDeliveredSpans(text string) map[string][]lineSpan {
	var response struct {
		Results []struct {
			FilePath         string `json:"file_path"`
			SnippetStartLine int    `json:"snippet_start_line"`
			Snippet          string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &response); err != nil || len(response.Results) == 0 {
		return nil
	}
	spans := map[string][]lineSpan{}
	for _, result := range response.Results {
		path := normaliseOverlapPath(result.FilePath)
		if path == "" || result.SnippetStartLine < 1 || result.Snippet == "" {
			continue
		}
		lines := strings.Count(strings.TrimRight(result.Snippet, "\n"), "\n") + 1
		spans[path] = append(spans[path], lineSpan{result.SnippetStartLine, result.SnippetStartLine + lines - 1})
	}
	return spans
}

func normaliseOverlapPath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	for strings.HasPrefix(path, "./") {
		path = path[2:]
	}
	return strings.TrimRight(path, "/.")
}

// pathOverlaps reports whether a file the agent touched is a file the graph named. Graph output is
// usually repo-relative and tool paths absolute, so the test is a whole-component suffix match in
// either direction: `/repo/internal/cli/stats.go` overlaps `internal/cli/stats.go` and `stats.go`,
// but not `xstats.go` or `other/stats.go`-vs-`internal/cli/stats.go`.
func pathOverlaps(named, touched string) bool {
	touched = normaliseOverlapPath(touched)
	if touched == "" || named == "" {
		return false
	}
	return touched == named || strings.HasSuffix(touched, "/"+named) || strings.HasSuffix(named, "/"+touched)
}

// pathsOverlap reports whether any touched path overlaps any named path.
func pathsOverlap(named, touched []string) bool {
	for _, t := range touched {
		for _, n := range named {
			if pathOverlaps(n, t) {
				return true
			}
		}
	}
	return false
}

// readSubclass decides which FOLLOW-UP READ bucket a matching read falls in.
func readSubclass(locate, read timelineEntry, edited []string) string {
	for _, touched := range read.paths {
		for _, named := range locate.paths {
			if !pathOverlaps(named, touched) {
				continue
			}
			for _, e := range edited {
				if pathOverlaps(named, e) {
					return followUpReadUnknownSpan // delivered lines may be stale
				}
			}
			spans, known := locate.delivered[named]
			if read.readSpan == nil || !known {
				return followUpReadUnknownSpan
			}
			for _, span := range spans {
				if span.overlaps(*read.readSpan) {
					return followUpReadOverlapping
				}
			}
			return followUpReadDifferent
		}
	}
	return followUpReadUnknownSpan
}

// classifyFollowUp decides the class of the locate result at timeline[index], scanning the window
// that starts at its DELIVERY — calls issued before the result arrived (a parallel batch, or calls
// made while it was outstanding) are not reactions to it. claimed makes read attribution exclusive:
// a read already attributed to a later-delivered result is skipped, so one read is never credited
// to two results.
func classifyFollowUp(timeline []timelineEntry, index, window int, claimed map[int]bool) string {
	entry := timeline[index]
	switch {
	case entry.isError:
		return followUpError
	case len(entry.paths) == 0:
		return followUpIneligible
	}
	start := entry.deliveredAt
	end := min(len(timeline), start+window)
	var edited []string
	for at := start; at < end; at++ {
		next := timeline[at]
		switch next.kind {
		case timelineLocate, timelineRequery:
			return followUpRequery
		case timelineEdit:
			edited = append(edited, next.paths...)
		case timelineRead:
			if claimed[at] || !pathsOverlap(entry.paths, next.paths) {
				continue
			}
			claimed[at] = true
			return readSubclass(entry, next, edited)
		}
	}
	if end-start < window {
		return followUpCensored
	}
	return followUpNone
}

// finishFollowUp stamps a class on every graph locate call whose result was observed, latest
// delivery first so read attribution goes to the most recent matching answer. A call without a
// result has no bytes and no named paths, and the model does not price it.
func (s *fileScanner) finishFollowUp() {
	var locates []int
	for index, entry := range s.timeline {
		if entry.kind == timelineLocate && entry.hasResult {
			locates = append(locates, index)
		}
	}
	sort.Slice(locates, func(i, j int) bool {
		return s.timeline[locates[i]].deliverSeq > s.timeline[locates[j]].deliverSeq
	})
	claimed := map[int]bool{}
	for _, index := range locates {
		entry := s.timeline[index]
		s.summary.Calls[entry.callIndex].Disp = classifyFollowUp(s.timeline, index, followUpWindow, claimed)
	}
}

// modeledBalanceBytes credits only NO FOLLOW-UP results, at the session's average exploration
// bytes/result — an ASSUMED price for a hypothetical avoided call, not an observed one — and
// subtracts every locate result's bytes, including errors, censored and ineligible results. Same
// comparison population as savingsBytes, so its 0 means the same thing.
func (a *sessionAcc) modeledBalanceBytes() int64 {
	_, locateResults, locateBytes := a.locateTotals()
	exploreResults := sumCounts(a.kindResults)
	if locateResults == 0 || exploreResults == 0 {
		return 0
	}
	credited := mulDiv(int64(a.displacement[followUpNone]), sumBytes(a.kindBytes), int64(exploreResults))
	return credited - locateBytes
}
