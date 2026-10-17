package sem

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// VERIFY ABOVE --repo
// ===================
//
// Every path the derivations see is relative to the searched scope, so their walk ends at --repo.
// Run from a subdirectory of a checkout (`cd cmd/entire/cli && entire graph query --repo .`) that is
// one directory short of where most build systems keep their manifest, and the block printed
// "none derivable (no build manifest found)" for a Go package whose go.mod sat three levels up.
//
// The fix does not teach each build system about a second root. It replays the SAME derivations in
// the checkout's own coordinates — subject paths lifted by the scope's prefix, reads served from the
// toplevel — which is exactly the walk a query run at the toplevel performs, and every derivation is
// already correct there. The command it yields is runnable from the toplevel, so it is emitted
// behind one `cd` from --repo up to the toplevel. That is correct for every build system for the
// same reason: nothing about the derived command changes, only where it is started from.
//
// It runs only after every in-scope walk (narrow and suite) has declined, so a subdirectory that is
// its own build root keeps exactly the command it had.

// searchVerifyAboveScope is the part of the checkout above the searched scope.
type searchVerifyAboveScope struct {
	// prefix is the scope's path from the git toplevel, slash-separated, without a trailing slash.
	// Never empty: a scope that IS the toplevel has nothing above it.
	prefix string
	// up is the absolute physical checkout root used by the emitted cd command.
	// Relative parent walks can reach another checkout when --repo is a symlink.
	up string
	// read reads a toplevel-relative file.
	read contentReader
}

// newSearchVerifyAboveScope returns a lazy resolver for the checkout above repo, and the function
// that releases whatever the resolver opened. Nothing is spawned until the resolver is first called.
func newSearchVerifyAboveScope(ctx context.Context, repo, commit string, useHead bool) (func() (searchVerifyAboveScope, bool), func()) {
	var (
		resolved bool
		scope    searchVerifyAboveScope
		ok       bool
		release  func()
	)
	resolve := func() (searchVerifyAboveScope, bool) {
		if resolved {
			return scope, ok
		}
		resolved = true
		scope, release, ok = openSearchVerifyAboveScope(ctx, repo, commit, useHead)
		return scope, ok
	}
	closeScope := func() {
		if release != nil {
			release()
		}
	}
	return resolve, closeScope
}

