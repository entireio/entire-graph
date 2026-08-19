package sem

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

type ignoreMatcher struct {
	rules []ignoreRule
}

type ignoreRule struct {
	ignore       bool
	includeFile  bool
	directory    bool
	basenameOnly bool
	pattern      string
	origin       ignoreOrigin
	expression   *regexp.Regexp
}

// ignoreOrigin records WHO controls a rule, which is the whole difference between
// an exclusion worth reporting and one that would only be noise.
//
// A rule from an ignore file that lives in the repository — .gitignore,
// .graphignore, .git/info/exclude, a nested .gitignore — is written by whoever can
// commit to that repository, i.e. potentially by someone other than the person
// running the graph. A rule from --ignore-file/--include-file is that person's own
// instruction. Only the first kind can narrow a reader's field of view without
// the reader having asked for it, so only the first kind is disclosed.
//
// The zero value is repo-controlled ON PURPOSE. If a future ignore source is
// added and nobody labels it, the failure mode is an exclusion reported that did
// not need to be (visible, quickly corrected) rather than an exclusion that
// silently disappears from the report, which is the exact defect this type exists
// to close.
type ignoreOrigin struct {
	callerControlled bool
	// label names the file the rule came from, repo-relative where that is
	// meaningful (".graphignore", "backend/.gitignore"). It is reported to the
	// caller, so it is repository-controlled text: render it accordingly.
	label string
}

// repoIgnoreOrigin labels rules loaded from an ignore file that lives in the
// repository and can therefore be changed by a contributor.
func repoIgnoreOrigin(label string) ignoreOrigin { return ignoreOrigin{label: label} }

// callerIgnoreOrigin labels rules the person running the graph supplied
// themselves, via --ignore-file or --include-file.
func callerIgnoreOrigin(label string) ignoreOrigin {
	return ignoreOrigin{callerControlled: true, label: label}
}

// RepoExclusion names one path that the repository's own ignore rules removed
// from a corpus, and the rule that removed it.
type RepoExclusion struct {
	Path string `json:"path"`
	// Source is the ignore file the deciding rule came from.
	Source string `json:"source"`
	// Rule is the pattern line itself, normalized the way the matcher stores it.
	Rule string `json:"rule"`
}

// RepoIgnoreSource counts one ignore file's contribution to the exclusions.
type RepoIgnoreSource struct {
	File  string `json:"file"`
	Files int    `json:"files"`
}

// RepoIgnoreReport is the disclosure: how much of what Git itself listed the
// repository's own ignore rules removed from the corpus a query was answered
// from, and which files did the removing.
//
// It exists because the graph's coverage figures otherwise describe the corpus
// that survived, with nothing to distinguish "this repository has two files" from
// "this repository has two files left after a committed rule deleted a third".
type RepoIgnoreReport struct {
	// Files is the exact number of listed paths excluded, even when Sample is capped.
	Files   int                `json:"files"`
	Sources []RepoIgnoreSource `json:"sources"`
	// Sample names the excluded paths, capped at maxRepoExclusionSample so a
	// repository that ignores thousands of vendored blobs cannot flood a payload.
	Sample          []RepoExclusion `json:"sample,omitempty"`
	SampleTruncated bool            `json:"sample_truncated,omitempty"`
}

// maxRepoExclusionSample bounds the named paths in a RepoIgnoreReport. The count
// stays exact; only the list is capped. A repository that legitimately keeps
// dozens of vendored blobs out of the graph must not be able to turn every
// response into a wall of paths — that would make the disclosure something
// readers learn to skip, which is the same blindness by a different route.
const maxRepoExclusionSample = 10

// repoIgnoreLedger accumulates repository-controlled exclusions during one
// listing. A nil ledger accumulates nothing, so callers that do not want the
// accounting pay for none of it.
type repoIgnoreLedger struct {
	files   int
	sources map[string]int
	order   []string
	// seen keeps the count a count of PATHS. A listing can offer the same path
	// twice (Git's tracked listing plus an include file's re-inclusion), and a
	// disclosure that inflates is a disclosure readers stop believing.
	seen      map[string]struct{}
	sample    []RepoExclusion
	truncated bool
}

