package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
//     symlinked final component is refused rather than followed.
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
// that directory is not required to be a git repository at all; a missing git
// binary, a bare repository and a plain directory must all keep working. Falling
// back can only NARROW the confined region to the directory the caller named, so
// no path that was confined before becomes unconfined by it.
func confinementRoot(ctx context.Context, repoRoot string) string {
	if repoRoot == "" {
		return repoRoot
	}
	top, err := gitutil.RepoRoot(ctx, repoRoot)
	if err != nil || strings.TrimSpace(top) == "" {
		return repoRoot
	}
	return top
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
// The cleaned spelling is still classified, second, and inside wins, because the
// two disagree in the direction this fix exists to close: a committed symlinked
// DIRECTORY (`out` -> /etc, then `--report out/x.md`) resolves outside the
// repository on disk, and calling that caller-owned is exactly the escape. Its
// cleaned spelling stays under the root, is confined, and os.Root then refuses to
// walk out through the link.
func classifyOutputPath(repoRoot, path string) (repoOutputTarget, error) {
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
	if rel, ok := containedRel(rootAbs, onDisk); ok {
		return repoOutputTarget{root: rootAbs, rel: rel, path: onDisk, given: path}, nil
	}
	if cleaned != onDisk {
		if rel, ok := containedRel(rootAbs, cleaned); ok {
			return repoOutputTarget{root: rootAbs, rel: rel, path: cleaned, given: path}, nil
		}
	}
	return repoOutputTarget{path: onDisk, given: path}, nil
}

// realOutputPath makes path absolute the way the KERNEL reads it: the directory
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
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !hasDotDot(path) {
		return cleaned, nil
	}
	raw := path
	if !filepath.IsAbs(raw) {
		// Only a plainly relative path can be made absolute by concatenation.
		// Windows has two spellings that are neither absolute nor plainly relative
		// — drive-relative (`C:file`, resolved against that drive's own working
		// directory) and rooted-relative (`\file`, resolved against the current
		// drive) — and only filepath.Abs knows what they mean.
		if filepath.VolumeName(raw) != "" || (raw != "" && os.IsPathSeparator(raw[0])) {
			return cleaned, nil
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Concatenated, not filepath.Join: Join cleans, which is the collapse this
		// function exists to avoid.
		raw = cwd + string(filepath.Separator) + raw
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

// writeOutputFile writes one caller-named output file under the rule documented at
// the top of this file. createParents mirrors `verify --record-baseline`, which is
// documented to create missing parent directories; `index --report` passes false
// and keeps its "no such directory" error.
func writeOutputFile(
	ctx context.Context, repoRoot, path string, data []byte, perm os.FileMode, createParents bool,
) error {
	target, err := classifyOutputPath(confinementRoot(ctx, repoRoot), path)
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
	if createParents {
		if directory := filepath.Dir(target.rel); directory != "" && directory != "." {
			if err := root.MkdirAll(directory, 0o755); err != nil {
				return err
			}
		}
	}
	// Lstat before the open is what refuses a link the REPOSITORY planted. os.Root
	// on its own only guarantees the write cannot leave the root: it resolves an
	// in-root symlink and would still truncate the file the repository aimed it at.
	// Root.Lstat does not traverse the link, and it is the portable spelling of the
	// refusal — syscall.O_NOFOLLOW does not exist on Windows, and Root.OpenFile
	// passes the platform's own no-follow flag either way.
	//
	// The check is not a TOCTOU hole in the property that matters: were the link
	// swapped between the two calls, os.Root still cannot be walked out of the
	// repository, so the worst case stays a write inside the tree being scanned.
	if info, err := root.Lstat(target.rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"refusing to write %s: it is a symbolic link committed inside the repository, "+
				"and writing through it would truncate whatever it points at. "+
				"Remove the link, or name a path outside the repository",
			target.given)
	}
	file, err := root.OpenFile(target.rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
