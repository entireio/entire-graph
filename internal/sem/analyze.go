package sem

import (
	"context"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// maxDiffFileBytes caps how large a blob the semantic diff will materialize,
// per side. It is deliberately the same ceiling the snapshot parser uses
// (defaultMaxParseBytes): a file the graph declines to parse is not one the
// diff should read twice in order to parse it anyway.
const maxDiffFileBytes = defaultMaxParseBytes

// Keep metadata prefetch bounded so a short analysis budget does not inspect an
// entire huge range before the per-file budget check can stop it.
const analyzeMetadataPrimeFiles = 128

func AnalyzeGitRange(ctx context.Context, repo, base, head string, paths []string) (Result, error) {
	return AnalyzeGitRangeWithOptions(ctx, repo, base, head, paths, AnalyzeOptions{})
}

// AnalyzeOptions configures optional semantic diff behavior.
type AnalyzeOptions struct {
	Progress func(AnalyzeProgressEvent)
	// MaxDuration is the overall wall-clock budget for the analysis. When it
	// runs out the analysis stops cleanly and returns the partial result built
	// so far, with machine-readable W_ANALYSIS_BUDGET_EXCEEDED warnings
	// enumerating what was skipped. Zero means no budget (historical
	// behavior).
	MaxDuration time.Duration
}

// AnalyzeProgressEvent reports coarse progress for a semantic diff.
type AnalyzeProgressEvent struct {
	Phase      string
	FilesDone  int
	FilesTotal int
	// Path is the file currently being processed ("" on phase boundaries).
	Path    string
	Elapsed time.Duration
}

// AnalyzeGitRangeWithOptions analyzes a Git range with optional progress reporting.
func AnalyzeGitRangeWithOptions(ctx context.Context, repo, base, head string, paths []string, options AnalyzeOptions) (Result, error) {
	if err := EnsureGitMetadataSafeForSubprocess(repo); err != nil {
		return Result{}, err
	}
	started := time.Now()
	var deadline time.Time
	if options.MaxDuration > 0 {
		deadline = started.Add(options.MaxDuration)
	}
	overBudget := func() bool {
		return !deadline.IsZero() && !time.Now().Before(deadline)
	}
	var lastEmit time.Time
	// force emits phase boundaries unconditionally; per-file events are
	// throttled to roughly one per second so slow files stay visible without
	// flooding stderr on fast ones.
	emitProgressEvent := func(phase string, filesDone, filesTotal int, path string, force bool) {
		if options.Progress == nil {
			return
		}
		if !force && time.Since(lastEmit) < time.Second {
			return
		}
		lastEmit = time.Now()
		options.Progress(AnalyzeProgressEvent{
			Phase:      phase,
			FilesDone:  filesDone,
			FilesTotal: filesTotal,
			Path:       path,
			Elapsed:    time.Since(started),
		})
	}
	emitProgress := func(phase string, filesDone, filesTotal int) {
		emitProgressEvent(phase, filesDone, filesTotal, "", true)
	}

	// Bind both caller-facing revision labels to immutable raw trees before any
	// discovery or content read. A branch can advance between ChangedFiles, the
	// size probe, and ShowFile; resolving each operation through the original
	// label would then compare bytes from different ranges and could invalidate
	// the read ceiling. Trees preserve the command's historical tree-ish input
	// contract; requiring commits would reject valid tree OIDs and expressions.
	// gitutil disables replace refs for every subprocess, so refs/replace cannot
	// mutate either pinned tree between discovery, metadata, content, and grep.
	// When both labels are byte-identical, reuse the first resolution so a ref
	// advance between subprocesses cannot turn an intended empty diff into an
	// old-tree/new-tree comparison. Keep the labels below for result provenance.
	pinnedBase, pinnedHead, rootRelativeNames, err := resolveDiffTrees(ctx, repo, base, head, gitutil.RevParse)
	if err != nil {
		return Result{}, err
	}
	// ChangedFiles intentionally retains the caller's cwd so explicit path
	// arguments keep Git's native --repo-relative meaning. Its emitted names,
	// however, are relative to the compared tree. Perform every subsequent
	// object read and grep from Git's command root so a repository subdirectory
	// is not applied again when pinnedBase/pinnedHead are already subtree OIDs.
	objectRepo, err := gitutil.RepoCommandRoot(ctx, repo)
	if err != nil {
		return Result{}, err
	}

	emitProgress("discover", 0, 0)
	changed, err := gitutil.ChangedFiles(ctx, repo, pinnedBase, pinnedHead, paths)
	if err != nil {
		return Result{}, err
	}
	// Every graph command honors a repo-root .graphignore and the vendored-tree
	// heuristic (see the `index` help text and docs/operations.md). The
	// snapshot/search family applies both through openSource; the diff family
	// applied neither, so a tracked-but-vendored or generated tree that the
	// graph never indexes still produced entity changes here. A consumer that
	// builds its index from `snapshot` and updates it from `diff` received
	// symbols for files that its index has no record of and never will.
	basePolicy, headPolicy, err := newDiffIndexPolicies(ctx, objectRepo, pinnedBase, pinnedHead, rootRelativeNames, changed, overBudget)
	if err != nil {
		return Result{}, err
	}
	policySource := changed
	if len(paths) > 0 {
		// A pathspec narrows what Git reports, but not what an exclusion rule
		// decides: a root .gitignore edited outside the requested scope still
		// changes membership inside it, and the scoped list cannot show that.
		// Ask Git for the policy files directly, anchored at the repository root
		// with :(top) so the caller's cwd does not narrow them a second time.
		policySource, err = gitutil.ChangedFiles(ctx, repo, pinnedBase, pinnedHead, indexPolicyPathspecs)
		if err != nil {
			return Result{}, err
		}
	}
	policyWarnings := indexPolicyChangeWarnings(policySource, basePolicy)
	changed = admitChangedFiles(changed, basePolicy, headPolicy)
	emitProgress("parse", 0, len(changed))
	parser := TreeSitterParser{}
	baseReader := gitutil.NewLimitedFileReader(ctx, objectRepo, pinnedBase, maxDiffFileBytes)
	headReader := gitutil.NewLimitedFileReader(ctx, objectRepo, pinnedHead, maxDiffFileBytes)
	baseReader.SetDeadline(deadline)
	headReader.SetDeadline(deadline)
	defer func() {
		_ = baseReader.Close()
		_ = headReader.Close()
	}()
	// SchemaVersion is set here, at the one place a content-bearing Result is
	// constructed; every caller (AnalyzeGitRange, AnalyzeCheckpoint, and the
	// diff/analyze CLI handlers that call through them) inherits it.
	result := Result{Base: base, Head: head, SchemaVersion: SchemaVersion}
	result.Warnings = append(result.Warnings, policyWarnings...)
	var deltas []*fileDelta
	appendBudgetWarnings := func(start int) {
		for _, skipped := range changed[start:] {
			result.Warnings = append(result.Warnings, budgetSkippedFileWarning(skipped.Path, start, len(changed), options.MaxDuration))
		}
	}
	for i, file := range changed {
		if overBudget() {
			// Stop cleanly: keep everything analyzed so far and enumerate each
			// skipped changed file with a machine-readable warning, so the
			// partial result is never silently incomplete.
			appendBudgetWarnings(i)
			break
		}
		if i%analyzeMetadataPrimeFiles == 0 {
			end := min(i+analyzeMetadataPrimeFiles, len(changed))
			basePaths := make([]string, 0, end-i)
			headPaths := make([]string, 0, end-i)
			for _, candidate := range changed[i:end] {
				oldCandidatePath := candidate.OldPath
				if oldCandidatePath == "" {
					oldCandidatePath = candidate.Path
				}
				if extensionUnsupported(oldCandidatePath) && extensionUnsupported(candidate.Path) {
					continue
				}
				if candidate.Status != "A" && gitutil.IsCanonicalGitTreePath(oldCandidatePath) {
					basePaths = append(basePaths, oldCandidatePath)
				}
				if candidate.Status != "D" && gitutil.IsCanonicalGitTreePath(candidate.Path) {
					headPaths = append(headPaths, candidate.Path)
				}
			}
			if err := baseReader.Prime(basePaths); err != nil {
				return Result{}, err
			}
			if err := headReader.Prime(headPaths); err != nil {
				return Result{}, err
			}
			// Component traversal observes the same deadline between metadata
			// subprocesses. Re-check immediately so the current file and the rest
			// receive the ordinary budget warnings instead of being misreported as
			// unaddressable/read failures after Prime stopped at that deadline.
			if overBudget() {
				appendBudgetWarnings(i)
				break
			}
		}
		emitProgressEvent("parse", i, len(changed), file.Path, i > 0 && i%100 == 0)
		path := file.Path
		oldPath := file.OldPath
		if oldPath == "" {
			oldPath = path
		}
		beforeInvalidPath := file.Status != "A" && !gitutil.IsCanonicalGitTreePath(oldPath)
		afterInvalidPath := file.Status != "D" && !gitutil.IsCanonicalGitTreePath(path)
		if beforeInvalidPath || afterInvalidPath {
			warningPath := path
			detail := "head path is not a canonical Git tree path"
			if beforeInvalidPath && !afterInvalidPath {
				warningPath = oldPath
				detail = "base path is not a canonical Git tree path"
			} else if beforeInvalidPath && afterInvalidPath {
				detail = "base and head paths are not canonical Git tree paths"
			}
			result.Warnings = append(result.Warnings, diffFileReadWarning(warningPath, detail))
			continue
		}

		var before, after string
		var beforeOK, afterOK bool
		// Fast-path: a file WITH a recognized-unsupported extension classifies without reading
		// its blobs — shebang sniffing only matters for extensionless files. Avoids loading a
		// large binary twice just to conclude "unsupported"; the marker below needs no content.
		if extensionUnsupported(oldPath) && extensionUnsupported(path) {
			beforeOK = file.Status != "A"
			afterOK = file.Status != "D"
		} else {
			// Both sides are read under the same ceiling the snapshot parser
			// uses. A semantic diff holds TWO blobs of one file at once, so an
			// unbounded read here sets this command's memory and time by the
			// largest object in the range rather than by anything the caller
			// chose -- and unlike the snapshot readers, nothing downstream
			// would have declined to parse it.
			var beforeRead, afterRead gitutil.LimitedFileResult
			if file.Status != "A" {
				beforeRead, err = baseReader.ReadFile(oldPath)
				if err != nil {
					return Result{}, err
				}
				before, beforeOK = beforeRead.Content, beforeRead.Status == gitutil.LimitedFileContent
			}
			if file.Status != "D" {
				afterRead, err = headReader.ReadFile(path)
				if err != nil {
					return Result{}, err
				}
				after, afterOK = afterRead.Content, afterRead.Status == gitutil.LimitedFileContent
			}

			beforeMissing := file.Status != "A" && beforeRead.Status == gitutil.LimitedFileMissing
			afterMissing := file.Status != "D" && afterRead.Status == gitutil.LimitedFileMissing
			if beforeMissing || afterMissing {
				warningPath := path
				detail := "head version was missing after changed-file discovery"
				if beforeMissing && !afterMissing {
					warningPath = oldPath
					detail = "base version was missing after changed-file discovery"
				} else if beforeMissing && afterMissing {
					detail = "base and head versions were missing after changed-file discovery"
				}
				result.Warnings = append(result.Warnings, diffFileReadWarning(warningPath, detail))
			}

			beforeUnreadable := file.Status != "A" && beforeRead.Status == gitutil.LimitedFileUnreadable
			afterUnreadable := file.Status != "D" && afterRead.Status == gitutil.LimitedFileUnreadable
			if beforeUnreadable || afterUnreadable {
				warningPath := path
				detail := "head version references an unreadable Git blob object"
				if beforeUnreadable && !afterUnreadable {
					warningPath = oldPath
					detail = "base version references an unreadable Git blob object"
				} else if beforeUnreadable && afterUnreadable {
					detail = "base and head versions reference unreadable Git blob objects"
				}
				result.Warnings = append(result.Warnings, diffFileReadWarning(warningPath, detail))
			}

			beforeNonBlob := file.Status != "A" && beforeRead.Status == gitutil.LimitedFileNonBlob
			afterNonBlob := file.Status != "D" && afterRead.Status == gitutil.LimitedFileNonBlob
			if beforeNonBlob || afterNonBlob {
				warningPath := path
				detail := "head version is a non-blob Git tree entry"
				if beforeNonBlob && !afterNonBlob {
					warningPath = oldPath
					detail = "base version is a non-blob Git tree entry"
				} else if beforeNonBlob && afterNonBlob {
					detail = "base and head versions are non-blob Git tree entries"
				}
				result.Warnings = append(result.Warnings, diffNonBlobWarning(warningPath, detail))
			}

			beforeOversize := file.Status != "A" && beforeRead.Status == gitutil.LimitedFileOversize
			afterOversize := file.Status != "D" && afterRead.Status == gitutil.LimitedFileOversize
			if beforeOversize || afterOversize {
				warningPath := path
				detail := "head version is above the diff read cap"
				if beforeOversize && !afterOversize {
					warningPath = oldPath
					detail = "base version is above the diff read cap"
				} else if beforeOversize && afterOversize {
					detail = "base and head versions are above the diff read cap"
				}
				result.Warnings = append(result.Warnings, diffFileTooLargeWarning(warningPath, detail))
			}

			beforeUnaddressable := file.Status != "A" && beforeRead.Status == gitutil.LimitedFileUnaddressable
			afterUnaddressable := file.Status != "D" && afterRead.Status == gitutil.LimitedFileUnaddressable
			if beforeUnaddressable || afterUnaddressable {
				warningPath := path
				detail := "head path could not be resolved within bounded Git metadata traversal"
				if beforeUnaddressable && !afterUnaddressable {
					warningPath = oldPath
					detail = "base path could not be resolved within bounded Git metadata traversal"
				} else if beforeUnaddressable && afterUnaddressable {
					detail = "base and head paths could not be resolved within bounded Git metadata traversal"
				}
				result.Warnings = append(result.Warnings, diffFileReadWarning(warningPath, detail))
			}
			if beforeMissing || afterMissing || beforeUnreadable || afterUnreadable || beforeNonBlob || afterNonBlob || beforeOversize || afterOversize || beforeUnaddressable || afterUnaddressable {
				continue
			}
		}

		// Support is content-aware: extensionless executables can still route to a
		// parser through their shebang, so each existing side is classified
		// independently.
		//
		// A side with no parser holds nothing in the graph — that is what "the
		// graph does not index .txt" means — so a rename across the parser
		// boundary is a file leaving or entering the index, and the honest delta
		// is the removals or additions that a snapshot of each side would show.
		// This used to suppress the whole record to avoid a "one-sided phantom
		// remove/add", but the removals are not phantom: `x.go` -> `x.txt` really
		// does take Run out of every snapshot, and suppressing it left a consumer
		// unable to retire the old compound-v1 IDs while the warning named only
		// the head path. The marker below still says the comparison is one-sided.
		_, beforeSupported := languageForContent(oldPath, before)
		_, afterSupported := languageForContent(path, after)
		beforeUnsupported := beforeOK && !beforeSupported
		afterUnsupported := afterOK && !afterSupported
		if beforeUnsupported || afterUnsupported {
			warningPath := path
			detail := "head version has no supported parser"
			if beforeUnsupported && !afterUnsupported {
				warningPath = oldPath
				detail = "base version has no supported parser"
			} else if beforeUnsupported && afterUnsupported {
				detail = "base and head versions have no supported parser"
			}
			effect := "file skipped; no parser for this file type, so its changes are not analyzed"
			if beforeOK && afterOK && beforeUnsupported != afterUnsupported {
				effect = "one side has no parser, so its symbols are reported as leaving or entering the graph rather than compared"
			}
			result.Warnings = append(result.Warnings, ProviderWarning{
				Code:                 "W_UNSUPPORTED_FILE",
				Severity:             "info",
				FilePath:             warningPath,
				EffectOnCompleteness: effect,
				Detail:               detail,
			})
			// Neither side is in the graph, so there is nothing to add or retire
			// and a record here would invent one for a path no snapshot holds.
			if beforeUnsupported && afterUnsupported {
				continue
			}
		}

		beforeEntities, beforeLanguage, beforeStatus := parser.ParseWithStatus(oldPath, before)
		afterEntities, afterLanguage, afterStatus := parser.ParseWithStatus(path, after)
		// The head side names the file as it now is, and a rename can cross
		// extensions: mod.js -> mod.ts is one file whose language changed with
		// its path. The graph indexes the head path under the head parser's
		// language, so reporting the base label here would disagree with every
		// snapshot of that tree. Fall back to the base label only when there is
		// no head version to ask — a deletion — or when it has no opinion.
		language := afterLanguage
		if (file.Status == "D" || language == "") && beforeLanguage != "" {
			language = beforeLanguage
		}
		// A parse failure on either side degrades the diff. A TOTAL failure
		// (ParseError with ZERO recovered entities) gives compareEntities no
		// signal at all and would make it report every entity on that side as
		// a phantom removed/added, so the delta is skipped and a
		// machine-readable warning is surfaced instead. A PARTIAL recovery
		// (ParseError with some entities extracted) keeps the diff — the
		// recovered changes are real — but is still flagged with a warning,
		// because symbols missing from the recovered set can surface as
		// phantom removed/added. A validly-emptied file (ParseError false) is
		// never suppressed or flagged, so its real removed changes stand.
		afterParseFailed := afterStatus.ParseError && len(afterEntities) == 0
		beforeParseFailed := beforeStatus.ParseError && len(beforeEntities) == 0
		// A failed parse is this provider failing to READ the file, not evidence
		// that the file is empty. The symbols are most likely still there, so
		// reporting them as removed would state a deletion that did not happen —
		// the phantom the suppression here exists to prevent, and a different
		// situation from a side the graph does not index at all (above), where
		// their absence is a fact rather than a blind spot.
		if afterParseFailed || beforeParseFailed {
			status, warnPath := afterStatus, path
			if !afterParseFailed {
				status, warnPath = beforeStatus, oldPath
			}
			result.Warnings = append(result.Warnings, parseFailureWarning(warnPath, status, true))
			continue
		}
		// A side with no parser holds nothing in the graph, so emptying it turns
		// the comparison below into the removals or additions a snapshot of each
		// side would show.
		if beforeUnsupported {
			beforeEntities = nil
		}
		if afterUnsupported {
			afterEntities = nil
		}
		if afterStatus.ParseError || beforeStatus.ParseError {
			status, warnPath := afterStatus, path
			if !afterStatus.ParseError {
				status, warnPath = beforeStatus, oldPath
			}
			result.Warnings = append(result.Warnings, parseFailureWarning(warnPath, status, false))
		}
		if !beforeOK {
			beforeEntities = nil
		}
		if !afterOK {
			afterEntities = nil
		}

		changes, removed, added := compareEntities(beforeEntities, afterEntities)
		if len(changes) == 0 && len(removed) == 0 && len(added) == 0 {
			// The file changed but no named symbol did: the edit lives at
			// module scope (top-level statements, imports, comments). Surface
			// it as a synthetic module-level change instead of dropping it.
			mod, ok := moduleScopeChange(path, before, after, beforeOK, afterOK)
			if !ok {
				// A pure rename reaches here: both sides parse to the same
				// entities and the bytes are identical, so there is no content
				// change to report. The path still changed, and the path is a
				// component of every compound-v1 symbol ID in the file, so
				// every entity in it was re-identified. Report the path move so
				// the file is never absent from the diff.
				mod, ok = pathScopeChange(oldPath, path)
			}
			if !ok {
				continue
			}
			changes = append(changes, mod)
		}
		deltas = append(deltas, &fileDelta{
			path:     path,
			oldPath:  file.OldPath,
			status:   file.Status,
			language: language,
			changes:  changes,
			removed:  removed,
			added:    added,
		})
	}
	emitProgress("parse", len(changed), len(changed))
	baseCloseErr := baseReader.Close()
	headCloseErr := headReader.Close()
	if baseCloseErr != nil {
		return Result{}, baseCloseErr
	}
	if headCloseErr != nil {
		return Result{}, headCloseErr
	}

	emitProgress("reconcile", len(changed), len(changed))
	result.Warnings = append(result.Warnings, reconcileMoves(deltas)...)

	for _, delta := range deltas {
		changes := delta.changes
		for _, oldEntity := range delta.removed {
			changes = append(changes, removedChange(oldEntity))
		}
		for _, newEntity := range delta.added {
			changes = append(changes, addedChange(newEntity))
		}
		if len(changes) == 0 {
			continue
		}
		sortChanges(changes)
		result.Files = append(result.Files, FileChange{
			Path:     delta.path,
			OldPath:  delta.oldPath,
			Status:   delta.status,
			Language: delta.language,
			Changes:  changes,
		})
	}

	// The dependents scan reads the whole head tree, so it needs the head tree's
	// real rules rather than the ones the changed-file probe was allowed to skip.
	// Resolve them only when there is something to count, and only while the
	// budget still allows work: resolving lists the whole head tree and reads its
	// ignore files, and an exhausted range must return with its budget warning
	// rather than spend more time than the caller asked for. The scan below
	// re-checks the same deadline and stops immediately, so the policy it would
	// have used cannot matter then.
	dependentsPolicy := headPolicy
	if len(result.Files) > 0 && !overBudget() {
		dependentsPolicy, err = headPolicy.resolvedForTree(ctx, objectRepo, pinnedHead)
		if err != nil {
			return Result{}, err
		}
	}
	if err := addDependentCountsWithProgress(ctx, objectRepo, pinnedHead, &result, dependentsScanOptions{
		progress: func(done, total int, path string) {
			emitProgressEvent("dependents", done, total, path, path == "")
		},
		deadline: deadline,
		budget:   options.MaxDuration,
		admit:    dependentsPolicy.admitFunc(),
	}); err != nil {
		return Result{}, err
	}
	emitProgress("complete", len(changed), len(changed))
	return result, nil
}

// resolveDiffTrees pins both revision labels to immutable trees and reports
// whether ChangedFiles' names for the resulting range are repository-root
// relative.
//
// That last question decides whether any exclusion policy may be applied at all.
// ChangedFiles passes --no-relative, so a range between two COMMITS is named
// from the repository root, which is the namespace the root .graphignore and the
// vendored-tree rules are written in. A tree expression such as HEAD:scope names
// a subtree, and every emitted name is then relative to THAT tree: matching
// root-relative rules against those names both misses the rules that were meant
// to apply and fires rules that were not, which silently drops indexed files.
// There is no prefix to recover, because a tree object does not know its own
// path, so the only correct answer for a subtree range is to apply no policy.
// maxRevisionPeels bounds how many trailing ^{...} groups are stripped while
// deciding whether a label bottoms out at a commit. Each step costs a git
// subprocess, and no real revision expression stacks more than a couple.
const maxRevisionPeels = 4

// labelSelectsTreePath reports whether a revision label reaches INTO a tree
// rather than naming a revision.
//
// Resolving to a commit object is not sufficient evidence that a label named a
// commit: a gitlink resolves to one too. `HEAD:sub` on a submodule entry yields
// that submodule's commit, and a range over its tree is named relative to the
// SUBMODULE's root, not this repository's — so a superproject rule set matched
// against those names is the same category error as a subtree range. The probe
// happens to fail today because the submodule's objects usually are not in the
// superproject's object database, but an absorbed or fetched submodule puts them
// there, and correctness here should not rest on which objects a repository
// happens to hold.
//
// `:` is the only object-name syntax that reaches into a tree (`<rev>:<path>`
// and `:<stage>:<path>`), but not every colon is that syntax. Three forms carry
// colons that are data:
//
//	:/text                            a commit-message search; the colons are text
//	HEAD@{2026-08-27 12:34:56 +0000}  a reflog date selector
//	HEAD^{/release: fix}              a commit-message search from a starting point
//
// All three name a commit and reach into no tree, so only a colon outside every
// `@{...}` and `^{...}` block is the separator. Both brace forms are tracked, not
// just the one that happened to come up first: mistaking a commit for a subtree
// loses no data, but it silently withdraws the exclusion guarantee for the whole
// range, which is the kind of failure nothing downstream can notice.
func labelSelectsTreePath(label string) bool {
	if strings.HasPrefix(label, ":/") {
		return false
	}
	depth := 0
	for index := 0; index < len(label); index++ {
		switch label[index] {
		case '@', '^':
			if index+1 < len(label) && label[index+1] == '{' {
				depth++
				index++
			}
		case '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func resolveDiffTrees(
	ctx context.Context,
	repo, base, head string,
	resolve func(context.Context, string, string) (string, error),
) (string, string, bool, error) {
	rootRelative := func(label, objectID string) bool {
		if labelSelectsTreePath(label) {
			return false
		}
		// Probe the immutable OID rather than the caller's expression, for the
		// same reason the tree peel below does: appending ^{commit} to
		// HEAD:scope can select a repository path literally named
		// scope^{commit}. A commit-ish label resolves through ^{commit}; a tree
		// expression does not.
		if _, err := resolve(ctx, repo, objectID+"^{commit}"); err == nil {
			return true
		}
		// A caller who wrote <commit-ish>^{tree} still named a root tree, and Git
		// spells that several ways: ^{tree}, ^{tree}^{}, ^{tree}^{object}. Peel
		// the trailing groups THEY wrote — adding none of our own — and ask again
		// after each, so any spelling that bottoms out at a commit is recognized.
		// The peel count is capped because each step costs a subprocess and a
		// label can carry arbitrarily many groups.
		peeled := label
		for range maxRevisionPeels {
			open := strings.LastIndex(peeled, "^{")
			if open <= 0 || !strings.HasSuffix(peeled, "}") {
				return false
			}
			peeled = peeled[:open]
			peeledID, err := resolve(ctx, repo, peeled)
			if err != nil {
				continue
			}
			if _, err := resolve(ctx, repo, peeledID+"^{commit}"); err == nil {
				return true
			}
		}
		return false
	}
	resolveTree := func(label string) (string, bool, error) {
		// Resolve the caller's expression exactly before adding syntax of our
		// own. Appending ^{tree} directly to HEAD:scope can select a different
		// repository path literally named scope^{tree}; an immutable OID has no
		// such revision/path ambiguity.
		objectID, err := resolve(ctx, repo, label)
		if err != nil {
			return "", false, err
		}
		tree, err := resolve(ctx, repo, objectID+"^{tree}")
		if err != nil {
			return "", false, err
		}
		return tree, rootRelative(label, objectID), nil
	}

	pinnedBase, baseFromCommit, err := resolveTree(base)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve diff base %q: %w", base, err)
	}
	if head == base {
		return pinnedBase, pinnedBase, baseFromCommit, nil
	}
	pinnedHead, headFromCommit, err := resolveTree(head)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve diff head %q: %w", head, err)
	}
	return pinnedBase, pinnedHead, baseFromCommit && headFromCommit, nil
}

// budgetSkippedFileWarning marks one changed file that was never analyzed
// because the overall wall-clock budget (AnalyzeOptions.MaxDuration) ran out
// first. It shares the W_ANALYSIS_BUDGET_EXCEEDED code with the dependents
// scan's early-stop warning; FilePath distinguishes the per-file skips.
func budgetSkippedFileWarning(path string, done, total int, budget time.Duration) ProviderWarning {
	detail := fmt.Sprintf("analysis time budget ran out after %d of %d changed files", done, total)
	if budget > 0 {
		detail += fmt.Sprintf(" (budget %s)", budget)
	}
	return ProviderWarning{
		Code:                 "W_ANALYSIS_BUDGET_EXCEEDED",
		Severity:             "warning",
		FilePath:             path,
		EffectOnCompleteness: "file skipped; analysis time budget ran out before this changed file was analyzed",
		Detail:               detail,
	}
}

// diffFileTooLargeWarning builds the warning emitted when one side of a changed
// file is above maxDiffFileBytes. It reuses the provider's E_FILE_TOO_LARGE
// code and severity (see provider_parallel.go, and dependents.go for the same
// reuse) so one code covers "this file was too large to read" everywhere; the
// effect wording is diff-specific, because here the whole delta is dropped
// rather than only the file's symbol parsing.
func diffFileTooLargeWarning(path, detail string) ProviderWarning {
	return ProviderWarning{
		Code:                 "E_FILE_TOO_LARGE",
		Severity:             "warning",
		FilePath:             path,
		EffectOnCompleteness: "file skipped; its content was not read, so its changes are not analyzed",
		Detail:               fmt.Sprintf("%s of %d bytes", detail, maxDiffFileBytes),
	}
}

// diffNonBlobWarning marks a changed tree entry that cannot contain source
// text. Gitlinks are the ordinary case: their object is a commit, and asking
// `git show` for it would render a patch rather than read file content.
func diffNonBlobWarning(path, detail string) ProviderWarning {
	return ProviderWarning{
		Code:                 "W_UNSUPPORTED_FILE",
		Severity:             "info",
		FilePath:             path,
		EffectOnCompleteness: "file skipped; the Git tree entry has no blob content, so its changes are not analyzed",
		Detail:               detail,
	}
}

// diffFileReadWarning keeps an unexpectedly absent side distinct from the
// deliberate size refusal above. ChangedFiles and these reads use the same
// pinned trees, so a present side disappearing is an input/read failure, not
// evidence that the file exceeded the cap.
func diffFileReadWarning(path, detail string) ProviderWarning {
	return ProviderWarning{
		Code:                 "E_FILE_READ",
		Severity:             "error",
		FilePath:             path,
		EffectOnCompleteness: "file skipped; expected Git blob content was unavailable, so its changes are not analyzed",
		Detail:               detail,
	}
}

// parseFailureWarning builds the warning emitted when a changed file fails to
// parse on one side of the diff. It reuses the provider path's machine-readable
// codes (parseStatus.ParseError → PartialFailure, see provider.go), and both
// surfaces warn on any ParseError — but the effect wording is diff-specific:
// the provider always emits its (possibly partial) output, while the diff path
// suppresses the file's delta entirely on a total failure (suppressed == true)
// and keeps a possibly-degraded diff on a partial recovery.
func parseFailureWarning(path string, status ParseStatus, suppressed bool) ProviderWarning {
	code := status.Code
	if code == "" {
		code = "E_PARSE_ERROR"
	}
	var effect string
	switch {
	case suppressed && code == "E_PARSE_TIMEOUT":
		effect = "file diff suppressed; changes omitted because parser time budget was exceeded"
	case suppressed:
		effect = "file diff suppressed; changes omitted because the file could not be parsed"
	default:
		effect = "file parsed with syntax errors on one side; diff kept but may be incomplete or contain phantom changes"
	}
	return ProviderWarning{
		Code:                 code,
		Severity:             "warning",
		FilePath:             path,
		EffectOnCompleteness: effect,
		Detail:               status.Detail,
	}
}

// fileDelta accumulates a file's resolved changes plus the removed/added
// entities still eligible for cross-file move reconciliation.
type fileDelta struct {
	path     string
	oldPath  string
	status   string
	language string
	changes  []EntityChange
	removed  []Entity
	added    []Entity
}

// reconcileMoves matches removed entities in one file against added entities in
// another and rewrites unambiguous high-similarity pairs as a single MOVED
// change on the destination file. Ambiguous matches are left as remove/add and
// reported as warnings. Consumed entities are stripped from the deltas.
func reconcileMoves(deltas []*fileDelta) []ProviderWarning {
	type ref struct {
		delta  int
		index  int
		entity Entity
		path   string
	}
	var removed, added []ref
	for di, delta := range deltas {
		for ri, entity := range delta.removed {
			removed = append(removed, ref{delta: di, index: ri, entity: entity, path: delta.path})
		}
		for ai, entity := range delta.added {
			added = append(added, ref{delta: di, index: ai, entity: entity, path: delta.path})
		}
	}

	usedAdded := make([]bool, len(added))
	usedRemoved := make(map[[2]int]bool)
	consumedAdded := make(map[[2]int]bool)
	var warnings []ProviderWarning

	for ri := range removed {
		r := removed[ri]
		bestAi := -1
		bestScore := 0.0
		secondScore := 0.0
		for ai := range added {
			if usedAdded[ai] {
				continue
			}
			a := added[ai]
			if a.path == r.path || a.entity.Kind != r.entity.Kind {
				continue
			}
			score := similarity(r.entity, a.entity)
			if score > bestScore {
				secondScore = bestScore
				bestScore = score
				bestAi = ai
			} else if score > secondScore {
				secondScore = score
			}
		}
		if bestAi < 0 || bestScore < moveThreshold {
			continue
		}
		if secondScore >= moveThreshold && bestScore-secondScore < ambiguityMargin {
			warnings = append(warnings, ProviderWarning{
				Code:                 "W_MOVE_AMBIGUOUS",
				Severity:             "warning",
				FilePath:             r.path,
				EffectOnCompleteness: "symbol move could not be reconciled unambiguously; reported as remove/add",
				Detail:               fmt.Sprintf("%s %s has multiple equally similar destinations", r.entity.Kind, r.entity.Name),
			})
			continue
		}

		a := added[bestAi]
		usedAdded[bestAi] = true
		usedRemoved[[2]int{r.delta, r.index}] = true
		consumedAdded[[2]int{a.delta, a.index}] = true

		change := EntityChange{
			Type:            "moved",
			Kind:            a.entity.Kind,
			Name:            a.entity.Name,
			OldSignature:    r.entity.Signature,
			NewSignature:    a.entity.Signature,
			OldPath:         r.path,
			NewPath:         a.path,
			BeforeStartLine: r.entity.StartLine,
			AfterStartLine:  a.entity.StartLine,
			Similarity:      bestScore,
			Reconciliation:  "MOVED",
		}
		if r.entity.Name != a.entity.Name {
			change.OldName = r.entity.Name
			change.NewName = a.entity.Name
		}
		deltas[a.delta].changes = append(deltas[a.delta].changes, change)
	}

	for di, delta := range deltas {
		if len(usedRemoved) > 0 {
			var keep []Entity
			for ri, entity := range delta.removed {
				if !usedRemoved[[2]int{di, ri}] {
					keep = append(keep, entity)
				}
			}
			delta.removed = keep
		}
		if len(consumedAdded) > 0 {
			var keep []Entity
			for ai, entity := range delta.added {
				if !consumedAdded[[2]int{di, ai}] {
					keep = append(keep, entity)
				}
			}
			delta.added = keep
		}
	}

	return warnings
}

func AnalyzeCheckpoint(ctx context.Context, repo, checkpointID string) (Result, error) {
	if err := EnsureGitMetadataSafeForSubprocess(repo); err != nil {
		return Result{}, err
	}
	head, err := gitutil.FindCommitWithCheckpoint(ctx, repo, checkpointID)
	if err != nil {
		return Result{}, err
	}
	base, err := gitutil.FirstParent(ctx, repo, head)
	if err != nil {
		return Result{}, err
	}
	result, err := AnalyzeGitRange(ctx, repo, base, head, nil)
	if err != nil {
		return Result{}, err
	}
	result.Checkpoint = checkpointID
	return result, nil
}

// Compare reports the entity-level changes between two parses of the same file.
// Removed and added entities that are not reconciled within the file (rename)
// are emitted as plain removed/added changes.
func Compare(before, after []Entity) []EntityChange {
	changes, removed, added := compareEntities(before, after)
	for _, oldEntity := range removed {
		changes = append(changes, removedChange(oldEntity))
	}
	for _, newEntity := range added {
		changes = append(changes, addedChange(newEntity))
	}
	sortChanges(changes)
	return changes
}

// compareEntities diffs two entity sets from the same file. It returns the
// resolved changes (signature/body changes and within-file renames) plus the
// removed and added entities that were not reconciled, sorted deterministically
// so callers can run a cross-file reconciliation pass over the leftovers.
func compareEntities(before, after []Entity) (changes []EntityChange, removed, added []Entity) {
	beforeByKey, afterByKey := keyedEntityMaps(before, after)

	deleted := map[string]Entity{}
	addedByKey := map[string]Entity{}

	for key, oldEntity := range beforeByKey {
		newEntity, ok := afterByKey[key]
		if !ok {
			deleted[key] = oldEntity
			continue
		}
		switch {
		case oldEntity.Signature != newEntity.Signature:
			changes = append(changes, EntityChange{
				Type:            "signature_changed",
				Kind:            oldEntity.Kind,
				Name:            oldEntity.Name,
				OldSignature:    oldEntity.Signature,
				NewSignature:    newEntity.Signature,
				BeforeStartLine: oldEntity.StartLine,
				AfterStartLine:  newEntity.StartLine,
			})
		case oldEntity.BodyHash != newEntity.BodyHash:
			changes = append(changes, EntityChange{
				Type:            "body_changed",
				Kind:            oldEntity.Kind,
				Name:            oldEntity.Name,
				BeforeStartLine: oldEntity.StartLine,
				AfterStartLine:  newEntity.StartLine,
			})
		}
	}
	for key, newEntity := range afterByKey {
		if _, ok := beforeByKey[key]; !ok {
			addedByKey[key] = newEntity
		}
	}

	for oldKey, oldEntity := range deleted {
		bestKey, bestEntity, score := bestRename(oldEntity, addedByKey)
		if score >= renameThreshold {
			delete(deleted, oldKey)
			delete(addedByKey, bestKey)
			changes = append(changes, EntityChange{
				Type:            "renamed",
				Kind:            oldEntity.Kind,
				Name:            bestEntity.Name,
				OldName:         oldEntity.Name,
				NewName:         bestEntity.Name,
				OldSignature:    oldEntity.Signature,
				NewSignature:    bestEntity.Signature,
				BeforeStartLine: oldEntity.StartLine,
				AfterStartLine:  bestEntity.StartLine,
				Similarity:      score,
				Reconciliation:  "RENAMED",
			})
		}
	}

	removed = sortedEntities(deleted)
	added = sortedEntities(addedByKey)
	return changes, removed, added
}

const (
	renameThreshold = 0.92
	moveThreshold   = 0.92
	// ambiguityMargin marks a move as ambiguous when a second candidate scores
	// within this distance of the best one, so we report remove/add and warn
	// rather than guessing.
	ambiguityMargin = 0.05
)

// diffIndexPolicy answers, for ONE compared tree, the question the committed-tree
// snapshot asks of its own listing (openSource in provider.go): would the graph
// index this path? It applies the same two filters in the same order — the
// vendored-tree heuristic, then the explicit .graphignore and built-in secret
// matcher — so the diff can neither report an entity for a file no snapshot of
// that tree contains nor omit one a snapshot does contain.
//
// The policy is per-tree rather than per-range because its two halves answer
// differently on the two sides. The matcher is read from the working tree and so
// is common to both, but the vendored-tree rules come from each compared tree's
// own .gitignore files: a project can re-include part of a vendored tree in one
// revision and not the other, which makes an otherwise unremarkable edit an
// entry into, or an exit from, the graph.
type diffIndexPolicy struct {
	ignores     ignoreMatcher
	vendorRules vendorIgnoreRules
	// vendorRulesLoaded distinguishes "this tree really re-includes nothing"
	// from "nobody has needed the rules yet", which is what makes it safe to
	// skip loading them for a range that touches nothing vendorable.
	vendorRulesLoaded bool
	enabled           bool
}

// resolvedForTree returns a copy of p holding this tree's real vendored-tree
// rules, loading them if the changed-file probe skipped them.
//
// That probe licenses one question only: whether any of the paths GIT reported
// could be vendored. The dependents scan asks a different and much larger one —
// it walks the whole head tree — so a `vendor/...` path the project re-included
// can turn up there even when nothing vendorable changed. Reusing the probe's
// policy would then judge that caller by empty rules, drop it, and undercount a
// dependent the graph really holds.
func (p diffIndexPolicy) resolvedForTree(ctx context.Context, repo, tree string) (diffIndexPolicy, error) {
	if !p.enabled || p.vendorRulesLoaded {
		return p, nil
	}
	rules, err := treeVendorIgnoreRules(ctx, repo, tree, p.ignores)
	if err != nil {
		return diffIndexPolicy{}, err
	}
	p.vendorRules = rules
	p.vendorRulesLoaded = true
	return p, nil
}

// indexed reports whether the graph would hold records for rel in this tree.
//
// A disabled policy admits everything. That is not a weaker guess, it is the
// only correct answer available: the names it would be asked about are relative
// to some subtree, and neither rule set describes that namespace (see
// resolveDiffTrees).
func (p diffIndexPolicy) indexed(rel string) bool {
	if !p.enabled {
		return true
	}
	rel = filepath.ToSlash(rel)
	if vendoredPath(rel, p.vendorRules) {
		return false
	}
	return !p.ignores.Ignored(rel, false)
}

// admitFunc exposes indexed to the dependents scan, or nil when this policy has
// no opinion, which is the scan's own "admit everything" signal.
func (p diffIndexPolicy) admitFunc() func(string) bool {
	if !p.enabled {
		return nil
	}
	return p.indexed
}

// admitChangedFiles rewrites Git's changed-file list into the delta the GRAPH
// saw, which is not always the delta Git saw.
//
// Git reports what happened to a path; this command reports what happened to the
// index. The two diverge whenever a change crosses the boundary between indexed
// and unindexed — a rename into a vendored tree, a rename out of one, or a file
// whose tree re-includes it on one side only. Deciding that from the destination
// path alone gets both crossings wrong: it discards the removals the base side
// still owes a consumer, and it reports a body change against a base file that
// no snapshot contains while never naming the head file's other symbols at all.
//
// So each side is decided independently and the status is rewritten to match:
//
//	base yes, head yes   unchanged
//	base yes, head no    a deletion of the base path; its symbols leave the graph
//	base no,  head yes   an addition of the head path; all of its symbols are new
//	base no,  head no    dropped
//
// Rewriting to "D" and "A" needs no new emission code: both are ordinary
// name-status inputs, and the loop above already derives its read plan from
// Status alone. The emitted FileChange.Status is then the graph's truth rather
// than Git's — a rename out of the index IS a deletion of everything the index
// held.
//
// A copy (status "C") is the one entry that is never a comparison at all: its
// source still exists, so the destination is purely an addition and must never
// be reported as a deletion of, or a change to, the file it was copied from.
// ChangedFiles asks for --find-renames and not --find-copies, and Git emits "C"
// only under --find-copies-harder, so this is a guard against a future flag
// rather than a path exercised today.
//
// Files are omitted rather than warned about, exactly as the snapshot omits them:
// an exclusion rule is a deliberate choice, not an input the provider failed to
// read.
func admitChangedFiles(changed []gitutil.ChangedFile, base, head diffIndexPolicy) []gitutil.ChangedFile {
	kept := changed[:0]
	for _, file := range changed {
		oldPath := file.OldPath
		if oldPath == "" {
			oldPath = file.Path
		}
		inBase := file.Status != "A" && base.indexed(oldPath)
		inHead := file.Status != "D" && head.indexed(file.Path)
		if file.Status == "C" {
			// A copy's source survives, so it is not part of this delta at all
			// and the destination is simply new to the graph. That makes a copy
			// an addition whenever its destination is indexed and nothing
			// otherwise: never a deletion of a file that still exists, and never
			// a comparison against a source this change did not touch.
			if inHead {
				kept = append(kept, gitutil.ChangedFile{Status: "A", Path: file.Path})
			}
			continue
		}
		switch {
		case inBase && inHead:
			kept = append(kept, file)
		case inBase:
			kept = append(kept, gitutil.ChangedFile{Status: "D", Path: oldPath})
		case inHead:
			kept = append(kept, gitutil.ChangedFile{Status: "A", Path: file.Path})
		}
	}
	return kept
}

// indexPolicyChangeWarnings discloses the one incompleteness a change-based diff
// cannot close by filtering.
//
// A committed exclusion rule decides membership for files it never mentions in a
// commit. Deleting a `!vendor/pkg/**` re-inclusion drops that whole subtree from
// the head snapshot while Git reports only `.gitignore` as changed, so the
// correct delta contains removals for files that appear nowhere in this
// command's input. Synthesizing them would mean comparing the two trees' entire
// corpora and could emit an entry per file in a vendored tree — a different and
// unbounded operation, not a filter over a change list.
//
// What is not acceptable is letting a consumer believe the delta is complete
// when it is not, so the range says so. The check runs on Git's original list,
// before admission, because the policy file may itself be excluded.
func indexPolicyChangeWarnings(changed []gitutil.ChangedFile, policy diffIndexPolicy) []ProviderWarning {
	if !policy.enabled {
		return nil
	}
	var warnings []ProviderWarning
	for _, file := range changed {
		for _, candidate := range [...]string{file.Path, file.OldPath} {
			if candidate == "" || !isIndexPolicyFile(candidate) {
				continue
			}
			warnings = append(warnings, indexPolicyChangedWarning(candidate))
			break
		}
	}
	return warnings
}

// indexPolicyPathspecs names every committed file that can decide index
// membership, anchored at the repository root so a caller's cwd cannot narrow
// them. Git matches a wildcard pathspec at any depth, which is what nested
// .gitignore files need.
var indexPolicyPathspecs = []string{":(top)*.gitignore", ":(top)" + graphIgnoreFileName}

// isIndexPolicyFile reports whether a path is one of the committed files whose
// contents decide what the graph indexes.
//
// The two files are scoped differently, and the warning must follow that rather
// than match on name alone. Git applies a .gitignore to its own directory and
// below, and the vendored-tree rules read every one of them in the tree, so any
// of them can change membership. A .graphignore is only ever read from the
// repository root (loadExplicitIgnoreMatcher), so `pkg/.graphignore` decides
// nothing and warning about it would be a false report of incompleteness.
func isIndexPolicyFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == graphIgnoreFileName {
		return true
	}
	return path.Base(rel) == ".gitignore"
}

