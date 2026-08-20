package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// Caller-named output files: `index --report` and `verify --record-baseline`
// ==========================================================================
//
// Both verbs write ONE file to a path the CALLER names, and both are documented
// to take a path anywhere on the machine: docs/trust-and-security.md says each
// writes "to the path you give it", and the verify help itself advertises
// `--record-baseline /tmp/base.json`. That freedom is the feature; confining
// every write under the repository would break the documented invocation, break
// `--report ../reports/x.md` in CI, and break every /tmp example on macOS, where
// /tmp is itself a symlink.
//
// The hazard is the other end of that range. When the named path lands INSIDE the
// scanned repository — `--report GRAPH_REPORT.md`, the in-repo spelling the tool
// prints — the file sitting at that path was chosen by the repository, which is
// untrusted input. A hostile clone commits GRAPH_REPORT.md as a mode-120000
// symlink; os.WriteFile follows it, and the report bytes truncate and replace
// whatever it points at while the link survives for the next run.
//
// So the rule is conditional, and the condition is OWNERSHIP:
//
//   - OUTSIDE the repository: write exactly as before. A symlink there was placed
//     by the caller or their operator, not by the scanned tree, and anyone who can
//     plant it can write the file directly. This is the same reasoning that leaves
//     cache writes unconfined (see internal/sem/cache_entry.go).
//   - INSIDE the repository: the path is repository-controlled, so the write goes
//     through os.Root — which cannot be made to leave the repository root — and a
//     symlinked component at ANY depth is refused rather than followed. os.Root
//     alone is not enough for the second half: it stops the write leaving the root,
//     but it happily FOLLOWS a link that stays inside it (on every platform, Windows
//     included), and `.git/config` is inside it. The refusal covers the ROUTE the
//     path is read through as well as the destination it names, because a ".."
//     resolves a link away before the destination is spelled — see
//     refuseSymlinkedComponents and, for the route, refuseTraversedSymlinks.
//
// Those are CHECKS, and a check is not the enforcement. The write itself is opened
// component by component, holding each one open and verifying it by identity, so
// there is no window between deciding and opening for the repository to swap a link
// into — see openConfinedOutputFile.
//
// Two things decide "inside", and both exist so that a SPELLING cannot move the
// boundary. The root is the checkout's git top level rather than the --repo value
// (confinementRoot, since --repo may name a subdirectory), and containment is
// filesystem identity rather than lexical comparison (containedRel, since one
// directory has many names). Each carries its own reasoning below.

// repoOutputTarget is the resolved destination of one caller-named output file.
type repoOutputTarget struct {
	// root is the repository root the write is confined to. It is empty when the
	// target is outside the repository and therefore caller-owned.
	root string
	// rel is the target's path beneath root, in native separators. Set only with root.
	rel string
	// path is the absolute destination, used for the unconfined write.
	path string
	// given is the caller's own spelling of the path, used in messages so the
	// remedy names the flag value the caller typed.
	given string
	// traversed is that same spelling made absolute WITHOUT cleaning: the route
	// the kernel walks to reach path, which is where a committed symlink can be
	// resolved away before the destination is named. Empty when there is nothing
	// to walk that the destination's own components do not already cover.
	traversed string
}

func (target repoOutputTarget) insideRepo() bool { return target.root != "" }

// confinementRoot resolves the boundary a write is classified against: the TOP
// LEVEL of the checkout repoRoot sits in, rather than repoRoot itself.
//
// --repo may name a SUBDIRECTORY (`index --repo internal/cli`), and the untrusted
// thing is the CHECKOUT, not the subtree that was indexed. The root-level files
// are on disk next to the subdirectory and came out of the same hostile clone;
// `git log -z --name-only` (gitutil/git.go:440, which backs co-change) even
// reports paths across the whole top level while git runs from the subdirectory.
// Classifying `--report GRAPH_REPORT.md` against the subdirectory would call that
// root-level file caller-owned and write through whatever the clone left there.
//
// The fallback — repoRoot unchanged — is load-bearing rather than defensive.
// `verify` runs the caller's own test command in any directory they name, and
// that directory is not required to be a git repository at all; a bare repository
// and a plain directory must both keep working.
//
// It is NOT, as this comment used to claim, free of consequence for being a
// narrowing. Narrowing is safe only where there is nothing above repoRoot to
// confine against; when repoRoot IS inside a checkout, narrowing to it is the
// original escape, because the checkout's root-level files then sit outside the
// narrowed boundary, classify as caller-owned and take the unconfined write,
// straight through whatever symlink the clone committed there.
//
// Asking git is not a reliable way to find that out, because
// the question is answered by an EXTERNAL PROGRAM that fails for reasons which have
// nothing to do with the answer: no git on PATH (`verify` needs none, and indexing a
// mounted tree from a minimal image is an ordinary way to run this), a checkout whose
// files another uid owns, so every git command in it stops at `detected dubious
// ownership`, or a --repo that is a committed symlink to a directory outside the
// checkout, where `rev-parse` runs somewhere that is not a repository at all.
//
// So a failure to ASK is not evidence of an ANSWER. discoverCheckoutRoot reads the
// same thing git would, from the filesystem, and the fallback is taken only once that
// finds nothing either.
func confinementRoot(ctx context.Context, repoRoot string) string {
	if repoRoot == "" {
		return repoRoot
	}
	top, err := gitutil.RepoRoot(ctx, repoRoot)
	if err == nil && strings.TrimSpace(top) != "" && containsByIdentity(top, repoRoot) {
		return top
	}
	if discovered, ok := discoverCheckoutRoot(repoRoot); ok && containsByIdentity(discovered, repoRoot) {
		return discovered
	}
	return repoRoot
}

