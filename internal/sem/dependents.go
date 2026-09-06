package sem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
)

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
	// admit reports whether a candidate file is part of the graph at head, so a
	// dependent count means "references the graph holds" rather than "textual
	// references anywhere in the tree". nil admits every candidate, which is
	// what every caller that has no index policy to offer wants.
	admit func(rel string) bool
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
		setDependentsEvidence(result, nil)
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
	setDependentsEvidence(result, warnings)
	return nil
}

// setDependentsEvidence marks every change in result with how much its DependentsCount can
// be trusted. It runs unconditionally (including the "nothing to scan" early return above),
// so DependentsEvidence is always one of the three defined states rather than a Go zero value
// a JSON consumer would have no vocabulary for.
func setDependentsEvidence(result *Result, warnings []ProviderWarning) {
	state := dependentsEvidenceState(warnings)
	for fileIndex := range result.Files {
		for changeIndex := range result.Files[fileIndex].Changes {
			result.Files[fileIndex].Changes[changeIndex].DependentsEvidence = state
		}
	}
}

// dependentsEvidenceState reports how much the dependents-count scan behind THIS result can
// be trusted. The scan is an exact-token text match (see forEachIdentifierToken below), which
// is why it defaults to Partial even when nothing went wrong: it can miss a caller reached only
// through an alias, reflection, or a generated binding whose text never spells the changed
// name, and it is never cross-checked against the resolved call graph the way a `neighbors` or
// `impact` edge is. It escalates to RequiresVerification when the scan itself additionally hit
// a real limit — a time budget, an unreadable or too-large candidate file, or a parse failure —
// meaning some candidate files were never counted at all, which is graver than the scan's
// ordinary token-matching imprecision.
//
// W_DEPENDENTS_PREFILTER_FAILED is deliberately NOT one of the escalating codes: it means the
// git-grep prefilter failed and the scan fell back to walking every file in the tree, which is a
// strict superset of the prefiltered candidate set, not a narrower one.
func dependentsEvidenceState(warnings []ProviderWarning) EvidenceState {
	for _, warning := range warnings {
		switch warning.Code {
		case "W_ANALYSIS_BUDGET_EXCEEDED", "E_FILE_TOO_LARGE", "E_FILE_READ",
			"E_PARSE_ERROR", "E_PARSE_TIMEOUT", "E_PARSE_DEPTH_EXCEEDED":
			return EvidenceRequiresVerification
		}
	}
	return EvidencePartial
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

// candidateScan is one candidate file's share of the reference scan: the
// index entries it contributes and the warnings it raises, held until the
// reducer folds them in candidate order.
//
// stopped records that the worker ran out of budget. The ordered reducer also
// checks the deadline before accepting buffered hits, so earlier work cannot
// let prefetched candidates bypass the budget.
type candidateScan struct {
	hits     []referenceHit
	warnings []ProviderWarning
	stopped  bool
	err      error
}

// referenceHit is one changed name found as an exact identifier token inside
// one entity. index is a set of sets, so these fold in any order; they are
// still applied in candidate order to keep the warnings beside them ordered.
type referenceHit struct {
	token     string
	entityKey string
}

// errDependentsScanStopped unwinds the pipeline when the budget runs out. It
// never reaches a caller: the scan reports a stop with a warning and its
// partial index, exactly as the sequential scan did.
var errDependentsScanStopped = errors.New("dependents scan stopped")

// dependentsScanWorkers sizes the candidate scan. It reads and parses files the
// same way the snapshot's parse phase does, so it takes the same bound.
func dependentsScanWorkers() int {
	return defaultProviderWorkerCount()
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
		return !options.deadline.IsZero() && !time.Now().Before(options.deadline)
	}
	if overBudget() {
		return index, []ProviderWarning{dependentsBudgetWarning(0, -1, options.budget)}, nil
	}

	files, prefiltered, warnings, err := referenceCandidateFiles(ctx, repo, head, names)
	if err != nil {
		return nil, nil, err
	}
	// Drop candidates the graph does not index before any of them is read. A
	// caller inside a vendored or .graphignore'd tree is not a dependent the
	// graph can show: after the diff stopped naming those files, counting them
	// left a number nothing in the output accounted for.
	if options.admit != nil {
		admitted := files[:0]
		for _, file := range files {
			if options.admit(file) {
				admitted = append(admitted, file)
			}
		}
		files = admitted
	}
	if options.progress != nil {
		options.progress(0, len(files), "")
	}

	// One persistent `git cat-file --batch-command` process replaces a one-shot Git
	// subprocess per candidate file; on large repos the per-file spawn cost
	// alone was tens of seconds. Paths the LF protocol cannot represent and a
	// batch startup failure share one bounded metadata reader, so exceptional
	// deep paths cannot each reset the component-process allowance.
	limited := gitutil.NewLimitedFileReader(ctx, repo, head, defaultMaxParseBytes)
	limited.SetDeadline(options.deadline)
	defer func() { _ = limited.Close() }()

	// The candidate scan below runs on workers, so everything a read records for
	// the warnings that follow it is behind one lock. Each entry is written once
	// per candidate and read once, so the lock is never contended for long.
	var readOutcomeMu sync.Mutex
	limitedOversize := map[string]int64{}
	// limitedOversizeUnscanned records every oversized path whose size came
	// from LimitedFileReader rather than the batch reader's oversize scanner
	// below: LimitedFileReader has no content-scanning capability of its own,
	// so oversizeMatched can never be populated for these paths. That used to
	// silently read as "did not match" on the full-tree fallback (prefiltered
	// == false), undercounting dependents for any line-unsafe or
	// batch-ineligible oversized file that does contain a changed name. With
	// no candidate evidence either way, warn rather than assume a miss.
	limitedOversizeUnscanned := map[string]bool{}
	limitedUnaddressable := map[string]bool{}
	limitedUnavailable := map[string]gitutil.LimitedFileStatus{}
	readLimitedFile := func(path string) (string, bool, error) {
		if !gitutil.IsCanonicalGitTreePath(path) {
			readOutcomeMu.Lock()
			limitedUnaddressable[path] = true
			readOutcomeMu.Unlock()
			return "", false, nil
		}
		result, err := limited.ReadFile(path)
		if err != nil {
			return "", false, err
		}
		// Every outcome a read records goes under the one lock, in one
		// acquisition. The statuses are distinct values, so folding the three
		// separate checks into one switch records exactly what they did.
		readOutcomeMu.Lock()
		switch result.Status {
		case gitutil.LimitedFileOversize:
			limitedOversize[path] = result.Bytes
			limitedOversizeUnscanned[path] = true
		case gitutil.LimitedFileUnaddressable:
			limitedUnaddressable[path] = true
		case gitutil.LimitedFileMissing, gitutil.LimitedFileNonBlob, gitutil.LimitedFileUnreadable:
			limitedUnavailable[path] = result.Status
		}
		readOutcomeMu.Unlock()
		return result.Content, result.Status == gitutil.LimitedFileContent, nil
	}
	readFile := readLimitedFile
	// oversizeBytes reports a file the reader declined because it exceeds the
	// parse limit, so the scan can warn about it (below) without the read that
	// would have cost the file's size twice for a file it refuses to parse anyway.
	// Set for an oversized blob whose streamed bytes contained a changed name.
	oversizeMatched := map[string]bool{}
	oversizeBytes := func(path string) (int64, bool) {
		readOutcomeMu.Lock()
		defer readOutcomeMu.Unlock()
		size, ok := limitedOversize[path]
		return size, ok
	}
	batch, batchErr := gitutil.NewBatchFileReader(ctx, repo, head)
	if batchErr == nil {
		defer func() { _ = batch.Close() }()
		batch.SetMaxBytes(defaultMaxParseBytes)
		readFile = func(path string) (string, bool, error) {
			if !gitutil.IsCanonicalGitTreePath(path) || !batch.IsPathSafe(path) {
				return readLimitedFile(path)
			}
			content, ok, err := batch.ReadFile(path)
			if err == nil && !ok {
				if _, oversize := batch.OversizeBlob(path); !oversize {
					// Git grep already proved a prefiltered path existed and
					// matched. A later missing/non-blob batch response means the
					// promised blob became unavailable between discovery and read.
					readOutcomeMu.Lock()
					limitedUnavailable[path] = gitutil.LimitedFileUnreadable
					readOutcomeMu.Unlock()
				}
			}
			return content, ok, err
		}
		oversizeBytes = func(path string) (int64, bool) {
			readOutcomeMu.Lock()
			size, ok := limitedOversize[path]
			readOutcomeMu.Unlock()
			if ok {
				return size, true
			}
			blob, ok := batch.OversizeBlob(path)
			return blob.Bytes, ok
		}
		// An oversized blob is streamed past and discarded, so its content is never available to
		// isCandidate below. Decide relevance during that same pass: if none of the changed names
		// appears in the blob, it was never a candidate and must not produce a warning (the fallback
		// scan would otherwise spray E_FILE_TOO_LARGE over every huge vendored blob in the tree).
		// Chunks may split an identifier, so carry maxNameLen-1 bytes of overlap between them.
		maxNameLen := 0
		for name := range names {
			if len(name) > maxNameLen {
				maxNameLen = len(name)
			}
		}
		carry := map[string]string{}
		batch.SetOversizeScanner(func(path string, chunk []byte) {
			readOutcomeMu.Lock()
			defer readOutcomeMu.Unlock()
			if oversizeMatched[path] {
				return
			}
			window := carry[path] + string(chunk)
			if containsAnyName(window, names) {
				oversizeMatched[path] = true
				delete(carry, path)
				return
			}
			if keep := maxNameLen - 1; keep > 0 && len(window) > keep {
				window = window[len(window)-keep:]
			}
			carry[path] = window
		})
	}

	// Prime exactly the canonical, supported paths that will use the fallback,
	// in candidate-list order. Without this pass, every LF-unsafe short path
	// performs its own lazy ls-tree lookup even though LimitedFileReader can
	// resolve up to 128 exact paths per bounded metadata subprocess. If the
	// primary content batch failed to start, every readable candidate uses the
	// same primed fallback instead.
	limitedPaths := make([]string, 0, len(files))
	for _, path := range files {
		if !Supported(path) || !gitutil.IsCanonicalGitTreePath(path) {
			continue
		}
		if batchErr != nil || !batch.IsPathSafe(path) {
			limitedPaths = append(limitedPaths, path)
		}
	}
	if err := limited.Prime(limitedPaths); err != nil {
		return nil, nil, err
	}
	// A bounded metadata batch can consume the remaining analysis budget. Keep
	// the existing semantics: stop before parsing and report one budget warning,
	// rather than misclassifying deadline-driven component results as read errors.
	if len(files) > 0 && overBudget() {
		warnings = append(warnings, dependentsBudgetWarning(0, len(files), options.budget))
		return index, warnings, nil
	}

	// When the grep prefilter ran, every file below already matched a changed
	// name, so every skip is worth warning about. On the fallback full-tree
	// scan, apply the same candidate test in-process before warning, so the
	// fallback does not spray warnings about files (e.g. huge vendored blobs)
	// that contain no changed name and were never real candidates.
	isCandidate := func(content string) bool {
		return prefiltered || containsAnyName(content, names)
	}

	// Each candidate is read, screened and parsed independently: nothing it
	// looks at changes while the scan runs, and the reference index it feeds is
	// a set of sets. So candidates run on workers and a reducer folds each
	// one's hits and warnings in candidate order, which is the order the
	// sequential scan produced them in.
	parser := TreeSitterParser{}
	scanCandidate := func(i int, path string) candidateScan {
		var scan candidateScan
		if overBudget() {
			scan.stopped = true
			return scan
		}
		if !Supported(path) {
			return scan
		}
		content, ok, err := readFile(path)
		if err != nil {
			scan.err = err
			return scan
		}
		// A deep-path metadata read can consume the remaining budget. Stop
		// immediately after it returns, before its deadline-driven
		// Unaddressable result is mistaken for a file-read failure.
		if overBudget() {
			scan.stopped = true
			return scan
		}
		if !ok {
			// In a fallback full-tree scan, an unsafe oversized file has no
			// streamed content evidence unless the batch reader's oversize
			// scanner actually ran on it (oversizeMatched). A path the batch
			// reader could not touch at all -- read through LimitedFileReader
			// instead, tracked by limitedOversizeUnscanned -- has no candidate
			// evidence in either direction, so it must warn rather than be
			// silently assumed clean; the same applies to a path this scan
			// cannot address at all. Both are rare, so this does not reopen
			// the vendored-blob warning spam the isCandidate check above
			// exists to avoid. Unavailable content has the same uncertainty: on a
			// full-tree fallback there is no sound way to prove the unreadable file
			// did not contain a changed name, so it is always disclosed too.
			size, oversize := oversizeBytes(path)
			readOutcomeMu.Lock()
			matched, unscanned := oversizeMatched[path], limitedOversizeUnscanned[path]
			unaddressable := limitedUnaddressable[path]
			status, unavailable := limitedUnavailable[path]
			readOutcomeMu.Unlock()
			if oversize && (prefiltered || matched || unscanned) {
				scan.warnings = append(scan.warnings, dependentsFileTooLargeWarning(path, int(size)))
			}
			if unaddressable {
				scan.warnings = append(scan.warnings, dependentsFileUnaddressableWarning(path))
			}
			if unavailable {
				scan.warnings = append(scan.warnings, dependentsFileUnavailableWarning(path, status))
			}
			return scan
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
				scan.warnings = append(scan.warnings, dependentsFileTooLargeWarning(path, len(content)))
			}
			return scan
		}

		// Pre-parse screen: tokenize the raw content once and keep only the
		// changed names that occur as exact identifier tokens. The git-grep
		// prefilter matches substrings, a superset of this token check, so a
		// file that only matched as a substring of a longer identifier drops
		// out here — and, crucially, a file with no exact-token occurrence
		// skips the (far more expensive) tree-sitter parse entirely.
		relevant := map[string]struct{}{}
		forEachIdentifierToken(content, func(token string) {
			if _, isName := names[token]; isName {
				relevant[token] = struct{}{}
			}
		})
		if len(relevant) == 0 {
			return scan
		}

		entities, _, status := parser.ParseWithStatus(path, content)
		if status.ParseError && isCandidate(content) {
			scan.warnings = append(scan.warnings, dependentsParseFailureWarning(path, status))
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
			forEachIdentifierToken(block, func(token string) {
				if token == self {
					return
				}
				if _, isRelevant := relevant[token]; isRelevant {
					scan.hits = append(scan.hits, referenceHit{token: token, entityKey: entityKey})
				}
			})
		}
		return scan
	}

	scanErr := runIndexedPipeline(ctx, len(files), dependentsScanWorkers(),
		func(_ context.Context, i int) candidateScan { return scanCandidate(i, files[i]) },
		func(i int, scan candidateScan) error {
			if scan.err != nil {
				return scan.err
			}
			if scan.stopped || overBudget() {
				warnings = append(warnings, dependentsBudgetWarning(i, len(files), options.budget))
				return errDependentsScanStopped
			}
			if i > 0 && i%100 == 0 && options.progress != nil {
				options.progress(i, len(files), files[i])
			}
			warnings = append(warnings, scan.warnings...)
			for _, hit := range scan.hits {
				index[hit.token][hit.entityKey] = struct{}{}
			}
			return nil
		})
	if scanErr != nil && !errors.Is(scanErr, errDependentsScanStopped) {
		return nil, nil, scanErr
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

// dependentsFileUnaddressableWarning reports a candidate whose unusual path
// could not be resolved within the shared bounded metadata traversal. Its
// content is never available by definition, so relevance can never be
// confirmed OR ruled out on the full-tree grep fallback either; callers emit
// this warning unconditionally rather than silently undercounting
// dependents_count for a path that might have been relevant.
func dependentsFileUnaddressableWarning(path string) ProviderWarning {
	return ProviderWarning{
		Code:                 "E_FILE_READ",
		Severity:             "error",
		FilePath:             path,
		EffectOnCompleteness: "dependent references in this file were not counted because its Git metadata could not be resolved",
		Detail:               "path could not be resolved within bounded Git metadata traversal",
	}
}

func dependentsFileUnavailableWarning(path string, status gitutil.LimitedFileStatus) ProviderWarning {
	detail := "the tree entry identifies a blob, but its Git content was unavailable during dependent analysis"
	if status == gitutil.LimitedFileNonBlob {
		detail = "the tree entry was not a blob when dependent content was read"
	} else if status == gitutil.LimitedFileMissing {
		detail = "the tree entry was missing when dependent content was read"
	}
	return ProviderWarning{
		Code:                 "E_FILE_READ",
		Severity:             "error",
		FilePath:             path,
		EffectOnCompleteness: "dependent references in this file were not counted because its Git content was unavailable",
		Detail:               detail,
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
// tree. This avoids trusting partial grep stdout; per-file warnings separately
// disclose any content the fallback itself cannot read.
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
// The superset guarantee is about TEXT, and the caller may narrow the result
// afterwards by index membership (dependentsScanOptions.admit). Nothing dropped
// there is a dependent the graph could ever show, so the guarantee still holds
// where it is claimed: relative to the files the graph indexes.
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
	forEachIdentifierToken(content, func(token string) {
		identifiers[token] = struct{}{}
	})
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