func (l *repoIgnoreLedger) note(exclusion RepoExclusion) {
	if l == nil {
		return
	}
	if l.seen == nil {
		l.seen = make(map[string]struct{})
	}
	if _, duplicate := l.seen[exclusion.Path]; duplicate {
		return
	}
	l.seen[exclusion.Path] = struct{}{}
	l.files++
	if l.sources == nil {
		l.sources = make(map[string]int)
	}
	if _, seen := l.sources[exclusion.Source]; !seen {
		l.order = append(l.order, exclusion.Source)
	}
	l.sources[exclusion.Source]++
	if len(l.sample) < maxRepoExclusionSample {
		l.sample = append(l.sample, exclusion)
		return
	}
	l.truncated = true
}

// report renders the ledger, or nil when nothing was excluded. Nil is what keeps
// the field absent from the overwhelmingly common payload that has nothing to
// disclose.
func (l *repoIgnoreLedger) report() *RepoIgnoreReport {
	if l == nil || l.files == 0 {
		return nil
	}
	sources := make([]RepoIgnoreSource, 0, len(l.order))
	for _, file := range l.order {
		sources = append(sources, RepoIgnoreSource{File: file, Files: l.sources[file]})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Files != sources[j].Files {
			return sources[i].Files > sources[j].Files
		}
		return sources[i].File < sources[j].File
	})
	// Sorted, so the same repository view always renders the same disclosure —
	// the determinism the rest of the provider promises applies here too.
	sample := append([]RepoExclusion(nil), l.sample...)
	sort.Slice(sample, func(i, j int) bool { return sample[i].Path < sample[j].Path })
	return &RepoIgnoreReport{
		Files:           l.files,
		Sources:         sources,
		Sample:          sample,
		SampleTruncated: l.truncated,
	}
}

type ignoreMatchKind int

const (
	ignoreNoMatch ignoreMatchKind = iota
	ignoreAncestorMatch
	ignoreSelfMatch
)

// graphIgnoreFileName is a repo-root ignore list the graph honors in addition to
// .gitignore, using the same gitignore syntax. It exists for paths that are
// tracked in git on purpose (so .gitignore cannot exclude them) yet should be
// kept out of the code graph — e.g. vendored or generated sources such as the
// multi-MB tree-sitter parser.c blobs, which only ever produce E_FILE_TOO_LARGE /
// E_PARSE_ERROR noise and a false "degraded" completeness. It is loaded with the
// same authority as the root .gitignore, before any explicit --ignore-file, so a
// caller's --include-file can still override it.
const graphIgnoreFileName = ".graphignore"

func loadWorktreeIgnoreMatcher(repo string, ignoreFiles, includeFiles []string) (ignoreMatcher, error) {
	var matcher ignoreMatcher
	if err := matcher.loadOptional(filepath.Join(repo, ".gitignore"), false, repoIgnoreOrigin(".gitignore")); err != nil {
		return ignoreMatcher{}, err
	}
	if err := matcher.loadOptional(filepath.Join(repo, graphIgnoreFileName), false, repoIgnoreOrigin(graphIgnoreFileName)); err != nil {
		return ignoreMatcher{}, err
	}
	// info/exclude is the repository's private exclude list: same syntax and same
	// authority as the root .gitignore, and Git applies both. Reading only
	// .gitignore silently pulled excluded trees into the working-tree scan.
	//
	// It is NOT always at <repo>/.git/info/exclude. In a linked worktree, <repo>/.git
	// is a regular file holding "gitdir: <path>", so that join names a path under a
	// non-directory: os.Stat returns ENOTDIR rather than ErrNotExist, and treating
	// that as fatal aborted the entire search with zero results in every worktree.
	if exclude := gitInfoExcludePath(repo); exclude != "" {
		if err := matcher.loadOptional(exclude, false, repoIgnoreOrigin(".git/info/exclude")); err != nil {
			return ignoreMatcher{}, err
		}
	}
	if err := matcher.loadExplicit(repo, ignoreFiles, includeFiles); err != nil {
		return ignoreMatcher{}, err
	}
	return matcher, nil
}

