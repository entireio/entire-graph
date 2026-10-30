package sem

// Effect scopes say how far the loss a diagnostic reports can reach. They are
// published as effect_scope on every warning and partial failure (schema 1.5),
// because a consumer deciding whether to trust an answer needs that bound and
// the only other place it lived was effect_on_semantic_completeness, which is
// prose.
const (
	// EffectScopeFile confines the loss to what the graph reads from the
	// record's file_path: symbols defined there, and relations that start or
	// end at them, may be missing or partial. Every other file was read in full.
	EffectScopeFile = "file"
	// EffectScopeSnapshot puts no file bound on the diagnostic: it describes
	// the run or the corpus as a whole (the corpus was narrowed, a budget ran
	// out, the snapshot came from the working tree, ...), so treat any fact in
	// the snapshot as possibly affected.
	EffectScopeSnapshot = "snapshot"
)

// fileScopedDiagnosticCodes are the codes whose effect is confined to the one
// file the record names. A record carrying one of them WITHOUT a file_path has
// nothing to confine it to and is snapshot-scoped (W_ANALYSIS_BUDGET_EXCEEDED
// is both: a diff file the budget skipped, and the dependents scan stopping).
//
// Every code the provider can emit is in exactly one of these two tables;
// TestEveryDiagnosticCodeHasAnEffectScope fails on a code that is in neither.
var fileScopedDiagnosticCodes = map[string]bool{
	"E_FILE_READ":                    true,
	"E_FILE_TOO_LARGE":               true,
	"E_GO_ROUTE_BUDGET_EXCEEDED":     true,
	"E_GO_SIGNATURE_BUDGET_EXCEEDED": true,
	"E_MINIFIED":                     true,
	"E_PARSE_DEPTH_EXCEEDED":         true,
	"E_PARSE_ERROR":                  true,
	"E_PARSE_TIMEOUT":                true,
	"E_UNSUPPORTED_LANGUAGE":         true,
	"W_ANALYSIS_BUDGET_EXCEEDED":     true,
	GoGrammarGapCode:                 true,
	"W_UNSUPPORTED_FILE":             true,
}

// snapshotScopedDiagnosticCodes describe the run rather than one file. Several
// name a path anyway, only as an example: the repo-ignore shortfalls and
// W_REPO_IGNORED_SOURCE name the first path of a tree, W_INDEX_POLICY_CHANGED
// names the policy file whose edit moved other files, and W_MOVE_AMBIGUOUS names
// one end of a move whose other end is in another file.
var snapshotScopedDiagnosticCodes = map[string]bool{
	AnalysisBudgetExceededCode:            true,
	"E_NO_GIT_HEAD":                       true,
	repoIgnoreGitUnavailableCode:          true,
	repoIgnoreIncompleteCode:              true,
	repoIgnoreTruncatedCode:               true,
	CochangeHistoryUnavailableCode:        true,
	"W_DATA_FLOW_EVIDENCE_UNMERGED":       true,
	"W_DEPENDENTS_PREFILTER_FAILED":       true,
	"W_FILE_LIMIT":                        true,
	"W_GIT_WORKTREE_FALLBACK":             true,
	"W_GITDIR_POINTER_READ_BUDGET":        true,
	"W_GITDIR_POINTER_UNREADABLE":         true,
	"W_GITDIR_SWEEP_BUDGET":               true,
	"W_GITDIR_SWEEP_CANCELLED":            true,
	"W_GITDIR_SWEEP_UNREADABLE_DIRECTORY": true,
	"W_INDEX_POLICY_CHANGED":              true,
	"W_MOVE_AMBIGUOUS":                    true,
	"W_NEWER_SCHEMA_MINOR":                true,
	repoIgnoreDisclosureCode:              true,
	"W_VALUE_REFERENCES_UNMODELLED":       true,
	"W_WALK_UNREADABLE_DIRECTORY":         true,
	"W_WALK_UNREADABLE_IGNORE_POLICY":     true,
	"W_WORKTREE_SNAPSHOT":                 true,
}

// DiagnosticEffectScope classifies a diagnostic by code and file path. A code
// nobody classified is snapshot-scoped: claiming a file bound that was never
// established is the one wrong answer here.
func DiagnosticEffectScope(code, filePath string) string {
	if filePath != "" && fileScopedDiagnosticCodes[code] {
		return EffectScopeFile
	}
	return EffectScopeSnapshot
}

// PartialFailureAffectsCode reports whether failure can have removed a fact
// about source code. Only a file-scoped failure in a data, configuration or
// documentation format cannot: such a file holds no code facts to lose. The
// format policy is sourceFileLanguage, the one health accounting already uses,
// so this answer and completeness.health agree on which files are source. A file
// whose language cannot be resolved is treated as source, since it cannot be
// ruled out.
func PartialFailureAffectsCode(failure PartialFailure) bool {
	if DiagnosticEffectScope(failure.Code, failure.FilePath) != EffectScopeFile {
		return true
	}
	language, source := sourceFileLanguage(failure.FilePath, failure.Language)
	return source || language == ""
}

// withWarningEffectScopes stamps effect_scope on every warning. It is applied
// where diagnostics leave the provider (the snapshot summary, a derived
// selective view, the search response, the diff result), so no construction
// site has to remember it. It never mutates its input: the slice may be shared,
// so a copy is made as soon as one record needs a change.
func withWarningEffectScopes(warnings []ProviderWarning) []ProviderWarning {
	var stamped []ProviderWarning
	for index, warning := range warnings {
		scope := DiagnosticEffectScope(warning.Code, warning.FilePath)
		if warning.EffectScope == scope {
			continue
		}
		if stamped == nil {
			stamped = append([]ProviderWarning(nil), warnings...)
		}
		stamped[index].EffectScope = scope
	}
	if stamped == nil {
		return warnings
	}
	return stamped
}

// withFailureEffectScopes is withWarningEffectScopes for partial failures.
func withFailureEffectScopes(failures []PartialFailure) []PartialFailure {
	var stamped []PartialFailure
	for index, failure := range failures {
		scope := DiagnosticEffectScope(failure.Code, failure.FilePath)
		if failure.EffectScope == scope {
			continue
		}
		if stamped == nil {
			stamped = append([]PartialFailure(nil), failures...)
		}
		stamped[index].EffectScope = scope
	}
	if stamped == nil {
		return failures
	}
	return stamped
}
