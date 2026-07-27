package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

// defaultSearchContextBytes is the default `--max-context-bytes` ceiling for one search call.
//
// It is sized by TURN economics, not by payload economics. Measured on real agentic sessions,
// the search payload is ~0.6% of what a session spends, while a single extra agent turn costs
// ~42.5k tokens (95.9% of billed tokens are context re-read). A search that stops one Read
// short of an edit therefore costs about 40x more than the entire payload that caused it, so
// the ceiling's job is NOT to be small — it is to never be the reason a head result comes back
// as half a function.
//
// 24 kB ≈ 6k tokens: it clears the largest ranked payloads observed (~15 kB) with room for the
// five complete head bodies the allocator may buy, so the allocator is bounded by what bodies
// actually cost rather than silently truncated by the ceiling. The old 16 kB default could not
// fit even one complete body on top of a large ranking. In practice payloads do NOT expand to
// fill it: the allocator only spends bytes on complete head bodies and always picks the
// cheapest plan that delivers them (measured on a 14-repo probe: mean payload 11.8 kB, i.e.
// half the ceiling). `--max-context-bytes` remains honored exactly, so a caller who wants the
// old behaviour passes `--max-context-bytes 16384`.
const defaultSearchContextBytes = 24 * 1024

type searchFlags struct {
	Repo              string
	Query             string
	Format            string
	Profile           string
	Worktree          bool
	TopK              int
	ContextLines      int
	MaxRegionLines    int
	MaxSnippetLines   int
	MaxRegionsPerFile int
	IgnoreFiles       []string
	IncludeFiles      []string
	CacheDir          string
	DisableCache      bool
	MaxIndexedFiles   int
	IndexAllFiles     bool
	MaxContextBytes   int
	Deep              bool
}

func runSearch(ctx context.Context, opts Options, args []string) error {
	flags, rest, err := parseSearchFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("search received unexpected arguments: %s", strings.Join(rest, " "))
	}
	if strings.TrimSpace(flags.Query) == "" {
		return errors.New("search requires --query")
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
	contextBudget := flags.MaxContextBytes
	// Agent output has a much smaller wire representation than the public JSON
	// response. Keep full snippets until that representation is budgeted below.
	if flags.Format == "agent" {
		flags.MaxContextBytes = 0
	}
	response, err := sem.SearchRepository(ctx, repo, opts.Version, flags.Query, sem.SearchOptions{
		Worktree:          flags.Worktree,
		IgnoreFiles:       flags.IgnoreFiles,
		IncludeFiles:      flags.IncludeFiles,
		Profile:           profile,
		TopK:              flags.TopK,
		ContextLines:      flags.ContextLines,
		MaxRegionLines:    flags.MaxRegionLines,
		MaxSnippetLines:   flags.MaxSnippetLines,
		MaxRegionsPerFile: flags.MaxRegionsPerFile,
		CacheDir:          cacheDir,
		DisableCache:      flags.DisableCache,
		MaxIndexedFiles:   flags.MaxIndexedFiles,
		IndexAllFiles:     flags.IndexAllFiles,
		MaxContextBytes:   flags.MaxContextBytes,
		Deep:              flags.Deep,
	})
	if err != nil {
		return err
	}
	if err := response.Validate(); err != nil {
		return err
	}
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "ndjson":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(map[string]any{
			"record_type": "search_header",
			"query":       response.Query,
			"repo_root":   response.RepoRoot,
			"commit":      response.Commit,
			"tree":        response.Tree,
			"profile":     response.Profile,
		}); err != nil {
			return err
		}
		for _, result := range response.Results {
			if err := encoder.Encode(struct {
				RecordType string `json:"record_type"`
				sem.SearchResult
			}{RecordType: "search_result", SearchResult: result}); err != nil {
				return err
			}
		}
		return encoder.Encode(map[string]any{
			"record_type":      "search_summary",
			"stats":            response.Stats,
			"warnings":         response.Warnings,
			"partial_failures": response.PartialFailures,
			"completeness":     response.Completeness,
		})
	case "text":
		return writeTextSearch(opts.Stdout, response)
	case "agent":
		return writeAgentSearch(opts.Stdout, response, contextBudget)
	default:
		return fmt.Errorf("search --format must be json, ndjson, text, or agent, got %q", flags.Format)
	}
}