func loadExplicitIgnoreMatcher(repo string, ignoreFiles, includeFiles []string) (ignoreMatcher, error) {
	var matcher ignoreMatcher
	if err := matcher.loadOptional(filepath.Join(repo, graphIgnoreFileName), false, repoIgnoreOrigin(graphIgnoreFileName)); err != nil {
		return ignoreMatcher{}, err
	}
	if err := matcher.loadExplicit(repo, ignoreFiles, includeFiles); err != nil {
		return ignoreMatcher{}, err
	}
	return matcher, nil
}

func (m *ignoreMatcher) loadExplicit(repo string, ignoreFiles, includeFiles []string) error {
	for _, ignoreFile := range ignoreFiles {
		resolved := ignoreFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repo, resolved)
		}
		if err := m.loadRequired(resolved, false, callerIgnoreOrigin(ignoreFile)); err != nil {
			return err
		}
	}
	for _, includeFile := range includeFiles {
		resolved := includeFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repo, resolved)
		}
		if err := m.loadRequired(resolved, true, callerIgnoreOrigin(includeFile)); err != nil {
			return err
		}
	}
	return nil
}

// gitInfoExcludePath resolves the info/exclude that Git itself would apply to a
// working tree, or "" when there is no git directory to consult.
//
// <repo>/.git is a directory in an ordinary clone but a regular file in a linked
// worktree, where it holds "gitdir: <path to .git/worktrees/<name>>". Git shares
// info/ across worktrees via that gitdir's commondir pointer, so the exclude file
// lives under the common directory, not under <repo>/.git.
func gitInfoExcludePath(repo string) string {
	dotGit := filepath.Join(repo, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return filepath.Join(dotGit, "info", "exclude")
	}
	if !info.Mode().IsRegular() {
		return ""
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	// commondir points at the shared .git that owns info/; it may be relative to gitDir.
	if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		if c := strings.TrimSpace(string(common)); c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			gitDir = filepath.Clean(c)
		}
	}
	return filepath.Join(gitDir, "info", "exclude")
}

func (m *ignoreMatcher) loadOptional(file string, includeMode bool, origin ignoreOrigin) error {
	label := ignoreFileLabel(includeMode)
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		// ENOTDIR: a parent component is not a directory, so the file cannot exist.
		// For an OPTIONAL exclude file that is absence, never a hard failure.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q is not a regular file", label, file)
	}
	return m.loadFile(file, includeMode, origin)
}

func (m *ignoreMatcher) loadRequired(file string, includeMode bool, origin ignoreOrigin) error {
	label := ignoreFileLabel(includeMode)
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s %q does not exist", label, file)
	}
	if err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q is not a regular file", label, file)
	}
	return m.loadFile(file, includeMode, origin)
}

func (m *ignoreMatcher) loadFile(file string, includeMode bool, origin ignoreOrigin) error {
	label := ignoreFileLabel(includeMode)
	content, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	if err := m.loadContent(string(content), includeMode, origin); err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	return nil
}

func (m *ignoreMatcher) loadContent(content string, includeMode bool, origin ignoreOrigin) error {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		rule, ok := parseIgnoreRule(scanner.Text(), includeMode, origin)
		if ok {
			m.rules = append(m.rules, rule)
		}
	}
	return scanner.Err()
}

func ignoreFileLabel(includeMode bool) string {
	if includeMode {
		return "include file"
	}
	return "ignore file"
}

