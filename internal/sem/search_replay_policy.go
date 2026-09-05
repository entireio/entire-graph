package sem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

const searchReplayPolicyVersion = "search-replay-policy-v4"

const (
	searchReplayViewHead = "head"

	// SearchReplayMaxPathCount, SearchReplayMaxPathBytes and
	// SearchReplayMaxAggregatePathBytes bound persisted provenance before it can
	// drive filesystem traversal or Git subprocess input. They are exported so
	// the session reader can reject hostile records before policy resolution.
	SearchReplayMaxPathCount          = 1024
	SearchReplayMaxPathBytes          = 4096
	SearchReplayMaxAggregatePathBytes = 128 << 10

	// This is a second-order bound on ancestor walks and nested-ignore reads.
	// Ordinary repository paths are nowhere near this depth.
	searchReplayMaxPathComponents = 128
)

// SearchReplayPolicy is the exclusion policy a persisted search payload must
// still satisfy before it can be replayed. Its representation is deliberately
// opaque: callers compare Fingerprint values and ask whether recorded paths are
// still admissible instead of recreating ignore semantics outside sem. A zero
// value is the deliberate result for every mutable worktree view.
type SearchReplayPolicy struct {
	repo        string
	commit      string
	tree        string
	ignores     ignoreMatcher
	fingerprint string
	ctx         context.Context
}

// ResolveSearchReplayPolicy assembles the immutable HEAD policy for a persisted
// search payload. Worktree views, including a real search's no-HEAD fallback,
// return a zero policy because mutable source cannot be pinned through final
// admission and output. The fingerprint binds the ordered, parsed rule semantics
// rather than merely the rule-file paths.
func ResolveSearchReplayPolicy(ctx context.Context, repo string, options SearchOptions) (SearchReplayPolicy, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return SearchReplayPolicy{}, err
	}
	// A mutable worktree cannot be pinned through final path admission and output,
	// so no worktree policy can authorize replay. Return before loading policy or
	// enumerating a corpus that MatchesTree would categorically reject.
	if options.Worktree {
		return SearchReplayPolicy{}, nil
	}
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		// A real search may fall back to the worktree, but that mutable view remains
		// deliberately non-replayable.
		return SearchReplayPolicy{}, nil
	}
	ignores, err := loadExplicitIgnoreMatcher(absRepo, options.IgnoreFiles, options.IncludeFiles)
	if err != nil {
		return SearchReplayPolicy{}, err
	}
	effectiveMaxFiles := resolveMaxSourceFiles(0)
	policyParts := []string{"max-files=" + strconv.Itoa(effectiveMaxFiles)}

	return SearchReplayPolicy{
		repo:        absRepo,
		commit:      commit,
		tree:        tree,
		ignores:     ignores,
		fingerprint: searchReplayPolicyFingerprint(searchReplayViewHead, ignores, policyParts...),
		ctx:         ctx,
	}, nil
}

// Fingerprint identifies the policy semantics that govern a replay. It is
// stable for equivalent parsed rules, sensitive to rule order, and non-empty
// only for a successfully resolved policy.
func (p SearchReplayPolicy) Fingerprint() string {
	return p.fingerprint
}

// MatchesTree reports whether a search result's tree observation belongs to a
// persistable immutable repository view. Worktree state cannot be pinned across
// the final admission-to-output interval: a newly written `.git` pointer can
// turn an already-cached path into Git-directory content after every check.
// Worktree policies therefore never authorize persisted replay. Committed views
// must match the exact tree resolved with the commit used for every probe.
func (p SearchReplayPolicy) MatchesTree(tree string) bool {
	if p.fingerprint == "" {
		return false
	}
	return p.tree != "" && p.tree == tree
}

