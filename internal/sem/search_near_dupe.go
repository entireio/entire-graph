package sem

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Near-duplicate collapsing
// =========================
//
// Repositories carry copies of the same content on purpose: versioned
// documentation trees (`version-2.x/x` next to `version-3.0.1/x`), vendored
// snapshots, generated mirrors. Every copy scores identically, so a single
// document can occupy the whole result budget and push the real fix site off
// the page.
//
// A copy is collapsed into the best-scoring survivor rather than dropped
// silently: the survivor advertises `+N similar` so the caller knows other
// copies exist. The rules are deliberately conservative, because collapsing two
// genuinely different sites would cost recall:
//
//  1. the two candidates must live in DIFFERENT files with the SAME basename;
//  2. they must not belong to different monorepo units (packages/a vs
//     packages/b legitimately own same-named files) — same rule the semantic
//     mirror dedupe already applies;
//  3. and either their normalized region text is IDENTICAL, or their paths are
//     equal modulo version-like segments AND their region text is >= 90%
//     token-identical.
//
// Regions shorter than searchNearDuplicateMinChars are never collapsed: a few
// lines of boilerplate coincide across unrelated files all the time.
const (
	searchNearDuplicateMinChars = 96
	searchNearDuplicateJaccard  = 0.9
)

type searchNearDuplicateEntry struct {
	outIndex int
	path     string
	tokens   map[string]struct{}
}

// collapseNearDuplicateCandidates expects candidates in descending score order
// so the survivor of each collapse group is its best-ranked member.
func collapseNearDuplicateCandidates(candidates []searchCandidate) []searchCandidate {
	if len(candidates) < 2 {
		return candidates
	}
	out := make([]searchCandidate, 0, len(candidates))
	collapsed := make([]int, 0, len(candidates))
	byText := make(map[string][]searchNearDuplicateEntry)
	byVersionPath := make(map[string][]searchNearDuplicateEntry)

	for _, candidate := range candidates {
		text := normalizeSearchDuplicateText(candidate.result.Snippet)
		if len(text) < searchNearDuplicateMinChars {
			out = append(out, candidate)
			collapsed = append(collapsed, 0)
			continue
		}
		path := filepath.ToSlash(candidate.result.FilePath)
		versionKey, hasVersion := versionAgnosticSearchPath(path)

		match := -1
		for _, entry := range byText[text] {
			if searchNearDuplicatePathPair(entry.path, path) {
				match = entry.outIndex
				break
			}
		}
		var tokens map[string]struct{}
		if match < 0 && hasVersion {
			tokens = searchDuplicateTokens(text)
			for _, entry := range byVersionPath[versionKey] {
				if !searchNearDuplicatePathPair(entry.path, path) {
					continue
				}
				if searchTokenJaccard(entry.tokens, tokens) >= searchNearDuplicateJaccard {
					match = entry.outIndex
					break
				}
			}
		}
		if match >= 0 {
			collapsed[match]++
			continue
		}

		if tokens == nil {
			tokens = searchDuplicateTokens(text)
		}
		entry := searchNearDuplicateEntry{outIndex: len(out), path: path, tokens: tokens}
		byText[text] = append(byText[text], entry)
		if hasVersion {
			byVersionPath[versionKey] = append(byVersionPath[versionKey], entry)
		}
		out = append(out, candidate)
		collapsed = append(collapsed, 0)
	}

	for index := range out {
		if collapsed[index] > 0 {
			out[index].result.Signals = appendUnique(
				out[index].result.Signals, fmt.Sprintf("+%d similar", collapsed[index]),
			)
		}
	}
	return out
}

// searchNearDuplicatePathPair reports whether two paths are plausibly copies of
// one another: distinct files, same basename, and not two different units of the
// same monorepo.
func searchNearDuplicatePathPair(left, right string) bool {
	left = filepath.ToSlash(filepath.Clean(left))
	right = filepath.ToSlash(filepath.Clean(right))
	if left == right || !strings.EqualFold(filepath.Base(left), filepath.Base(right)) {
		return false
	}
	leftUnit, rightUnit := searchMonorepoUnit(left), searchMonorepoUnit(right)
	return leftUnit == "" || rightUnit == "" || leftUnit == rightUnit
}

// versionAgnosticSearchPath replaces version-like path segments with a
// placeholder. The second return value reports whether any segment matched, so
// callers can skip the rule entirely for paths that carry no version at all.
func versionAgnosticSearchPath(filePath string) (string, bool) {
	parts := strings.Split(strings.Trim(filepath.ToSlash(filePath), "/"), "/")
	found := false
	for index, part := range parts {
		if index == len(parts)-1 {
			break
		}
		if searchVersionPathSegment(part) {
			parts[index] = "*"
			found = true
		}
	}
	return strings.Join(parts, "/"), found
}

// searchVersionPathSegment recognizes the directory names repositories use to
// hold a second copy of the same content at another version.
func searchVersionPathSegment(segment string) bool {
	lower := strings.ToLower(segment)
	switch lower {
	case "latest", "next", "stable", "current", "unreleased":
		return true
	}
	for _, prefix := range []string{"version-", "version_", "version", "release-", "release_", "release", "rel-", "v"} {
		if strings.HasPrefix(lower, prefix) && len(lower) > len(prefix) &&
			searchVersionNumberSegment(lower[len(prefix):]) {
			return true
		}
	}
	return searchVersionNumberSegment(lower)
}

// searchVersionNumberSegment matches "2", "3.0.1", "2.x", "1_4" and similar.
// At least one digit is required, so "x" or "next-gen" never qualifies.
func searchVersionNumberSegment(value string) bool {
	if value == "" {
		return false
	}
	digits := false
	for index := 0; index < len(value); index++ {
		switch character := value[index]; {
		case character >= '0' && character <= '9':
			digits = true
		case character == '.' || character == '-' || character == '_':
		case character == 'x':
		default:
			return false
		}
	}
	return digits
}

// normalizeSearchDuplicateText lowercases a region and collapses every run of
// whitespace so indentation or line-wrapping differences do not hide a copy.
func normalizeSearchDuplicateText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func searchDuplicateTokens(normalized string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(normalized, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_'
	}) {
		if len(token) >= 2 {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func searchTokenJaccard(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	smaller, larger := left, right
	if len(larger) < len(smaller) {
		smaller, larger = larger, smaller
	}
	intersection := 0
	for token := range smaller {
		if _, ok := larger[token]; ok {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
