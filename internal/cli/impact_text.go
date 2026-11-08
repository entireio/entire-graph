package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// Group the complete text response before limiting rows. JSON retains the
// original relation sections and their existing per-section limits.
func writeImpactOverview(out io.Writer, response impactResponse) {
	focus := *response.Focus
	fmt.Fprint(out, termsafe.Line(endpointDisplayName(focus)))
	if focus.Kind != "" {
		fmt.Fprintf(out, " — %s", termsafe.Line(focus.Kind))
	}
	if response.Container != nil {
		fmt.Fprintf(out, " in %s", termsafe.Line(endpointDisplayName(*response.Container)))
	}
	fmt.Fprintf(out, ", %s:%d", termsafe.Line(focus.FilePath), focus.StartLine)
	if focus.EndLine > focus.StartLine {
		fmt.Fprintf(out, "-%d", focus.EndLine)
	}
	fmt.Fprint(out, "\n\nSummary:\n")

	var callers, tests, mentions, callees, flows []impactEntry
	seen := map[string]bool{}
	for _, entry := range response.Callers.Entries {
		switch {
		case entry.Detail == impactMentionDetail:
			mentions = append(mentions, entry)
		case isConventionalTestPath(entry.Endpoint.FilePath):
			tests = append(tests, entry)
		default:
			callers = append(callers, entry)
		}
		if entry.Detail != impactMentionDetail {
			seen[entry.Endpoint.ID] = true
		}
	}
	external := 0
	for _, entry := range response.Callees.Entries {
		seen[entry.Endpoint.ID] = true
		if entry.Endpoint.External {
			external++
		} else {
			callees = append(callees, entry)
		}
	}
	// Put local dependencies first, then retain deterministic path/line order.
	sort.SliceStable(callees, func(i, j int) bool {
		a, b := callees[i].Endpoint, callees[j].Endpoint
		if (a.FilePath == focus.FilePath) != (b.FilePath == focus.FilePath) {
			return a.FilePath == focus.FilePath
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		return a.StartLine < b.StartLine
	})
	for _, entry := range response.DataFlows.Entries {
		if !seen[entry.Endpoint.ID] {
			flows = append(flows, entry)
		}
	}
	types := impactTextTypes(response.TypeConsumers.Entries)
	fmt.Fprintf(out, "- %d production caller%s\n- %d test%s\n- %d internal callee%s\n- %d type%s\n",
		len(callers), pluralSuffix(len(callers)), len(tests), pluralSuffix(len(tests)), len(callees), pluralSuffix(len(callees)), len(types), pluralSuffix(len(types)))
	if external > 0 {
		// External endpoints do not establish whether a dependency is stdlib.
		fmt.Fprintf(out, "- %d external callee%s (--format json for names)\n", external, pluralSuffix(external))
	}
	if len(mentions) > 0 {
		fmt.Fprintf(out, "- %d documentation mention%s\n", len(mentions), pluralSuffix(len(mentions)))
	}
	if len(flows) > 0 {
		fmt.Fprintf(out, "- %d additional data flow%s\n", len(flows), pluralSuffix(len(flows)))
	}
	// Beside the caller counts it qualifies, and above every section, so the bounded
	// renderer's section caps and hard cut reach it last. See value_refs.go.
	if note := valueReferenceNote(response.Warnings, focus.Name); note != "" {
		fmt.Fprintf(out, "- %s\n", note)
	}
	for _, coupling := range []struct {
		count int
		name  string
		unit  string
	}{
		{response.CoChanges.Total, "co-change", "file"},
		{response.Siblings.Total, "sibling", "symbol"},
	} {
		if coupling.count == 0 && coupling.name == "co-change" && hasWarningCode(response.Warnings, sem.CochangeHistoryUnavailableCode) {
			// Not "none found": the history was never read, so the answer is unknown.
			fmt.Fprintf(out, "- Co-change coupling unknown: git history unavailable (%s)\n", sem.CochangeHistoryUnavailableCode)
		} else if coupling.count == 0 {
			fmt.Fprintf(out, "- No %s coupling found\n", coupling.name)
		} else {
			fmt.Fprintf(out, "- %d %s %s%s\n", coupling.count, coupling.name, coupling.unit, pluralSuffix(coupling.count))
		}
	}

	section := func(header string, entries []impactEntry, total int, arrows bool) {
		if total == 0 {
			return
		}
		fmt.Fprintln(out)
		if response.textLimit > 0 && len(entries) > response.textLimit {
			entries = entries[:response.textLimit]
		}
		writeImpactSection(out, header, impactSection{Total: total, Entries: entries}, arrows)
	}
	section(fmt.Sprintf("Callers (%d) — may break if behavior changes", len(callers)), callers, len(callers), false)
	section(fmt.Sprintf("Tests (%d) — run these after editing", len(tests)), tests, len(tests), false)
	section(fmt.Sprintf("Documentation mentions (%d)", len(mentions)), mentions, len(mentions), false)
	// A response decoded from capped JSON may lack rows needed to classify its
	// totals. Disclose those omissions instead of guessing their categories.
	if omitted := response.Callers.Total - len(response.Callers.Entries); omitted > 0 {
		fmt.Fprintf(out, "- ... +%d more callers (use --format json for the full list)\n", omitted)
	}
	section(fmt.Sprintf("Callees (%d internal, %d external) — its dependencies", len(callees), external), callees, len(callees), false)
	if omitted := response.Callees.Total - len(response.Callees.Entries); omitted > 0 {
		fmt.Fprintf(out, "- ... +%d more callees (use --format json for the full list)\n", omitted)
	}
	section(fmt.Sprintf("Types (%d)", len(types)), types, len(types), false)
	section(fmt.Sprintf("Data flows (%d additional)", len(flows)), flows, len(flows), true)
	section(fmt.Sprintf("Co-change coupling (%d)", response.CoChanges.Total), response.CoChanges.Entries, response.CoChanges.Total, false)
	section(fmt.Sprintf("Same-container siblings (%d)", response.Siblings.Total), response.Siblings.Entries, response.Siblings.Total, false)
}

// Merge relation tags for each endpoint, preserving direction as well as the
// reason for coupling. An incoming consumer is not an outgoing dependency.
func impactTextTypes(entries []impactEntry) []impactEntry {
	var result []impactEntry
	positions := map[string]int{}
	for _, entry := range entries {
		label := entry.Relation
		switch entry.Relation {
		case "PARAM_TYPE":
			label = "parameter"
		case "RETURNS_TYPE":
			label = "return type"
		case "USES_TYPE":
			label = "body use"
		}
		if entry.Direction == "in" {
			label = "<- " + label
		} else {
			label = "-> " + label
		}
		if position, ok := positions[entry.Endpoint.ID]; ok {
			result[position].Relation += "; " + label
		} else {
			positions[entry.Endpoint.ID] = len(result)
			entry.Relation = label
			result = append(result, entry)
		}
	}
	return result
}

func writeImpactFooter(out io.Writer, response impactResponse, scope completenessScope) {
	fmt.Fprintln(out)
	rung := relationLatencyHeader(response.IndexCacheHit, response.IndexLatencyMS, response.QueryLatencyMS, response.TotalLatencyMS)
	fmt.Fprint(out, strings.TrimSuffix(string(rung.printed), "\n"))
	if scope.Language != "" {
		if scope.LanguageFailed > 0 {
			fmt.Fprintf(out, " | %s coverage: %d failed files", scope.Language, scope.LanguageFailed)
		} else {
			fmt.Fprintf(out, " | %s coverage: no parse failures (%d file%s)", scope.Language, scope.LanguageFiles, pluralSuffix(scope.LanguageFiles))
		}
	}
	fmt.Fprintln(out)
	// Relevant parse failures were already shown above the answer. Other
	// diagnostics stay visible here without displacing the symbol overview.
	if scope.LanguageFailed == 0 && len(scope.InScopeFailures) == 0 {
		var diagnostics []string
		for _, warning := range scope.InScopeWarnings {
			diagnostics = append(diagnostics, warning.Code)
		}
		if scope.OtherFailures > 0 {
			diagnostics = append(diagnostics, fmt.Sprintf("%d in other languages", scope.OtherFailures))
		}
		if len(diagnostics) > 0 {
			fmt.Fprintf(out, "Diagnostics: %s (--format json for details)\n", strings.Join(diagnostics, "; "))
		}
	}
	writeIndexCostNotice(out, response.IndexCacheHit, response.IndexCacheDisabled)
}
