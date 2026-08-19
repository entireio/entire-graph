package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// Classification runs BOTH lexically and on resolved paths, and inside wins,
// because each comparison alone is wrong in one direction:
//
//   - resolved alone misjudges a repository reached through a symlink (a macOS
//     TMPDIR under /var, which is a link to /private/var), classifying the
//     repository's own files as outside it and leaving them unconfined;
//   - lexical alone is defeated by a committed symlinked DIRECTORY: with `out` a
//     link to /etc, `--report out/x.md` resolves outside the repository, and
//     calling it caller-owned is precisely the escape being closed.

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

// classifyOutputPath decides whether path is repository-controlled or caller-owned.
// It never resolves the FINAL component: that component is the symlink a confined
// write exists to refuse, and resolving it would report the link's target as the
// destination and classify the write by the attacker's choice.
func classifyOutputPath(repoRoot, path string) (repoOutputTarget, error) {
	// filepath.Abs, not the resolved root, matches today's semantics: the path is
	// interpreted against the process working directory, not against --repo.
	abs, err := filepath.Abs(path)
	if err != nil {
		return repoOutputTarget{}, fmt.Errorf("resolve output path %q: %w", path, err)
	}
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return repoOutputTarget{}, fmt.Errorf("resolve repository root %q: %w", repoRoot, err)
	}
	if rel, ok := containedRel(rootAbs, abs); ok {
		return repoOutputTarget{root: rootAbs, rel: rel, path: abs, given: path}, nil
	}
	rootReal := resolveExistingPrefix(rootAbs)
	absReal := filepath.Join(resolveExistingPrefix(filepath.Dir(abs)), filepath.Base(abs))
	if rel, ok := containedRel(rootReal, absReal); ok {
		return repoOutputTarget{root: rootReal, rel: rel, path: abs, given: path}, nil
	}
	return repoOutputTarget{path: abs, given: path}, nil
}

// containedRel reports target's path beneath root, and whether it is beneath it at
// all. Both arguments must already be absolute and cleaned. "." — the root itself —
// is reported as not contained: it names a directory, so the write is a caller
// error that the ordinary write path reports better than a containment message can.
func containedRel(root, target string) (string, bool) {
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
// resolved comparison, and the lexical one has already run.
func resolveExistingPrefix(dir string) string {
	remainder := ""
	current := dir
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved
			}
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// writeOutputFile writes one caller-named output file under the rule documented at
// the top of this file. createParents mirrors `verify --record-baseline`, which is
// documented to create missing parent directories; `index --report` passes false
// and keeps its "no such directory" error.
func writeOutputFile(repoRoot, path string, data []byte, perm os.FileMode, createParents bool) error {
	target, err := classifyOutputPath(repoRoot, path)
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