func indexPolicyChangedWarning(rel string) ProviderWarning {
	return ProviderWarning{
		Code:     "W_INDEX_POLICY_CHANGED",
		Severity: "warning",
		FilePath: rel,
		EffectOnCompleteness: "files that did not change may have entered or left the graph in this range; " +
			"this delta alone is not sufficient to update an index built from a snapshot",
		Detail: "an exclusion policy file changed, and it decides index membership for files it does not name",
	}
}

// newDiffIndexPolicies builds the base-side and head-side policies for one range.
//
// rootRelative gates the whole thing: when the compared trees were not named by
// commit-ish expressions, ChangedFiles' names are relative to some subtree while
// both rule sets are written against repository-root paths, and applying one to
// the other silently omits indexed files. Admitting everything there is exactly
// what this command did before the policy existed.
func newDiffIndexPolicies(
	ctx context.Context,
	repo, baseTree, headTree string,
	rootRelative bool,
	changed []gitutil.ChangedFile,
	overBudget func() bool,
) (diffIndexPolicy, diffIndexPolicy, error) {
	// Nothing has been parsed yet, so a range that is already over budget will
	// return an empty result with its budget warnings whatever the policy says.
	// Listing whole trees to build one would only make the command overshoot the
	// budget it promised the caller.
	if !rootRelative || overBudget() {
		return diffIndexPolicy{}, diffIndexPolicy{}, nil
	}
	ignores, err := loadExplicitIgnoreMatcher(repo, nil, nil)
	if err != nil {
		return diffIndexPolicy{}, diffIndexPolicy{}, err
	}
	// ignoreMatcher{} re-includes nothing, which is the correct starting rule set
	// for a range that touches no vendorable path and the exact set the probe
	// below is proved against.
	base := diffIndexPolicy{ignores: ignores, vendorRules: ignoreMatcher{}, enabled: true}
	head := base
	if !changedFilesMayBeVendored(changed) {
		return base, head, nil
	}
	baseRules, err := treeVendorIgnoreRules(ctx, repo, baseTree, ignores)
	if err != nil {
		return diffIndexPolicy{}, diffIndexPolicy{}, err
	}
	base.vendorRules = baseRules
	base.vendorRulesLoaded = true
	if overBudget() {
		// The base listing alone spent what was left. Stop before the second one
		// and let the caller's budget warnings describe the range.
		return diffIndexPolicy{}, diffIndexPolicy{}, nil
	}
	if headTree == baseTree {
		head.vendorRules = baseRules
		head.vendorRulesLoaded = true
		return base, head, nil
	}
	headRules, err := treeVendorIgnoreRules(ctx, repo, headTree, ignores)
	if err != nil {
		return diffIndexPolicy{}, diffIndexPolicy{}, err
	}
	head.vendorRules = headRules
	head.vendorRulesLoaded = true
	return base, head, nil
}

