package sem

import (
	"path/filepath"
	"strings"
)

// dedupeSemanticMirrorCandidates removes redundant search regions emitted for
// checked-in generated or runtime mirrors of an authored symbol. It is
// deliberately conservative: candidates must have an exact semantic
// fingerprint and their paths must look related. A directory name alone never
// excludes a result, so first-party code kept in lib/, dist/, or build/ remains
// searchable when it is not an exact copy of another result.
func dedupeSemanticMirrorCandidates(candidates []searchCandidate, q searchQuery, symbolsByID map[string]SymbolRecord) []searchCandidate {
	if len(candidates) < 2 {
		return candidates
	}

	out := make([]searchCandidate, 0, len(candidates))
	byFingerprint := make(map[string][]int)
	for _, candidate := range candidates {
		fingerprint, ok := searchMirrorFingerprint(candidate.result, symbolsByID)
		if !ok {
			out = append(out, candidate)
			continue
		}

		duplicateIndex := -1
		for _, index := range byFingerprint[fingerprint] {
			if searchMirrorPathPair(out[index].result.FilePath, candidate.result.FilePath) &&
				!searchMirrorQueryDistinguishesPaths(q, out[index].result.FilePath, candidate.result.FilePath) {
				duplicateIndex = index
				break
			}
		}
		if duplicateIndex < 0 {
			byFingerprint[fingerprint] = append(byFingerprint[fingerprint], len(out))
			out = append(out, candidate)
			continue
		}

		kept := out[duplicateIndex]
		if canonicalSearchPathLess(candidate.result.FilePath, kept.result.FilePath) {
			candidate.score = maxFloat64(candidate.score, kept.score)
			candidate.baseScore = maxFloat64(candidate.baseScore, kept.baseScore)
			candidate.result.Signals = appendUnique(candidate.result.Signals, kept.result.Signals...)
			out[duplicateIndex] = candidate
		} else {
			kept.score = maxFloat64(kept.score, candidate.score)
			kept.baseScore = maxFloat64(kept.baseScore, candidate.baseScore)
			kept.result.Signals = appendUnique(kept.result.Signals, candidate.result.Signals...)
			out[duplicateIndex] = kept
		}
	}
	return out
}

// searchMirrorQueryDistinguishesPaths preserves both copies when the query
// itself names a path-specific variant (for example, asks specifically about
// a dist or generated tree). Ordinary symbol queries give equivalent paths the
// same score and are still deduplicated.
func searchMirrorQueryDistinguishesPaths(q searchQuery, left, right string) bool {
	return pathSearchScore(q, left) != pathSearchScore(q, right)
}

func searchMirrorFingerprint(result SearchResult, symbolsByID map[string]SymbolRecord) (string, bool) {
	language := strings.ToLower(strings.TrimSpace(result.Language))
	kind := strings.ToLower(strings.TrimSpace(result.Kind))
	qualified := normalize(result.QualifiedName)
	if qualified == "" {
		qualified = normalize(result.SymbolName)
	}
	signature := normalize(result.Signature)
	if symbol, ok := symbolsByID[result.SymbolID]; ok {
		if language == "" {
			language = strings.ToLower(strings.TrimSpace(symbol.Language))
		}
		if kind == "" {
			kind = strings.ToLower(strings.TrimSpace(symbol.Kind))
		}
		if qualified == "" {
			qualified = normalize(symbol.QualifiedName)
			if qualified == "" {
				qualified = normalize(symbol.Name)
			}
		}
		if signature == "" {
			signature = normalize(symbol.Signature)
		}
		// BodyHash is the parser's exact normalized implementation identity. A
		// few declaration kinds do not expose a useful signature, but language,
		// kind, qualified name, and body hash still form a strong fingerprint.
		if language != "" && kind != "" && qualified != "" && symbol.BodyHash != "" {
			return strings.Join([]string{language, kind, qualified, signature, symbol.BodyHash}, "\x00"), true
		}
	}

	// Some search candidates come from sparse or derived regions and may no
	// longer carry their SymbolRecord. Fall back only when all strong public
	// identity fields and the exact normalized snippet are available.
	if language == "" || kind == "" || qualified == "" || signature == "" || result.Snippet == "" {
		return "", false
	}
	return strings.Join([]string{language, kind, qualified, signature, hash(normalize(result.Snippet))}, "\x00"), true
}

