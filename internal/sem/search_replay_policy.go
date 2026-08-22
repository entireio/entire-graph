package sem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

const searchReplayPolicyVersion = "search-replay-policy-v4"

const (
	searchReplayViewHead               = "head"
	searchReplayViewGitWorktree        = "worktree-git"
	searchReplayViewFilesystemWorktree = "worktree-filesystem"

	// SearchReplayMaxPathCount, SearchReplayMaxPathBytes and
	// SearchReplayMaxAggregatePathBytes bound persisted provenance before it can
	// drive filesystem traversal or Git subprocess input. They are exported so
	// the session reader can reject hostile records before policy resolution.
	SearchReplayMaxPathCount          = 1024
	SearchReplayMaxPathBytes          = 4096
	SearchReplayMaxAggregatePathBytes = 128 << 10

	// This is a second-order bound on ancestor walks and nested-ignore reads.
	// Ordinary repository paths are nowhere near this depth.
	searchReplayMaxPathComponents    = 128
	searchReplayMaxVendorIndexProbes = 64
	searchReplayCorpusVersion        = "search-replay-corpus-v1"
)

// SearchReplayPolicy is the exclusion policy a persisted search payload must
// still satisfy before it can be replayed. Its representation is deliberately
// opaque: callers compare Fingerprint values and ask whether recorded paths are
// still admissible instead of recreating ignore semantics outside sem.
type SearchReplayPolicy struct {
	repo        string
	commit      string
	tree        string
	worktree    bool
	gitWorktree bool
	hasIncludes bool
	ignores     ignoreMatcher
	// worktreeCorpus is the exact bounded provider listing observed while the
	// fingerprint was built. Worktree replay is disabled when the listing does
	// not fit the replay bounds, so membership here includes git-directory,
	// vendored, ignore, regular-file, and symlink eligibility in one decision.
	worktreeCorpus map[string]struct{}
	fingerprint    string
	ctx            context.Context
}

// ResolveSearchReplayPolicy assembles the same root policy as a real search.
// A requested committed-tree view falls back to the worktree when HEAD cannot
// be resolved, matching prepareSource. The fingerprint binds the resolved view
// and the ordered, parsed rule semantics rather than merely the rule-file paths.
func ResolveSearchReplayPolicy(ctx context.Context, repo string, options SearchOptions) (SearchReplayPolicy, error) {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return SearchReplayPolicy{}, err
	}

	worktree := options.Worktree
	commit := ""
	tree := ""
	if !worktree {
		resolvedCommit, resolvedTree, headErr := resolveCommittedHEAD(ctx, absRepo)
		if headErr != nil {
			worktree = true
		} else {
			commit = resolvedCommit
			tree = resolvedTree
		}
	}
	gitWorktree := false
	if worktree {
		if _, gitErr := gitutil.RepoRoot(ctx, absRepo); gitErr == nil {
			gitWorktree = true
		} else if repositoryHasGitMetadata(absRepo) {
			// A genuine Git worktree whose eligibility probe is broken must not
			// silently switch to the less complete non-Git ignore fallback.
			return SearchReplayPolicy{}, fmt.Errorf("resolve Git worktree for replay policy: %w", gitErr)
		}
	}

	view := searchReplayViewHead
	loader := loadExplicitIgnoreMatcher
	if worktree {
		view = searchReplayViewFilesystemWorktree
		if gitWorktree {
			view = searchReplayViewGitWorktree
		}
		loader = loadWorktreeIgnoreMatcher
	}
	ignores, err := loader(absRepo, options.IgnoreFiles, options.IncludeFiles)
	if err != nil {
		return SearchReplayPolicy{}, err
	}
	effectiveMaxFiles := resolveMaxSourceFiles(0)
	policyParts := []string{"max-files=" + strconv.Itoa(effectiveMaxFiles)}
	var worktreeCorpus map[string]struct{}
	if worktree {
		policyParts = append(policyParts, "sweep-dir-budget="+strconv.Itoa(resolveSweepDirectoryBudget()))
		corpusIdentity, resolvedCorpus, corpusErr := observeSearchReplayWorktreeCorpus(
			ctx,
			absRepo,
			ignores,
			len(options.IncludeFiles) > 0,
			gitWorktree,
		)
		if corpusErr != nil {
			return SearchReplayPolicy{}, corpusErr
		}
		policyParts = append(policyParts, "corpus="+corpusIdentity)
		worktreeCorpus = resolvedCorpus
	}

	return SearchReplayPolicy{
		repo:           absRepo,
		commit:         commit,
		tree:           tree,
		worktree:       worktree,
		gitWorktree:    gitWorktree,
		hasIncludes:    len(options.IncludeFiles) > 0,
		ignores:        ignores,
		worktreeCorpus: worktreeCorpus,
		fingerprint:    searchReplayPolicyFingerprint(view, ignores, policyParts...),
		ctx:            ctx,
	}, nil
}

