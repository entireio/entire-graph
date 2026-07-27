package sem

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
)

var identifierBoundary = regexp.MustCompile(`[A-Za-z0-9_$]+`)

type referenceIndex map[string]map[string]struct{}

// dependentsScanOptions bounds and instruments the reference scan. The zero
// value keeps the historical behavior: no progress reporting, no deadline.
type dependentsScanOptions struct {
	// progress reports candidate files scanned; path is the file about to be
	// scanned ("" on the initial and final events).
	progress func(done, total int, path string)
	// deadline stops the scan cleanly when wall-clock time runs out. Zero
	// means no deadline. A stopped scan appends one machine-readable
	// W_ANALYSIS_BUDGET_EXCEEDED warning; already-counted dependents keep
	// their values, uncounted ones stay at their current (possibly zero)
	// count.
	deadline time.Time
	budget   time.Duration
}

func addDependentCounts(ctx context.Context, repo, head string, result *Result) error {
	return addDependentCountsWithProgress(ctx, repo, head, result, dependentsScanOptions{})
}

func addDependentCountsWithProgress(ctx context.Context, repo, head string, result *Result, options dependentsScanOptions) error {
	names := changedReferenceNames(*result)
	if len(names) == 0 {
		if options.progress != nil {
			options.progress(0, 0, "")
		}
		return nil
	}

	index, warnings, err := buildReferenceIndexWithProgress(ctx, repo, head, names, options)
	if err != nil {
		return err
	}
	result.Warnings = append(result.Warnings, warnings...)

	for fileIndex := range result.Files {
		for changeIndex := range result.Files[fileIndex].Changes {
			change := &result.Files[fileIndex].Changes[changeIndex]
			name := referenceName(*change)
			change.DependentsCount = len(index[name])
		}
	}
	return nil
}

func changedReferenceNames(result Result) map[string]struct{} {
	out := map[string]struct{}{}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			name := referenceName(change)
			if name != "" {
				out[name] = struct{}{}
			}
		}
	}
	return out
}

func buildReferenceIndex(ctx context.Context, repo, head string, names map[string]struct{}) (referenceIndex, []ProviderWarning, error) {
	return buildReferenceIndexWithProgress(ctx, repo, head, names, dependentsScanOptions{})
}

