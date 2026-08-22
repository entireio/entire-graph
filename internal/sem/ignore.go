package sem

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
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
// .graphignore, a nested .gitignore — is written by whoever can commit to that
// repository, i.e. potentially by someone other than the person running the
// graph. A rule from --ignore-file/--include-file, or from the checkout's own
// .git/info/exclude, is that person's own instruction: info/exclude is not in the
// tree, is never pushed and never arrives in a clone. Only the first kind can
// narrow a reader's field of view without the reader having asked for it, so only
// the first kind is disclosed.
//
// The zero value is repo-controlled ON PURPOSE. If a future ignore source is
// added and nobody labels it, the failure mode is an exclusion reported that did
// not need to be (visible, quickly corrected) rather than an exclusion that
// silently disappears from the report, which is the exact defect this type exists
// to close.
type ignoreOrigin struct {
	callerControlled bool
	// gitInvisible marks a rule GIT DOES NOT APPLY: the graph's own .graphignore,
	// and a --ignore-file/--include-file the caller passed. Git applies .gitignore
	// (root and nested) to everything and .git/info/exclude to untracked discovery,
	// so those are visible to it.
	//
	// It exists because a path only deserves the disclosure if Git itself would
	// still have listed it, and in the filesystem-walk fallback there is no Git
	// listing to ask. See nestedIgnoreStack.noteRepoExclusion.
	gitInvisible bool
	// localExclude marks a rule from the checkout's OWN exclude list,
	// .git/info/exclude. Git consults that list only while discovering UNTRACKED
	// files: a tracked path named there is still listed by `git ls-files --cached
	// --others --exclude-standard`, and `git check-ignore -v` reports it as not
	// ignored (git 2.54.0). So wherever Git produced the listing, these rules have
	// already been applied to everything they govern, and reapplying them could
	// only remove tracked source Git would have shown — silently, since the list
	// is the operator's and carries no disclosure. See withoutLocalExcludes.
	localExclude bool
	// label names the file the rule came from, repo-relative where that is
	// meaningful (".graphignore", "backend/.gitignore"). It is reported to the
	// caller, so it is repository-controlled text: render it accordingly.
	label string
}

// repoIgnoreOrigin labels rules loaded from an ignore file that lives in the
// repository and can therefore be changed by a contributor.
func repoIgnoreOrigin(label string) ignoreOrigin { return ignoreOrigin{label: label} }

// graphIgnoreOrigin labels rules from .graphignore: repository-controlled like
// .gitignore, but invisible to Git, so Git's own listing never pre-filters what
// it removes.
func graphIgnoreOrigin() ignoreOrigin {
	return ignoreOrigin{gitInvisible: true, label: graphIgnoreFileName}
}

// callerIgnoreOrigin labels rules the person running the graph supplied
// themselves, via --ignore-file or --include-file.
func callerIgnoreOrigin(label string) ignoreOrigin {
	return ignoreOrigin{callerControlled: true, gitInvisible: true, label: label}
}

// localIgnoreOrigin labels rules from an exclude list that belongs to THIS
// CHECKOUT rather than to the repository: .git/info/exclude.
//
// It is not part of the tree, cannot be pushed, and is never delivered by a
// clone — so no contributor can use it to narrow another reader's field of view,
// which is the only thing the disclosure exists to catch. It is the local
// operator's own instruction, exactly like --ignore-file, and reporting it back
// would be both a false "the repository hid this" alarm and a leak of that
// operator's private exclusion paths into every payload.
func localIgnoreOrigin(label string) ignoreOrigin {
	// Not gitInvisible: Git does apply info/exclude, so a path it covers is one
	// Git would have hidden too.
	return ignoreOrigin{callerControlled: true, localExclude: true, label: label}
}

// withoutLocalExcludes returns the matcher with the checkout's own exclude list
// dropped, for the listing paths where Git has already applied it.
//
// Git scopes .git/info/exclude to untracked discovery. Once `git ls-files
// --cached --others --exclude-standard` has produced the listing, every path the
// list governs is already gone from it, and every rule left to fire can only
// fire on a TRACKED file — one Git itself still shows. Removing such a file is
// wrong twice over: it is not what the operator's exclude list means, and the
// exclusion disclosure deliberately stays quiet about caller-controlled rules,
// so the file leaves the corpus without a word. Dropping the rules here makes
// this path agree with the committed-revision path, which never loads them.
//
// The filesystem-walk fallback keeps them: there is no Git listing to have
// applied them, and no way to tell tracked from untracked without one.
func (m ignoreMatcher) withoutLocalExcludes() ignoreMatcher {
	kept := make([]ignoreRule, 0, len(m.rules))
	for _, rule := range m.rules {
		if rule.origin.localExclude {
			continue
		}
		kept = append(kept, rule)
	}
	out := m
	out.rules = kept
	return out
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
	// CountIncomplete says Files is a LOWER BOUND rather than the exact number:
	// enumerating an excluded directory tree hit something it could not read, and
	// whatever that subtree held is excluded and uncounted. It is the one case in
	// which Files does not mean what it says, so it is stated rather than left to
	// be inferred from a number that looks like every other number. Unreadable
	// names what stopped it; the same fact rides the response's partial_failures,
	// so a consumer that reads only that channel still learns the count is short.
	CountIncomplete bool `json:"count_incomplete,omitempty"`
	// Unreadable lists the paths the enumeration could not read, capped like Sample.
	Unreadable []string `json:"unreadable,omitempty"`
	// GitListingUnavailable says the listing this report describes was produced
	// WITHOUT Git's own enumeration of a real checkout — the filesystem-walk
	// fallback over a directory that is a git working tree — and that a
	// repository-controlled rule Git also applies (a `.gitignore`) removed at
	// least one path from it.
	//
	// In that mode there is no tracked/untracked distinction to ask, so such a
	// path cannot be attributed: it is ordinary build output Git would have
	// hidden too, OR a tracked source Git would still have listed and this
	// removed. Naming every one of them would bury the disclosure in build
	// output; naming none of them silently answers "the repository hid nothing"
	// to a corpus where it may well have. Stating the limitation is what is
	// left, and it is stated rather than inferred.
	GitListingUnavailable bool `json:"git_listing_unavailable,omitempty"`
}

