package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// Completeness reporting is a valued feature and is not reduced here — every
// warning and every partial failure stays in the JSON payload, in full. What is
// fixed is the PRESENTATION: an answer about a Rust symbol used to open with
// "degraded ... 273 partial failures" and itemize Python files, which reads as
// "the graph you are about to trust is broken" and pushes a reader back to grep.
//
// A failure is only actionable for a query if it could have removed a fact the
// query would have used. The unit of that relevance the graph actually knows is
// LANGUAGE: relations do not cross language boundaries here, so a Python parse
// error cannot hide a Rust caller. Failures outside the query's language are
// therefore reported as one collapsed line, not itemized, and the headline says
// whether the query's own language is clean.

// maxScopedDiagnostics caps how many in-scope diagnostics are itemized. In-scope
// failures ARE actionable, so they keep the itemized treatment.
const maxScopedDiagnostics = 3

// completenessScope is the query-relative view of a snapshot's coverage.
type completenessScope struct {
	// Language is the language of the symbol the query is about. Empty means
	// the query resolved to nothing, and every diagnostic is then in scope.
	Language string `json:"language,omitempty"`
	// LanguageFiles / LanguageParsed count files of that language.
	LanguageFiles  int `json:"language_files,omitempty"`
	LanguageFailed int `json:"language_failed_files,omitempty"`
	// OtherFailures is the number of partial failures in other languages, with
	// the languages named for auditability.
	OtherFailures  int      `json:"other_language_failures,omitempty"`
	OtherLanguages []string `json:"other_failure_languages,omitempty"`
	// InScope are the diagnostics that can affect this query's answer.
	InScopeWarnings []sem.ProviderWarning `json:"in_scope_warnings,omitempty"`
	InScopeFailures []sem.PartialFailure  `json:"in_scope_failures,omitempty"`
	// Worktree records that the snapshot came from the working tree. That is
	// provenance, not lost coverage, so it no longer degrades the headline.
	Worktree bool `json:"worktree_snapshot,omitempty"`
	// Level is the snapshot's own completeness level, carried through unchanged.
	Level string `json:"level,omitempty"`
}

// worktreeProvenanceWarning is the code the provider emits to say a snapshot was
// read from the working tree rather than a commit. It reports WHICH tree was
// read, not that anything failed to parse, so it is presented as provenance.
const worktreeProvenanceWarning = "W_WORKTREE_SNAPSHOT"

// buildCompletenessScope partitions a snapshot's diagnostics by whether they can
// affect a query about queryLanguage.
func buildCompletenessScope(snapshot sem.ProviderSnapshot, queryLanguage string) completenessScope {
	scope := completenessScope{Language: queryLanguage, Level: snapshot.Header.Stats.CompletenessLevel}
	if language, ok := snapshot.Header.Completeness.Languages[queryLanguage]; ok {
		scope.LanguageFiles = language.Files
	}
	languageByPath := make(map[string]string, len(snapshot.Files))
	for _, file := range snapshot.Files {
		languageByPath[filepath.ToSlash(file.Path)] = file.Language
	}
	otherLanguages := map[string]int{}
	for _, failure := range snapshot.Header.PartialFailures {
		language := languageByPath[filepath.ToSlash(failure.FilePath)]
		if diagnosticInScope(queryLanguage, language, failure.FilePath) {
			scope.LanguageFailed++
			if len(scope.InScopeFailures) < maxScopedDiagnostics {
				scope.InScopeFailures = append(scope.InScopeFailures, failure)
			}
			continue
		}
		scope.OtherFailures++
		if language == "" {
			language = "unknown"
		}
		otherLanguages[language]++
	}
	for _, warning := range snapshot.Header.Warnings {
		if warning.Code == worktreeProvenanceWarning {
			scope.Worktree = true
			continue
		}
		language := languageByPath[filepath.ToSlash(warning.FilePath)]
		if !diagnosticInScope(queryLanguage, language, warning.FilePath) {
			scope.OtherFailures++
			if language == "" {
				language = "unknown"
			}
			otherLanguages[language]++
			continue
		}
		if len(scope.InScopeWarnings) < maxScopedDiagnostics {
			scope.InScopeWarnings = append(scope.InScopeWarnings, warning)
		}
	}
	scope.OtherLanguages = rankedLanguageNames(otherLanguages)
	return scope
}