func buildReferenceIndexWithProgress(ctx context.Context, repo, head string, names map[string]struct{}, options dependentsScanOptions) (referenceIndex, []ProviderWarning, error) {
	index := referenceIndex{}
	for name := range names {
		index[name] = map[string]struct{}{}
	}
	if len(names) == 0 {
		return index, nil, nil
	}

	overBudget := func() bool {
		return !options.deadline.IsZero() && time.Now().After(options.deadline)
	}
	if overBudget() {
		return index, []ProviderWarning{dependentsBudgetWarning(0, -1, options.budget)}, nil
	}

	files, prefiltered, warnings, err := referenceCandidateFiles(ctx, repo, head, names)
	if err != nil {
		return nil, nil, err
	}
	if options.progress != nil {
		options.progress(0, len(files), "")
	}

	// One persistent `git cat-file --batch` process replaces a `git show`
	// subprocess per candidate file; on large repos the per-file spawn cost
	// alone was tens of seconds. Falls back to per-file ShowFile only when the
	// batch process cannot start at all.
	readFile := func(path string) (string, bool, error) {
		return gitutil.ShowFile(ctx, repo, head, path)
	}
	// oversizeBytes reports a file the reader declined because it exceeds the
	// parse limit, so the scan can warn about it (below) without the read that
	// would have cost the file's size twice for a file it refuses to parse anyway.
	oversizeBytes := func(string) (int64, bool) { return 0, false }
	if batch, batchErr := gitutil.NewBatchFileReader(ctx, repo, head); batchErr == nil {
		defer func() { _ = batch.Close() }()
		batch.SetMaxBytes(defaultMaxParseBytes)
		readFile = batch.ReadFile
		oversizeBytes = func(path string) (int64, bool) {
			blob, ok := batch.OversizeBlob(path)
			return blob.Bytes, ok
		}
	}

	// When the grep prefilter ran, every file below already matched a changed
	// name, so every skip is worth warning about. On the fallback full-tree
	// scan, apply the same candidate test in-process before warning, so the
	// fallback does not spray warnings about files (e.g. huge vendored blobs)
	// that contain no changed name and were never real candidates.
	isCandidate := func(content string) bool {
		return prefiltered || containsAnyName(content, names)
	}

	parser := TreeSitterParser{}
	for i, path := range files {
		if overBudget() {
			warnings = append(warnings, dependentsBudgetWarning(i, len(files), options.budget))
			break
		}
		if i > 0 && i%100 == 0 && options.progress != nil {
			options.progress(i, len(files), path)
		}
		if !Supported(path) {
			continue
		}
		content, ok, err := readFile(path)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			if size, oversize := oversizeBytes(path); oversize {
				warnings = append(warnings, dependentsFileTooLargeWarning(path, int(size)))
			}
			continue
		}
		// Size parity with the provider's default MaxParseBytes eligibility:
		// never count dependents inside a file the graph itself refuses to
		// parse for size. Parity is size-only -- the provider additionally
		// skips minified files (E_MINIFIED) and non-default-build Go files,
		// which this scan still parses, exactly as it did before the
		// prefilter existed. The analyze path has no option plumbing today,
		// so this is always the provider's DEFAULT limit, never a
		// caller-supplied override.
		if len(content) > defaultMaxParseBytes {
			if isCandidate(content) {
				warnings = append(warnings, dependentsFileTooLargeWarning(path, len(content)))
			}
			continue
		}

		// Pre-parse screen: tokenize the raw content once and keep only the
		// changed names that occur as exact identifier tokens. The git-grep
		// prefilter matches substrings, a superset of this token check, so a
		// file that only matched as a substring of a longer identifier drops
		// out here — and, crucially, a file with no exact-token occurrence
		// skips the (far more expensive) tree-sitter parse entirely.
		relevant := map[string]struct{}{}
		for _, span := range identifierBoundary.FindAllStringIndex(content, -1) {
			token := content[span[0]:span[1]]
			if _, isName := names[token]; isName {
				relevant[token] = struct{}{}
			}
		}
		if len(relevant) == 0 {
			continue
		}

		entities, _, status := parser.ParseWithStatus(path, content)
		if status.ParseError && isCandidate(content) {
			warnings = append(warnings, dependentsParseFailureWarning(path, status))
		}
		lines := strings.Split(content, "\n")
		for _, entity := range entities {
			block := entityBlock(lines, entity)
			if block == "" {
				continue
			}
			// One tokenization pass per entity block replaces one full regex
			// scan per (block, name) pair; with N changed names that was N
			// scans of every block in the repo.
			self := shortEntityName(entity.Name)
			entityKey := path + "#" + entity.Kind + ":" + entity.Name
			for _, span := range identifierBoundary.FindAllStringIndex(block, -1) {
				token := block[span[0]:span[1]]
				if token == self {
					continue
				}
				if _, isRelevant := relevant[token]; isRelevant {
					index[token][entityKey] = struct{}{}
				}
			}
		}
	}
	if options.progress != nil {
		options.progress(len(files), len(files), "")
	}

	return index, warnings, nil
}

// dependentsBudgetWarning is surfaced once when the reference scan stops early
// because the caller's wall-clock budget ran out. Dependent counts already
// resolved are kept; the rest may be undercounted. total < 0 means the budget
// was exhausted before candidate files were even enumerated.
func dependentsBudgetWarning(done, total int, budget time.Duration) ProviderWarning {
	detail := fmt.Sprintf("scanned %d of %d candidate files before the analysis time budget ran out", done, total)
	if total < 0 {
		detail = "analysis time budget ran out before the dependents scan started"
	}
	if budget > 0 {
		detail += fmt.Sprintf(" (budget %s)", budget)
	}
	return ProviderWarning{
		Code:                 "W_ANALYSIS_BUDGET_EXCEEDED",
		Severity:             "warning",
		EffectOnCompleteness: "dependents scan stopped early; dependents_count values may be undercounted",
		Detail:               detail,
	}
}

// dependentsFileTooLargeWarning mirrors the provider's E_FILE_TOO_LARGE
// partial failure (provider.go's MaxParseBytes handling), reusing its code
// and severity, but the effect wording is dependents-specific: the file is
// skipped as a candidate entirely, so a real reference to a changed name
// inside it goes uncounted rather than merely losing symbol parsing.
func dependentsFileTooLargeWarning(path string, size int) ProviderWarning {
	return ProviderWarning{
		Code:                 "E_FILE_TOO_LARGE",
		Severity:             "warning",
		FilePath:             path,
		EffectOnCompleteness: "dependent references in this file were not counted because it exceeds max parser input",
		Detail:               fmt.Sprintf("file is %d bytes, above max parser input %d bytes", size, defaultMaxParseBytes),
	}
}

// dependentsParseFailureWarning mirrors the provider's parse-failure partial
// failure (provider.go's ParseStatus.ParseError handling, which emits
// E_PARSE_ERROR or E_PARSE_TIMEOUT depending on ParseStatus.Code), reusing
// its code, severity, and detail so the wording lines up across both paths.
// The effect wording is dependents-specific: entities the parser still
// recovers keep counting exactly as before -- this warning is purely
// additive observability, not a change to which entities get counted.
func dependentsParseFailureWarning(path string, status ParseStatus) ProviderWarning {
	code := status.Code
	if code == "" {
		code = "E_PARSE_ERROR"
	}
	return ProviderWarning{
		Code:                 code,
		Severity:             "warning",
		FilePath:             path,
		EffectOnCompleteness: "dependent references in this file may be undercounted because it failed to parse cleanly",
		Detail:               status.Detail,
	}
}