// changedFilesMayBeVendored reports whether any changed path could be excluded by
// the vendored-tree heuristic, evaluated against EMPTY re-inclusion rules.
//
// The probe is exact rather than approximate. Rules reach the heuristic only
// through ReincludesDescendant in skipVendoredDir, which can only ever
// un-vendor a path, so empty rules yield the maximal candidate set and "no
// candidate here" proves that loading the real rules would change no verdict.
// That keeps the two tree listings they cost off the overwhelmingly common range
// that touches nothing vendorable.
func changedFilesMayBeVendored(changed []gitutil.ChangedFile) bool {
	for _, file := range changed {
		if vendoredPath(filepath.ToSlash(file.Path), ignoreMatcher{}) {
			return true
		}
		if file.OldPath != "" && vendoredPath(filepath.ToSlash(file.OldPath), ignoreMatcher{}) {
			return true
		}
	}
	return false
}

// treeVendorIgnoreRules loads one compared tree's nested .gitignore rules the way
// the snapshot loads its own: from that exact tree, through the same bounded
// reader, so the diff and a snapshot of the same revision cannot disagree about
// which vendored trees a project meant to keep.
func treeVendorIgnoreRules(ctx context.Context, repo, tree string, policyBase ignoreMatcher) (vendorIgnoreRules, error) {
	paths, err := gitutil.ListFiles(ctx, repo, tree)
	if err != nil {
		return nil, err
	}
	return headVendorIgnoreRules(ctx, repo, tree, paths, policyBase)
}