func searchMirrorPathPair(left, right string) bool {
	left = filepath.ToSlash(filepath.Clean(left))
	right = filepath.ToSlash(filepath.Clean(right))
	if left == right || !strings.EqualFold(filepath.Base(left), filepath.Base(right)) {
		return false
	}
	// A generated-directory cue describes a tree within one package; it is
	// not evidence that two sibling monorepo packages own the same source.
	// Preserve exact copies across those package boundaries, including when
	// one sibling uses src/ and the other checks in dist/ output.
	leftUnit, rightUnit := searchMonorepoUnit(left), searchMonorepoUnit(right)
	if leftUnit != "" && rightUnit != "" && leftUnit != rightUnit {
		return false
	}
	if hasGeneratedSearchPathCue(left) || hasGeneratedSearchPathCue(right) {
		return true
	}
	if hasAuthoredSearchPathCue(left) != hasAuthoredSearchPathCue(right) {
		return true
	}
	if commonSearchPathSuffixSegments(left, right) < 2 {
		return false
	}
	// A shared source-relative tail is mirror evidence only when one path has
	// an authored-source anchor and the other does not (src/helpers/x versus a
	// runtime/lib/helpers/x copy), or when one complete path is nested beneath
	// an extra wrapper. Two monorepo packages may legitimately have identical
	// src/services/x files and must not be merged merely for sharing a suffix.
	return searchPathIsStrictSuffix(left, right) || searchPathIsStrictSuffix(right, left)
}

func searchMonorepoUnit(filePath string) string {
	parts := strings.Split(strings.Trim(strings.ToLower(filepath.ToSlash(filepath.Clean(filePath))), "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		switch parts[index] {
		case "apps", "crates", "modules", "packages", "plugins", "workspaces":
			end := index + 2
			if strings.HasPrefix(parts[index+1], "@") && end < len(parts) {
				end++
			}
			return strings.Join(parts[:end], "/")
		}
	}
	return ""
}

func commonSearchPathSuffixSegments(left, right string) int {
	leftParts := strings.Split(strings.Trim(left, "/"), "/")
	rightParts := strings.Split(strings.Trim(right, "/"), "/")
	count := 0
	for leftIndex, rightIndex := len(leftParts)-1, len(rightParts)-1; leftIndex >= 0 && rightIndex >= 0; leftIndex, rightIndex = leftIndex-1, rightIndex-1 {
		if !strings.EqualFold(leftParts[leftIndex], rightParts[rightIndex]) {
			break
		}
		count++
	}
	return count
}

func hasGeneratedSearchPathCue(filePath string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(filePath)), "/") {
		switch part {
		case "generated", "generated-src", "generated-sources", "gen", "autogen", "codegen", "build", "dist", "out", "output":
			return true
		}
	}
	return false
}

func hasAuthoredSearchPathCue(filePath string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(filePath)), "/") {
		switch part {
		case "src", "source", "sources", "include":
			return true
		}
	}
	return false
}

func searchPathIsStrictSuffix(shorter, longer string) bool {
	shorterParts := strings.Split(strings.Trim(filepath.ToSlash(shorter), "/"), "/")
	longerParts := strings.Split(strings.Trim(filepath.ToSlash(longer), "/"), "/")
	if len(shorterParts) >= len(longerParts) {
		return false
	}
	for offset := 1; offset <= len(shorterParts); offset++ {
		if !strings.EqualFold(shorterParts[len(shorterParts)-offset], longerParts[len(longerParts)-offset]) {
			return false
		}
	}
	return true
}

func canonicalSearchPathLess(left, right string) bool {
	leftPenalty, leftDepth := searchMirrorPathPenalty(left)
	rightPenalty, rightDepth := searchMirrorPathPenalty(right)
	if leftPenalty != rightPenalty {
		return leftPenalty < rightPenalty
	}
	if leftDepth != rightDepth {
		return leftDepth < rightDepth
	}
	return filepath.ToSlash(left) < filepath.ToSlash(right)
}

func searchMirrorPathPenalty(filePath string) (penalty, depth int) {
	parts := strings.Split(strings.Trim(filepath.ToSlash(filepath.Clean(filePath)), "/"), "/")
	depth = len(parts)
	for _, rawPart := range parts[:maxInt(0, len(parts)-1)] {
		part := strings.ToLower(rawPart)
		switch part {
		case "generated", "generated-src", "generated-sources", "gen", "autogen", "codegen":
			penalty += 100
		case "build", "dist", "out", "output":
			penalty += 30
		case "src", "source", "sources", "include":
			penalty -= 20
		}
	}
	return penalty, depth
}