// searchTextFullRanks is how deep `--format text` renders a full snippet for a result the
// snippet allocator did NOT upgrade to a complete body. Below it a hit is used only to decide
// "is this worth opening", so a full window there is token waste.
const searchTextFullRanks = 2

// Section headers for `--format text`. They label what the group IS, because the failure they
// exist to prevent is an agent treating a non-fix-site as the fix site.
const (
	searchTextRelatedHeader = "RELATED SITES (the same change usually also lands here):"
	searchTextDocsHeader    = "DOCS & FIXTURES (matched the query; not fix sites):"
)

// writeTextSearch renders the default `--format text` output, grouped by section.
//
// The primary group is the only one presented as candidate fix sites. It is tiered, and the
// tier is decided by what the result CARRIES rather than by rank alone: a result the allocator
// spent budget to return as a complete function body is always printed in full — dropping it
// here would throw away the very bytes that let an agent edit without opening the file — while
// an ordinary window is printed for the first `searchTextFullRanks` entries of the group and
// collapsed to a terse "N. path:line symbol" locator below that.
//
// The tier counts POSITION IN THE GROUP, not absolute rank. A non-code hit that outranks the
// code is sectioned away, and charging its rank against the full-snippet tier would leave the
// agent one full window short for no reason.
//
// Ranks are the payload's own, unchanged, so a rank missing from the primary group is a visible
// signal that it was sectioned rather than dropped.
func writeTextSearch(out interface{ Write([]byte) (int, error) }, response sem.SearchResponse) error {
	if notice, _ := searchLowConfidenceNotices(response); len(notice) > 0 {
		if _, err := out.Write(notice); err != nil {
			return err
		}
	}
	primary, related, docs := partitionSearchSections(response.Results)
	for index, result := range primary {
		writeTextSearchResult(out, result, index < searchTextFullRanks)
	}
	if len(related) > 0 {
		fmt.Fprintf(out, "%s\n", searchTextRelatedHeader)
		for _, result := range related {
			name := searchResultDisplayName(result)
			fmt.Fprintf(out, "%d. %s:%d", result.Rank, result.FilePath, searchResultLocatorLine(result))
			if name != "" {
				fmt.Fprintf(out, " symbol=%s", name)
			}
			fmt.Fprintf(out, " (%s)\n", sem.RelatedSiteKind(result))
		}
	}
	if len(docs) > 0 {
		fmt.Fprintf(out, "%s\n", searchTextDocsHeader)
		for _, result := range docs {
			writeTextSearchResult(out, result, false)
		}
	}
	return nil
}

func writeTextSearchResult(out interface{ Write([]byte) (int, error) }, result sem.SearchResult, full bool) {
	name := searchResultDisplayName(result)
	if !full && !searchResultCarriesCompleteBody(result) {
		if name != "" {
			fmt.Fprintf(out, "%d. %s:%d %s\n", result.Rank, result.FilePath, searchResultLocatorLine(result), name)
		} else {
			fmt.Fprintf(out, "%d. %s:%d\n", result.Rank, result.FilePath, searchResultLocatorLine(result))
		}
		return
	}
	start, end := searchResultPrintedRange(result)
	fmt.Fprintf(out, "%d. %s:%d-%d score=%.4f", result.Rank, result.FilePath, start, end, result.Score)
	if name != "" {
		fmt.Fprintf(out, " symbol=%s", name)
	}
	fmt.Fprintf(out, " signals=%s\n%s\n\n", strings.Join(result.Signals, ","), result.Snippet)
}