// pathScopeChange builds a synthetic change for a file that Git reported as a
// rename or copy but whose content is byte-identical on both sides, so
// moduleScopeChange found nothing to report. Without it the file is dropped and
// a 100%-similarity rename produces an empty diff — indistinguishable from "no
// change at all". That is wrong for the command's own contract: the file path
// is a component of every compound-v1 symbol ID (see symbolID), so moving a
// file re-identifies every entity in it. The change reuses the module-scope
// entity and the existing "moved" reconciliation shape, which already carries
// OldPath/NewPath, rather than adding a new change type.
// The caller normalizes an empty OldPath to path before calling, so the empty
// check below cannot fire today; it is kept so a second caller passing Git's raw
// OldPath cannot turn "not a rename" into a move with no source.
func pathScopeChange(oldPath, path string) (EntityChange, bool) {
	if oldPath == "" || oldPath == path {
		return EntityChange{}, false
	}
	return EntityChange{
		Type:            "moved",
		Kind:            moduleKind,
		Name:            path,
		OldPath:         oldPath,
		NewPath:         path,
		BeforeStartLine: 1,
		AfterStartLine:  1,
		Similarity:      1,
		Reconciliation:  "MOVED",
	}, true
}

// moduleScopeChange builds a synthetic change for a file whose contents changed
// but where no named symbol changed. Attributing the edit to a module-level
// entity (keyed by the file path) keeps module-scope changes visible in the diff
// rather than emitting a null/empty entry. It returns false when there is no
// observable content change to report (e.g. a pure rename).
func moduleScopeChange(path, before, after string, beforeOK, afterOK bool) (EntityChange, bool) {
	switch {
	case afterOK && !beforeOK:
		return EntityChange{Type: "added", Kind: moduleKind, Name: path, AfterStartLine: 1}, true
	case beforeOK && !afterOK:
		return EntityChange{Type: "removed", Kind: moduleKind, Name: path, BeforeStartLine: 1}, true
	case beforeOK && afterOK && before != after:
		return EntityChange{Type: "body_changed", Kind: moduleKind, Name: path, BeforeStartLine: 1, AfterStartLine: 1}, true
	default:
		return EntityChange{}, false
	}
}

