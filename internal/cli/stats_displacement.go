package cli

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Observed displacement.
//
// The 1:1 model credits every graph locate result with displacing exactly one exploration result.
// Transcripts contradict that assumption often enough to matter: agents re-query after a miss,
// and read the file a graph hit pointed at anyway. So instead of assuming the substitution, each
// graph locate call is classified by what the agent did NEXT, within the same transcript:
//
//   - RE-QUERY: within the next displacementWindow tool calls the agent issued another lookup —
//     a graph lookup verb, Grep, Glob, or a shell search (grep/rg/find/...). The graph answer did
//     not end the search, so it is credited with nothing and its bytes are a cost.
//   - READ-ANYWAY: before any re-query, within the window, the agent read a file (Read, or a shell
//     cat/head/tail/sed/...) whose path overlaps a path the graph output named. The agent paid for
//     the file anyway, so the graph result is credited with nothing and its bytes are a cost.
//   - DISPLACED: neither happened. Credited with that session's average exploration bytes/result,
//     exactly the price the 1:1 model already uses.
//
// The FIRST qualifying call in the window decides the class. A read of an unrelated file does not
// qualify and does not stop the scan.

// displacementWindow is K, how many tool calls after a graph locate call are inspected.
//
// Why 5: a locate call is followed by its consequence almost immediately — the agent either opens
// the answer or asks again. Parallel tool calls in one assistant turn land as consecutive calls,
// and Claude Code commonly issues 2-4 of them per turn, so K must span at least one full parallel
// batch plus the next turn's first call. Much beyond that, the calls belong to a different subtask
// (editing, running tests), and a later grep is no longer evidence about THIS graph answer: a
// larger K can only move calls from DISPLACED into the uncredited classes, so it would bias the
// report against the tool as surely as K=1 biases it for the tool. The PR that introduced this
// constant reports the sensitivity of the rates to K on a real corpus.
const displacementWindow = 5

const (
	displacementDisplaced  = "d"
	displacementRequery    = "q"
	displacementReadAnyway = "r"
)

// graphLookupVerbs are graph verbs that answer "where is X / what touches X". Any of them after a
// locate call is a re-query. Meta verbs (stats, index, doctor, help, ...) are not lookups.
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

type timelineKind uint8

const (
	timelineOther timelineKind = iota
	timelineLocate
	timelineRequery
	timelineRead
)

// timelineEntry is one tool call in transcript order. It lives only while a file is scanned; what
// is cached is the resulting class on the locate call's summaryCall.
type timelineEntry struct {
	kind      timelineKind
	paths     []string // read: the file(s) opened; locate: the paths its output named
	callIndex int      // locate: index into fileSummary.Calls
}

// timelineFromToolUse classifies a tool_use for the displacement window. It is deliberately
// independent of the exploration classifier's buckets: an Edit or `go test` is "other", but it
// still occupies a slot in the window.
func timelineFromToolUse(block contentBlock) timelineEntry {
	switch block.Name {
	case "Grep", "Glob":
		return timelineEntry{kind: timelineRequery}
	case "Read":
		path, _ := block.Input["file_path"].(string)
		if path == "" {
			return timelineEntry{kind: timelineOther}
		}
		return timelineEntry{kind: timelineRead, paths: []string{path}}
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

// shellTimelineEntry applies the exploration classifier's rules (leading pipeline stage only,
// writes excluded) and then splits a locate command into search vs read. A search anywhere in the
// command makes the whole call a re-query.
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

func normaliseOverlapPath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	for strings.HasPrefix(path, "./") {
		path = path[2:]
	}
	return strings.TrimRight(path, "/.")
}

// pathsOverlap reports whether a file the agent read is a file the graph named. Graph output is
// usually repo-relative and Read paths absolute, so the test is a whole-component suffix match in
// either direction: `/repo/internal/cli/stats.go` overlaps `internal/cli/stats.go` and `stats.go`,
// but not `xstats.go` or `other/stats.go`-vs-`internal/cli/stats.go`.
func pathsOverlap(named, read []string) bool {
	for _, r := range read {
		r = normaliseOverlapPath(r)
		if r == "" {
			continue
		}
		for _, n := range named {
			if r == n || strings.HasSuffix(r, "/"+n) || strings.HasSuffix(n, "/"+r) {
				return true
			}
		}
	}
	return false
}

// classifyDisplacement decides the class of the locate call at timeline[index].
func classifyDisplacement(timeline []timelineEntry, index int) string {
	return classifyDisplacementWithin(timeline, index, displacementWindow)
}

// classifyDisplacementWithin is classifyDisplacement with an explicit window, so K's sensitivity
// can be measured without a second copy of the rule.
func classifyDisplacementWithin(timeline []timelineEntry, index, window int) string {
	named := timeline[index].paths
	end := min(len(timeline), index+1+window)
	for _, next := range timeline[index+1 : end] {
		switch next.kind {
		case timelineLocate, timelineRequery:
			return displacementRequery
		case timelineRead:
			if pathsOverlap(named, next.paths) {
				return displacementReadAnyway
			}
		}
	}
	return displacementDisplaced
}

// finishDisplacement stamps a class on every graph locate call whose result was observed. A call
// without a result has no bytes and no named paths, and the model does not price it.
func (s *fileScanner) finishDisplacement() {
	for index, entry := range s.timeline {
		if entry.kind != timelineLocate {
			continue
		}
		call := &s.summary.Calls[entry.callIndex]
		if !call.HasResult {
			continue
		}
		call.Disp = classifyDisplacement(s.timeline, index)
	}
}

// observedSavingsBytes is the observed-displacement counterpart of savingsBytes: only DISPLACED
// results are credited, at the same per-session exploration price, and every locate result's bytes
// are subtracted. Same comparison population as savingsBytes, so its 0 means the same thing.
func (a *sessionAcc) observedSavingsBytes() int64 {
	_, locateResults, locateBytes := a.locateTotals()
	exploreResults := sumCounts(a.kindResults)
	if locateResults == 0 || exploreResults == 0 {
		return 0
	}
	credited := mulDiv(int64(a.displacement[displacementDisplaced]), sumBytes(a.kindBytes), int64(exploreResults))
	return credited - locateBytes
}