// searchResultPrintedRange is the range of the source that follows on the next
// lines — the SNIPPET's range, not the ranked region's.
//
// Those differ: a matched region can start a line or two above the definition
// (a blank line, a doc comment) while the snippet is snapped to the symbol's own
// bounds. Printing the region above the symbol's source made one symbol look
// like it had two different definition lines within one session — `:779-848`
// here against `:781` from a relation answer — with nothing saying which was
// which. The header now describes exactly the lines printed under it, so both
// verbs report the symbol's own span, and the number costs nothing extra.
func searchResultPrintedRange(result sem.SearchResult) (int, int) {
	if result.SnippetStartLine > 0 && result.SnippetEndLine >= result.SnippetStartLine {
		return result.SnippetStartLine, result.SnippetEndLine
	}
	return result.StartLine, result.EndLine
}

// partitionSearchSections splits a payload into its presentation groups while preserving rank
// order inside each. Results carrying an unknown section label are treated as primary: a
// renderer that silently hid a group it did not recognize would lose recall on upgrade.
func partitionSearchSections(results []sem.SearchResult) (primary, related, docs []sem.SearchResult) {
	for _, result := range results {
		switch result.Section {
		case sem.SearchSectionRelated:
			related = append(related, result)
		case sem.SearchSectionDocs:
			docs = append(docs, result)
		default:
			primary = append(primary, result)
		}
	}
	return primary, related, docs
}

func searchResultDisplayName(result sem.SearchResult) string {
	if result.QualifiedName != "" {
		return result.QualifiedName
	}
	return result.SymbolName
}

func searchResultLocatorLine(result sem.SearchResult) int {
	if result.FocusLine > 0 {
		return result.FocusLine
	}
	return result.StartLine
}

func searchResultCarriesCompleteBody(result sem.SearchResult) bool {
	for _, signal := range result.Signals {
		if signal == sem.CompleteSymbolSignal {
			return true
		}
	}
	return false
}

// orderAgentSearchResults groups a payload for `--format agent`: candidate fix sites, then the
// related-site block, then docs and fixtures. The agent format is a hard byte cap that fits
// results one block at a time, so a group HEADER is the one thing it cannot afford — the label
// rides on each block's own location line instead (see agentSearchSectionTag), and grouping is
// expressed by order. Docs and fixtures sort last, so under a tight cap the entries dropped
// first are the ones that were never fix sites.
func orderAgentSearchResults(results []sem.SearchResult) []sem.SearchResult {
	primary, related, docs := partitionSearchSections(results)
	if len(related) == 0 && len(docs) == 0 {
		return results
	}
	ordered := make([]sem.SearchResult, 0, len(results))
	ordered = append(ordered, primary...)
	ordered = append(ordered, related...)
	return append(ordered, docs...)
}

// agentSearchSectionTag labels a block that is not a candidate fix site. Primary results carry
// no tag: the common case must cost zero bytes.
func agentSearchSectionTag(result sem.SearchResult) string {
	switch result.Section {
	case sem.SearchSectionRelated:
		if kind := sem.RelatedSiteKind(result); kind != "" {
			return "(" + kind + ")"
		}
		return "(related)"
	case sem.SearchSectionDocs:
		return "(docs)"
	}
	return ""
}