func removedChange(oldEntity Entity) EntityChange {
	return EntityChange{
		Type:            "removed",
		Kind:            oldEntity.Kind,
		Name:            oldEntity.Name,
		OldSignature:    oldEntity.Signature,
		BeforeStartLine: oldEntity.StartLine,
	}
}

func addedChange(newEntity Entity) EntityChange {
	return EntityChange{
		Type:           "added",
		Kind:           newEntity.Kind,
		Name:           newEntity.Name,
		NewSignature:   newEntity.Signature,
		AfterStartLine: newEntity.StartLine,
	}
}

func sortedEntities(byKey map[string]Entity) []Entity {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Entity, 0, len(byKey))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

func sortChanges(changes []EntityChange) {
	sort.Slice(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		if leftLine, rightLine := lineForSort(left), lineForSort(right); leftLine != rightLine {
			return leftLine < rightLine
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.OldSignature != right.OldSignature {
			return left.OldSignature < right.OldSignature
		}
		if left.NewSignature != right.NewSignature {
			return left.NewSignature < right.NewSignature
		}
		if left.BeforeStartLine != right.BeforeStartLine {
			return left.BeforeStartLine < right.BeforeStartLine
		}
		if left.AfterStartLine != right.AfterStartLine {
			return left.AfterStartLine < right.AfterStartLine
		}
		if left.OldName != right.OldName {
			return left.OldName < right.OldName
		}
		if left.NewName != right.NewName {
			return left.NewName < right.NewName
		}
		if left.OldPath != right.OldPath {
			return left.OldPath < right.OldPath
		}
		if left.NewPath != right.NewPath {
			return left.NewPath < right.NewPath
		}
		if left.Reconciliation != right.Reconciliation {
			return left.Reconciliation < right.Reconciliation
		}
		if left.Similarity != right.Similarity {
			return left.Similarity < right.Similarity
		}
		return left.DependentsCount < right.DependentsCount
	})
}