func parseIgnoreRule(line string, includeMode bool, origin ignoreOrigin) (ignoreRule, bool) {
	line = strings.TrimRight(line, "\r")
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	if strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	negated := false
	if strings.HasPrefix(line, "!") {
		negated = true
		line = strings.TrimSpace(line[1:])
		if line == "" {
			return ignoreRule{}, false
		}
	}
	line = filepath.ToSlash(line)
	line = strings.TrimPrefix(line, "./")
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimLeft(line, "/")
	directory := strings.HasSuffix(line, "/")
	line = strings.TrimRight(line, "/")
	line = cleanIgnorePath(line)
	if line == "" {
		return ignoreRule{}, false
	}

	basenameOnly := !anchored && !strings.Contains(line, "/")
	ignore := !negated
	if includeMode {
		ignore = negated
	}
	return ignoreRule{
		ignore:       ignore,
		includeFile:  includeMode,
		directory:    directory,
		basenameOnly: basenameOnly,
		pattern:      line,
		origin:       origin,
		expression:   regexp.MustCompile(globPatternExpression(line)),
	}, true
}

func (m ignoreMatcher) Ignored(rel string, isDir bool) bool {
	matched, ignored := m.decide(rel, isDir)
	return matched && ignored
}

// decide reports whether any rule matched rel and, when one did, the verdict of
// the winning rule (a rule matching the path itself beats one matching only an
// ancestor directory; within each of those, the last rule loaded wins). The
// caller needs "matched" separately from "ignored" so a stack of per-directory
// ignore files can let the deepest file that has an opinion decide, exactly as
// Git does.
func (m ignoreMatcher) decide(rel string, isDir bool) (bool, bool) {
	winner, matched := m.decideRule(rel, isDir)
	if !matched {
		return false, false
	}
	return true, winner.ignore
}

// decideRule is decide's single source of truth: it returns the rule that decides
// rel, so a caller that needs the verdict and a caller that needs to attribute
// the verdict cannot drift apart. Attributing an exclusion to the wrong file
// would be worse than not attributing it, so the two share one traversal.
func (m ignoreMatcher) decideRule(rel string, isDir bool) (ignoreRule, bool) {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return ignoreRule{}, false
	}
	var selfRule, ancestorRule ignoreRule
	selfMatched := false
	ancestorMatched := false
	for _, rule := range m.rules {
		switch rule.matchKind(rel, isDir) {
		case ignoreSelfMatch:
			selfMatched = true
			selfRule = rule
		case ignoreAncestorMatch:
			ancestorMatched = true
			ancestorRule = rule
		}
	}
	if selfMatched {
		return selfRule, true
	}
	if ancestorMatched {
		return ancestorRule, true
	}
	return ignoreRule{}, false
}

// repoExclusion reports the repository-controlled rule that excluded rel, if one
// did.
//
// It answers only for the rule that ACTUALLY decided the path, under the same
// precedence Ignored uses. A caller's own --ignore-file that overrides a
// repository rule takes the attribution with it (the caller asked for that
// exclusion, so there is nothing to disclose), and a caller's --include-file that
// re-includes the path means there is no exclusion to report at all.
func (m ignoreMatcher) repoExclusion(rel string, isDir bool) (RepoExclusion, bool) {
	rule, matched := m.decideRule(rel, isDir)
	if !matched || !rule.ignore || rule.origin.callerControlled {
		return RepoExclusion{}, false
	}
	return RepoExclusion{
		Path:   cleanIgnorePath(rel),
		Source: rule.origin.label,
		Rule:   rule.pattern,
	}, true
}

// noteRepoExclusion records rel in the ledger when the repository's own ignore
// rules are what removed it. It is the one call sites make, so that "did we
// exclude it" and "who excluded it" can never answer differently.
func (m ignoreMatcher) noteRepoExclusion(ledger *repoIgnoreLedger, rel string, isDir bool) {
	if ledger == nil {
		return
	}
	if exclusion, ok := m.repoExclusion(rel, isDir); ok {
		ledger.note(exclusion)
	}
}

