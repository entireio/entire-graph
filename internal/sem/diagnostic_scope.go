package sem

// Effect scopes say how far the loss a partial failure reports can reach.
//
// A consumer deciding whether to trust an answer needs that bound, and the only
// other place it lives is effect_on_semantic_completeness, which is prose.
const (
	// EffectScopeFile confines the loss to what the graph reads from the
	// record's file_path: symbols defined there, and relations that start or
	// end at them, may be missing or partial. Every other file was read in full.
	EffectScopeFile = "file"
	// EffectScopeSnapshot puts no file bound on the loss: any fact in the
	// snapshot may be affected (the corpus was narrowed, a budget ran out, ...).
	EffectScopeSnapshot = "snapshot"
)

// fileScopedFailureCodes are the partial-failure codes whose loss is confined to
// the one file the record names. A record carrying one of them WITHOUT a
// file_path has nothing to confine it to, so it is snapshot-scoped.
var fileScopedFailureCodes = map[string]bool{
	"E_FILE_READ":                    true,
	"E_FILE_TOO_LARGE":               true,
	"E_GO_ROUTE_BUDGET_EXCEEDED":     true,
	"E_GO_SIGNATURE_BUDGET_EXCEEDED": true,
	"E_MINIFIED":                     true,
	"E_PARSE_DEPTH_EXCEEDED":         true,
	"E_PARSE_ERROR":                  true,
	"E_PARSE_TIMEOUT":                true,
	"E_UNSUPPORTED_LANGUAGE":         true,
}

// snapshotScopedFailureCodes are the partial-failure codes that describe the run
// rather than one file. The repo-ignore shortfalls name a path, but only as the
// first example of a tree whose size they could not count.
var snapshotScopedFailureCodes = map[string]bool{
	AnalysisBudgetExceededCode:   true,
	repoIgnoreGitUnavailableCode: true,
	repoIgnoreIncompleteCode:     true,
	repoIgnoreTruncatedCode:      true,
}

// DiagnosticEffectScope classifies a diagnostic by code and file path. A code
// nobody classified is snapshot-scoped: claiming a file bound that was never
// established is the one wrong answer here.
func DiagnosticEffectScope(code, filePath string) string {
	if filePath != "" && fileScopedFailureCodes[code] {
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