// discoverCheckoutRoot finds the top level of the checkout dir sits in without
// running git: the nearest ancestor holding a `.git` entry, which is git's own
// discovery rule (`setup_git_directory`), so the boundary it reports is the one
// `rev-parse --show-toplevel` would have reported when git can be asked at all.
//
// The entry is looked for with Lstat and is not required to be a directory: a
// linked worktree and a submodule both spell `.git` as a FILE holding `gitdir:`,
// and a repository is no less untrusted for being checked out that way.
//
// Both chains are walked, named then resolved, for the reason containedRel walks
// two: --repo may be an alias INTO a checkout (`/tmp/alias` -> `<checkout>/sub`),
// whose lexical ancestors are /tmp's and never reach the checkout.
func discoverCheckoutRoot(dir string) (string, bool) {
	if top, ok := gitEntryAncestor(dir); ok {
		return top, true
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved == dir {
		return "", false
	}
	return gitEntryAncestor(resolved)
}

// gitEntryAncestor climbs dir's own parents looking for the nearest one that holds a
// `.git` entry, and returns it.
func gitEntryAncestor(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
			return abs, true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", false
		}
		abs = parent
	}
}

// containsByIdentity reports whether top is repoRoot itself, or one of the
// directories above it, compared by os.SameFile rather than by name.
//
// It is what lets confinementRoot widen the boundary without trusting the
// SPELLING that came back from git. A root that names some OTHER directory is not
// a narrower boundary, it is NO boundary: every path in the real checkout then
// classifies as outside it and takes the unconfined write, which is exactly the
// symlink escape this file exists to close. The name can be wrong without anything
// being adversarial — git prints the path and a newline, and any trimming that
// takes one byte too many (a checkout whose name ends in a space) renames the root.
//
// Refusing sends confinementRoot to discoverCheckoutRoot and, failing that, to the
// directory the caller named. That last fallback is safe as a DEFAULT and unsafe as
// an OUTCOME — the checkout's own root-level files are not under it, so they classify
// as caller-owned and take the unconfined write — which is why both chains below are
// walked before refusing, and why refusing is no longer the end of the search.
func containsByIdentity(top, repoRoot string) bool {
	topInfo, err := os.Stat(top)
	if err != nil || !topInfo.IsDir() {
		return false
	}
	dir, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	// walkToRoot starts at the parent, so the ordinary case — --repo IS the top
	// level — is checked here first. os.Stat FOLLOWS links, so this also covers a
	// --repo that is a symlink to the top level itself.
	if info, err := os.Stat(dir); err == nil && os.SameFile(topInfo, info) {
		return true
	}
	if _, ok := walkToRoot(topInfo, dir); ok {
		return true
	}
	// The NAMED chain is not the only chain, for the same reason containedRel walks
	// two: one directory has many names. --repo may be an external symlink INTO the
	// checkout (`/tmp/alias` -> `<checkout>/sub`, the shape a build script or an
	// editor's "open recent" leaves behind). Git resolves it and reports the real
	// top level, but the alias's lexical parents are /tmp's and never reach it, so
	// the widening was refused and the boundary fell back to the alias — under which
	// the checkout's root-level files do not sit. They then classified as
	// caller-owned and took the unconfined write, straight through whatever symlink
	// the clone committed there. Resolving the alias puts the walk back on the
	// checkout's own chain.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved == dir {
		return false
	}
	_, ok := walkToRoot(topInfo, resolved)
	return ok
}