// maxRepoExclusionSample bounds the named paths in a RepoIgnoreReport. The count
// stays exact; only the list is capped. A repository that legitimately keeps
// dozens of vendored blobs out of the graph must not be able to turn every
// response into a wall of paths — that would make the disclosure something
// readers learn to skip, which is the same blindness by a different route.
const maxRepoExclusionSample = 10

// maxRepoExclusionWalkEntries bounds how many filesystem entries the accounting
// of pruned directories visits across ONE listing.
//
// The prune is what makes an ignored tree cheap: `filepath.WalkDir` returns
// SkipDir and nothing under it is ever stat'd. Enumerating that tree to say what
// it removed gives the cost back, and its size is set by the repository — the
// same party whose rules this report exists to expose. Left unbounded, a
// committed rule over a five-million-file tree buys a multi-second filesystem
// crawl on EVERY search, and the ignore file a project added to make searching
// cheap stops doing that.
//
// A cap on the walk IS a cap on the count, which is why it could not be applied
// before: Files documents itself as exact. It can be applied now because the
// report can say the count is a lower bound (CountIncomplete), so the bound
// trades an exact number for a stated one rather than for a silently short one —
// "at least N files removed, the count is a lower bound" is a true disclosure,
// and an unbounded crawl is not the price of making it.
//
// Shared across the whole listing rather than per prune, so a repository cannot
// multiply the budget by adding ignore rules. Sized well above any tree a
// project would keep in a checkout deliberately, so a real repository never
// reaches it.
const maxRepoExclusionWalkEntries = 20000

// maxRepoExclusionRuleBytes bounds the pattern text one sample entry carries.
//
// Rule is the only field of a RepoExclusion whose length the repository sets
// freely: Path and Source are filesystem paths, but a pattern is one line of an
// ignore file, and a line is bounded only by bufio.Scanner's 64KiB token. The
// same deciding rule is copied into EVERY entry it matched, so one 60KiB line
// over ten sampled paths put 600KiB of repository-controlled text into a payload
// whose whole context budget is 24KiB — a disclosure meant to protect a reader's
// field of view turned into the thing that floods it.
//
// A real gitignore pattern is a few dozen bytes. Truncating past this keeps the
// rule identifiable (the head is what names the file class) while making the
// payload's size a property of the report, not of the repository.
const maxRepoExclusionRuleBytes = 200

// boundRuleText truncates a repository-controlled pattern to
// maxRepoExclusionRuleBytes, on a rune boundary so the result is still valid
// UTF-8, and marks that it was cut so a reader never mistakes the prefix for the
// whole rule.
func boundRuleText(rule string) string {
	if len(rule) <= maxRepoExclusionRuleBytes {
		return rule
	}
	cut := maxRepoExclusionRuleBytes
	for cut > 0 && !utf8.RuneStart(rule[cut]) {
		cut--
	}
	return rule[:cut] + "..."
}

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
	// unreadable records the paths an enumeration of an excluded tree could not
	// read. Those subtrees are excluded like every other descendant and cannot be
	// counted, so files stops being exact — and a disclosure that quietly
	// understates is the same blindness this ledger exists to end, one step
	// further in.
	unreadable     []string
	unreadableSeen map[string]struct{}
	// direntsRead counts the entries the accounting has READ from directories in
	// this listing. It is not the same number as walkVisited — a directory is
	// read whole before any of its entries is visited — and keeping it is what
	// lets a test assert that the bound holds for the WORK and not only for the
	// reported count.
	direntsRead int
	// ignoreBytesRead counts the nested `.gitignore` bytes the pruned-directory
	// accounting has read in this listing, against maxRepoExclusionIgnoreBytes.
	// The entry budget bounds how many entries are visited; this bounds what
	// visiting one is allowed to cost.
	ignoreBytesRead int64
	// walkVisited counts the filesystem entries the pruned-directory accounting
	// has visited in this listing, against maxRepoExclusionWalkEntries.
	walkVisited int
	// countIncomplete records that the accounting stopped short of the whole
	// excluded tree for a reason other than an unreadable path — it ran out of
	// walk budget. Files is a lower bound then, exactly as it is for an
	// unreadable subtree, and for the same reason: content is excluded and
	// uncounted.
	countIncomplete bool
	// gitListingUnavailable records that this listing could not consult Git's own
	// enumeration of a real checkout while a Git-applied repository rule was
	// removing paths from it. See RepoIgnoreReport.GitListingUnavailable.
	gitListingUnavailable bool
	// listingLimit is the file cap the snapshot applies to this listing
	// (sourceOptions.maxFiles, resolved). Zero means uncapped.
	//
	// It is here because the cap is the other way a path can be absent from a
	// corpus without any ignore rule having removed it. A path the cap would have
	// discarded anyway was never in the corpus to hide, and naming it is a claim
	// about the repository that is not true — the same rule the per-file sink
	// already applies to a staged-deleted file and a symlink.
	listingLimit int
	// listingPosition counts the paths that have reached the ignore decision in
	// this listing, kept AND excluded. That is the position a path would have held
	// in the listing this repository would have had with none of its own ignore
	// rules, which is the only listing against which "the rule is what removed it
	// from the corpus" can be tested.
	listingPosition int
	// positionIncomplete records that listingPosition has become a LOWER BOUND
	// rather than the position itself.
	//
	// Every bound on the pruned-directory accounting — a spent walk budget, a
	// spent nested-ignore byte budget, a subtree that could not be read — stops
	// the enumeration of a tree the counterfactual listing DOES contain, and the
	// descendants it abandons never advance listingPosition. A later exclusion is
	// then tested against a position short by an unknown amount, so one whose true
	// position is outside the snapshot's cap can test as inside it and be blamed
	// on a committed rule that is not what removed it from the corpus. See
	// beyondListingCap.
	positionIncomplete bool
}

// noteListingCandidate advances the counterfactual listing by one path. Every
// producer calls it for each path that reaches the ignore decision, kept or
// excluded, BEFORE the decision — so the position an exclusion is recorded at is
// the one it would have occupied had the rule not been there.
//
// A producer that never calls it leaves the position at zero and every exclusion
// inside the cap: an unaccounted listing mode stays as noisy as it is today
// rather than falling silent, which is the direction this ledger errs in
// everywhere else.
func (l *repoIgnoreLedger) noteListingCandidate() {
	if l == nil {
		return
	}
	l.listingPosition++
}