// grepFallbackWarning is surfaced once when the git-grep prefilter itself
// fails and referenceCandidateFiles falls back to scanning every file in the
// tree. The fallback keeps dependent counts correct, but it is far slower
// than the prefiltered path, so callers should know it happened.
func grepFallbackWarning(err error) ProviderWarning {
	return ProviderWarning{
		Code:                 "W_DEPENDENTS_PREFILTER_FAILED",
		Severity:             "warning",
		EffectOnCompleteness: "dependents git-grep prefilter failed; fell back to scanning every file in the tree",
		Detail:               err.Error(),
	}
}

// referenceCandidateFiles narrows the head tree to files worth parsing, using
// git grep's fixed-string, case-sensitive substring search as a preselection
// pass. That test is a strict superset of the exact-token check applied per
// entity below -- a whole-token occurrence is always also a case-sensitive
// substring occurrence -- so it can only add extra candidate files, never
// drop a real dependent. (Case-sensitive rather than the previous -i: the
// token check is case-sensitive anyway, so folding case only inflated the
// candidate set. NOT -w: word-boundary mode leaves git grep's multi-pattern
// fixed-string fast path and is orders of magnitude slower with hundreds of
// patterns; substring false positives are cheap because the token pre-screen
// above skips the parse for them.) It uses the IncludingBinary variant
// specifically to preserve that superset guarantee for files Git itself
// classifies as binary (an embedded NUL byte, or a `.gitattributes`
// binary/-diff marking): a Supported source file flagged binary is still real
// source that gets parsed below, so the prefilter must not silently drop it.
// If the grep call itself fails for any reason, fall back to scanning every
// file in the tree so a git-grep quirk never silently zeroes out dependent
// counts, and surface exactly one warning noting the prefilter failure so the
// fallback (much slower) scan is not silent.
//
// The prefiltered return reports whether the grep preselection actually ran:
// true means every returned file already matched a changed name; false means
// the list is the whole tree and callers must apply their own candidate test
// before treating a file as relevant to the changed names.
func referenceCandidateFiles(ctx context.Context, repo, head string, names map[string]struct{}) (files []string, prefiltered bool, warnings []ProviderWarning, err error) {
	patterns := make([]string, 0, len(names))
	for name := range names {
		if name != "" {
			patterns = append(patterns, name)
		}
	}
	if len(patterns) > 0 {
		matches, grepErr := gitutil.GrepTreePathsCaseSensitiveIncludingBinary(ctx, repo, head, patterns)
		if grepErr == nil {
			return matches, true, nil, nil
		}
		files, err = gitutil.ListFiles(ctx, repo, head)
		if err != nil {
			return nil, false, nil, err
		}
		return files, false, []ProviderWarning{grepFallbackWarning(grepErr)}, nil
	}
	files, err = gitutil.ListFiles(ctx, repo, head)
	return files, false, nil, err
}

// containsAnyName mirrors the git-grep prefilter's case-sensitive fixed-string
// substring test in-process, so the fallback full-tree scan warns about
// exactly the files the prefiltered path would have surfaced as candidates.
// This only gates warnings; dependent counting still uses exact identifier
// tokens below.
func containsAnyName(content string, names map[string]struct{}) bool {
	for name := range names {
		if name == "" {
			continue
		}
		if strings.Contains(content, name) {
			return true
		}
	}
	return false
}

func entityBlock(lines []string, entity Entity) string {
	start := entity.StartLine - 1
	if start < 0 {
		start = 0
	}
	end := entity.EndLine
	if end > len(lines) {
		end = len(lines)
	}
	if end <= start {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

func identifiersIn(content string) map[string]struct{} {
	identifiers := map[string]struct{}{}
	for _, token := range identifierBoundary.FindAllString(content, -1) {
		identifiers[token] = struct{}{}
	}
	return identifiers
}

func referenceName(change EntityChange) string {
	// Module-scope entities are keyed by file path, not by a callable name, so
	// they have no dependents to resolve.
	if change.Kind == moduleKind {
		return ""
	}
	switch change.Type {
	case "renamed":
		if change.NewName != "" {
			return shortEntityName(change.NewName)
		}
		if change.OldName != "" {
			return shortEntityName(change.OldName)
		}
	}
	return shortEntityName(change.Name)
}

func shortEntityName(name string) string {
	if index := strings.LastIndex(name, "."); index >= 0 {
		return name[index+1:]
	}
	return name
}