func writeAgentSearch(out interface{ Write([]byte) (int, error) }, response sem.SearchResponse, budget int) error {
	results := orderAgentSearchResults(response.Results)
	stats := response.Stats
	cacheState := "miss"
	if stats.IndexCacheHit {
		cacheState = "hit"
	}
	fullHeader := []byte(fmt.Sprintf(
		"Index: cache-%s (%dms) | Query: %dms | Preselect: %dms | Total: %dms\n",
		cacheState,
		stats.IndexLatencyMS,
		stats.QueryLatencyMS,
		stats.PreselectLatencyMS,
		stats.TotalLatencyMS,
	))
	fullDiagnostics, compactDiagnostics := agentSearchDiagnostics(response)
	fullConfidence, compactConfidence := searchLowConfidenceNotices(response)
	if budget <= 0 {
		payload := append([]byte{}, fullHeader...)
		payload = append(payload, fullDiagnostics...)
		payload = append(payload, fullConfidence...)
		if len(results) == 0 {
			payload = append(payload, "No search results.\n"...)
		} else {
			payload = append(payload, fitAgentSearchResults(results, 0)...)
		}
		_, err := out.Write(payload)
		return err
	}

	// Preserve retrieval usefulness under tight budgets. The full telemetry
	// header is preferred, but a compact equivalent leaves room for the
	// top-ranked location at budgets where the expanded diagnostic otherwise
	// crowds out every result.
	compactHeader := []byte(fmt.Sprintf(
		"I:%s/%d Q:%d P:%d T:%d\n",
		cacheState,
		stats.IndexLatencyMS,
		stats.QueryLatencyMS,
		stats.PreselectLatencyMS,
		stats.TotalLatencyMS,
	))
	legacyHeader := []byte(fmt.Sprintf("Index: cache-%s (%dms)\n", cacheState, stats.IndexLatencyMS))
	diagnosticVariants := [][]byte{fullDiagnostics}
	if !bytes.Equal(fullDiagnostics, compactDiagnostics) {
		diagnosticVariants = append(diagnosticVariants, compactDiagnostics)
	}
	// The low-confidence marker degrades like the coverage diagnostic and, when the budget
	// cannot hold even its compact form, is dropped rather than displacing the ranking: a
	// caller that got no results at all does not need to be told they are weak.
	confidenceVariants := [][]byte{fullConfidence}
	if !bytes.Equal(fullConfidence, compactConfidence) {
		confidenceVariants = append(confidenceVariants, compactConfidence)
	}
	if len(fullConfidence) > 0 {
		confidenceVariants = append(confidenceVariants, nil)
	}
	for _, header := range [][]byte{fullHeader, compactHeader, legacyHeader} {
		for _, diagnostics := range diagnosticVariants {
			for _, confidence := range confidenceVariants {
				remaining := budget - len(header) - len(diagnostics) - len(confidence)
				if remaining <= 0 {
					continue
				}
				prefix := func() []byte {
					payload := append([]byte{}, header...)
					payload = append(payload, diagnostics...)
					return append(payload, confidence...)
				}
				if len(results) == 0 {
					noResults := []byte("No search results.\n")
					if len(noResults) <= remaining {
						_, err := out.Write(append(prefix(), noResults...))
						return err
					}
					continue
				}
				formatted := fitAgentSearchResults(results, remaining)
				if len(formatted) > 0 {
					_, err := out.Write(append(prefix(), formatted...))
					return err
				}
			}
		}
	}

	// Some positive budgets cannot hold even a single ranked location. Preserve
	// a degraded-coverage marker ahead of telemetry when one is required, and
	// never exceed the caller's exact byte cap.
	payload := legacyHeader
	if len(compactDiagnostics) > 0 {
		marker := "!N"
		if len(response.PartialFailures) > 0 {
			marker = "!D"
		}
		combined := []byte(fmt.Sprintf("Index: cache-%s%s\n", cacheState, marker))
		if len(combined) <= budget {
			payload = combined
		} else {
			payload = []byte(fmt.Sprintf("%s I:%s\n", marker, cacheState))
		}
	}
	if len(payload) > budget {
		payload = payload[:budget]
	}
	_, err := out.Write(payload)
	return err
}