func openSearchVerifyAboveScope(ctx context.Context, repo, commit string, useHead bool) (searchVerifyAboveScope, func(), bool) {
	if useHead && commit == "" {
		return searchVerifyAboveScope{}, nil, false
	}
	if !gitMetadataSafeForSubprocessContext(ctx, repo) {
		return searchVerifyAboveScope{}, nil, false
	}
	prefix, err := gitutil.RepoPrefix(ctx, repo)
	if err != nil {
		return searchVerifyAboveScope{}, nil, false
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" || path.Clean(prefix) != prefix || strings.HasPrefix(prefix, "../") || prefix == ".." {
		return searchVerifyAboveScope{}, nil, false
	}
	toplevel, err := gitutil.RepoRoot(ctx, repo)
	if err != nil || toplevel == "" {
		return searchVerifyAboveScope{}, nil, false
	}
	// A Git core.worktree override must not move verification outside the
	// nearest physical checkout marker that encloses the selected scope.
	absolute, err := filepath.Abs(repo)
	if err != nil {
		return searchVerifyAboveScope{}, nil, false
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return searchVerifyAboveScope{}, nil, false
	}
	boundary := physical
	for depth := 0; depth <= searchVerifyMaxDepth; depth++ {
		if _, err := os.Lstat(filepath.Join(boundary, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(boundary)
		if parent == boundary {
			return searchVerifyAboveScope{}, nil, false
		}
		boundary = parent
	}
	physicalTop, err := filepath.EvalSymlinks(toplevel)
	if err != nil || boundary != physicalTop {
		return searchVerifyAboveScope{}, nil, false
	}
	scope := searchVerifyAboveScope{
		prefix: prefix,
		up:     physicalTop,
	}
	if useHead {
		// The committed tree, like every other read this query makes: a --head answer must not
		// depend on what is lying uncommitted in the worktree above the scope.
		batch, err := gitutil.NewBatchFileReader(ctx, toplevel, commit)
		if err != nil {
			return searchVerifyAboveScope{}, nil, false
		}
		batch.SetMaxBytes(defaultMaxParseBytes)
		scope.read = func(filePath string) (string, bool) {
			if !batch.IsPathSafe(filePath) {
				return "", false
			}
			content, found, err := batch.ReadFile(filePath)
			return content, found && err == nil
		}
		return scope, func() { _ = batch.Close() }, true
	}
	// One os.Root keeps every read inside the checkout, through intermediate symlinks too; the final
	// component is Lstat-ed so a symlinked manifest is refused rather than followed, exactly as the
	// in-scope worktree reader refuses it.
	root, err := os.OpenRoot(toplevel)
	if err != nil {
		return searchVerifyAboveScope{}, nil, false
	}
	scope.read = func(filePath string) (string, bool) {
		name := filepath.FromSlash(filePath)
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > defaultMaxParseBytes {
			return "", false
		}
		file, err := openSearchVerifyRegularFile(root, name)
		if err != nil {
			return "", false
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return "", false
		}
		content, err := io.ReadAll(io.LimitReader(file, defaultMaxParseBytes+1))
		if err != nil || len(content) > defaultMaxParseBytes {
			return "", false
		}
		return string(content), true
	}
	return scope, func() { _ = root.Close() }, true
}

// deriveSearchVerifyAboveScope continues the narrow and then the suite walk from the scope's parent
// to the git toplevel, in toplevel coordinates, and returns the command it finds wrapped in the `cd`
// that makes it runnable from --repo. The second result is the evidence the command was derived
// against, which is what its runner check must consult: a `./gradlew` at the toplevel does not
// exist in the scope.
func deriveSearchVerifyAboveScope(subject searchVerifySubject, evidence *searchVerifyEvidence) (*SearchVerifyCommand, *searchVerifyEvidence) {
	if evidence.aboveScope == nil || subject.sourcePath == "" {
		return nil, nil
	}
	scope, ok := evidence.aboveScope()
	if !ok || scope.prefix == "" || scope.read == nil {
		return nil, nil
	}
	outer := &searchVerifyEvidence{
		// Continue the lookup's existing budget rather than allowing a second
		// full budget just because its coordinates change at the scope boundary.
		reads: evidence.reads,
		// In-scope paths go through the scope's own evidence, so they share its cache, read budget
		// and replay provenance; only the manifests above the scope are read through the toplevel.
		read: func(filePath string) (string, bool) {
			if relative, inside := searchVerifyRelative(scope.prefix, filePath); inside {
				return evidence.file(relative)
			}
			return scope.read(filePath)
		},
		lookPath: evidence.lookPath,
	}
	defer func() { evidence.reads = outer.reads }()
	// Native Go inputs need their same-directory Go files as positive package
	// evidence. Preserve the scoped inventory in the outer walk's coordinates.
	outer.files = make([]string, len(evidence.files))
	for index, filePath := range evidence.files {
		outer.files[index] = searchVerifyJoin(scope.prefix, filePath)
	}
	lifted := subject
	lifted.sourcePath = searchVerifyJoin(scope.prefix, subject.sourcePath)
	if subject.testPath != "" {
		lifted.testPath = searchVerifyJoin(scope.prefix, subject.testPath)
	}
	start := searchVerifyStartDir(scope.prefix)
	command := searchVerifyWalk(start, lifted, outer, searchVerifyDerivations)
	tier := searchVerifyTierNarrow
	if command == nil {
		command = searchVerifyWalk(start, lifted, outer, searchVerifySuiteDerivations)
		tier = searchVerifyTierSuite
	}
	if command == nil {
		return nil, nil
	}
	if command.Tier == "" {
		command.Tier = tier
	}
	command.Command = searchVerifyRunIn(scope.up, command.Command)
	// Targets and the derivation name toplevel-relative paths, because that is where the command
	// runs; saying so keeps a reader standing in --repo from resolving them against the wrong root.
	command.DerivedFrom += " (above --repo; paths relative to " + scope.up + ")"
	return command, outer
}
