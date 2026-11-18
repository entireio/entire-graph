package cli

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

// UNPARSED FILES THAT MENTION THE SYMBOL
// ======================================
//
// Callers come from parsed files. A Svelte component, an Astro page or an HTML
// file with an inline script is inventory-only: it has a file record and no
// relations, so a JavaScript or TypeScript function called only from such files
// shows "Callers: none" and an impact of zero, stated as confidently as a real
// zero. Vue components were in this position until their scripts were parsed;
// the formats below still are.
//
// The disclosure names the inventory-only files that mention the symbol as a
// whole word, so the reader knows where the graph cannot see. Vue templates are
// included: a component's script is parsed, but calls written in its template
// (`{{ formatDate(due) }}`, `@click="save"`) are not modelled yet. It is a
// bounded text scan, not a resolution: a mention may be a comment or another
// symbol of the same name, which is why it is disclosed rather than counted as
// a caller.

// inventoryMentionDisclosureCode is the warning code. Like the value-reference
// disclosure it is query-scoped, so the completeness banners do not count it.
const inventoryMentionDisclosureCode = "W_INVENTORY_MENTIONS_UNMODELLED"

// inventoryMentionLanguages are inventory-only formats whose files hold code or
// templates that can call into JavaScript and TypeScript.
var inventoryMentionLanguages = map[string]bool{
	"Astro": true, "CoffeeScript": true, "Elm": true, "HTML": true, "Handlebars": true,
	"LiveScript": true, "Mint": true, "Pug": true, "Svelte": true,
}

// inventoryMentionFocusLanguages are the focus languages such files can call.
var inventoryMentionFocusLanguages = map[string]bool{"JavaScript": true, "TypeScript": true, "Vue": true}

// The scan bounds; variables so tests can reach the truncation path.
var (
	inventoryMentionMaxFiles = 2000
	inventoryMentionMaxLines = 400000
	inventoryMentionMaxBytes = 32 << 20
)

const inventoryMentionShown = 5

var jsIdentifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// inventoryMentionDisclosure returns the warning for focus, and false when no
// file that could call it outside the graph mentions it.
func inventoryMentionDisclosure(snapshot sem.ProviderSnapshot, focus neighborEndpoint, read lineReader) (sem.ProviderWarning, bool) {
	if read == nil || focus.External || !inventoryMentionFocusLanguages[focus.Language] || !jsIdentifierPattern.MatchString(focus.Name) {
		return sem.ProviderWarning{}, false
	}
	labelOf := map[string]string{}
	var paths []string
	for _, file := range snapshot.Files {
		switch {
		case file.Language == "Vue":
			labelOf[file.Path] = "Vue template"
		case inventoryMentionLanguages[file.Language] && snapshot.Header.LanguageTiers[file.Language] == "inventory-only":
			labelOf[file.Path] = file.Language
		default:
			continue
		}
		paths = append(paths, file.Path)
	}
	if len(paths) == 0 {
		return sem.ProviderWarning{}, false
	}
	sort.Strings(paths)
	mention := regexp.MustCompile(`(^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(focus.Name) + `([^A-Za-z0-9_$]|$)`)
	var hits []string
	labels := map[string]bool{}
	scannedFiles, scannedLines, scannedBytes, truncated := 0, 0, 0, false
	for _, path := range paths {
		if scannedFiles >= inventoryMentionMaxFiles || scannedLines >= inventoryMentionMaxLines || scannedBytes >= inventoryMentionMaxBytes {
			truncated = true
			break
		}
		lines, ok := read(path)
		if !ok {
			continue
		}
		scannedFiles++
		from, to := 0, len(lines)
		if labelOf[path] == "Vue template" {
			if from, to, ok = vueTemplateLineRange(lines); !ok {
				continue
			}
		}
		for _, line := range lines[from:to] {
			scannedLines++
			scannedBytes += len(line) + 1
			if mention.MatchString(line) {
				hits = append(hits, path)
				labels[labelOf[path]] = true
				break
			}
			if scannedBytes >= inventoryMentionMaxBytes {
				break
			}
		}
	}
	if len(hits) == 0 {
		return sem.ProviderWarning{}, false
	}
	names := make([]string, 0, len(labels))
	for label := range labels {
		names = append(names, label)
	}
	sort.Strings(names)
	shown := hits
	if len(shown) > inventoryMentionShown {
		shown = shown[:inventoryMentionShown]
	}
	detail := "files: " + strings.Join(shown, ", ")
	if more := len(hits) - len(shown); more > 0 {
		detail += fmt.Sprintf(" (+%d more)", more)
	}
	if truncated {
		detail += fmt.Sprintf("; scan stopped after %d files, so more may exist", scannedFiles)
	}
	verb := "mention"
	if len(hits) == 1 {
		verb = "mentions"
	}
	return sem.ProviderWarning{
		Code:        inventoryMentionDisclosureCode,
		Severity:    "info",
		EffectScope: sem.DiagnosticEffectScope(inventoryMentionDisclosureCode, ""),
		EffectOnCompleteness: fmt.Sprintf("%d file%s (%s) %s %s, but calls written there are not in the graph, so they are missing from its callers and impact",
			len(hits), pluralSuffix(len(hits)), strings.Join(names, ", "), verb, focus.Name),
		Detail: detail,
	}, true
}

// vueTemplateLineRange returns the lines of a component's top-level template,
// from its opening `<template` line to its closing `</template>` line, both
// written at the start of a line as top-level blocks are.
func vueTemplateLineRange(lines []string) (int, int, bool) {
	start := -1
	for index, line := range lines {
		if start < 0 && strings.HasPrefix(line, "<template") {
			start = index
			continue
		}
		if start >= 0 && strings.HasPrefix(strings.TrimRight(line, " \t\r"), "</template>") {
			return start, index + 1, true
		}
	}
	return 0, 0, false
}

// withInventoryMentionDisclosure returns warnings plus the disclosure for focus
// when it applies. The input is never mutated: it is the snapshot's own slice.
func withInventoryMentionDisclosure(warnings []sem.ProviderWarning, snapshot sem.ProviderSnapshot, focus neighborEndpoint, read lineReader) []sem.ProviderWarning {
	disclosure, ok := inventoryMentionDisclosure(snapshot, focus, read)
	if !ok {
		return warnings
	}
	return append(append(make([]sem.ProviderWarning, 0, len(warnings)+1), warnings...), disclosure)
}

// inventoryMentionNote is the one-line agent and text form, rendered from the
// warning so text and JSON cannot disagree, or "" when there is none.
func inventoryMentionNote(warnings []sem.ProviderWarning) string {
	for _, warning := range warnings {
		if warning.Code == inventoryMentionDisclosureCode {
			return "Unmodelled mentions (calls there are not in the graph): " + warning.Detail
		}
	}
	return ""
}