// searchLowConfidenceNotices renders the low-confidence marker in a full and a compact
// form. Both are empty when the payload is not weak, so a good query pays nothing.
//
// The wording states what to DO, because the failure it exists to prevent is an agent
// reading a plausible-looking hit as the fix site in a repo that never contained the thing
// it asked about.
func searchLowConfidenceNotices(response sem.SearchResponse) ([]byte, []byte) {
	assessment := sem.AssessSearchConfidence(response)
	if !assessment.Low {
		return nil, nil
	}
	full := []byte(fmt.Sprintf(
		"LOW CONFIDENCE: top score %.1f (weak, below %.0f) and %s. This repo may not contain what you asked for; verify before editing.\n",
		assessment.TopScore, sem.LowConfidenceScoreCeiling(), assessment.Reason,
	))
	compact := []byte(fmt.Sprintf("!LOW s=%.1f\n", assessment.TopScore))
	return full, compact
}

func agentSearchDiagnostics(response sem.SearchResponse) ([]byte, []byte) {
	if len(response.Warnings) == 0 && len(response.PartialFailures) == 0 {
		return nil, nil
	}
	languages, files := searchCompletenessCounts(response.Completeness)
	level := "notice"
	if len(response.PartialFailures) > 0 {
		level = "degraded"
	}
	var full bytes.Buffer
	fmt.Fprintf(&full, "Coverage: %s (%d language%s/%d file%s; %d warning%s; %d partial failure%s)\n",
		level, languages, pluralSuffix(languages), files, pluralSuffix(files),
		len(response.Warnings), pluralSuffix(len(response.Warnings)),
		len(response.PartialFailures), pluralSuffix(len(response.PartialFailures)),
	)
	const maxAgentDiagnostics = 3
	warningsVisible, failuresVisible := agentDiagnosticVisibility(
		len(response.Warnings), len(response.PartialFailures), maxAgentDiagnostics,
	)
	for _, warning := range response.Warnings[:warningsVisible] {
		fmt.Fprintf(&full, "- warning %s%s\n", warning.Code, agentDiagnosticPath(warning.FilePath))
	}
	for _, failure := range response.PartialFailures[:failuresVisible] {
		fmt.Fprintf(&full, "- partial %s%s\n", failure.Code, agentDiagnosticPath(failure.FilePath))
	}
	visible := warningsVisible + failuresVisible
	if omitted := len(response.Warnings) + len(response.PartialFailures) - visible; omitted > 0 {
		fmt.Fprintf(&full, "- ... %d more diagnostic%s in JSON output\n", omitted, pluralSuffix(omitted))
	}
	marker := "N"
	if level == "degraded" {
		marker = "D"
	}
	compact := []byte(fmt.Sprintf("!%s W%d F%d L%d/%d\n",
		marker, len(response.Warnings), len(response.PartialFailures), languages, files))
	return full.Bytes(), compact
}

func agentDiagnosticVisibility(warnings, failures, limit int) (int, int) {
	if limit <= 0 {
		return 0, 0
	}
	warningsVisible := minIntCLI(warnings, limit)
	failuresVisible := minIntCLI(failures, limit-warningsVisible)
	if failures > 0 && failuresVisible == 0 {
		warningsVisible--
		failuresVisible = 1
	}
	return warningsVisible, failuresVisible
}

func searchCompletenessCounts(report sem.CompletenessReport) (int, int) {
	files := 0
	for _, language := range report.Languages {
		files += language.Files
	}
	return len(report.Languages), files
}

func agentDiagnosticPath(path string) string {
	if path == "" {
		return ""
	}
	return ": " + path
}

func fitAgentSearchResults(results []sem.SearchResult, budget int) []byte {
	if budget <= 0 {
		return renderAgentSearchResults(results, nil)
	}
	for count := len(results); count > 0; count-- {
		available := budget - (count - 1)
		if available <= 0 {
			continue
		}
		resultBudgets := rankedAgentSearchBudgets(count, available)
		formatted := renderAgentSearchResults(results[:count], resultBudgets)
		if len(formatted) <= budget {
			return formatted
		}
	}
	return nil
}