// decideSelf reports the verdict of the last rule that names the path itself
// rather than one of its ancestor directories — the most specific kind of rule,
// whichever file it came from.
func (m ignoreMatcher) decideSelf(rel string, isDir bool) (bool, bool) {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false, false
	}
	matched := false
	ignored := false
	for _, rule := range m.rules {
		if rule.matchKind(rel, isDir) == ignoreSelfMatch {
			matched = true
			ignored = rule.ignore
		}
	}
	return matched, ignored
}

// Reincluded reports whether an explicit include file re-includes rel, which is
// the only way a path Git's own exclude rules cover may enter a listing at all.
// It gates whether such a path is considered; the merged ignore rules then make
// the final call, so an include file that reopens a directory does not override a
// rule naming one file inside it.
func (m ignoreMatcher) Reincluded(rel string, isDir bool) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	for _, rule := range m.rules {
		if !rule.includeFile || rule.ignore {
			continue
		}
		if rule.matchKind(rel, isDir) != ignoreNoMatch {
			return true
		}
	}
	return false
}

// maxNestedIgnoreFileBytes bounds one .gitignore read during a walk. Real ignore
// files are a few kilobytes; anything past this is not an ignore file and must
// not be materialized just because it is named like one.
const maxNestedIgnoreFileBytes = 1 << 20

// nestedIgnoreStack applies per-directory .gitignore files during a walk the way
// Git does: a .gitignore governs its own subtree, and the deepest file with an
// opinion about a path wins. It is the filesystem-walk fallback's answer to the
// gap that put vendored dependency trees in the graph — a tree ignored by
// `backend/.gitignore` is invisible to a reader that only ever parsed the
// repository root's .gitignore.
type nestedIgnoreStack struct {
	repo   string
	base   ignoreMatcher
	levels []nestedIgnoreLevel
}

type nestedIgnoreLevel struct {
	dir     string
	matcher ignoreMatcher
}

func newNestedIgnoreStack(repo string, base ignoreMatcher) *nestedIgnoreStack {
	return &nestedIgnoreStack{repo: repo, base: base}
}

// enter registers the directory the walk is about to descend into (repo-relative,
// slash-separated; "" for the repository root) and loads its .gitignore, if any.
// Levels the walk has left are dropped, so the stack holds one matcher per
// ancestor directory of the current position.
func (s *nestedIgnoreStack) enter(dir string) {
	dir = cleanIgnorePath(dir)
	kept := s.levels[:0]
	for _, level := range s.levels {
		if level.dir == dir || strings.HasPrefix(dir, level.dir+"/") {
			kept = append(kept, level)
		}
	}
	s.levels = kept
	if dir == "" {
		// The root .gitignore is already part of base, alongside the explicit
		// ignore/include files that must keep overriding it.
		return
	}
	file := filepath.Join(s.repo, filepath.FromSlash(dir), ".gitignore")
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxNestedIgnoreFileBytes {
		return
	}
	var matcher ignoreMatcher
	if err := matcher.loadFile(file, false, repoIgnoreOrigin(path.Join(dir, ".gitignore"))); err != nil {
		return
	}
	s.levels = append(s.levels, nestedIgnoreLevel{dir: dir, matcher: matcher})
}

// Ignored reports the stack's verdict for a repo-relative path.
//
// Precedence, most specific first: a rule that names the path itself (from the
// root .gitignore, .git/info/exclude, or an explicit ignore/include file), then
// the deepest nested .gitignore with an opinion, then the remaining
// directory-level rules of the root set. That ordering is what lets a project
// ignore `cache/` and still name `cache/skip.py`, while a nested
// `backend/.gitignore` keeps its own subtree's verdict.
func (s *nestedIgnoreStack) Ignored(rel string, isDir bool) bool {
	if matched, ignored := s.base.decideSelf(rel, isDir); matched {
		return ignored
	}
	rel = cleanIgnorePath(rel)
	for i := len(s.levels) - 1; i >= 0; i-- {
		level := s.levels[i]
		sub, ok := pathUnder(level.dir, rel)
		if !ok {
			continue
		}
		if matched, ignored := level.matcher.decide(sub, isDir); matched {
			return ignored
		}
	}
	return s.base.Ignored(rel, isDir)
}

