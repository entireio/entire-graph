package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

// renderGraphReport renders a snapshot as GRAPH_REPORT.md: the thirty-second
// answer to "what did this tool find in my repository?", for a human deciding
// whether to keep it. It is a pure projection of the summary a snapshot already
// carries — no re-parsing, no second pass over source, no network — and every map
// is sorted before printing, so the same snapshot always renders the same bytes.
func renderGraphReport(snapshot sem.ProviderSnapshot) string {
	header := snapshot.Header
	stats := header.Stats
	var out strings.Builder
	out.WriteString("# Graph report\n\n")
	fmt.Fprintf(&out, "- Repository: `%s`\n- Commit: `%s`\n- Tree: `%s`\n",
		header.RepoKey, header.Commit, header.Tree)
	fmt.Fprintf(&out, "- Provider: `%s` %s, schema `%s`, profile `%s`\n",
		header.Provider, header.ProviderVersion, header.SchemaVersion, header.Profile)
	fmt.Fprintf(&out, "- Completeness: **%s** (%d of %d files parsed, %d partial failures)\n",
		stats.CompletenessLevel, stats.ParsedFiles, stats.Files, stats.PartialFailures)
	fmt.Fprintf(&out, "- Graph: **%d symbols**, **%d relations**\n", stats.Symbols, stats.Relations)

	// Languages carry their fidelity tier, because counting an inventory-only
	// language as semantic coverage is the misreading this table exists to prevent.
	out.WriteString("\n## Languages\n\n| Language | Tier | Files | Symbols |\n| --- | --- | --- | --- |\n")
	names := make([]string, 0, len(header.Completeness.Languages))
	for name := range header.Completeness.Languages {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := header.Completeness.Languages[names[i]], header.Completeness.Languages[names[j]]
		if left.Symbols != right.Symbols {
			return left.Symbols > right.Symbols
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		tier := header.LanguageTiers[name]
		if tier == "" {
			tier = "unknown"
		}
		completeness := header.Completeness.Languages[name]
		fmt.Fprintf(&out, "| %s | %s | %d | %d |\n", name, tier, completeness.Files, completeness.Symbols)
	}

	writeCountTable(&out, "Symbol kinds", "Kind", countBy(len(snapshot.Symbols), func(i int) string {
		return snapshot.Symbols[i].Kind
	}), 0)
	writeCountTable(&out, "Relations", "Relation", header.Completeness.Relations, 0)
	writeCountTable(&out, "Top files by symbol count", "File", countBy(len(snapshot.Symbols), func(i int) string {
		return snapshot.Symbols[i].FilePath
	}), 20)
	writeCountTable(&out, "Warnings", "Code", countBy(len(header.Warnings), func(i int) string {
		return header.Warnings[i].Code
	}), 0)
	writeCountTable(&out, "Partial failures", "Code", countBy(len(header.PartialFailures), func(i int) string {
		return header.PartialFailures[i].Code
	}), 0)
	return out.String()
}

// countBy tallies one field read from each of n records.
func countBy(n int, value func(int) string) map[string]int {
	counts := make(map[string]int)
	for i := 0; i < n; i++ {
		if key := value(i); key != "" {
			counts[key]++
		}
	}
	return counts
}

// writeCountTable prints one count table, ordered by count descending then key
// ascending so ties cannot reorder between runs. limit caps the rows (0 = all)
// and any omitted remainder is stated rather than silently dropped. An empty map
// prints nothing, so a clean repository has no empty warning sections.
func writeCountTable(out *strings.Builder, title, keyHeader string, counts map[string]int, limit int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	fmt.Fprintf(out, "\n## %s\n\n| %s | Count |\n| --- | --- |\n", title, keyHeader)
	shown := keys
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, key := range shown {
		fmt.Fprintf(out, "| %s | %d |\n", key, counts[key])
	}
	if len(shown) < len(keys) {
		fmt.Fprintf(out, "\n%d more not shown.\n", len(keys)-len(shown))
	}
}