// diagnosticInScope decides whether a diagnostic can affect an answer about
// queryLanguage. With no query language, or a diagnostic with no file (a
// repository-wide condition), everything is in scope — the safe direction.
func diagnosticInScope(queryLanguage, fileLanguage, filePath string) bool {
	if queryLanguage == "" || filePath == "" {
		return true
	}
	if fileLanguage == "" {
		// The file is not in the snapshot's file records: its language is
		// unknown, so it cannot be ruled out.
		return true
	}
	return strings.EqualFold(fileLanguage, queryLanguage)
}

// rankedLanguageNames renders "Python 271, JSON 2" style breakdowns, most
// failures first, capped so the collapsed line stays one line.
func rankedLanguageNames(counts map[string]int) []string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool {
		if counts[names[left]] != counts[names[right]] {
			return counts[names[left]] > counts[names[right]]
		}
		return names[left] < names[right]
	})
	if len(names) > 3 {
		names = names[:3]
	}
	rendered := make([]string, 0, len(names))
	for _, name := range names {
		rendered = append(rendered, fmt.Sprintf("%s %d", name, counts[name]))
	}
	return rendered
}

// writeScopedCompletenessBlock prints the coverage banner for a relation answer.
//
// Silent when there is nothing to say. One line when nothing is actionable for
// this query. Itemized only for diagnostics that could actually have removed a
// fact the query needed.
func writeScopedCompletenessBlock(
	out io.Writer,
	scope completenessScope,
	warnings []sem.ProviderWarning,
	partialFailures []sem.PartialFailure,
	stats sem.ProviderStats,
) {
	actionable := len(scope.InScopeWarnings) > 0 || scope.LanguageFailed > 0
	if !actionable && scope.OtherFailures == 0 &&
		(stats.CompletenessLevel == "" || stats.CompletenessLevel == "ok") {
		return
	}
	if !actionable {
		// Nothing in this query's scope failed. Say exactly that, in one line,
		// and keep the honest total plus a pointer to the full list.
		if scope.OtherFailures == 0 {
			return
		}
		fmt.Fprintf(out, "Completeness: no parse failures in %s (%s); %d elsewhere (%s) cannot affect this answer — see --format json.\n",
			scopeLanguageLabel(scope), scopeLanguageFileCount(scope),
			scope.OtherFailures, strings.Join(scope.OtherLanguages, ", "),
		)
		return
	}
	level := stats.CompletenessLevel
	if level == "" {
		level = "degraded"
	}
	// A FRACTION only when the denominator is real. `LanguageFiles` counts files that parsed, so a
	// language whose files all failed reports 0 — and "35 of 0 files failed to parse" (three.js),
	// "6 of 0" (terraform) is not a small error, it is a number that cannot be true and it discredits
	// every other count on the line. When the denominator is missing or smaller than the numerator, the
	// honest form is the count alone.
	total := len(warnings) + len(partialFailures)
	if scope.LanguageFiles <= 0 || scope.LanguageFailed > scope.LanguageFiles {
		fmt.Fprintf(out, "Completeness: %s for %s (%d %s file%s failed to parse; %d total diagnostic%s in this snapshot)\n",
			level, scopeLanguageLabel(scope),
			scope.LanguageFailed, scopeLanguageLabel(scope), pluralSuffix(scope.LanguageFailed),
			total, pluralSuffix(total),
		)
	} else {
		fmt.Fprintf(out, "Completeness: %s for %s (%d of %d %s file%s failed to parse; %d total diagnostic%s in this snapshot)\n",
			level, scopeLanguageLabel(scope),
			scope.LanguageFailed, scope.LanguageFiles, scopeLanguageLabel(scope), pluralSuffix(scope.LanguageFiles),
			total, pluralSuffix(total),
		)
	}
	for _, warning := range scope.InScopeWarnings {
		if warning.FilePath == "" {
			fmt.Fprintf(out, "- warning %s\n", warning.Code)
		} else {
			fmt.Fprintf(out, "- warning %s: %s\n", warning.Code, termsafe.Line(warning.FilePath))
		}
	}
	for _, failure := range scope.InScopeFailures {
		if failure.FilePath == "" {
			fmt.Fprintf(out, "- partial %s\n", failure.Code)
		} else {
			fmt.Fprintf(out, "- partial %s: %s\n", failure.Code, termsafe.Line(failure.FilePath))
		}
	}
	if omitted := scope.LanguageFailed - len(scope.InScopeFailures); omitted > 0 {
		fmt.Fprintf(out, "- ... %d more in %s; see --format json\n", omitted, scopeLanguageLabel(scope))
	}
	if scope.OtherFailures > 0 {
		fmt.Fprintf(out, "- plus %d diagnostic%s in other languages (%s), which cannot affect this answer\n",
			scope.OtherFailures, pluralSuffix(scope.OtherFailures), strings.Join(scope.OtherLanguages, ", "))
	}
}