// beyondListingCap reports whether the path now at the head of the listing would
// have fallen outside the snapshot's file cap even with no ignore rule at all.
//
// Unknown counts as outside. Once the accounting has abandoned part of a tree it
// was enumerating, listingPosition is a lower bound short by an unknown amount,
// so "inside the cap" stops being something this ledger can establish — and the
// invariant the cap gate exists for is that a committed rule is never blamed for
// a path the cap alone had already discarded. CountIncomplete is true in exactly
// the cases that set positionIncomplete, so what is withheld here is withheld out
// loud rather than in silence.
func (l *repoIgnoreLedger) beyondListingCap() bool {
	if l == nil || l.listingLimit <= 0 {
		return false
	}
	return l.positionIncomplete || l.listingPosition > l.listingLimit
}

// listingCapFull reports whether NO path still to reach the ledger can be inside
// the snapshot's file cap. beyondListingCap is the test for a path that has
// already taken its position; this is the lookahead for one that has not, and the
// off-by-one between them is real: a listing at position N with a cap of N still
// passes beyondListingCap and can still admit nothing, because the next candidate
// takes position N+1.
func (l *repoIgnoreLedger) listingCapFull() bool {
	if l == nil || l.listingLimit <= 0 {
		return false
	}
	return l.positionIncomplete || l.listingPosition >= l.listingLimit
}

// accountingStoppedShort reports whether any enumeration in this listing gave up
// on content it had started to count. Both halves are the same fact the report
// renders as CountIncomplete, read from the ledger while the walk is still
// running.
func (l *repoIgnoreLedger) accountingStoppedShort() bool {
	if l == nil {
		return false
	}
	return l.countIncomplete || len(l.unreadable) > 0
}

// notePositionIncomplete records that listingPosition is now a lower bound.
//
// It is called with a whole prune behind it rather than at the moment a budget is
// spent, and the difference is the disclosure a reader most needs. The read bound
// fires while a directory is being READ, before any of its entries have been
// visited: gating on it there would refuse the entire enumerated prefix and turn
// a 19,998-path disclosure of a runaway ignored tree into silence. Those paths
// are inside the tree the rule pruned and hold the positions counted for them;
// it is everything AFTER the prune whose position the shortfall makes unknowable.
func (l *repoIgnoreLedger) notePositionIncomplete() {
	if l == nil {
		return
	}
	l.positionIncomplete = true
}

