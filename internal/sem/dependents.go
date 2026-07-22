package sem

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

var identifierBoundary = regexp.MustCompile(`[A-Za-z0-9_$]+`)

type referenceIndex map[string]map[string]struct{}

func addDependentCounts(ctx context.Context, repo, head string, result *Result) error {
	names := changedReferenceNames(*result)
	if len(names) == 0 {
		return nil
	}

	index, warnings, err := buildReferenceIndex(ctx, repo, head, names)
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
	index := referenceIndex{}
	for name := range names {
		index[name] = map[string]struct{}{}
	}
	if len(names) == 0 {
		return index, nil, nil
	}

	files, prefiltered, warnings, err := referenceCandidateFiles(ctx, repo, head, names)
	if err != nil {
		return nil, nil, err
	}

	// When the grep prefilter ran, every file below already matched a changed
	// name, so every skip is worth warning about. On the fallback full-tree
	// scan, apply the same candidate test in-process before warning, so the
	// fallback does not spray warnings about files (e.g. huge vendored blobs)
	// that contain no changed name and were never real candidates.
	isCandidate := func(content string) bool {
		return prefiltered || containsAnyNameFold(content, names)
	}

	parser := TreeSitterParser{}
	for _, path := range files {
		if !Supported(path) {
			continue
		}
		content, ok, err := gitutil.ShowFile(ctx, repo, head, path)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
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

		entities, _, status := parser.ParseWithStatus(path, content)
		if status.ParseError && isCandidate(content) {
			warnings = append(warnings, dependentsParseFailureWarning(path, status))
		}
		lines := strings.Split(content, "\n")
		for _, entity := range entities {
			block := entityBlock(lines, entity)
			for name := range names {
				if shortEntityName(entity.Name) == name {
					continue
				}
				if containsIdentifier(block, name) {
					index[name][path+"#"+entity.Kind+":"+entity.Name] = struct{}{}
				}
			}
		}
	}

	return index, warnings, nil
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
// git grep's fixed-string, case-insensitive substring search as a
// preselection pass. That test is a strict superset of containsIdentifier's
// case-sensitive whole-token check -- a case-sensitive substring is always
// also a case-insensitive one -- so it can only add extra candidate files,
// never drop a real dependent; the per-entity containsIdentifier check below
// still runs unchanged. It uses GrepTreePathsIncludingBinary rather than
// GrepTreePaths specifically to preserve that superset guarantee for files
// Git itself classifies as binary (an embedded NUL byte, or a
// `.gitattributes` binary/-diff marking): a Supported source file flagged
// binary is still real source that gets parsed below, so the prefilter must
// not silently drop it. If the grep call itself fails for any reason, fall
// back to scanning every file in the tree so a git-grep quirk never silently
// zeroes out dependent counts, and surface exactly one warning noting the
// prefilter failure so the fallback (much slower) scan is not silent.
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
		matches, grepErr := gitutil.GrepTreePathsIncludingBinary(ctx, repo, head, patterns)
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

// containsAnyNameFold mirrors the git-grep prefilter's case-insensitive
// substring test (grep -i -F) in-process, so the fallback full-tree scan can
// warn about exactly the files the prefiltered path would have surfaced as
// candidates. Go's Unicode case folding and git's can differ at the margins
// for non-ASCII names, but this only gates warnings -- dependent counting is
// unaffected because the per-entity containsIdentifier check runs on every
// scanned file either way.
func containsAnyNameFold(content string, names map[string]struct{}) bool {
	lower := strings.ToLower(content)
	for name := range names {
		if name == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(name)) {
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

func containsIdentifier(content, name string) bool {
	for _, token := range identifierBoundary.FindAllString(content, -1) {
		if token == name {
			return true
		}
	}
	return false
}

func identifiersIn(content string) map[string]struct{} {
	identifiers := map[string]struct{}{}
	for _, token := range identifierBoundary.FindAllString(content, -1) {
		identifiers[token] = struct{}{}
	}
	return identifiers
}

func referenceName(change EntityChange) string {
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