func scopeLanguageLabel(scope completenessScope) string {
	if scope.Language == "" {
		return "this query"
	}
	return scope.Language
}

func scopeLanguageFileCount(scope completenessScope) string {
	if scope.LanguageFiles == 0 {
		return "the query's language"
	}
	return fmt.Sprintf("%d file%s parsed", scope.LanguageFiles, pluralSuffix(scope.LanguageFiles))
}

// compactCompletenessLine is the byte-capped form used by `--format agent`. It
// carries the same scoped verdict as the text banner in a fraction of the bytes:
// `<failed>/<files>` for the query's own scope, and one count for everything
// that cannot affect it.
func compactCompletenessLine(scope completenessScope, stats sem.ProviderStats) string {
	label := ""
	if scope.Language != "" {
		label = scope.Language + " "
	}
	if scope.LanguageFailed == 0 && len(scope.InScopeWarnings) == 0 {
		if scope.OtherFailures == 0 {
			return ""
		}
		return fmt.Sprintf("C:%sclean F%d-other\n", label, scope.OtherFailures)
	}
	level := stats.CompletenessLevel
	if level == "" {
		level = "degraded"
	}
	return fmt.Sprintf("C:%s %s%d/%d W%d F%d-other\n",
		level, label, scope.LanguageFailed, scope.LanguageFiles,
		len(scope.InScopeWarnings), scope.OtherFailures)
}

// writeIndexCostNotice states, on a cold run with nowhere to cache, that the
// cost just paid will be paid again.
//
// The relation verbs index the whole repository; `search` parses only its
// candidate files. That asymmetry is invisible from the CLI surface, so a reader
// cannot budget for it — and the natural reaction to an unexplained 26-second
// call is to batch queries into one command, which costs more, not less. One
// line, only when it is actionable.
func writeIndexCostNotice(out io.Writer, cacheHit, cacheDisabled bool) {
	if cacheHit || !cacheDisabled {
		return
	}
	fmt.Fprintln(out, "Index not cached (no --cache-dir, ENTIRE_PLUGIN_DATA_DIR unset): relation queries index the whole repository, and the next one will pay this cost again. Pass --cache-dir to reuse it.")
}

// completenessScopeOrAll returns a usable scope for a response that carries
// diagnostics but no scope of its own. Scoping decides what is PRESENTED; it
// must never be able to drop coverage reporting because a field was left unset,
// so the fallback treats every diagnostic as in scope — the safe direction.
func completenessScopeOrAll(
	scope completenessScope,
	warnings []sem.ProviderWarning,
	partialFailures []sem.PartialFailure,
	stats sem.ProviderStats,
) completenessScope {
	if scope.Language != "" || scope.LanguageFailed > 0 || scope.OtherFailures > 0 ||
		len(scope.InScopeWarnings) > 0 || len(scope.InScopeFailures) > 0 {
		return scope
	}
	if len(warnings) == 0 && len(partialFailures) == 0 {
		return scope
	}
	rebuilt := completenessScope{LanguageFiles: stats.Files, Level: stats.CompletenessLevel}
	for _, warning := range warnings {
		if warning.Code == worktreeProvenanceWarning {
			rebuilt.Worktree = true
			continue
		}
		if len(rebuilt.InScopeWarnings) < maxScopedDiagnostics {
			rebuilt.InScopeWarnings = append(rebuilt.InScopeWarnings, warning)
		}
	}
	rebuilt.LanguageFailed = len(partialFailures)
	for _, failure := range partialFailures {
		if len(rebuilt.InScopeFailures) < maxScopedDiagnostics {
			rebuilt.InScopeFailures = append(rebuilt.InScopeFailures, failure)
		}
	}
	return rebuilt
}