// classifyOutputPath decides whether path is repository-controlled or caller-owned.
// It never resolves the FINAL component: that component is the symlink a confined
// write exists to refuse, and resolving it would report the link's target as the
// destination and classify the write by the attacker's choice.
//
// TWO spellings of the path are classified, and the ON-DISK one decides first.
// filepath.Abs CLEANS, which collapses a ".." textually before any symlink is
// traversed; the os.WriteFile this helper replaced did not, because it handed the
// path to the KERNEL, where ".." steps out of the link's TARGET. With
// `link -> real/sub`, `link/../report.md` is `real/report.md` on disk and
// `report.md` beside the link once cleaned — two different files. realOutputPath
// keeps the on-disk reading, so the destination does not move.
//
// The cleaned spelling is classified second, and only for a route the REPOSITORY
// controls. The two spellings disagree exactly when a ".." is traversed through a
// link, and which one to keep turns on who owns that link:
//
//   - the repository's own (`link` -> .git/objects, then `--report link/../config`):
//     on disk that leaves the repository, and calling it caller-owned is the escape.
//     The cleaned spelling stays under the root, so it is what carries the path into
//     the confined write where the traversal is refused.
//   - the CALLER's, outside the repository (`/work/link` -> /other/a/b, then
//     `--report /work/link/../repo/out.md`): the kernel opens /other/a/repo/out.md
//     and the cleaned spelling names /work/repo/out.md, a different file that
//     happens to sit inside the scanned repository. Preferring it there moves the
//     caller's output and truncates a file they never named. There is nothing to
//     confine either — that link is caller-owned by the same rule that leaves the
//     whole out-of-repository write unconfined — so the on-disk destination stands.
func classifyOutputPath(repoRoot, path string) (repoOutputTarget, error) {
	// Both spellings this function compares are made absolute by filepath.Abs, which
	// CLEANS, and cleaning erases the one thing that says "this is a directory": the
	// trailing separator, and a final "." or "..". The kernel does not erase it —
	// `open("out.md/", O_CREAT|O_TRUNC)` is ENOTDIR while out.md is a file — so the
	// os.WriteFile this helper replaced failed and left the file alone, and the
	// cleaned spelling instead named out.md itself and truncated it. Both verbs write
	// a FILE, so a spelling that names a directory can only ever fail; refusing it
	// here keeps that failure and keeps it at the one sink both verbs pass through.
	if namesADirectory(path) {
		return repoOutputTarget{}, fmt.Errorf(
			"refusing to write %s: the path names a directory, not a file. "+
				"Name the output file itself", path)
	}
	// Both spellings are taken against the process working directory, not against
	// --repo, which is what the os.WriteFile this helper replaced did.
	onDisk, err := realOutputPath(path)
	if err != nil {
		return repoOutputTarget{}, fmt.Errorf("resolve output path %q: %w", path, err)
	}
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return repoOutputTarget{}, fmt.Errorf("resolve output path %q: %w", path, err)
	}
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return repoOutputTarget{}, fmt.Errorf("resolve repository root %q: %w", repoRoot, err)
	}
	traversed := traversedPath(path)
	if rel, ok := containedRel(rootAbs, onDisk); ok {
		return repoOutputTarget{root: rootAbs, rel: rel, path: onDisk, given: path, traversed: traversed}, nil
	}
	// Neither the destination the kernel opens nor the cleaned one has to stay under
	// the root for the ROUTE to be the repository's. `<repo>/link/../../victim`, with
	// `link` committed as a symlink to /attacker/a/b, leaves the repository on both
	// readings — the kernel lands on /attacker/victim, cleaning lands beside the
	// checkout — and the file it lands on was chosen by the clone either way. So the
	// question the route asks is answered BEFORE either destination is trusted.
	if routed, ok := traversedRepositorySymlink(rootAbs, traversed); ok {
		// The cleaned spelling names a DIFFERENT FILE than the one the kernel opens,
		// so preferring it is limited to the routes that need it: this one, which the
		// repository controls. Carrying it into the confined write is what puts the
		// route in front of refuseTraversedSymlinks with the destination still under
		// the root; where it is not under the root there is no confined write to carry
		// it into, and the same refusal is the answer here.
		if rel, ok := containedRel(rootAbs, cleaned); ok {
			return repoOutputTarget{root: rootAbs, rel: rel, path: cleaned, given: path, traversed: traversed}, nil
		}
		return repoOutputTarget{}, traversedSymlinkError(routed, path)
	}
	// Where the only link on the route is the caller's own, the on-disk destination
	// stands, exactly as the os.WriteFile this helper replaced left it.
	return repoOutputTarget{path: onDisk, given: path}, nil
}

// realOutputPath makes path absolute the way the PLATFORM reads it: the directory
// part is resolved through symlinks, so a ".." after a symlinked component steps
// out of the link's TARGET rather than out of the link's parent. The final
// component is deliberately left as a name — it is the symlink a confined write
// exists to refuse.
//
// Without a ".." component there is nothing for filepath.Clean to move across a
// link, so the cleaned absolute path already names the file the kernel would open
// and is returned unchanged. That keeps this helper off the common path entirely,
// and keeps every spelling filepath.Abs understands that the concatenation below
// could not reproduce.
func realOutputPath(path string) (string, error) {
	return realOutputPathOn(path, runtime.GOOS == "windows")
}