// ValidateSearchReplayPaths applies the hard provenance shape and size limits
// without consulting a repository. Callers may use it immediately after
// decoding a session; AllowsReplayPaths repeats it before any expensive work.
func ValidateSearchReplayPaths(paths []string) error {
	if len(paths) > SearchReplayMaxPathCount {
		return fmt.Errorf("search replay provenance has %d paths; maximum is %d", len(paths), SearchReplayMaxPathCount)
	}
	total := 0
	for _, value := range paths {
		if len(value) > SearchReplayMaxPathBytes {
			return fmt.Errorf("search replay path has %d bytes; maximum is %d", len(value), SearchReplayMaxPathBytes)
		}
		if len(value) > SearchReplayMaxAggregatePathBytes-total {
			return fmt.Errorf("search replay provenance exceeds %d aggregate path bytes", SearchReplayMaxAggregatePathBytes)
		}
		total += len(value)
		cleaned, ok := cleanSearchReplayPath(value)
		if !ok {
			return fmt.Errorf("search replay provenance contains an invalid repository path")
		}
		if strings.Count(cleaned, "/")+1 > searchReplayMaxPathComponents {
			return fmt.Errorf("search replay path has too many components")
		}
	}
	return nil
}

// AllowsReplayPaths reports whether every path recorded as contributing to a
// payload remains admissible. Invalid, absolute, or traversal paths always
// reject replay. Paths must remain literal members of the exact resolved
// commit and may not cross the provider's ignore or vendored filters.
func (p SearchReplayPolicy) AllowsReplayPaths(paths []string) bool {
	if p.fingerprint == "" || p.repo == "" || ValidateSearchReplayPaths(paths) != nil {
		return false
	}
	cleaned := make([]string, 0, len(paths))
	for _, value := range paths {
		rel, _ := cleanSearchReplayPath(value)
		cleaned = append(cleaned, rel)
		if p.ignores.Ignored(rel, false) {
			return false
		}
	}
	if p.ctx == nil {
		// Fail closed rather than root a fresh context here. The Git probes
		// below run under the caller's wall-clock budget; a context.Background
		// fallback would start subprocesses no deadline can reach, which is the
		// exact shape TestTF142R6NoUnbudgetedParseContext exists to keep out of
		// this package. ResolveSearchReplayPolicy always captures a context, so
		// the only policies that reach this line were assembled outside it and
		// have no caller to be bounded by.
		return false
	}
	return p.allowsHeadReplayPaths(p.ctx, cleaned)
}

func (p SearchReplayPolicy) allowsHeadReplayPaths(ctx context.Context, paths []string) bool {
	if p.commit == "" {
		return false
	}
	// A policy may outlive its construction call. Revalidate immediately before
	// its later Git probes so a metadata redirect introduced in between cannot
	// turn replay admission into filesystem egress.
	if EnsureGitMetadataSafeForSubprocess(p.repo) != nil {
		return false
	}
	if len(paths) == 0 {
		return true
	}
	members, err := gitutil.TreeContainsPaths(ctx, p.repo, p.commit, paths)
	if err != nil {
		return false
	}
	for _, rel := range paths {
		if _, exists := members[rel]; !exists {
			return false
		}
	}

	ancestors, ok := searchReplayNestedIgnoreCandidates(paths)
	if !ok {
		return false
	}
	var selected []string
	if len(ancestors) > 0 {
		selected, err = gitutil.BoundedTreeNestedIgnorePaths(ctx, p.repo, p.commit, maxNestedIgnoreFiles)
		if err != nil {
			return false
		}
	}
	candidates := selectedSearchReplayAncestorIgnores(ancestors, selected)
	vendorRules, err := loadHeadNestedIgnoreRules(ctx, p.repo, p.commit, candidates, p.ignores)
	if err != nil {
		return false
	}
	for _, rel := range paths {
		if vendoredPath(rel, vendorRules) {
			return false
		}
	}
	return true
}

func selectedSearchReplayAncestorIgnores(ancestors, selected []string) []string {
	wanted := make(map[string]struct{}, len(ancestors))
	for _, candidate := range ancestors {
		wanted[candidate] = struct{}{}
	}
	result := make([]string, 0, len(ancestors))
	for _, candidate := range selected {
		if _, exists := wanted[candidate]; exists {
			result = append(result, candidate)
		}
	}
	return result
}