func rankedAgentSearchBudgets(count, budget int) []int {
	if count <= 0 {
		return nil
	}
	budgets := make([]int, count)
	minimum := minIntCLI(128, budget/count)
	remaining := budget - minimum*count
	weightTotal := count * (count + 1) / 2
	allocated := 0
	for index := range budgets {
		extra := 0
		if weightTotal > 0 {
			extra = remaining * (count - index) / weightTotal
		}
		budgets[index] = minimum + extra
		allocated += extra
	}
	// Integer division can leave a few bytes. They are most valuable at the
	// top of the ranking, where agents make their first navigation decision.
	for index := 0; allocated < remaining; index = (index + 1) % count {
		budgets[index]++
		allocated++
	}
	return budgets
}

func renderAgentSearchResults(results []sem.SearchResult, budgets []int) []byte {
	var output bytes.Buffer
	wrote := false
	for index, result := range results {
		budget := 0
		if index < len(budgets) {
			budget = budgets[index]
		}
		block := agentSearchBlock(result, budget)
		if len(block) == 0 {
			return nil
		}
		if wrote {
			output.WriteByte('\n')
		}
		output.Write(block)
		wrote = true
	}
	return output.Bytes()
}

// agentSearchScoreTag renders the ` s=<n>` suffix for a block, or "" when the score carries
// no meaning. Related sites are not ranked answers — they are the other places the top hit's
// change usually has to land — and they carry no relevance score, so printing `s=0.0` beside
// one would read as "worthless" rather than "not applicable".
func agentSearchScoreTag(result sem.SearchResult) string {
	if result.Section == sem.SearchSectionRelated {
		return ""
	}
	return fmt.Sprintf(" s=%.1f", result.Score)
}