type searchReplayCorpusCollector struct {
	paths        []string
	seen         map[string]struct{}
	observations []string
	pathBytes    int
	exceeded     bool
	exceedWhat   string
}

func (c *searchReplayCorpusCollector) addWarning(warning ProviderWarning) {
	c.observations = append(c.observations,
		warning.Code,
		warning.Severity,
		warning.EffectOnCompleteness,
		warning.Detail,
	)
}

func newSearchReplayCorpusCollector() *searchReplayCorpusCollector {
	return &searchReplayCorpusCollector{
		paths: make([]string, 0, SearchReplayMaxPathCount),
		seen:  make(map[string]struct{}),
	}
}

// add retains a complete effective provider corpus only while it fits every
// replay bound. Returning false tells a streaming source to stop immediately:
// once a replay ceiling is crossed no fingerprint could make this response
// replayable. The provider's own cap is deliberately not a collection bound:
// the complete pre-cap set determines both its sorted prefix and W_FILE_LIMIT
// total, and remains safe to fingerprint while that full set fits these bounds.
func (c *searchReplayCorpusCollector) add(rel string) bool {
	if _, exists := c.seen[rel]; exists {
		return true
	}
	if len(c.paths) >= SearchReplayMaxPathCount {
		c.exceeded = true
		c.exceedWhat = "replay path count"
		return false
	}
	if len(rel) > SearchReplayMaxPathBytes {
		c.exceeded = true
		c.exceedWhat = "replay path bytes"
		return false
	}
	if len(rel) > SearchReplayMaxAggregatePathBytes-c.pathBytes {
		c.exceeded = true
		c.exceedWhat = "aggregate replay path bytes"
		return false
	}
	c.seen[rel] = struct{}{}
	c.paths = append(c.paths, rel)
	c.pathBytes += len(rel)
	return true
}