// keyedEntityMaps assigns each entity an ephemeral key such that entities that
// should be diffed against each other receive the same key on both sides. The
// keys are opaque: they are used only inside compareEntities/bestRename and are
// never persisted or emitted.
//
// Matching is evidence-first within each Kind:Name group:
//
//  1. Exact-signature entities pair as content multisets: combined body and
//     fingerprint evidence first, then body hash, then fingerprint. Members
//     of an equal-content class are interchangeable, so repeated hashes pair
//     safely up to the count present on both sides. This keeps an inserted
//     exact-signature duplicate from shifting every surviving duplicate.
//
//  2. Remaining exact signatures pair by occurrence before leftovers take
//     unambiguous fingerprint/body anchors across signatures. This prevents
//     copied or swapped bodies from stealing an unchanged signature, while the
//     identity anchors preserve the right survivor when one overload is removed
//     as another changes signature. A final positional fallback still reports
//     an otherwise-unanchored in-place signature edit as signature_changed
//     rather than remove+add (issue #35). Multiple residuals without identity
//     evidence remain inherently ambiguous; their positional pairing is
//     retained as a compatibility heuristic.
//
// Numeric pair keys and side-specific unmatched keys avoid delimiter aliasing
// with source-language names and signatures.
func keyedEntityMaps(before, after []Entity) (map[string]Entity, map[string]Entity) {
	type groupKey struct {
		kind string
		name string
	}
	type entityGroup struct {
		before []int
		after  []int
	}

	groups := map[groupKey]*entityGroup{}
	groupFor := func(entity Entity) *entityGroup {
		key := groupKey{kind: entity.Kind, name: entity.Name}
		group := groups[key]
		if group == nil {
			group = &entityGroup{}
			groups[key] = group
		}
		return group
	}
	for i, entity := range before {
		group := groupFor(entity)
		group.before = append(group.before, i)
	}
	for i, entity := range after {
		group := groupFor(entity)
		group.after = append(group.after, i)
	}

	groupKeys := make([]groupKey, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		if groupKeys[i].kind != groupKeys[j].kind {
			return groupKeys[i].kind < groupKeys[j].kind
		}
		return groupKeys[i].name < groupKeys[j].name
	})

	beforeByKey := make(map[string]Entity, len(before))
	afterByKey := make(map[string]Entity, len(after))
	beforeMatched := make([]bool, len(before))
	afterMatched := make([]bool, len(after))
	pairCount := 0

	pair := func(beforeIndex, afterIndex int) {
		key := fmt.Sprintf("=%09d", pairCount)
		pairCount++
		beforeMatched[beforeIndex] = true
		afterMatched[afterIndex] = true
		beforeByKey[key] = before[beforeIndex]
		afterByKey[key] = after[afterIndex]
	}
	type evidenceKey struct {
		signature   string
		bodyHash    string
		fingerprint string
	}
	matchClass := func(group *entityGroup, keyFor func(Entity) (evidenceKey, bool)) {
		afterBuckets := map[evidenceKey][]int{}
		for _, afterIndex := range group.after {
			if afterMatched[afterIndex] {
				continue
			}
			key, ok := keyFor(after[afterIndex])
			if ok {
				afterBuckets[key] = append(afterBuckets[key], afterIndex)
			}
		}
		afterOffsets := map[evidenceKey]int{}
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] {
				continue
			}
			key, ok := keyFor(before[beforeIndex])
			if !ok {
				continue
			}
			offset := afterOffsets[key]
			if offset >= len(afterBuckets[key]) {
				continue
			}
			pair(beforeIndex, afterBuckets[key][offset])
			afterOffsets[key]++
		}
	}
	matchUnique := func(group *entityGroup, keyFor func(Entity) (evidenceKey, bool)) {
		beforeCounts := map[evidenceKey]int{}
		afterCounts := map[evidenceKey]int{}
		afterIndexByKey := map[evidenceKey]int{}
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] {
				continue
			}
			if key, ok := keyFor(before[beforeIndex]); ok {
				beforeCounts[key]++
			}
		}
		for _, afterIndex := range group.after {
			if afterMatched[afterIndex] {
				continue
			}
			if key, ok := keyFor(after[afterIndex]); ok {
				afterCounts[key]++
				afterIndexByKey[key] = afterIndex
			}
		}
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] {
				continue
			}
			key, ok := keyFor(before[beforeIndex])
			if ok && beforeCounts[key] == 1 && afterCounts[key] == 1 {
				pair(beforeIndex, afterIndexByKey[key])
			}
		}
	}
	matchRemaining := func(group *entityGroup) {
		afterRemaining := make([]int, 0, len(group.after))
		for _, afterIndex := range group.after {
			if !afterMatched[afterIndex] {
				afterRemaining = append(afterRemaining, afterIndex)
			}
		}
		afterOffset := 0
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] || afterOffset >= len(afterRemaining) {
				continue
			}
			pair(beforeIndex, afterRemaining[afterOffset])
			afterOffset++
		}
	}

	for _, key := range groupKeys {
		group := groups[key]
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			ok := entity.BodyHash != "" && entity.Fingerprint != ""
			return evidenceKey{signature: entity.Signature, bodyHash: entity.BodyHash, fingerprint: entity.Fingerprint}, ok
		})
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{signature: entity.Signature, bodyHash: entity.BodyHash}, entity.BodyHash != ""
		})
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{signature: entity.Signature, fingerprint: entity.Fingerprint}, entity.Fingerprint != ""
		})
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{signature: entity.Signature}, true
		})
		matchUnique(group, func(entity Entity) (evidenceKey, bool) {
			ok := entity.BodyHash != "" && entity.Fingerprint != ""
			return evidenceKey{bodyHash: entity.BodyHash, fingerprint: entity.Fingerprint}, ok
		})
		matchUnique(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{fingerprint: entity.Fingerprint}, entity.Fingerprint != ""
		})
		matchUnique(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{bodyHash: entity.BodyHash}, entity.BodyHash != ""
		})
		matchRemaining(group)
	}

	for i, entity := range before {
		if !beforeMatched[i] {
			beforeByKey[fmt.Sprintf("-before:%09d", i)] = entity
		}
	}
	for i, entity := range after {
		if !afterMatched[i] {
			afterByKey[fmt.Sprintf("+after:%09d", i)] = entity
		}
	}
	return beforeByKey, afterByKey
}

