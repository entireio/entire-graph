package cli

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/entireio/entire-graph/internal/sem"
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
//     was edited in between so delivered lines may be stale). One read is span-attributed to ONE
//     result (the latest delivered). Any other result whose named file that same read opened is a
//     SHARED READ: still a follow-up read, never NO FOLLOW-UP, but not counted again in a span
//     sub-bucket.
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
	followUpReadShared      = "s" // the read was span-attributed to a sibling result
	followUpCensored        = "c"
	followUpError           = "e"
	followUpIneligible      = "x"
)

// followUpReadClasses are the four FOLLOW-UP READ sub-buckets.
var followUpReadClasses = []string{followUpReadOverlapping, followUpReadDifferent, followUpReadUnknownSpan, followUpReadShared}

// graphLookupVerbs are graph verbs that answer "where is X / what touches X". Any of them after a
// delivered locate result is a re-query. Meta verbs (stats, index, doctor, help, ...) are not.
var graphLookupVerbs = map[string]bool{
	"query": true, "search": true, "neighbors": true, "impact": true, "def": true, "explain": true,
	"symbols": true, "edges": true,
	"snapshot": true, "snapshot-query": true,
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
//
// Command substitution bodies are scanned too, with the same depth bound, because
// timelineFromToolUse's graph branch (graphVerbFromCommand) already finds a graph call inside one:
// counting `x=$(entire graph search ...)` as a locate while missing a follow-up
// `files=$(grep -rl Foo .)` as its re-query would credit the graph with a displacement it did not
// make.
func shellTimelineEntry(command string) timelineEntry {
	if command == "" {
		return timelineEntry{kind: timelineOther}
	}
	var reads []string
	if shellTimelineScan(command, 0, &reads) {
		return timelineEntry{kind: timelineRequery}
	}
	if len(reads) == 0 {
		return timelineEntry{kind: timelineOther}
	}
	return timelineEntry{kind: timelineRead, paths: reads}
}

// shellTimelineScan reports whether the command, or any command substitution inside it, runs a
// search; reads are collected along the way.
func shellTimelineScan(command string, depth int, reads *[]string) bool {
	for _, stage := range pipelineHeadStages(command) {
		if isWritingShellStage(stage) {
			continue
		}
		word := shellCommandWord(stage)
		switch {
		case shellSearchCommands[word]:
			return true
		case shellReadCommands[word]:
			*reads = append(*reads, shellStageOperands(stage)...)
		}
	}
	if depth >= maxSubstitutionDepth {
		return false
	}
	for _, body := range shellSubstitutionBodies(command) {
		if shellTimelineScan(body, depth+1, reads) {
			return true
		}
	}
	return false
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

// Named paths come from a graph answer's RESULT ROWS only.
//
// A graph result names a file when it LOCATES something in it. Everything else the renderers print
// around the rows is about the run, not the answer: the Index:/latency line, the Coverage: and
// Completeness: banners and their "- warning"/"- partial" diagnostic lines (which name the files
// that FAILED to parse), the VERIFY: block (a command to run), LOW CONFIDENCE:, the compact "!"/"C:"
// forms of all of those, and prose such as "No search results." or "No symbols matched". Reading
// paths out of the whole output made a zero-result query that printed a degraded-coverage banner
// look like an answer naming that file — eligible, and then credited as NO FOLLOW-UP.
//
// So text output yields only LOCATED tokens: a path immediately followed by ":<line>", which is how
// every result row prints (query/search "1. path:10-20", neighbors/impact "Name (path:40)", def
// "path:6  function Foo", call-site windows "  path:4-6", closed-set sites), plus the two row forms
// that carry no line — impact's co-change "- path [detail]" and "CONTAINER MAP path [n lines]".
// JSON output yields "file_path" values outside the diagnostic sections, whether it is the whole
// output or one line of it (after an echoed header, or in a persisted-output preview that clipped
// it). A response that says it found nothing ("No search results.", or an empty "results" array)
// names nothing.

// graphPathToken is a file-path-shaped token: optional directories, then a name with a lowercase
// extension. The lowercase-extension rule keeps Go selectors like `fmt.Errorf` from being read as
// paths. Extensionless files (Makefile, Dockerfile) are NOT matched — see the help text.
const graphPathToken = `[A-Za-z0-9_@+~./-]*[A-Za-z0-9_-]\.[a-z][a-z0-9]{0,7}`

var (
	// graphLocatedPathPattern: a path token followed by ":<digit>", not glued to a preceding
	// path character (RE2 has no lookbehind, so the boundary is consumed as group 0's prefix).
	graphLocatedPathPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_@+~./-])(` + graphPathToken + `):[0-9]`)
	// Impact's compact heading names the definition before its summary; its
	// telemetry now follows the answer instead of opening it.
	graphImpactHeadingPattern = regexp.MustCompile(`^\S.* — [^,\r\n]+, ` + graphPathToken + `:[0-9]+(?:-[0-9]+)?$`)
	// Located tokens count only in renderer record rows, never ordinary source text.
	graphLocatedRowPattern = regexp.MustCompile(`^(?:[ \t]*(?:[0-9]+\. )?` + graphPathToken + `:[0-9]|(?:Focus|Impact): .* \(` + graphPathToken + `:[0-9]|[-<>] .* \(` + graphPathToken + `:[0-9]|[ \t]+(?:owner|also declared|supertypes|implemented by|fields|nested|impl [^:]+): .*` + graphPathToken + `:[0-9])`)
	// graphLinelessRowPattern: the two result-row forms that name a file without a line.
	graphLinelessRowPattern = regexp.MustCompile(`^(?:- |CONTAINER MAP )(` + graphPathToken + `) \[`)
	graphRankedRowPattern   = regexp.MustCompile(`^[0-9]+\. ` + graphPathToken + `:[0-9]`)
	// graphSnippetLinePattern: a numbered source line inside a delivered window ("  4 | code",
	// "   6→ code"). Its text is repository content, not a row.
	graphSnippetLinePattern = regexp.MustCompile(`^\s*[0-9]+\s*(?:\||→)`)
)

// graphNoResultsLine is what query/search print when nothing matched.
const graphNoResultsLine = "No search results."

// graphTextSingleLineHeads are column-0 heads of one-line, non-result records.
var graphTextSingleLineHeads = []string{
	"Index:", "Index not cached", "LOW CONFIDENCE:", "!", "C:", "No symbols matched", "No exact match",
	"Blast radius:", searchForgeryNoticePrefix,
}

// graphJSONDiagnosticKeys are JSON members that describe the run, not the answer. Their file_path
// values name files that failed to parse or were skipped, never located results.
var graphJSONDiagnosticKeys = map[string]bool{
	"completeness": true, "completeness_scope": true, "partial_failures": true, "warnings": true,
	"verify_command": true, "stats": true, "tree": true, "repo_root": true,
}

// maxGraphOutputPaths bounds per-call memory on adversarially large outputs.
const maxGraphOutputPaths = 512

// graphKnownExtensions is the provider's own extension set. A slash-less token must carry one of
// them, so prose such as "e.g." or a lowercase selector like `store.list` is not a file.
var graphKnownExtensions = sync.OnceValue(func() map[string]bool {
	known := map[string]bool{}
	for _, extension := range sem.Capabilities().SupportedFileExtensions {
		known[strings.ToLower(extension)] = true
	}
	return known
})

func graphOutputPaths(text string) []string {
	candidates, ok := graphJSONOutputPaths(text)
	if !ok {
		candidates = graphTextOutputPaths(text)
	}
	seen := map[string]bool{}
	var paths []string
	for _, candidate := range candidates {
		path := normaliseOverlapPath(candidate)
		if path == "" || seen[path] || !plausibleGraphPath(path) {
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

// plausibleGraphPath rejects slash-less tokens whose extension the provider does not index.
func plausibleGraphPath(path string) bool {
	if strings.Contains(path, "/") {
		return true
	}
	return graphKnownExtensions()[strings.ToLower(filepath.Ext(path))]
}

// graphJSONResultKeys open the answer sections of query/search, neighbors, impact and def JSON.
// An ambiguous neighbors/impact answers with its candidates — "match_bodies" and impact's
// "definitions" — instead of a focus, so those open an answer section too.
var graphJSONResultKeys = map[string]bool{
	"results": true, "matches": true, "focus": true, "callers": true, "callees": true,
	"type_consumers": true, "data_flows": true, "co_changes": true, "siblings": true, "declarations": true,
	"definitions": true, "match_bodies": true,
}

var (
	// The value is a whole JSON string literal, escapes included, decoded before use.
	graphJSONFilePathPattern = regexp.MustCompile(`"file_path"\s*:\s*("(?:[^"\\]|\\.)*")`)
	graphJSONOpenerPattern   = regexp.MustCompile(`"([A-Za-z_]+)"\s*:\s*[\[{]`)
)

// graphJSONOutputPaths handles output that is a JSON object. ok=false means "not JSON": the caller
// applies the text rules. Output that parses whole is walked; output that starts like a JSON object
// but does not parse (clipped by `| head`, truncated) is scanned in order, keeping only file_path
// values that follow an answer-section opener and no later diagnostic-section opener.
func graphJSONOutputPaths(text string) ([]string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		return graphClippedJSONPaths(trimmed), true
	}
	if results, isList := root["results"].([]any); isList && len(results) == 0 {
		return nil, true
	}
	var paths []string
	var walk func(value any)
	walk = func(value any) {
		if len(paths) >= maxGraphOutputPaths {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			// Sorted, so the same output always names its paths in the same order.
			for _, key := range slices.Sorted(maps.Keys(typed)) {
				child := typed[key]
				if graphJSONDiagnosticKeys[key] {
					continue
				}
				if path, isString := child.(string); isString && key == "file_path" {
					paths = append(paths, path)
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(root)
	return paths, true
}

// graphClippedJSONPaths scans unparseable JSON. Sections are tracked by their opening key only, so a
// nested object (an endpoint, a symbol) never changes which section a file_path belongs to, and a
// path before any answer section (the run's own diagnostics) is never taken.
func graphClippedJSONPaths(text string) []string {
	openers := graphJSONOpenerPattern.FindAllStringSubmatchIndex(text, -1)
	var paths []string
	inAnswer := false
	next := 0
	for _, match := range graphJSONFilePathPattern.FindAllStringSubmatchIndex(text, -1) {
		for ; next < len(openers) && openers[next][0] < match[0]; next++ {
			key := text[openers[next][2]:openers[next][3]]
			switch {
			case graphJSONResultKeys[key]:
				inAnswer = true
			case graphJSONDiagnosticKeys[key]:
				inAnswer = false
			}
		}
		var path string
		if inAnswer && json.Unmarshal([]byte(text[match[2]:match[3]]), &path) == nil {
			paths = append(paths, path)
			if len(paths) >= maxGraphOutputPaths {
				break
			}
		}
	}
	return paths
}

// graphTextOutputPaths applies the row rules to text/agent output, skipping diagnostic blocks.
func graphTextOutputPaths(text string) []string {
	const (
		blockNone        = iota
		blockDiagnostics // Coverage:/Completeness: then "- ..." lines
		blockVerify      // VERIFY: then indented lines
	)
	block := blockNone
	var paths []string
	seenSearchRow := false
	emptySearchResponse := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch block {
		case blockDiagnostics:
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				continue
			}
		case blockVerify:
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				continue
			}
		}
		block = blockNone
		if graphRankedRowPattern.MatchString(line) {
			seenSearchRow = true
			emptySearchResponse = false
		} else if graphImpactHeadingPattern.MatchString(line) || strings.HasPrefix(line, "Index:") || strings.HasPrefix(line, "Index not cached") {
			// A chained invocation starts a new response; source-body text remains
			// subject to the renderer's record quarantine.
			seenSearchRow = false
			emptySearchResponse = false
		}
		if emptySearchResponse {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			if seenSearchRow {
				// A JSON-looking line in a search body is source, not another response.
				continue
			}
			// A JSON response printed after other text (an echo header, a persisted-output
			// preview) is one line: it gets the JSON rules, parsed or clipped.
			linePaths, _ := graphJSONOutputPaths(line)
			paths = append(paths, linePaths...)
			continue
		}
		switch {
		case strings.TrimSpace(line) == graphNoResultsLine:
			// This response located nothing, but an earlier chained response may
			// already have named real hits. Keep those paths.
			emptySearchResponse = true
			continue
		case strings.HasPrefix(line, "Coverage:") || strings.HasPrefix(line, "Completeness:"):
			block = blockDiagnostics
			continue
		case strings.HasPrefix(line, "VERIFY:"):
			block = blockVerify
			continue
		case hasAnyPrefix(line, graphTextSingleLineHeads) || graphSnippetLinePattern.MatchString(line):
			continue
		}
		if match := graphLinelessRowPattern.FindStringSubmatch(line); match != nil {
			paths = append(paths, match[1])
		}
		if !graphLocatedRowPattern.MatchString(line) && !graphImpactHeadingPattern.MatchString(line) {
			continue
		}
		for _, match := range graphLocatedPathPattern.FindAllStringSubmatch(line, -1) {
			paths = append(paths, match[1])
		}
		if len(paths) >= maxGraphOutputPaths {
			break
		}
	}
	return paths
}

func hasAnyPrefix(line string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// graphDeliveredSpans returns, per file, the source lines a search response actually delivered:
// each result's primary snippet and every attached passage (kept on the parent by
// --single-resolution), each from its start line plus the number of lines its snippet text carries.
// It returns nil unless the WHOLE output parses as a search response, so clipped output (a
// `| head`, a truncated body) never yields spans — a header that survived clipping is not delivered
// source. A result whose snippet is empty (a locator) delivers no lines.
func graphDeliveredSpans(text string) map[string][]lineSpan {
	var response struct {
		Results []struct {
			FilePath         string `json:"file_path"`
			SnippetStartLine int    `json:"snippet_start_line"`
			SnippetEndLine   int    `json:"snippet_end_line"`
			Snippet          string `json:"snippet"`
			Passages         []struct {
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
				Snippet   string `json:"snippet"`
			} `json:"passages"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &response); err != nil || len(response.Results) == 0 {
		return nil
	}
	spans := map[string][]lineSpan{}
	for _, result := range response.Results {
		path := normaliseOverlapPath(result.FilePath)
		if path == "" {
			continue
		}
		if span, ok := snippetSpan(result.SnippetStartLine, result.SnippetEndLine, result.Snippet); ok {
			spans[path] = append(spans[path], span)
		}
		for _, passage := range result.Passages {
			if span, ok := snippetSpan(passage.StartLine, passage.EndLine, passage.Snippet); ok {
				spans[path] = append(spans[path], span)
			}
		}
	}
	return spans
}

// snippetSpan is the line range a snippet's text covers from start. Every line counts, trailing
// blank lines included: a snippet is an exact join of source lines, so "x\n\n" is three lines.
// When the producer states the end line, the span never extends past it, which absorbs a
// presentation terminator without trusting an end line that exceeds the delivered text.
func snippetSpan(start, end int, snippet string) (lineSpan, bool) {
	if start < 1 || snippet == "" {
		return lineSpan{}, false
	}
	last := start + strings.Count(snippet, "\n")
	if end >= start && end < last {
		last = end
	}
	return lineSpan{start, last}, true
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
// made while it was outstanding) are not reactions to it. claimed keeps the SPAN sub-buckets
// exclusive: a read already span-attributed to a later-delivered result makes this one a SHARED
// READ. The class describes the first matching follow-up event, not every read
// in the window; a later unclaimed read cannot replace that event. It must not be skipped: two parallel answers naming the same file and one read of it are
// two answers the agent followed up, and skipping the read let the second one run out its window
// and be credited as NO FOLLOW-UP.
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
			if !pathsOverlap(entry.paths, next.paths) {
				continue
			}
			if claimed[at] {
				return followUpReadShared
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