func (c *searchReplayCorpusCollector) identity() (string, error) {
	if c.exceeded {
		return "", fmt.Errorf("search replay worktree corpus exceeds %s bound", c.exceedWhat)
	}
	ordered := append([]string(nil), c.paths...)
	sort.Strings(ordered)
	hash := sha256.New()
	writeSearchReplayHashPart(hash, searchReplayCorpusVersion)
	for _, observation := range c.observations {
		writeSearchReplayHashPart(hash, observation)
	}
	for _, rel := range ordered {
		writeSearchReplayHashPart(hash, rel)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (c *searchReplayCorpusCollector) corpus() map[string]struct{} {
	result := make(map[string]struct{}, len(c.seen))
	for rel := range c.seen {
		result[rel] = struct{}{}
	}
	return result
}

func observeSearchReplayWorktreeCorpus(
	ctx context.Context,
	repo string,
	ignores ignoreMatcher,
	hasIncludeFiles bool,
	gitWorktree bool,
) (string, map[string]struct{}, error) {
	collector := newSearchReplayCorpusCollector()
	if gitWorktree {
		if err := observeSearchReplayGitWorktreeCorpus(
			ctx, repo, ignores, hasIncludeFiles, collector,
		); err != nil {
			return "", nil, err
		}
	} else {
		dirTracked := func(string) bool { return false }
		warnings, err := visitWalkWorktreeFiles(ctx, repo, ignores, dirTracked, collector.add)
		if err != nil {
			return "", nil, fmt.Errorf("observe filesystem worktree corpus for replay: %w", err)
		}
		for _, warning := range warnings {
			collector.addWarning(warning)
		}
	}
	identity, err := collector.identity()
	if err != nil {
		return "", nil, err
	}
	return identity, collector.corpus(), nil
}

func observeSearchReplayGitWorktreeCorpus(
	ctx context.Context,
	repo string,
	ignores ignoreMatcher,
	hasIncludeFiles bool,
	collector *searchReplayCorpusCollector,
) error {
	nestedPaths, err := gitutil.BoundedWorktreeNestedIgnorePaths(
		ctx, repo, maxNestedIgnoreFiles, includeEveryNestedIgnore,
	)
	if err != nil {
		return fmt.Errorf("observe Git worktree nested ignore paths for replay: %w", err)
	}
	vendorRules, err := strictSearchReplayWorktreeVendorRules(repo, ignores, nestedPaths)
	if err != nil {
		return err
	}

	// Retain the raw provider candidates first. If even that set does not fit
	// replay bounds, stop Git and decline replay; this keeps the later exact
	// git-directory sweep and final eligibility pass bounded without pretending
	// a truncated listing is complete.
	raw := newSearchReplayCorpusCollector()
	var observationErr error
	collect := func(rel string) bool {
		cleaned, ok := cleanSearchReplayPath(filepath.ToSlash(rel))
		if !ok || cleaned != filepath.ToSlash(rel) {
			observationErr = fmt.Errorf("Git returned invalid replay corpus path %q", rel)
			return false
		}
		return raw.add(cleaned)
	}
	if err := gitutil.VisitWorktreePaths(ctx, repo, false, collect); err != nil {
		return fmt.Errorf("observe Git worktree corpus for replay: %w", err)
	}
	if observationErr != nil {
		return observationErr
	}
	if hasIncludeFiles {
		visitIgnored := func(rel string) bool {
			rel = filepath.ToSlash(rel)
			if !ignores.Reincluded(rel, false) {
				return true
			}
			return collect(rel)
		}
		if err := gitutil.VisitWorktreePaths(ctx, repo, true, visitIgnored); err != nil {
			return fmt.Errorf("observe Git ignored worktree corpus for replay: %w", err)
		}
		if observationErr != nil {
			return observationErr
		}
	}
	if _, err := raw.identity(); err != nil {
		return err
	}

	tracked := make(map[string]bool)
	dirTracked := func(rel string) bool {
		if value, exists := tracked[rel]; exists {
			return value
		}
		if len(tracked) >= searchReplayMaxVendorIndexProbes {
			observationErr = errors.New("too many vendored-directory index probes while observing replay corpus")
			return false
		}
		value, probeErr := gitutil.IndexHasFilesUnder(ctx, repo, rel)
		if probeErr != nil {
			observationErr = fmt.Errorf("classify vendored directory %q for replay corpus: %w", rel, probeErr)
			return false
		}
		tracked[rel] = value
		return value
	}

	listedDirs := make([]string, 0)
	for _, rel := range raw.paths {
		info, statErr := os.Lstat(filepath.Join(repo, filepath.FromSlash(rel)))
		if statErr == nil && info.IsDir() {
			listedDirs = append(listedDirs, rel)
		}
	}
	gitDirs := newGitDirExcluder(ctx, repo)
	gitDirs.unlistedRoots, gitDirs.gitAnsweredRoots = gitSweepRootsFromGit(ctx, repo, gitDirs)
	gitDirs.observeListedPaths(raw.paths, listedDirs)
	for _, warning := range gitDirs.sweepWarnings() {
		collector.addWarning(warning)
	}
	for _, warning := range gitDirs.sweepUnreadableDirWarning() {
		collector.addWarning(warning)
	}
	for _, rel := range raw.paths {
		if gitDirs.excluded(rel) {
			continue
		}
		if eligibleGitWorktreeSourcePath(repo, rel, ignores, vendorRules, dirTracked) {
			if !collector.add(rel) {
				break
			}
		}
		if observationErr != nil {
			return observationErr
		}
	}
	return nil
}

func strictSearchReplayWorktreeVendorRules(
	repo string,
	base ignoreMatcher,
	paths []string,
) (*nestedIgnoreRules, error) {
	rules, err := worktreeVendorIgnoreRules(repo, base, paths)
	if err != nil {
		return nil, err
	}
	if len(rules.levels) != len(paths) {
		return nil, errors.New("one or more nested ignore files cannot be observed safely")
	}
	return rules, nil
}

func repositoryHasGitMetadata(repo string) bool {
	for directory := filepath.Clean(repo); ; directory = filepath.Dir(directory) {
		_, err := os.Lstat(filepath.Join(directory, ".git"))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			// Permission and other probe errors are metadata uncertainty, not
			// evidence that this is safely a non-Git directory.
			return true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false
		}
	}
}

// Fingerprint identifies the policy semantics that govern a replay. It is
// stable for equivalent parsed rules, sensitive to rule order, and non-empty
// only for a successfully resolved policy.
func (p SearchReplayPolicy) Fingerprint() string {
	return p.fingerprint
}

// MatchesTree reports whether a search result's tree observation belongs to
// this policy's resolved repository view. A worktree policy has no immutable
// Git tree to bind; a committed policy must match the exact tree resolved with
// the commit used for all subsequent admission probes.
func (p SearchReplayPolicy) MatchesTree(tree string) bool {
	if p.fingerprint == "" {
		return false
	}
	return p.worktree || (p.tree != "" && p.tree == tree)
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
// reject replay. Worktree paths must additionally still name regular,
// non-symlink files. Committed-tree paths must remain literal members of the
// exact resolved commit; neither view may cross the provider's ignore or
// vendored filters.
func (p SearchReplayPolicy) AllowsReplayPaths(paths []string) bool {
	if p.fingerprint == "" || p.repo == "" || ValidateSearchReplayPaths(paths) != nil {
		return false
	}
	cleaned := make([]string, 0, len(paths))
	for _, value := range paths {
		rel, _ := cleanSearchReplayPath(value)
		cleaned = append(cleaned, rel)
		if !p.worktree && p.ignores.Ignored(rel, false) {
			return false
		}
	}
	if !p.worktree {
		ctx := p.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return p.allowsHeadReplayPaths(ctx, cleaned)
	}
	if p.worktreeCorpus == nil {
		return false
	}
	for _, rel := range cleaned {
		if _, eligible := p.worktreeCorpus[rel]; !eligible {
			return false
		}
		if !regularWorktreeReplayPath(p.repo, rel) {
			return false
		}
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if p.gitWorktree {
		return p.allowsGitWorktreeReplayPaths(ctx, cleaned)
	}
	return p.allowsNonGitWorktreeReplayPaths(cleaned)
}

func (p SearchReplayPolicy) allowsHeadReplayPaths(ctx context.Context, paths []string) bool {
	if p.commit == "" {
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

// regularWorktreeReplayPath walks each component with Lstat. Checking only the
// leaf lets an ancestor symlink redirect the apparently repository-relative
// path outside the repository before the final regular-file check.
func regularWorktreeReplayPath(repo, rel string) bool {
	root, err := os.Lstat(repo)
	if err != nil || root.Mode()&fs.ModeSymlink != 0 || !root.IsDir() {
		return false
	}
	parts := strings.Split(rel, "/")
	current := repo
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return false
		}
		if index == len(parts)-1 {
			return info.Mode().IsRegular()
		}
		if !info.IsDir() {
			return false
		}
	}
	return false
}

func (p SearchReplayPolicy) allowsGitWorktreeReplayPaths(ctx context.Context, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	gitEligible, gitIgnored, err := gitutil.ClassifyWorktreePaths(ctx, p.repo, paths)
	if err != nil {
		return false
	}
	for _, rel := range paths {
		if _, eligible := gitEligible[rel]; !eligible {
			if _, ignored := gitIgnored[rel]; !ignored || !p.ignores.Reincluded(rel, false) {
				return false
			}
		}
		if p.ignores.Ignored(rel, false) {
			return false
		}
	}

	ancestors, ok := searchReplayNestedIgnoreCandidates(paths)
	if !ok {
		return false
	}
	var selected []string
	if len(ancestors) > 0 {
		selected, err = gitutil.BoundedWorktreeNestedIgnorePaths(
			ctx,
			p.repo,
			maxNestedIgnoreFiles,
			includeEveryNestedIgnore,
		)
		if err != nil {
			return false
		}
	}
	candidates := selectedSearchReplayAncestorIgnores(ancestors, selected)
	vendorRules, err := strictSearchReplayWorktreeVendorRules(p.repo, p.ignores, candidates)
	if err != nil {
		return false
	}

	tracked := make(map[string]bool)
	var trackedErr error
	dirTracked := func(rel string) bool {
		if value, exists := tracked[rel]; exists {
			return value
		}
		if len(tracked) >= searchReplayMaxVendorIndexProbes {
			trackedErr = errors.New("too many vendored-directory index probes")
			return false
		}
		value, err := gitutil.IndexHasFilesUnder(ctx, p.repo, rel)
		if err != nil {
			trackedErr = err
			return false
		}
		tracked[rel] = value
		return value
	}
	for _, rel := range paths {
		if vendoredScanPath(rel, vendorRules, dirTracked) || trackedErr != nil {
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

func (p SearchReplayPolicy) allowsNonGitWorktreeReplayPaths(paths []string) bool {
	candidates, ok := searchReplayNestedIgnoreCandidates(paths)
	if !ok {
		return false
	}
	matchers, err := p.loadSearchReplayNestedMatchers(candidates)
	if err != nil {
		return false
	}
	dirTracked := func(string) bool { return false }
	for _, rel := range paths {
		stack := newNestedIgnoreStack(p.repo, p.ignores)
		parts := strings.Split(rel, "/")
		for index, name := range parts[:len(parts)-1] {
			dir := strings.Join(parts[:index+1], "/")
			if matcher, exists := matchers[dir]; exists {
				stack.levels = append(stack.levels, nestedIgnoreLevel{dir: dir, matcher: matcher})
			}
			if skipVendoredDir(dir, name, stack, dirTracked) ||
				(stack.Ignored(dir, true) && !stack.MayIncludeDescendant(dir)) {
				return false
			}
		}
		if isVendoredScanFile(rel, parts[len(parts)-1]) || stack.Ignored(rel, false) {
			return false
		}
	}
	return true
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

func (p SearchReplayPolicy) loadSearchReplayNestedMatchers(
	candidates []string,
) (map[string]ignoreMatcher, error) {
	matchers := make(map[string]ignoreMatcher)
	budget := newIgnoreRuleBudget(p.ignores)
	root, err := os.OpenRoot(p.repo)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, candidate := range candidates {
		content, ok, err := readWorktreeNestedIgnore(root, p.repo, candidate)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		matcher, err := loadNestedIgnoreMatcher(content, budget)
		if err != nil {
			return nil, err
		}
		dir := cleanIgnorePath(path.Dir(candidate))
		matchers[dir] = matcher
	}
	return matchers, nil
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
