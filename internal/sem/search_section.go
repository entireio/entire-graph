package sem

import (
	"path/filepath"
	"strings"
)

// Result sections
// ===============
//
// `search` answers "where do I edit to fix this?", so its ranked list is read as a list of
// candidate FIX SITES. Two kinds of entry are legitimately in the payload without being that:
//
//   - Non-code hits: prose documentation, HTML templates, changelogs, serialized
//     configuration, recorded fixtures. Measured over 295 agentic SWE-bench instances, a
//     non-code file is rank 1 on 9.8% of them (29/295). The multiplicative file-class prior
//     already makes such a hit prove itself before it can win, but when it wins anyway an
//     agent reads it AS the fix site and spends its read window on a file no patch can land
//     in (measured: a `website/templates/.../*.html` hit ranked first over the handler that
//     actually had to change, and the agent's window never contained the gold line).
//
//     Non-code hits are still real evidence — an adapter fixture or a rule document is
//     sometimes exactly the file that has to change, and instances are measured where the
//     useful rank-1 hit IS non-code — so they are never suppressed, dropped or re-ranked.
//
//   - Related sites (searchSectionRelated): not ranked answers at all, but the other places
//     the top hit's change usually has to land. See search_related.go.
//
// Sectioning is therefore a LABEL, not a filter: every hit stays in `results`, keeps its rank
// and stays reachable. Renderers group by section, so the list an agent reads as "candidate
// fix sites" contains only entries that could be one.
//   - The covering test (searchSectionCoveringTest): the test that exercises the top hit. It is
//     the one entry in the payload whose file the ranker deliberately DEMOTES, and it is here
//     precisely because a test is not a fix site — it is the statement of what the fix has to
//     achieve. Labelling it away from the primary list is what makes it safe to include at all.
//     See search_covertest.go.
const (
	searchSectionPrimary      = ""
	searchSectionRelated      = "related"
	searchSectionDocs         = "docs-and-fixtures"
	searchSectionCoveringTest = "covering-test"
)

// SearchSectionRelated, SearchSectionDocs and SearchSectionCoveringTest are the section labels
// exported for renderers.
const (
	SearchSectionRelated      = searchSectionRelated
	SearchSectionDocs         = searchSectionDocs
	SearchSectionCoveringTest = searchSectionCoveringTest
)

// NonProgramTextPath reports whether a path holds no program text at all: prose
// documentation or serialized data/configuration.
//
// A name that appears in prose or in a serialized document is a MENTION, not a dependency:
// neither can execute, so neither breaks when the behavior it names changes. Relation
// consumers use this to keep name-only matches on such files out of call-chain reasoning.
//
// It asks the two questions directly rather than reusing classifySearchFile's
// mutually-exclusive class, because "generated" wins that classification and spans both
// kinds: `dist/bundle.js` and `internal/gen/client.go` ARE program text and can break,
// while `docs/generated/api.md` and `gen/schema.json` are not. Only lock files are admitted
// out of the generated set, since they are pure serialized data.
func NonProgramTextPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)
	dirs := searchPathSegments(lower)
	if len(dirs) > 0 {
		dirs = dirs[:len(dirs)-1]
	}
	if searchDocumentationClassPath(lower, base, dirs) {
		return true
	}
	if searchGeneratedBases[base] {
		return true
	}
	for _, ext := range searchDataExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// inventoryOnlyLanguages is the set of language NAMES that are parsed for inventory only,
// inverted once from the extension/filename tables so the lookup is O(1) per edge.
var inventoryOnlyLanguages = func() map[string]bool {
	names := make(map[string]bool, len(inventoryLanguageExtensions))
	for _, table := range []map[string]languageSpec{
		inventoryLanguageExtensions, inventoryLanguageFilenames,
	} {
		for _, spec := range table {
			if spec.inventoryOnly {
				names[spec.language] = true
			}
		}
	}
	return names
}()

// InventoryOnlyLanguage reports whether a language is parsed for inventory only, i.e. it
// contributes file records but no semantic relations. A name-only "call" attributed to such
// a file was matched lexically, never resolved.
func InventoryOnlyLanguage(language string) bool {
	return language != "" && inventoryOnlyLanguages[language]
}

// searchRelatedSignalPrefix carries WHY a related site is related in the result's signals, so
// the reason survives every serialization the payload already has instead of needing a field.
const searchRelatedSignalPrefix = "related:"

// RelatedSiteKind reports why a result is in the related-site section ("caller", "sibling",
// "near-dupe"), or "" when it is not a related site.
func RelatedSiteKind(result SearchResult) string {
	if result.Section != searchSectionRelated {
		return ""
	}
	for _, signal := range result.Signals {
		if kind, found := strings.CutPrefix(signal, searchRelatedSignalPrefix); found {
			return kind
		}
	}
	return ""
}

// searchDocsSectionClass reports whether a file class holds no program text: prose, serialized
// data/configuration, and machine-written artifacts. Vendored and example trees are
// deliberately absent — they ARE program text, and the ranker's prior already handles the fact
// that editing them is usually wrong.
func searchDocsSectionClass(class searchFileClass) bool {
	switch class {
	case searchFileClassDoc, searchFileClassData, searchFileClassGenerated:
		return true
	}
	return false
}

// searchDocsSectionPath reports whether a hit belongs in the docs-and-fixtures section.
//
// The decision defers to the file-class prior for intent: a query that ASKED for
// documentation (or configuration, or a generated artifact) gets that class at full ranking
// strength, and labelling it away would contradict the question the caller asked. That is why
// the gate is "the prior demoted this class" rather than a second, independently drifting
// list of intent words.
func searchDocsSectionPath(q searchQuery, filePath string) bool {
	if !searchDocsSectionClass(classifySearchFile(filePath)) {
		return false
	}
	return searchFileClassPrior(q, filePath) < 1
}

// assignSearchSections labels non-code results for the docs-and-fixtures section, in place.
//
// A payload whose hits are ALL non-code has no primary list to protect: those hits are the
// only answer there is, so they stay primary rather than being labelled away into an empty
// page of fix sites.
func assignSearchSections(results []SearchResult, q searchQuery) []SearchResult {
	docs := 0
	for index := range results {
		if results[index].Section != searchSectionPrimary {
			continue
		}
		if searchDocsSectionPath(q, results[index].FilePath) {
			results[index].Section = searchSectionDocs
			docs++
		}
	}
	if docs > 0 && docs == len(results) {
		for index := range results {
			results[index].Section = searchSectionPrimary
		}
	}
	return results
}