// MayIncludeDescendant defers to the explicit include files: only they can pull a
// path back out of an ignored directory, so only they can keep one walked.
func (s *nestedIgnoreStack) MayIncludeDescendant(rel string) bool {
	return s.base.MayIncludeDescendant(rel)
}

// ReincludesDescendant answers the vendored-directory heuristic over the whole
// stack: a negation in any ignore file on the current path — root or nested —
// declares part of that tree first-party.
func (s *nestedIgnoreStack) ReincludesDescendant(rel string) bool {
	if s.base.ReincludesDescendant(rel) {
		return true
	}
	for _, level := range s.levels {
		if level.matcher.reincludesDescendantUnder(level.dir, rel) {
			return true
		}
	}
	return false
}

// maxNestedIgnoreFiles bounds how many per-directory .gitignore files one listing
// merges. A repository with more ignore files than this is not a repository whose
// vendored-tree verdict hinges on the last one.
const maxNestedIgnoreFiles = 512

// nestedIgnoreRules merges the repository's per-directory .gitignore files for a
// listing that is not a walk — the committed-tree listing and Git's own
// working-tree listing both arrive as a flat path set, so there is no walk
// position to hang a stack off.
//
// It exists for one question: whether the project's own exclude rules re-include
// part of a tree the vendored-directory heuristic would otherwise skip. Reading
// only the root .gitignore answered "no" for every project that keeps those rules
// where Git expects them — beside the tree — which silently dropped tracked
// first-party source (`vendor/.gitignore` holding `*` and `!mypkg/` lost
// `vendor/mypkg/**` from both `--head` and the working tree, while the identical
// negation at the root kept it).
type nestedIgnoreRules struct {
	base   ignoreMatcher
	levels []nestedIgnoreLevel
}

func newNestedIgnoreRules(base ignoreMatcher) *nestedIgnoreRules {
	return &nestedIgnoreRules{base: base}
}

// addFile registers the parsed content of the .gitignore at repo-relative path
// file. Content that does not parse, or one file past the cap, is skipped: this
// is a heuristic's escape hatch, not a correctness boundary.
func (r *nestedIgnoreRules) addFile(file, content string) {
	dir := cleanIgnorePath(path.Dir(filepath.ToSlash(file)))
	if dir == "" || len(r.levels) >= maxNestedIgnoreFiles {
		return
	}
	var matcher ignoreMatcher
	if err := matcher.loadContent(content, false, repoIgnoreOrigin(path.Join(dir, ".gitignore"))); err != nil {
		return
	}
	r.levels = append(r.levels, nestedIgnoreLevel{dir: dir, matcher: matcher})
}

// ReincludesDescendant reports whether the root rules or any nested .gitignore
// negate a path at or below rel.
func (r *nestedIgnoreRules) ReincludesDescendant(rel string) bool {
	if r.base.ReincludesDescendant(rel) {
		return true
	}
	for _, level := range r.levels {
		if level.matcher.reincludesDescendantUnder(level.dir, rel) {
			return true
		}
	}
	return false
}

// pathUnder returns rel expressed relative to dir when dir contains it.
func pathUnder(dir, rel string) (string, bool) {
	if dir == "" {
		return rel, true
	}
	if !strings.HasPrefix(rel, dir+"/") {
		return "", false
	}
	return strings.TrimPrefix(rel, dir+"/"), true
}

func (m ignoreMatcher) MayIncludeDescendant(rel string) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	for _, rule := range m.rules {
		if rule.includeFile && !rule.ignore && rule.mayMatchDescendant(rel) {
			return true
		}
	}
	return false
}