func agentSearchBlock(result sem.SearchResult, budget int) []byte {
	name := searchResultDisplayName(result)
	tag := agentSearchSectionTag(result)
	scored := agentSearchScoreTag(result)
	lines := strings.Split(result.Snippet, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	snippetStart := result.SnippetStartLine
	if snippetStart <= 0 {
		snippetStart = result.StartLine
	}
	if snippetStart <= 0 {
		snippetStart = 1
	}
	focusLine := result.FocusLine
	if focusLine <= 0 {
		focusLine = snippetStart
	}
	focus := focusLine - snippetStart
	if focus < 0 || focus >= len(lines) {
		focus = len(lines) / 2
		focusLine = snippetStart + focus
	}
	if len(lines) == 0 {
		return fitAgentSearchLocation(result.Rank, result.FilePath, focusLine, name, tag, scored, budget)
	}

	// Prefer the widest balanced span containing the focus line. The location
	// in the header is rebuilt for each candidate, so it always describes the
	// lines actually displayed rather than the original untrimmed region.
	for span := len(lines); span > 0; span-- {
		leftMin := focus - span + 1
		if leftMin < 0 {
			leftMin = 0
		}
		leftMax := focus
		if limit := len(lines) - span; leftMax > limit {
			leftMax = limit
		}
		bestBalance := len(lines) + 1
		var best []byte
		for left := leftMin; left <= leftMax; left++ {
			right := left + span - 1
			text := strings.Join(lines[left:right+1], "\n")
			startLine, endLine := snippetStart+left, snippetStart+right
			for _, header := range agentSearchLocationHeaders(result.Rank, result.FilePath, startLine, endLine, focusLine, name, tag, scored) {
				candidate := []byte(header + text + "\n")
				if budget <= 0 || len(candidate) <= budget {
					balance := focus - left - (right - focus)
					if balance < 0 {
						balance = -balance
					}
					if best == nil || balance < bestBalance {
						best, bestBalance = candidate, balance
					}
					break
				}
			}
		}
		if best != nil {
			return best
		}
	}
	return fitAgentSearchLocation(result.Rank, result.FilePath, focusLine, name, tag, scored, budget)
}

// agentSearchLocationHeaders builds the location line in decreasing cost. `tag` labels a block
// that is not a candidate fix site; it rides on the two roomier variants and is dropped by the
// minimal one, which exists for caps too small to hold a rank and a name at all.
//
// `scored` is the pre-rendered relevance-score suffix (see agentSearchScoreTag). It is NOT
// an optional decoration: the score is the only per-result signal that separates a real hit
// from the best of a bad lot, and its absence from this format let a query about a technology
// absent from a repo come back as six confident-looking hits. It rides on the two roomier
// variants at ~7 bytes each and is dropped by the minimal one along with rank and name.
func agentSearchLocationHeaders(rank int, path string, start, end, focus int, name, tag, scored string) []string {
	location := fmt.Sprintf("%d. %s:%d", rank, path, start)
	if end != start {
		location += fmt.Sprintf("-%d", end)
	}
	rich := location
	if name != "" {
		rich += " " + name
	}
	if tag != "" {
		rich += " " + tag
	}
	rich += scored + fmt.Sprintf(" [focus:%d]\n", focus)
	compact := location
	if name != "" {
		compact += " " + name
	}
	if tag != "" {
		compact += " " + tag
	}
	compact += scored + " *\n"
	minimal := fmt.Sprintf("%s:%d *\n", path, focus)
	return []string{rich, compact, minimal}
}

func fitAgentSearchLocation(rank int, path string, focus int, name, tag, scored string, budget int) []byte {
	for _, header := range agentSearchLocationHeaders(rank, path, focus, focus, focus, name, tag, scored) {
		if budget <= 0 || len(header) <= budget {
			return []byte(header)
		}
	}
	return nil
}

func minIntCLI(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func parseSearchFlags(args []string) (searchFlags, []string, error) {
	flags := searchFlags{Format: "json", Profile: "syntax-only", Worktree: true, MaxContextBytes: defaultSearchContextBytes}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Repo, i = value, next
		case "--query":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Query, i = value, next
		case "--format":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Format, i = value, next
		case "--profile":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.Profile, i = value, next
		case "--top-k":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.TopK, i = value, next
		case "--context-lines":
			value, next, err := searchNonNegativeIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.ContextLines, i = value, next
		case "--max-region-lines":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxRegionLines, i = value, next
		case "--max-snippet-lines":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxSnippetLines, i = value, next
		case "--max-regions-per-file":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxRegionsPerFile, i = value, next
		case "--ignore-file":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.IgnoreFiles, i = append(flags.IgnoreFiles, value), next
		case "--include-file":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.IncludeFiles, i = append(flags.IncludeFiles, value), next
		case "--cache-dir":
			value, next, err := searchFlagValue(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.CacheDir, i = value, next
		case "--no-cache":
			flags.DisableCache = true
		case "--max-indexed-files":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxIndexedFiles, i = value, next
		case "--index-all-files":
			flags.IndexAllFiles = true
		case "--deep":
			flags.Deep = true
		case "--max-context-bytes":
			value, next, err := searchPositiveIntFlag(args, i)
			if err != nil {
				return flags, nil, err
			}
			flags.MaxContextBytes, i = value, next
		case "--worktree", "--no-network":
			if args[i] == "--worktree" {
				flags.Worktree = true
			}
		case "--head":
			flags.Worktree = false
		default:
			rest = append(rest, args[i])
		}
	}
	return flags, rest, nil
}

func searchFlagValue(args []string, index int) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("%s requires a value", args[index])
	}
	return args[index+1], index + 1, nil
}

func searchPositiveIntFlag(args []string, index int) (int, int, error) {
	value, next, err := searchNonNegativeIntFlag(args, index)
	if err != nil {
		return 0, index, err
	}
	if value == 0 {
		return 0, index, fmt.Errorf("%s must be positive", args[index])
	}
	return value, next, nil
}

func searchNonNegativeIntFlag(args []string, index int) (int, int, error) {
	raw, next, err := searchFlagValue(args, index)
	if err != nil {
		return 0, index, err
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, index, fmt.Errorf("%s requires a non-negative integer", args[index])
	}
	return value, next, nil
}