func searchReplayNestedIgnoreCandidates(paths []string) ([]string, bool) {
	seen := make(map[string]struct{})
	for _, rel := range paths {
		for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
			candidate := path.Join(dir, ".gitignore")
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			if len(seen) > maxNestedIgnoreFiles {
				return nil, false
			}
		}
	}
	candidates := make([]string, 0, len(seen))
	for candidate := range seen {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftDepth := strings.Count(candidates[i], "/")
		rightDepth := strings.Count(candidates[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return candidates[i] < candidates[j]
	})
	return candidates, true
}

func writeSearchReplayHashPart(hash io.Writer, value string) {
	writeIgnoreRuleHashPart(hash, value)
}

func searchReplayPolicyFingerprint(view string, ignores ignoreMatcher, policyParts ...string) string {
	hash := sha256.New()
	writePart := func(value string) { writeSearchReplayHashPart(hash, value) }
	writePart(searchReplayPolicyVersion)
	writePart(view)
	for _, part := range policyParts {
		writePart(part)
	}
	writeIgnoreRuleSemantics(hash, ignores.rules)
	return hex.EncodeToString(hash.Sum(nil))
}

func cleanSearchReplayPath(value string) (string, bool) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || strings.ContainsRune(value, '\\') || filepath.IsAbs(value) {
		return "", false
	}
	slashed := filepath.ToSlash(value)
	// Search paths are slash-separated on every platform. Reject foreign
	// absolute spellings too, so a session file cannot use a Windows path to
	// escape when replayed on Unix (or vice versa).
	if strings.HasPrefix(slashed, "/") ||
		(len(slashed) >= 2 && ((slashed[0] >= 'A' && slashed[0] <= 'Z') ||
			(slashed[0] >= 'a' && slashed[0] <= 'z')) && slashed[1] == ':') {
		return "", false
	}
	for _, component := range strings.Split(slashed, "/") {
		if component == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(slashed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

// SearchResponsePaths returns every repository path explicitly carried by a
// search response. The result is sorted and deduplicated so it can be persisted
// in a search session and checked deterministically before replay.
func SearchResponsePaths(response SearchResponse) []string {
	paths := make(map[string]struct{})
	add := func(value string) {
		if value != "" {
			paths[value] = struct{}{}
		}
	}

	for _, provenancePath := range response.replayProvenancePaths {
		add(provenancePath)
	}
	for _, result := range response.Results {
		add(result.FilePath)
	}
	for _, signatureType := range response.SignatureTypes {
		add(signatureType.FilePath)
		for _, provenancePath := range signatureType.provenancePaths {
			add(provenancePath)
		}
	}
	for _, entry := range response.TypeCard {
		add(entry.FilePath)
	}
	if response.ContainerMap != nil {
		add(response.ContainerMap.FilePath)
	}
	if response.LiteralCluster != nil {
		for _, hit := range response.LiteralCluster.Hits {
			add(hit.FilePath)
		}
		for _, provenancePath := range response.LiteralCluster.provenancePaths {
			add(provenancePath)
		}
	}
	for _, outline := range response.FileOutlines {
		add(outline.FilePath)
	}
	if response.ClosedSet != nil {
		for _, site := range response.ClosedSet.Sites {
			add(site.FilePath)
		}
		for _, provenancePath := range response.ClosedSet.provenancePaths {
			add(provenancePath)
		}
	}
	if response.CoverageNote != nil {
		add(response.CoverageNote.FilePath)
		for _, provenancePath := range response.CoverageNote.provenancePaths {
			add(provenancePath)
		}
	}
	if response.VerifyCommand != nil {
		for _, provenancePath := range response.VerifyCommand.provenancePaths {
			add(provenancePath)
		}
	}
	for _, warning := range response.Warnings {
		add(warning.FilePath)
	}
	for _, failure := range response.PartialFailures {
		add(failure.FilePath)
	}

	result := make([]string, 0, len(paths))
	for filePath := range paths {
		result = append(result, filePath)
	}
	sort.Strings(result)
	return result
}