func (l *repoIgnoreLedger) note(exclusion RepoExclusion) {
	if l == nil {
		return
	}
	// Gated HERE, at the one place an exclusion enters the ledger, for the same
	// reason the rule text is bounded here: a check at the call sites is the
	// shape that leaves one of them out. The snapshot's own file cap would have
	// discarded this path with no ignore rule in the repository at all, so the
	// rule is not what removed it from the corpus and the truncation warning
	// W_FILE_LIMIT already says what did.
	if l.beyondListingCap() {
		return
	}
	if l.seen == nil {
		l.seen = make(map[string]struct{})
	}
	if _, duplicate := l.seen[exclusion.Path]; duplicate {
		return
	}
	l.seen[exclusion.Path] = struct{}{}
	// Bounded HERE, at the one place an exclusion enters the ledger, so every
	// caller inherits it and none can forget: a second check at a call site is
	// exactly the shape that leaves one path unbounded.
	exclusion.Rule = boundRuleText(exclusion.Rule)
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

// spendExclusionWalk takes one entry from the listing's accounting budget and
// reports whether the walk may continue. Exhausting it marks the count a lower
// bound, because the rest of that excluded tree is excluded and uncounted.
func (l *repoIgnoreLedger) spendExclusionWalk() bool {
	if l == nil {
		return false
	}
	if l.walkVisited >= maxRepoExclusionWalkEntries {
		l.countIncomplete = true
		return false
	}
	l.walkVisited++
	return true
}

// remainingExclusionWalk reports how many more entries the accounting may visit
// in this listing. It is what bounds a directory READ, not merely the callbacks
// the read feeds: nothing past this many entries could ever be visited, so
// reading past it is pure cost.
func (l *repoIgnoreLedger) remainingExclusionWalk() int {
	if l == nil {
		return 0
	}
	if l.walkVisited >= maxRepoExclusionWalkEntries {
		return 0
	}
	return maxRepoExclusionWalkEntries - l.walkVisited
}

// maxRepoExclusionIgnoreBytes bounds the nested `.gitignore` text ONE listing's
// pruned-directory accounting may read. The walk-entry budget bounds how many
// entries are visited; it does not bound what visiting one costs, and entering a
// directory reads its `.gitignore` — up to maxNestedIgnoreFileBytes of it.
//
// Measured on this branch before this bound: a pruned tree of 300 directories
// each holding a 1 MiB `.gitignore` cost 26.65s of EVERY search against 9.83ms
// for the same tree with a 2-byte one, and both reported the identical 600
// exclusions. 901 of the 20,000 entries were visited, so the entry budget never
// came near firing. The size of that text is set by the repository whose rules
// the report exists to expose, which is the same amplification the rule-text
// bound and the walk bound already closed one layer up.
//
// 4 MiB is far past any real repository — 512 nested ignore files, the most one
// listing merges, at 8 KiB each — and it is shared across the whole listing, so
// splitting a rule over several pruned trees does not buy a fresh one.
const maxRepoExclusionIgnoreBytes = 4 << 20

// spendIgnoreBytes charges the listing for one nested `.gitignore` about to be
// read and reports whether the accounting may still afford it. A refusal means
// the file is NOT loaded, so the caller must stop descending rather than judge
// that subtree against rules it never read: attributing a descendant to the
// repository's rule when an unread nested `.gitignore` may have hidden it is the
// false alarm this disclosure spends every other bound avoiding.
func (l *repoIgnoreLedger) spendIgnoreBytes(n int64) bool {
	if l == nil {
		return false
	}
	if n < 0 {
		n = 0
	}
	if l.ignoreBytesRead+n > maxRepoExclusionIgnoreBytes {
		l.countIncomplete = true
		return false
	}
	l.ignoreBytesRead += n
	return true
}

// noteCountIncomplete marks Files a lower bound for a reason other than an
// unreadable path: a directory held more entries than the budget could pay to
// visit, so the rest of it is excluded and uncounted.
func (l *repoIgnoreLedger) noteCountIncomplete() {
	if l == nil {
		return
	}
	l.countIncomplete = true
}

// noteDirentsRead records entries read from one directory.
func (l *repoIgnoreLedger) noteDirentsRead(n int) {
	if l == nil {
		return
	}
	l.direntsRead += n
}

// walkDirentsRead counts the directory entries the accounting has READ in this
// listing, as opposed to the entries it went on to visit. The two diverged
// silently before the read was bounded, which is what made a bounded-looking
// count sit on top of an unbounded crawl, so it is counted rather than assumed.
func (l *repoIgnoreLedger) walkDirentsRead() int {
	if l == nil {
		return 0
	}
	return l.direntsRead
}

// noteUnreadable records a path an enumeration could not read. Deduplicated and
// capped the same way the sample is: one unreadable directory near the root can
// produce an error per entry, and a payload is not the place for all of them.
func (l *repoIgnoreLedger) noteUnreadable(path string) {
	if l == nil || path == "" {
		return
	}
	if l.unreadableSeen == nil {
		l.unreadableSeen = make(map[string]struct{})
	}
	if _, duplicate := l.unreadableSeen[path]; duplicate {
		return
	}
	l.unreadableSeen[path] = struct{}{}
	if len(l.unreadable) < maxRepoExclusionSample {
		l.unreadable = append(l.unreadable, path)
	}
}

// noteGitListingUnavailable records that a Git-applied repository rule removed a
// path from a listing produced without Git's own enumeration of a real checkout.
func (l *repoIgnoreLedger) noteGitListingUnavailable() {
	if l == nil {
		return
	}
	l.gitListingUnavailable = true
}

// report renders the ledger, or nil when nothing was excluded. Nil is what keeps
// the field absent from the overwhelmingly common payload that has nothing to
// disclose.
//
// An unreadable path alone is enough to render one: a prune whose whole tree was
// unreadable excluded an unknown number of files, and returning nil for it would
// answer "the repository hid nothing" to the one case where the truth is "the
// repository hid something and this could not see how much".
func (l *repoIgnoreLedger) report() *RepoIgnoreReport {
	if l == nil || (l.files == 0 && len(l.unreadable) == 0 && !l.countIncomplete && !l.gitListingUnavailable) {
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
	unreadable := append([]string(nil), l.unreadable...)
	sort.Strings(unreadable)
	return &RepoIgnoreReport{
		Files:           l.files,
		Sources:         sources,
		Sample:          sample,
		SampleTruncated: l.truncated,
		CountIncomplete: l.countIncomplete || len(unreadable) > 0,
		Unreadable:      unreadable,

		GitListingUnavailable: l.gitListingUnavailable,
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
	if err := matcher.loadOptional(filepath.Join(repo, graphIgnoreFileName), false, graphIgnoreOrigin()); err != nil {
		return ignoreMatcher{}, err
	}
	// info/exclude is the CHECKOUT's own exclude list — the local operator's, not
	// the repository's (see localIgnoreOrigin). Same syntax as .gitignore, but NOT
	// the same authority: Git consults it only while discovering UNTRACKED files,
	// so it is loaded here for the filesystem-walk fallback, where no Git listing
	// exists to have applied it, and dropped again wherever Git did list the tree
	// (withoutLocalExcludes). Reading only .gitignore silently pulled excluded
	// trees into that walk.
	//
	// It is NOT always at <repo>/.git/info/exclude. In a linked worktree, <repo>/.git
	// is a regular file holding "gitdir: <path>", so that join names a path under a
	// non-directory: os.Stat returns ENOTDIR rather than ErrNotExist, and treating
	// that as fatal aborted the entire search with zero results in every worktree.
	if exclude := gitInfoExcludePath(repo); exclude != "" {
		if err := matcher.loadOptional(exclude, false, localIgnoreOrigin(".git/info/exclude")); err != nil {
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
	if err := matcher.loadOptional(filepath.Join(repo, graphIgnoreFileName), false, graphIgnoreOrigin()); err != nil {
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
	gitDir, ok := readGitDirPointerFile(dotGit, maxGitFileBytes)
	if !ok {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	// commondir points at the shared .git that owns info/; it may be relative to gitDir.
	if common, ok := readTrimmedFile(filepath.Join(gitDir, "commondir"), maxGitPointerBytes); ok && common != "" {
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		gitDir = filepath.Clean(common)
	}
	return filepath.Join(gitDir, "info", "exclude")
}

const (
	// maxGitFileBytes mirrors Git's own read_gitfile_gently limit for a linked
	// worktree's .git file. Because an ordinary directory is a supported
	// repository here, an untrusted tree can plant an arbitrarily large
	// regular file at that path; refusing anything past this size via
	// os.Stat, before ever reading the content, bounds the allocation a
	// worktree search is forced to make per file rather than per repository.
	maxGitFileBytes = 1 << 20 // 1 MiB
	// maxGitPointerBytes bounds commondir, which Git does not size-limit
	// itself but which is never more than a short relative or absolute path
	// in a real repository.
	maxGitPointerBytes = 4096
)

// readGitDirPointerFile reads a linked worktree's .git file and returns the
// gitdir it names, or false if the file is oversized, unreadable, or not
// actually a Git-authored pointer. The size check runs via os.Stat before any
// read, so an oversized file never gets its content allocated at all. The
// "gitdir:" prefix is required rather than merely stripped: content lacking
// it is not metadata Git wrote, and treating it as a literal path anyway
// would let arbitrary file content redirect this repository's info/exclude
// lookup to a location the caller never intended.
func readGitDirPointerFile(path string, maxBytes int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	const prefix = "gitdir:"
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, prefix) {
		return "", false
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if gitDir == "" {
		return "", false
	}
	return gitDir, true
}

// readTrimmedFile reads a small pointer-style file and returns its trimmed
// content, or false if it is missing, not a regular file, larger than
// maxBytes, or unreadable. The size check runs via os.Stat before any read.
func readTrimmedFile(path string, maxBytes int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

func (m *ignoreMatcher) loadOptional(file string, includeMode bool, origin ignoreOrigin) error {
	label := ignoreFileLabel(includeMode)
	// Lstat, not Stat, but ONLY for a REPOSITORY-controlled ignore file
	// (.gitignore, .graphignore): such a file that is ITSELF a symlink can be
	// made to point outside the repository — at a sibling .env, say — and the
	// disclosure feature below echoes the matched PATTERN TEXT of whichever
	// rule decided a path into the JSON/NDJSON response (repoExclusion's Rule
	// field). Stat follows the link and reads the external target's content
	// as if it were the repository's own ignore rules, which turns that
	// disclosure into an arbitrary local-file-read primitive. Lstat reports
	// the link itself, which fails IsRegular() below the same way an existing
	// non-regular file (a directory, say) already does, so a symlinked
	// repo-controlled ignore file hits the SAME "not a regular file" hard
	// failure that path already produced — it is never opened, so its target
	// is never dereferenced or read.
	//
	// A CALLER-controlled source (.git/info/exclude here; --ignore-file and
	// --include-file in loadRequired below) is exactly the opposite case:
	// ignoreOrigin's own doc says only a repo-controlled rule is ever
	// disclosed, so a symlinked .git/info/exclude carries none of the leak
	// above — and Git itself follows such a symlink. Rejecting it anyway
	// turned "a user shares their local exclude file via symlink" into a
	// fatal error on every worktree search, for a file whose content was
	// never at risk. Stat, unchanged, for this branch.
	stat := os.Stat
	if !origin.callerControlled {
		stat = os.Lstat
	}
	info, err := stat(file)
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
	// Always caller-controlled (--ignore-file / --include-file): see
	// loadOptional for why that means an ordinary Stat, unchanged, is
	// correct here rather than the repo-controlled Lstat guard.
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
	rule, matched := m.decideSelfRule(rel, isDir)
	if !matched {
		return false, false
	}
	return true, rule.ignore
}

// decideSelfRule is decideSelf's single source of truth, so the verdict and the
// attribution of that verdict cannot come from different rules.
func (m ignoreMatcher) decideSelfRule(rel string, isDir bool) (ignoreRule, bool) {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return ignoreRule{}, false
	}
	var winner ignoreRule
	matched := false
	for _, rule := range m.rules {
		if rule.matchKind(rel, isDir) == ignoreSelfMatch {
			matched = true
			winner = rule
		}
	}
	return winner, matched
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
	repo string
	base ignoreMatcher
	// gitBase is base minus the rules Git does not apply, kept alongside it so the
	// walk can ask what Git alone would have done with a path. Every nested level
	// is a .gitignore, so the levels need no such twin.
	gitBase ignoreMatcher
	levels  []nestedIgnoreLevel
	// gitCheckout says repo is a real git working tree that Git itself could not
	// enumerate — the walk is running BECAUSE that listing failed. It is what
	// separates "no tracked files exist" (an ordinary directory: nothing Git
	// would have listed, so nothing suppressed here can be a tracked source)
	// from "tracked files exist and this mode cannot see which".
	gitCheckout bool
}

type nestedIgnoreLevel struct {
	dir     string
	matcher ignoreMatcher
}

func newNestedIgnoreStack(repo string, base ignoreMatcher) *nestedIgnoreStack {
	return &nestedIgnoreStack{
		repo:        repo,
		base:        base,
		gitBase:     base.gitApplied(),
		gitCheckout: isGitCheckout(repo),
	}
}

// isGitCheckout reports whether repo carries a git working tree's .git — a
// directory in an ordinary clone, a regular file holding "gitdir: ..." in a
// linked worktree. Lstat, not Stat: a symlinked .git is still a checkout, and
// following it is not this predicate's business.
func isGitCheckout(repo string) bool {
	_, err := os.Lstat(filepath.Join(repo, ".git"))
	return err == nil
}

// gitApplied is the subset of the rules Git ITSELF would apply: .gitignore, a
// nested .gitignore, .git/info/exclude. It answers "would Git have hidden this
// path anyway", which is the question the walk fallback has to answer for itself
// because the mode exists precisely where Git's own listing is unavailable.
func (m ignoreMatcher) gitApplied() ignoreMatcher {
	rules := make([]ignoreRule, 0, len(m.rules))
	for _, rule := range m.rules {
		if rule.origin.gitInvisible {
			continue
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		return ignoreMatcher{}
	}
	return ignoreMatcher{rules: rules}
}

// enter registers the directory the walk is about to descend into (repo-relative,
// slash-separated; "" for the repository root) and loads its .gitignore, if any.
// Levels the walk has left are dropped, so the stack holds one matcher per
// ancestor directory of the current position.
func (s *nestedIgnoreStack) enter(dir string) {
	// The listing walk reads a directory it is listing anyway, so its nested
	// .gitignore costs what any scan of that tree costs. A nil ledger charges
	// nothing and never refuses.
	s.enterCharged(nil, dir)
}

// enterCharged is enter for the pruned-directory accounting: it charges ledger
// for the nested .gitignore it is about to read and reports whether the caller
// may descend.
//
// It returns false ONLY when a .gitignore exists and the budget cannot pay to
// read it. The rules of an unread ignore file are rules this stack does not
// have, so every verdict below that directory would be reached without them —
// crediting the repository's rule with removing paths a nested .gitignore may
// have hidden. Not descending, and saying the count is a lower bound, is the
// same trade the walk budget and the unreadable-path disclosure already make.
func (s *nestedIgnoreStack) enterCharged(ledger *repoIgnoreLedger, dir string) bool {
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
		return true
	}
	file := filepath.Join(s.repo, filepath.FromSlash(dir), ".gitignore")
	// Lstat, not Stat: see loadOptional's comment. A nested .gitignore that is
	// itself a symlink to outside the repository must not have its target's
	// content read and echoed as rule text; treated as absent here exactly
	// like a missing file, descent continues with one fewer level of rules
	// rather than following the link.
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return true
	}
	if info.Size() > maxNestedIgnoreFileBytes {
		// Git can still apply this file's rules regardless of its size, so
		// treating it as absent and continuing to descend would credit the
		// repository's own rule with removing every descendant this ignore
		// file actually hides — the same phantom-exact-count the unparseable
		// and over-budget cases below already refuse to produce.
		s.noteUnreadablePath(ledger, file, dir)
		return false
	}
	if ledger != nil && !ledger.spendIgnoreBytes(info.Size()) {
		return false
	}
	var matcher ignoreMatcher
	if err := matcher.loadFile(file, false, repoIgnoreOrigin(path.Join(dir, ".gitignore"))); err != nil {
		// Read but not parsed leaves the stack in exactly the state the budget
		// refusal above describes: rules this stack does not have. bufio.Scanner
		// refuses a token over 64 KiB, so one long line in a .gitignore far under
		// maxNestedIgnoreFileBytes drops every rule in the file — including the
		// ones already parsed, since the partial matcher goes with the error — and
		// descending then credits the repository's own rule with removing paths
		// that nested .gitignore hides. Same refusal, and the path is named so the
		// disclosure reports an unreadable subtree rather than an ordering problem.
		s.noteUnreadablePath(ledger, file, dir)
		return false
	}
	s.levels = append(s.levels, nestedIgnoreLevel{dir: dir, matcher: matcher})
	return true
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

// decidingRule returns the rule behind Ignored's verdict, under exactly the same
// precedence, so a walk can attribute an exclusion to the file that caused it.
func (s *nestedIgnoreStack) decidingRule(rel string, isDir bool) (ignoreRule, bool) {
	if rule, matched := s.base.decideSelfRule(rel, isDir); matched {
		return rule, true
	}
	rel = cleanIgnorePath(rel)
	for i := len(s.levels) - 1; i >= 0; i-- {
		level := s.levels[i]
		sub, ok := pathUnder(level.dir, rel)
		if !ok {
			continue
		}
		if rule, matched := level.matcher.decideRule(sub, isDir); matched {
			return rule, true
		}
	}
	return s.base.decideRule(rel, isDir)
}

// noteRepoExclusion records rel in the ledger when a repository-controlled rule
// that GIT DOES NOT APPLY removed it — in practice .graphignore.
//
// The narrower test is what this listing mode can honestly support. Git's own
// listing is unavailable here (that is why the walk is running), so there is no
// tracked/untracked distinction to separate "a committed rule deleted a source
// file" from the ordinary build output every .gitignore excludes; counting the
// latter would bury the disclosure in noise. A .graphignore rule has no such
// ambiguity: Git does not know the file, so everything it removes is content Git
// would still have listed.
func (s *nestedIgnoreStack) noteRepoExclusion(ledger *repoIgnoreLedger, rel string, isDir bool) {
	if ledger == nil {
		return
	}
	rule, matched := s.decidingRule(rel, isDir)
	if !matched || !rule.ignore || rule.origin.callerControlled {
		return
	}
	// Winning the precedence contest is not enough. A .graphignore rule can win it
	// over a Git-applied rule that covers the same path — `*.gen.go` beats a
	// `.gitignore` line naming one generated file, and `.graphignore` loads last —
	// and then the path is ordinary build output Git would have hidden regardless.
	// Reporting it would cry wolf and print paths nobody asked about, which is the
	// noise that makes readers skip the disclosure that matters.
	//
	// "Would have hidden regardless" is true of an UNTRACKED path only. Git does
	// not apply .gitignore to a tracked file, so in a real checkout the same test
	// also swallows a tracked source the repository's rules removed — and this
	// mode runs because Git could not be asked which is which. Unattributable is
	// not the same as absent, so the limitation is recorded instead of the path.
	if !rule.origin.gitInvisible || s.ignoredByGit(rel, isDir) {
		s.noteGitBlindSpot(ledger)
		return
	}
	ledger.note(RepoExclusion{
		Path:   cleanIgnorePath(rel),
		Source: rule.origin.label,
		Rule:   rule.pattern,
	})
}

// notePrunedRepoExclusion records what a DIRECTORY prune removed.
//
// filepath.WalkDir returns SkipDir before any child of an ignored directory is
// tested, so the per-file noteRepoExclusion never sees them: one `hidden/` line
// in .graphignore could delete an entire source tree from the corpus with an
// empty ledger behind it. The descendants are enumerated here instead, so the
// disclosure names paths a reader can open.
//
// The qualification is the same one noteRepoExclusion applies to a file — a
// repository rule Git does not apply, whose verdict Git's own rules do not
// already reach — and it is applied to the DIRECTORY and then again to every
// descendant one at a time. Both halves are needed: without the first, every
// build/, dist/ and node_modules/ in the tree would print its contents; without
// the second, a .gitignore INSIDE the pruned tree stops being consulted the
// moment the prune happens, and generated files Git hides on its own get named
// as source the repository removed. Either way the disclosure cries wolf, which
// is the noise that makes readers skip the one that matters.
//
// The traversal is bounded twice, and the bound covers the READ as well as the
// visit. maxRepoExclusionWalkEntries caps the entries it may visit across the
// whole listing, and walkPrunedBounded stops each directory read at what is left
// of that budget — filepath.WalkDir reads a directory in full before its first
// entry reaches the callback, so a budget spent in the callback alone bounded
// the reported count while the crawl behind it stayed the repository's to size.
// Reaching the cap marks the count a lower bound rather than quietly returning a
// short exact-looking number: the
// prune is what makes an ignored tree cost nothing, and handing that cost back
// unbounded lets a committed rule over a huge tree slow every search that reads
// this repository. The list of named paths is capped separately, at
// maxRepoExclusionSample, by the ledger.
//
// Under that cap it also takes EVERY prune the outer walk takes:
// vendored directories and files, and directories Git's own rules exclude. That
// is what makes the stated cost true — accounting for a prune visits a subset of
// what the outer walk would have visited had the prune not happened, never more.
// Filtering those paths one at a time after descending into them was both slower
// (a `.graphignore` line over a tree holding node_modules crawled every entry in
// it) and wrong: the count then included files the scan never wanted, so the
// disclosure blamed the repository's rule for removing content no rule removed.
//
// Pruning at a Git-excluded directory rather than filtering its files is what Git
// itself does — a nested `!keep.go` cannot re-include a file whose parent
// directory is excluded — so the set of disclosed paths is unchanged by it.
func (s *nestedIgnoreStack) notePrunedRepoExclusion(ledger *repoIgnoreLedger, rel string, dirTracked func(string) bool) {
	if ledger == nil {
		return
	}
	// Nothing this walk could find is still attributable: note() refuses every
	// exclusion once the listing is past the snapshot's cap, so enumerating the
	// tree reads its directories and parses its nested ignore files to produce a
	// set of records that are all discarded. Worse than free — on a tree past the
	// walk budget it also spends that budget, raises CountIncomplete, and files a
	// repo_ignored partial failure over content the cap, not the rule, had already
	// removed from the corpus.
	if ledger.listingCapFull() {
		return
	}
	dir := cleanIgnorePath(rel)
	if dir == "" {
		return
	}
	if dirTracked == nil {
		// The walk that owns this ledger always supplies one; a caller that does
		// not gets the same answer a repository with no Git index gives.
		dirTracked = func(string) bool { return false }
	}
	rule, matched := s.decidingRule(dir, true)
	if !matched || !rule.ignore || rule.origin.callerControlled {
		return
	}
	if !rule.origin.gitInvisible || s.ignoredByGit(dir, true) {
		// Same unattributable case as a single file, one level up: a `.gitignore`
		// directory line prunes tracked sources as readily as build output, and
		// without Git's listing the two cannot be told apart.
		s.noteGitBlindSpot(ledger)
		return
	}
	// A private stack, so descending into the pruned tree to load its nested
	// .gitignore files cannot disturb the walk that is standing at the prune. Its
	// own level is dropped and re-entered below, so the tree is read the same way
	// whether or not the caller had already entered it.
	sub := &nestedIgnoreStack{repo: s.repo, base: s.base, gitBase: s.gitBase, gitCheckout: s.gitCheckout}
	for _, level := range s.levels {
		if level.dir == dir {
			continue
		}
		sub.levels = append(sub.levels, level)
	}
	root := filepath.Join(s.repo, filepath.FromSlash(dir))
	walkPrunedBounded(ledger, root, func(current string, entry fs.DirEntry, err error) error {
		// Budget first, before anything is stat'd or matched, so the bound holds
		// for the walk's own cost and not merely for what it reports. SkipAll ends
		// this tree; the ledger keeps the exhausted budget, so a repository cannot
		// win a fresh one by splitting its rule across several directories.
		if !ledger.spendExclusionWalk() {
			return fs.SkipAll
		}
		// An error here is a subtree that is excluded and cannot be counted. The
		// walk continues — one unreadable directory must not silence the disclosure
		// of the paths it CAN name — but the shortfall is recorded, because Files
		// promises to be exact and this is the one thing that can make it a lower
		// bound. Swallowing it returned a successful, understated security
		// disclosure with nothing to distinguish it from a true one.
		if err != nil || entry == nil {
			s.noteUnreadablePath(ledger, current, dir)
			return nil
		}
		child, relErr := filepath.Rel(s.repo, current)
		if relErr != nil {
			s.noteUnreadablePath(ledger, current, dir)
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		childRel := cleanIgnorePath(filepath.ToSlash(child))
		if entry.IsDir() {
			// Same discipline as the outer walk: enter before judging anything
			// inside, so the deepest .gitignore with an opinion is on the stack —
			// charged, because reading it is work the prune had already saved and
			// its size is set by the repository. Refused means the rules of this
			// subtree were never read, so nothing under it can be attributed.
			if !sub.enterCharged(ledger, childRel) {
				return filepath.SkipDir
			}
			// And the same prunes, in the same order. A vendored tree inside the
			// pruned one was never going to be in the corpus, so crediting the
			// repository's rule with removing it is a false alarm — and walking it
			// to find that out is the cost the outer walk avoids by not walking it.
			if skipVendoredDir(childRel, entry.Name(), sub, dirTracked) {
				return filepath.SkipDir
			}
			// A directory Git's own rules exclude is one the outer walk prunes
			// wholesale. Descending to filter its files one by one reached the same
			// verdict for each of them while paying for the whole subtree, and an
			// unreadable directory down there then reported this exclusion count as
			// a lower bound over content that was never part of the count.
			if sub.ignoredByGit(childRel, true) && !sub.MayIncludeDescendant(childRel) {
				// "Git would have hidden it anyway" is true of an UNTRACKED path
				// only, and this mode runs because Git could not be asked. Pruning
				// here therefore drops a whole subtree that may hold tracked sources
				// the repository's own rule removed — the same unattributable case
				// the per-file sink records one level up, and recording it there
				// while staying silent here reported a partial disclosure as a
				// complete one.
				sub.noteGitBlindSpot(ledger)
				// The whole subtree's descendants are skipped here without ever
				// reaching noteListingCandidate, and unlike the single-file sink
				// below, the count skipped is unbounded and unknown. Leaving
				// listingPosition unmarked let a LATER exclusion elsewhere in this
				// listing test as "inside the cap" only because this subtree's
				// positions were never counted — crediting .graphignore for an
				// exclusion the cap alone would already have produced.
				ledger.notePositionIncomplete()
				return filepath.SkipDir
			}
			return nil
		}
		// IsRegular is false for a symlink, so the listing's own rule — it never
		// follows one — holds for what the prune is credited with removing.
		if !entry.Type().IsRegular() {
			return nil
		}
		// Lockfiles and source maps: the outer walk drops them by name wherever
		// they sit, so no ignore rule can be what removed them.
		if isVendoredScanFile(childRel, entry.Name()) {
			return nil
		}
		if sub.ignoredByGit(childRel, false) {
			// Same swallow, one path at a time: a tracked source is visible to Git
			// whatever .gitignore says, so this drop is unattributable rather than
			// uninteresting.
			sub.noteGitBlindSpot(ledger)
			return nil
		}
		// A descendant of a pruned directory is a path the listing would have
		// offered had the rule not been there, so it takes a position in the
		// counterfactual listing exactly as a per-file candidate does.
		ledger.noteListingCandidate()
		ledger.note(RepoExclusion{
			Path:   childRel,
			Source: rule.origin.label,
			Rule:   rule.pattern,
		})
		return nil
	})
	// This is the ONE producer that can leave a path out of the counterfactual
	// position while the counterfactual listing still holds it, so it is the one
	// place a position can stop being a position. The outer walk counts every path
	// it reaches, kept or excluded, before deciding anything about it.
	if ledger.accountingStoppedShort() {
		ledger.notePositionIncomplete()
	}
}

// walkPrunedBounded walks root the way filepath.WalkDir does — a directory
// handed to fn before its children, children in lexical order — with the one
// difference the accounting budget depends on: it never reads more of a
// directory than the budget can still pay to visit.
//
// filepath.WalkDir reads and sorts a directory IN FULL before the first child
// reaches fn, so a budget spent inside fn bounds the callbacks and not the work.
// Measured on this branch before this change, one pruned directory of 200,000
// entries cost 468ms of every search against 112ms for 20,000 while the ledger
// recorded the same 19,998 exclusions for both: a bounded number sitting on top
// of an unbounded crawl. The prune is what makes an ignored tree cost nothing,
// and reading that tree to say what it removed must not hand back a cost the
// repository sets.
//
// Reading remaining+1 entries is exactly enough. A directory holding more than
// the budget can pay for will exhaust it while the prefix is being visited, so
// nothing past that prefix could ever be reached — reading it buys the report
// nothing and the repository a multiple of every search. The shortfall is
// recorded, so Files stays a stated lower bound rather than a silently short
// number.
//
// Entries are sorted by listingOrderKey after reading — the same key
// capSourceFiles' flat listing sorts by, not filepath.WalkDir's plain Name()
// order, which disagrees with it at every directory/file name collision (a
// sibling `a.go` and `a/` visit in the opposite order under the two keys; see
// listingOrderKey). Matching that key is what lets a position counted here
// agree with the position the same path would hold in the listing the file
// cap truncates. Only a directory larger than the remaining budget takes
// filesystem order for its prefix, and that report already says the count is
// incomplete.
func walkPrunedBounded(ledger *repoIgnoreLedger, root string, fn fs.WalkDirFunc) {
	info, err := os.Lstat(root)
	var entry fs.DirEntry
	if err == nil {
		entry = fs.FileInfoToDirEntry(info)
	}
	_ = walkPrunedBoundedNode(ledger, root, entry, err, fn)
}

// walkPrunedBoundedNode visits one node and, for a directory, its children.
// SkipDir returned for a directory skips that directory's contents; returned for
// anything else it skips the rest of the containing directory; SkipAll and any
// other error stop the walk. That is filepath.WalkDir's contract, kept because
// the callback this serves was written against it.
func walkPrunedBoundedNode(ledger *repoIgnoreLedger, current string, entry fs.DirEntry, statErr error, fn fs.WalkDirFunc) error {
	if err := fn(current, entry, statErr); err != nil {
		if errors.Is(err, filepath.SkipDir) {
			if entry != nil && entry.IsDir() {
				return nil
			}
			return filepath.SkipDir
		}
		return err
	}
	if statErr != nil || entry == nil || !entry.IsDir() {
		return nil
	}
	entries, err := readDirBounded(ledger, current)
	if err != nil {
		// filepath.WalkDir reports a directory whose listing failed to fn a second
		// time, with the error, and the callback here turns that into the
		// unreadable-path disclosure. Dropping it would report a short count as
		// exact.
		if skip := fn(current, entry, err); skip != nil {
			if errors.Is(skip, filepath.SkipDir) {
				return nil
			}
			return skip
		}
		return nil
	}
	for _, child := range entries {
		if err := walkPrunedBoundedNode(ledger, filepath.Join(current, child.Name()), child, nil, fn); err != nil {
			if errors.Is(err, filepath.SkipDir) {
				return nil
			}
			return err
		}
	}
	return nil
}

// readDirBounded reads at most the remaining budget's worth of one directory,
// sorted, and marks the count a lower bound when the directory held more.
//
// The order that prefix is taken IN is the filesystem's, not the repository's: a
// directory larger than the remaining budget hands back whichever entries
// getdents offers first, and only those are then sorted. So for such a directory
// both the count and the sampled paths are a property of the filesystem as well
// as of the tree, and the SAME repository view can disclose different paths on
// two machines.
//
// That is the price of the read bound and it is not paid down by sorting more:
// a deterministic prefix means reading the whole directory, which is exactly the
// unbounded, repository-sized crawl the bound exists to stop
// (TestPrunedExclusionAccountingBoundsWhatItReads fails the moment the read is
// made whole). What the report must not do is present the sample as canonical
// when it is not, so the shortfall this raises SAYS the enumerated paths are a
// filesystem-order sample — see withRepoIgnorePartialFailures.
func readDirBounded(ledger *repoIgnoreLedger, dir string) ([]fs.DirEntry, error) {
	remaining := ledger.remainingExclusionWalk()
	handle, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	// remaining+1 distinguishes "the whole directory" from "as much of it as the
	// budget allows"; the extra entry is read and discarded, never visited.
	entries, err := handle.ReadDir(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	ledger.noteDirentsRead(len(entries))
	if len(entries) > remaining {
		ledger.noteCountIncomplete()
		entries = entries[:remaining]
	}
	// Sort by the same key listingOrderWalk/capSourceFiles use, not by bare
	// Name(): a directory `a/` sorts before the sibling file `a.go` by name
	// alone, but capSourceFiles truncates the flat listing where `a.go`
	// (nothing after it sorts below the '.' in its own name) comes first and
	// everything under `a/` comes after. Sorting these children by Name()
	// alone let this walker visit `a/` — and prune-account for its
	// descendants — before `a.go`, while the outer walk's cap had already
	// admitted `a.go` and excluded `a/`'s descendants (or vice versa),
	// mismatching which candidates the two passes agree are inside the cap.
	sort.Slice(entries, func(i, j int) bool { return listingOrderKey(entries[i]) < listingOrderKey(entries[j]) })
	return entries, nil
}

// noteGitBlindSpot records that a repository rule GIT ITSELF APPLIES removed a
// path from this listing, in a mode that cannot ask Git whether Git would have
// listed it anyway.
//
// Gated on the directory actually being a checkout: where there is no .git there
// are no tracked files, so nothing a `.gitignore` removes could be content Git
// would still have shown, and the warning would be pure noise. Where there is
// one, the alternative is a report that says nothing about a corpus the
// repository may have narrowed — the exact silence this ledger exists to end.
func (s *nestedIgnoreStack) noteGitBlindSpot(ledger *repoIgnoreLedger) {
	if !s.gitCheckout {
		return
	}
	ledger.noteGitListingUnavailable()
}

// noteUnreadablePath records one enumeration failure as a repository-relative
// path. The absolute path is never recorded: it names the operator's own
// filesystem, and this report is delivered to whoever reads the answer. When the
// path cannot be made relative, the pruned directory stands in for it — less
// precise, still true, and still inside the repository.
func (s *nestedIgnoreStack) noteUnreadablePath(ledger *repoIgnoreLedger, current, fallback string) {
	rel, err := filepath.Rel(s.repo, current)
	if err != nil {
		ledger.noteUnreadable(fallback)
		return
	}
	ledger.noteUnreadable(cleanIgnorePath(filepath.ToSlash(rel)))
}

// ignoredByGit reports whether the rules Git itself applies already exclude rel,
// under the same precedence Ignored uses over that subset.
func (s *nestedIgnoreStack) ignoredByGit(rel string, isDir bool) bool {
	if matched, ignored := s.gitBase.decideSelf(rel, isDir); matched {
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
	return s.gitBase.Ignored(rel, isDir)
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