func lineForSort(change EntityChange) int {
	if change.AfterStartLine > 0 {
		return change.AfterStartLine
	}
	return change.BeforeStartLine
}

func bestRename(old Entity, added map[string]Entity) (string, Entity, float64) {
	var bestKey string
	var best Entity
	var bestScore float64
	for key, candidate := range added {
		if candidate.Kind != old.Kind {
			continue
		}
		score := similarity(old, candidate)
		if score > bestScore {
			bestKey = key
			best = candidate
			bestScore = score
		}
	}
	return bestKey, best, bestScore
}

func similarity(a, b Entity) float64 {
	if a.Fingerprint != "" && a.Fingerprint == b.Fingerprint {
		return 1
	}
	if a.BodyHash != "" && a.BodyHash == b.BodyHash {
		return 0.97
	}
	return jaccard(a.Signature, b.Signature)
}

func jaccard(a, b string) float64 {
	left := tokenSet(a)
	right := tokenSet(b)
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	var intersection int
	for token := range left {
		if right[token] {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return math.Round((float64(intersection)/float64(union))*100) / 100
}

func tokenSet(value string) map[string]bool {
	out := map[string]bool{}
	token := ""
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			token += string(r)
			continue
		}
		if token != "" {
			out[token] = true
			token = ""
		}
	}
	if token != "" {
		out[token] = true
	}
	return out
}

// extensionUnsupported reports whether the path carries an extension and that extension has
// no supported parser. Extensionless files return false — they may still route to a parser
// via shebang, which requires reading content.
func extensionUnsupported(path string) bool {
	if filepath.Ext(path) == "" {
		return false
	}
	_, ok := languageForContent(path, "")
	return !ok
}