// realOutputPathOn is realOutputPath with the platform's ".." rule passed in, so
// both rules can be exercised on one machine.
//
// dotDotIsLexical says the platform collapses ".." ITSELF, before the filesystem
// sees the path. Windows does: it normalises in user mode — the same textual
// removal filepath.Clean performs — so `missing\..\x` names `x` beside it and
// OPENS it, missing directory and all. A Unix kernel instead walks component by
// component, resolving ".." against the directory it just followed, so the same
// spelling fails at `missing` and a ".." after a symlink steps out of the link's
// TARGET. Only the walking platforms need the uncleaned reading below; on the
// normalising one the cleaned path already IS the file that gets opened, and
// taking the walk would refuse output paths that were written before.
func realOutputPathOn(path string, dotDotIsLexical bool) (string, error) {
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if dotDotIsLexical || !hasDotDot(path) {
		return cleaned, nil
	}
	raw, ok, err := rawAbs(path)
	if err != nil {
		return "", err
	}
	if !ok {
		return cleaned, nil
	}
	dir, last := splitFinalComponent(raw)
	if last == "" || last == "." || last == ".." {
		// The path names a DIRECTORY, so there is no final component to hold back;
		// the write fails on the directory, as it did before.
		return cleaned, nil
	}
	prefix, err := resolveExistingPrefix(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(prefix, last), nil
}

// namesADirectory reports whether path's own spelling says the destination is a
// directory: it ends in a separator, or its final component is "." or "..".
//
// It reads the RAW spelling, never a cleaned one, because cleaning is exactly what
// removes the evidence — filepath.Abs("out.md/") is "…/out.md", and splitFinalComponent
// strips trailing separators before it splits, so neither can be asked this question.
//
// A bare volume name ("C:") is left alone: only filepath.Abs knows what it means,
// and it resolves to that drive's working directory, which fails as a file on its
// own.
func namesADirectory(path string) bool {
	path = path[len(filepath.VolumeName(path)):]
	if path == "" {
		return false
	}
	if os.IsPathSeparator(path[len(path)-1]) {
		return true
	}
	start := len(path)
	for start > 0 && !os.IsPathSeparator(path[start-1]) {
		start--
	}
	last := path[start:]
	return last == "." || last == ".."
}

// hasDotDot reports whether path contains a ".." component. That component is the
// only thing filepath.Clean moves across a symlink, so it is the only reason to
// take the uncleaned walk above.
func hasDotDot(path string) bool {
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || os.IsPathSeparator(path[i]) {
			if path[start:i] == ".." {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// rawAbs makes path absolute by CONCATENATION rather than filepath.Join, because
// Join cleans and cleaning is the collapse the uncleaned reading exists to avoid.
//
// ok is false for the two Windows spellings that are neither absolute nor plainly
// relative — drive-relative (`C:file`, resolved against that drive's own working
// directory) and rooted-relative (`\file`, resolved against the current drive) —
// which only filepath.Abs knows how to read.
func rawAbs(path string) (string, bool, error) {
	if filepath.IsAbs(path) {
		return path, true, nil
	}
	if filepath.VolumeName(path) != "" || (path != "" && os.IsPathSeparator(path[0])) {
		return "", false, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, err
	}
	return cwd + string(filepath.Separator) + path, true, nil
}

// traversedPath is the caller's spelling made absolute without cleaning — the route
// refuseSymlinkedComponents cannot see and refuseTraversedSymlinks walks.
//
// It is empty whenever that walk would add nothing. Without a ".." there is nothing
// filepath.Clean can move across a link, so every component the kernel walks is still
// a component of the destination and the destination's own check covers it. And on a
// platform that collapses ".." itself, the cleaned path IS what gets opened —
// `link\..\config` names `config` beside the link there, never the link's target —
// so there is no traversal to inspect. (This comment used to add "Windows' os.Root
// refuses symlinked components on its own besides". STRUCK — it does not; see
// refuseSymlinkedComponents. The reason above never depended on it.)
func traversedPath(path string) string {
	if runtime.GOOS == "windows" || !hasDotDot(path) {
		return ""
	}
	raw, ok, err := rawAbs(path)
	if err != nil || !ok {
		return ""
	}
	return raw
}

// pathComponents splits path into its non-empty components WITHOUT cleaning, so a
// ".." survives as a component instead of being collapsed. A volume name is not a
// component.
func pathComponents(path string) []string {
	path = path[len(filepath.VolumeName(path)):]
	var components []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || os.IsPathSeparator(path[i]) {
			if i > start {
				components = append(components, path[start:i])
			}
			start = i + 1
		}
	}
	return components
}

// splitFinalComponent splits path before its final component, WITHOUT cleaning,
// ignoring trailing separators. parent keeps its trailing separator so that "/x"
// splits into "/" and "x" rather than "" and "x". An empty last means there was no
// component left to take.
func splitFinalComponent(path string) (parent, last string) {
	end := len(path)
	for end > 0 && os.IsPathSeparator(path[end-1]) {
		end--
	}
	start := end
	for start > 0 && !os.IsPathSeparator(path[start-1]) {
		start--
	}
	return path[:start], path[start:end]
}

// containedRel reports target's path beneath root, and whether it is beneath it at
// all. Containment is decided by filesystem IDENTITY — os.SameFile against each of
// target's ancestor directories — because comparing path STRINGS is wrong in ways
// that each silently leave a repository-controlled path unconfined:
//
//   - CASE. filepath.Rel compares bytes even where the filesystem does not, and
//     macOS and Windows are both case-insensitive by default. filepath.EvalSymlinks
//     does not canonicalise the case of ordinary components either, so a re-spelled
//     root (/repo against /Repo) reads as a different tree while naming the same
//     directory. No exotic setup is needed to reach it: the root here comes from
//     `git rev-parse --show-toplevel`, which reports the on-disk spelling, while the
//     output path is spelled however the caller typed it.
//   - OTHER NAMES FOR ONE DIRECTORY. Identity also absorbs the rest of that family
//     — the /private/tmp git reports for a checkout under /tmp, a macOS TMPDIR under
//     /var (a link to /private/var), a Windows 8.3 short name — none of which a
//     lexical prefix test survives and all of which name the same directory.
//
// os.Stat FOLLOWS links, which is right for every ancestor and would be wrong for
// the final component — that component is the symlink a confined write exists to
// refuse — so the walk starts at the target's parent and the final component stays
// a name. Ancestors that do not exist yet simply fail to match and the walk
// continues past them, which is the ordinary case: `verify --record-baseline` is
// documented to create its parent directories.
//
// The walk runs over BOTH the named chain and the resolved one, and inside wins,
// because each chain alone is wrong in one direction:
//
//   - the NAMED chain is defeated by a caller-owned link that points INTO the
//     repository (`/elsewhere/link` -> `<repo>/sub`, then `--report link/x.md`):
//     its lexical ancestors leave the repository immediately, yet the file it
//     names is a repository-controlled path;
//   - the RESOLVED chain is defeated by a committed symlinked DIRECTORY (`out` ->
//     /etc, then `--report out/x.md`): it resolves outside the repository, and
//     calling that caller-owned is precisely the escape being closed.
//
// The root itself is reported as not contained — it names a directory, so the
// write is a caller error that the ordinary write path reports better than a
// containment message can. Starting at the parent gets that for free.
func containedRel(root, target string) (string, bool) {
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		// No identity to compare against. A live invocation always has its
		// repository on disk, so this is the misconfiguration path; fall back to
		// the lexical comparison rather than to "not contained", which would drop
		// confinement silently.
		return lexicalRel(root, target)
	}
	if rel, ok := walkToRoot(rootInfo, target); ok {
		return rel, true
	}
	// target is already absolute and cleaned, so it holds no ".." for
	// resolveExistingPrefix to refuse; an error here can only mean there is no
	// resolved chain to try, and the named one has already run.
	prefix, err := resolveExistingPrefix(filepath.Dir(target))
	if err != nil {
		return "", false
	}
	resolved := filepath.Join(prefix, filepath.Base(target))
	if resolved != target {
		if rel, ok := walkToRoot(rootInfo, resolved); ok {
			return rel, true
		}
	}
	return "", false
}

// walkToRoot climbs target's parent directories looking for the one that IS root,
// and returns the path from it down to target. It compares identity, not names.
func walkToRoot(rootInfo os.FileInfo, target string) (string, bool) {
	rel := filepath.Base(target)
	for dir := filepath.Dir(target); ; {
		if info, err := os.Stat(dir); err == nil && os.SameFile(rootInfo, info) {
			return rel, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		rel = filepath.Join(filepath.Base(dir), rel)
		dir = parent
	}
}

// lexicalRel is containedRel's comparison of last resort, used only when root
// cannot be stat'd. Both arguments must already be absolute and cleaned.
func lexicalRel(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// resolveExistingPrefix resolves the longest existing prefix of dir through
// symlinks and re-appends the part that does not exist yet.
//
// The tail matters: filepath.EvalSymlinks fails outright when any component is
// missing, and `verify --record-baseline` is documented to CREATE its parent
// directories, so the common case is a path whose leaf directories do not exist
// yet. Returning dir unchanged when nothing resolves is safe — it only costs the
// resolved walk, and the named one has already run.
//
// A ".." reached by this walk is REFUSED rather than resolved, and that is the
// whole reason the function returns an error. Reaching one means EvalSymlinks
// failed on the directory the ".." follows, so the kernel fails there too:
// `missing/../victim` is ENOENT at `missing` and never reaches `victim`. There is
// no path that could be returned instead of the error. The tail is put back with
// filepath.Join — here, and again on the final component in realOutputPath — and
// Join CLEANS, so `missing/..` collapses to nothing and the destination silently
// becomes `victim`, turning the kernel's ENOENT into a truncating write of a file
// the caller never named. Refusing is what keeps the destination from moving.
//
// This costs no legitimate spelling: a ".." that follows a directory which DOES
// exist is resolved by EvalSymlinks before the walk can reach it, so the ordinary
// `../reports/x.md` — leaf directories not yet created included — never lands here.
// Nor does anything on a platform that collapses ".." itself: realOutputPathOn
// returns the cleaned path there without taking this walk, and containedRel's call
// passes an already-cleaned absolute path, which holds no ".." to reach.
func resolveExistingPrefix(dir string) (string, error) {
	remainder := ""
	current := dir
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		// splitFinalComponent, not filepath.Dir: Dir cleans, and cleaning a ".."
		// off the chain here would put back exactly the collapse this walk exists
		// to resolve. EvalSymlinks handles ".." itself, against the RESOLVED path.
		parent, last := splitFinalComponent(current)
		if last == "" {
			return dir, nil
		}
		if last == ".." {
			return "", &fs.PathError{Op: "resolve", Path: current, Err: fs.ErrNotExist}
		}
		remainder = filepath.Join(last, remainder)
		current = parent
	}
}

// refuseSymlinkedComponents refuses the write when ANY component of rel is a
// symbolic link, not only the final one.
//
// This is the check that NAMES the offence, and os.Root is not a substitute for
// it. os.Root guarantees only that the write cannot LEAVE the root; it does not
// refuse a link that stays inside it, it FOLLOWS one.
//
// STRUCK, and it was wrong on this very point: this comment used to say "Windows'
// Root refuses every symlinked component itself, so this check is what makes the
// two platforms agree". It does not. Go documents os.Root as following symbolic
// links that stay inside the root with no platform exception (os/root.go), and
// Windows reaches that by the same route Unix does — os/root_windows.go:147 opens
// with O_NOFOLLOW_ANY, then reads the reparse point on the resulting ELOOP and
// hands doInRoot an errSymlink to resolve (os/root_openat.go:450), exactly as
// os/root_unix.go:85 does. Neither platform gets a no-follow open out of os.Root.
//
// Checking only the FINAL component, as the first version of this fix did, leaves
// every parent followed: a repository that commits `reports -> .git` turns
// `--report reports/config` into a truncating write of git's own configuration,
// where core.pager and core.fsmonitor name programs git then runs. The leaf is a
// regular file at that point, so a leaf-only check sees nothing wrong.
//
// STRUCK: this comment used to say "the check is not a TOCTOU hole in the property
// that matters — os.Root still cannot be walked out of the repository, so the worst
// case stays a write inside the tree being scanned". The worst case inside the tree
// is .git/config, whose core.pager and core.fsmonitor name programs git then runs,
// which is the same escalation every refusal in this file exists to stop. The race
// was demonstrated: a swapper flipping `reports` between an ordinary directory and
// a link to .git truncated .git/config through this check within one test run
// (TestWriteOutputFileDoesNotFollowALinkSwappedInAfterTheCheck). So this walk is
// kept for the message it produces and for the route it is the only reader of, and
// the refusal is ENFORCED in openConfinedOutputFile, which leaves no window.
func refuseSymlinkedComponents(root *os.Root, rel, given string) error {
	prefix := ""
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		prefix = filepath.Join(prefix, component)
		info, err := root.Lstat(prefix)
		if err != nil {
			// Not on disk yet. `verify --record-baseline` is documented to create
			// its parents, so missing components are the ordinary case, and a
			// component that does not exist cannot be a link. Any other error is
			// left to the open below, which reports it against the caller's path.
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return symlinkedComponentError(prefix, given)
		}
	}
	return nil
}

// symlinkedComponentError is the one refusal for a repository-owned link ON the
// destination, worded once for the two places that reach it: the static check above,
// and openConfinedOutputFile, which reaches the same verdict about a link that was
// not there when the static check looked.
func symlinkedComponentError(rel, given string) error {
	return fmt.Errorf(
		"refusing to write %s: %s is a symbolic link committed inside the repository, "+
			"and writing through it would truncate whatever it points at. "+
			"Remove the link, or name a path outside the repository",
		given, rel)
}

// refuseTraversedSymlinks refuses the write when REACHING the destination would
// traverse a symbolic link committed inside the repository, as opposed to landing on
// one. Both are the same escape; only the second is visible to the check above.
//
// refuseSymlinkedComponents walks the destination's canonical relative path, and in
// the one spelling that needs the uncleaned reading — a path holding ".." — that
// canonical path has had every link in its directory part already resolved away by
// realOutputPath. A repository that commits `link -> .git/objects` turns
// `--report link/../config` into `.git/config`, whose components are an ordinary
// directory and an ordinary file: the component walk sees nothing wrong and
// truncates git's own configuration, where core.pager and core.fsmonitor name
// programs git then runs. That is the same file the leaf-only check and the
// symlinked-parent check were each written to protect, reached a third way, so the
// refusal belongs on the traversal itself rather than on one more spelling of it.
//
// The walk is the kernel's, component by component against a prefix that is always
// link-free, which is what makes filepath.Dir an exact ".." — a lexical parent is
// only wrong after a link has been followed. A link INSIDE the repository is refused.
// One outside it is caller-owned, by the same reasoning that leaves the whole
// out-of-repo write unconfined, so it is FOLLOWED rather than refused, and following
// it is also what keeps the prefix link-free for the ".." that may come after.
//
// Missing components cannot hide a link behind them: a ".." that follows one is
// refused outright by resolveExistingPrefix before this walk is ever reached.
func refuseTraversedSymlinks(root, raw, given string) error {
	rel, ok := traversedRepositorySymlink(root, raw)
	if !ok {
		return nil
	}
	return traversedSymlinkError(rel, given)
}

// traversedSymlinkError is the one refusal for a repository-owned link on the ROUTE,
// worded once for both places that reach the same verdict: classifyOutputPath, where
// the route leaves the repository and there is no confined write to hand it to, and
// refuseTraversedSymlinks, where there is.
func traversedSymlinkError(rel, given string) error {
	return fmt.Errorf(
		"refusing to write %s: the path leads through %s, a symbolic link committed "+
			"inside the repository, and resolving it moves the write to whatever that "+
			"link points at. Remove the link, or name a path outside the repository",
		given, rel)
}

// traversedRepositorySymlink walks raw the way the kernel does and reports the first
// component that is a symbolic link committed inside root, as a path beneath root.
//
// It is ONE walk with TWO callers, which is the point of it being a function. The
// refusal above rejects such a route; classifyOutputPath asks the same question
// BEFORE that, to decide whether the cleaned spelling is allowed to move the
// destination at all. Answering it twice, in two walks, is how a route ends up
// classified by one rule and refused by another.
//
// The walk keeps its prefix link-free — a link outside the repository is FOLLOWED,
// by the same ownership rule that leaves the whole out-of-repository write
// unconfined — which is what makes filepath.Dir an exact "..": a lexical parent is
// only wrong after a link has been followed. Missing components cannot hide a link
// behind them: a ".." that follows one is refused outright by resolveExistingPrefix
// before this walk is ever reached.
func traversedRepositorySymlink(root, raw string) (string, bool) {
	if root == "" || raw == "" {
		return "", false
	}
	prefix := raw[:len(filepath.VolumeName(raw))] + string(filepath.Separator)
	for _, component := range pathComponents(raw) {
		switch component {
		case ".":
			continue
		case "..":
			prefix = filepath.Dir(prefix)
			continue
		}
		next := filepath.Join(prefix, component)
		info, err := os.Lstat(next)
		if err != nil {
			prefix = next
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if rel, ok := containedRel(root, next); ok {
				return rel, true
			}
			if resolved, err := filepath.EvalSymlinks(next); err == nil {
				next = resolved
			}
		}
		prefix = next
	}
	return "", false
}

// refuseRepositorySymlinks is the whole refusal for a confined write: no committed
// link on the way to the destination, and none at the destination. Keeping the two
// walks behind one call is what stops a later spelling from being answered with yet
// another check at yet another call site.
func refuseRepositorySymlinks(root *os.Root, target repoOutputTarget) error {
	if err := refuseSymlinkedComponents(root, target.rel, target.given); err != nil {
		return err
	}
	return refuseTraversedSymlinks(target.root, target.traversed, target.given)
}

// writeOutputFile writes one caller-named output file under the rule documented at
// the top of this file. createParents mirrors `verify --record-baseline`, which is
// documented to create missing parent directories; `index --report` passes false
// and keeps its "no such directory" error.
func writeOutputFile(
	ctx context.Context, repoRoot, path string, data []byte, perm os.FileMode, createParents bool,
) error {
	return writeOutputFileUnder(confinementRoot(ctx, repoRoot), path, data, perm, createParents)
}

// writeOutputFileUnder is writeOutputFile with the boundary already resolved. The
// split exists because finding that boundary runs `git rev-parse` — a subprocess,
// and the only part of this sink that is neither a syscall nor arithmetic — which a
// test that has to repeat the write tens of thousands of times cannot afford to pay
// per iteration. Nothing else about the write differs.
func writeOutputFileUnder(
	confinement, path string, data []byte, perm os.FileMode, createParents bool,
) error {
	target, err := classifyOutputPath(confinement, path)
	if err != nil {
		return err
	}
	if !target.insideRepo() {
		if createParents {
			if directory := filepath.Dir(target.path); directory != "" && directory != "." {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					return err
				}
			}
		}
		return os.WriteFile(target.path, data, perm)
	}

	root, err := os.OpenRoot(target.root)
	if err != nil {
		return err
	}
	defer root.Close()
	// This is the STATIC answer, and it is not what makes the write safe — see
	// openConfinedOutputFile, which is. It runs first because it is the only place
	// the ROUTE (target.traversed, a spelling the destination's own components no
	// longer contain) can be judged at all, and because it names the offending
	// component in the message before anything is created on disk.
	if err := refuseRepositorySymlinks(root, target); err != nil {
		return err
	}
	file, err := openConfinedOutputFile(root, target, perm, createParents)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// openConfinedOutputFile opens the confined destination for writing, and is where
// the refusal is ENFORCED rather than merely asked.
//
// Every refusal above this one is a CHECK: it reads the filesystem, decides, and
// returns — and the write then happens in a separate call. Between the two the
// repository can change the answer. A directory the check accepted becomes a link
// to .git, os.Root resolves a link that stays inside the root, and .git/config is
// truncated by a write whose every check passed. That is not another SPELLING of
// the path — it is the same spelling answered twice, differently — so no smarter
// check can close it. The window has to stop existing.
//
// So the destination is reached by OPENING each component and keeping it open,
// never by naming a path and trusting the components to still be what they were:
//
//   - each directory component is opened as its own os.Root, and the handle is
//     what the next component is opened relative to. Once opened it is pinned:
//     renaming or replacing the entry afterwards cannot move the write, because
//     the write no longer goes through the name.
//   - the component is then verified by IDENTITY — os.SameFile between what the
//     name lstats to and what the held handle stats to. os.Root follows a link
//     that stays inside the root, so a symlinked component opens its TARGET, whose
//     identity is not the link's; the two agree only for a component that was a
//     real directory at the instant it was looked at, and that directory is the one
//     now held. This is the portable spelling of O_NOFOLLOW, which os.Root does not
//     accept (rootOpenFileNolog already passes it and resolves the ELOOP itself)
//     and which is undefined on Windows.
//   - the final component is opened WITHOUT O_TRUNC — O_CREATE|O_EXCL first, which
//     Go documents as never following a symlink whatever the OS returns, then a
//     plain O_WRONLY if it already exists — so nothing can be destroyed before the
//     same identity check has run. The truncation happens through the verified
//     handle afterwards, where no name is left to swap.
//
// What this does NOT claim: it is a check-free path to the FILE, not proof that no
// name in it ever pointed elsewhere. A write that loses the race is refused, not
// silently redirected, and the file it would have hit is untouched.
func openConfinedOutputFile(
	root *os.Root, target repoOutputTarget, perm os.FileMode, createParents bool,
) (*os.File, error) {
	components := pathComponents(target.rel)
	if len(components) == 0 {
		return nil, fmt.Errorf("refusing to write %s: the path names no file", target.given)
	}
	directory := root
	var opened []*os.Root
	defer func() {
		for _, held := range opened {
			held.Close()
		}
	}()
	rel := ""
	for _, component := range components[:len(components)-1] {
		if component == "." {
			continue
		}
		rel = filepath.Join(rel, component)
		if createParents {
			// `verify --record-baseline` is documented to create its parents. An
			// existing entry — including a link the walk is about to refuse — comes
			// back as EEXIST, which is not an answer to whether it may be used.
			if err := directory.Mkdir(component, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, err
			}
		}
		next, err := directory.OpenRoot(component)
		if err != nil {
			return nil, err
		}
		opened = append(opened, next)
		if err := refuseUnlessSameEntry(directory, component, next, rel, target.given); err != nil {
			return nil, err
		}
		directory = next
	}

	leaf := components[len(components)-1]
	rel = filepath.Join(rel, leaf)
	file, err := directory.OpenFile(leaf, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		// Not O_CREATE: the entry exists, and re-asking for creation is what would
		// let a dangling link committed in the repository be MATERIALISED at its
		// target before the identity check below can refuse it.
		file, err = directory.OpenFile(leaf, os.O_WRONLY, 0)
	}
	if err != nil {
		return nil, err
	}
	named, err := directory.Lstat(leaf)
	if err != nil {
		file.Close()
		return nil, err
	}
	held, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !os.SameFile(named, held) {
		file.Close()
		return nil, symlinkedComponentError(rel, target.given)
	}
	// Only now, and through the handle rather than the name.
	if err := file.Truncate(0); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// refuseUnlessSameEntry reports whether the directory now HELD open is the same
// filesystem object the name lstats to, and refuses the write when it is not.
//
// Lstat does not follow the final component, so it reports the LINK when component
// is one; the held handle reports what os.Root resolved that link to. Equality is
// therefore exactly "component was not a symlink", asked about the object the walk
// is already holding rather than about a name it is going to use later.
func refuseUnlessSameEntry(parent *os.Root, component string, held *os.Root, rel, given string) error {
	named, err := parent.Lstat(component)
	if err != nil {
		return err
	}
	info, err := held.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(named, info) {
		return symlinkedComponentError(rel, given)
	}
	return nil
}