// ReincludesDescendant reports whether the ignore rules negate (re-include) a
// specific path under rel — the project declares part of that tree as
// first-party. An erlang.mk/rebar monorepo gitignores fetched dependencies
// (`/deps/*`) but negates its own applications (`!/deps/rabbit/`), so the
// vendored-directory-name heuristic must not skip the tree wholesale; the
// ignore rules themselves keep the fetched dependencies out. Basename-only
// negations (e.g. `!.keep`) carry no path and are not treated as a signal.
func (m ignoreMatcher) ReincludesDescendant(rel string) bool {
	return m.reincludesDescendantUnder("", rel)
}

// reincludesDescendantUnder is ReincludesDescendant for an ignore file that lives
// in dir rather than at the repository root: its patterns are relative to dir, so
// each literal prefix is resolved against dir before being compared to rel.
// Basename-only negations carry no path in either position and are skipped in
// both, exactly as before.
func (m ignoreMatcher) reincludesDescendantUnder(dir, rel string) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	dir = cleanIgnorePath(dir)
	for _, rule := range m.rules {
		if rule.ignore || rule.includeFile || rule.basenameOnly {
			continue
		}
		prefix := literalPatternPrefix(rule.pattern)
		if prefix == "" {
			continue
		}
		if dir != "" {
			prefix = dir + "/" + prefix
		}
		if prefix == rel || strings.HasPrefix(prefix, rel+"/") {
			return true
		}
	}
	return false
}

func (r ignoreRule) matchKind(rel string, isDir bool) ignoreMatchKind {
	if r.basenameOnly {
		return r.matchBasename(rel, isDir)
	}
	return r.matchPath(rel, isDir)
}

func (r ignoreRule) matchBasename(rel string, isDir bool) ignoreMatchKind {
	segments := strings.Split(rel, "/")
	last := len(segments) - 1
	if r.directory {
		for i, segment := range segments {
			if i == last && !isDir {
				continue
			}
			if r.expression.MatchString(segment) {
				if i == last {
					return ignoreSelfMatch
				}
				return ignoreAncestorMatch
			}
		}
		return ignoreNoMatch
	}
	for i, segment := range segments {
		if r.expression.MatchString(segment) {
			if i == last {
				return ignoreSelfMatch
			}
			return ignoreAncestorMatch
		}
	}
	return ignoreNoMatch
}

func (r ignoreRule) matchPath(rel string, isDir bool) ignoreMatchKind {
	if !r.directory && r.expression.MatchString(rel) {
		return ignoreSelfMatch
	}
	if r.directory && isDir && r.expression.MatchString(rel) {
		return ignoreSelfMatch
	}
	for _, ancestor := range ancestorPaths(rel) {
		if r.expression.MatchString(ancestor) {
			return ignoreAncestorMatch
		}
	}
	return ignoreNoMatch
}

func (r ignoreRule) mayMatchDescendant(rel string) bool {
	if r.basenameOnly {
		return true
	}
	prefix := literalPatternPrefix(r.pattern)
	if prefix == "" {
		return true
	}
	return prefix == rel || strings.HasPrefix(prefix, rel+"/") || strings.HasPrefix(rel, prefix+"/")
}

func ancestorPaths(rel string) []string {
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

func cleanIgnorePath(value string) string {
	value = filepath.ToSlash(value)
	value = strings.TrimPrefix(value, "./")
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func literalPatternPrefix(pattern string) string {
	index := strings.IndexAny(pattern, "*?[")
	if index >= 0 {
		pattern = pattern[:index]
	}
	return strings.Trim(strings.TrimRight(cleanIgnorePath(pattern), "/"), "/")
}

func globPatternExpression(pattern string) string {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					out.WriteString("(?:.*/)?")
					i += 3
					continue
				}
				out.WriteString(".*")
				i += 2
				continue
			}
			out.WriteString(`[^/]*`)
			i++
		case '?':
			out.WriteString(`[^/]`)
			i++
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	out.WriteString("$")
	return out.String()
}
