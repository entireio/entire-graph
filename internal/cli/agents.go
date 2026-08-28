package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/entireio/entire-graph/internal/sem"
)

// agentGuide is the canonical, agent-agnostic operating guide for coding agents using the
// graph in a CONSUMING project (not this repo). It ships inside the binary so every install
// carries the current doctrine; `init-agents` distributes it into a project's AGENTS.md /
// CLAUDE.md via a small pointer block, and `agent-guide` prints it for any agent or human.
// It is resolution-first: graph retrieval narrows exploration, but source inspection and
// focused verification remain required before an agent declares the task complete.
const agentGuide = `# entire-graph — instructions for coding agents (follow directly)

You have a deterministic local code graph: ` + "`entire graph`" + ` (functions, classes, methods,
types, routes + call/inheritance relations; no network). These instructions are FOR YOU, the
agent reading this file. Use the graph to narrow exploration without trading away correctness.

## The workflow (mandatory for locate/fix/change tasks)

Your FIRST action on any task that requires finding code must be ONE search:

    entire graph search --repo . --profile full --query "<the task or bug in one sentence>"

Then open the top hit's file with your file-read tool (pass a line range around the reported
line), inspect enough surrounding behavior to justify the change, and make the smallest complete
edit. Treat graph output as evidence, not an oracle.

## Hard rules

1. SEARCH FIRST — never grep/find/cat to locate code before you have searched.
2. READ focused source around the result. Widen the check when aliases, generated code, dynamic
   dispatch, or related implementations could matter.
3. Use graph follow-ups only when they answer a real question. For impact or callers, prefer:
       entire graph impact --repo . --symbol X
4. Make the smallest complete edit and check sibling sites or contracts when the task implies them.
5. VERIFY before stopping. Run the most focused relevant test, build, or reproduction available.
   If execution is unavailable, perform a bounded source-level verification and state the limit.
6. Prefer precise queries and line ranges, but never trade resolution for fewer turns.
7. Feature-detect before relying on semantic relations:
       entire graph capabilities --json

## When NOT to use the graph

If the task already names the exact file and it is small, just read it — the graph saves tokens
by eliminating exploration; when there is nothing to explore, skip it.

## Reference

    locate  ->  entire graph search --repo . --profile full --query "..."
    impact  ->  entire graph impact --repo . --symbol X   (one shot: callers, callees, type consumers, data flow, co-change, siblings)
    callers ->  entire graph neighbors --repo . --symbol X --relation CALLS --direction in
    change  ->  entire graph diff --base A --head B --json
    detect  ->  entire graph capabilities --json   (inventory-only languages have no relations)
    stats   ->  entire graph stats --repo .        (human-facing token-savings report; not part of your workflow — do not run it unless asked)
`

// agentPointerBegin/End delimit the block init-agents manages inside AGENTS.md / CLAUDE.md,
// so re-runs update in place instead of appending duplicates.
const (
	agentPointerBegin = "<!-- entire-graph:begin -->"
	agentPointerEnd   = "<!-- entire-graph:end -->"
)

// maxInstructionFileBytes bounds AGENTS.md / CLAUDE.md, the only repository-authored files
// init-agents reads. Their path is fixed but their SIZE is chosen by the repository, and the
// command runs against clones the operator has not read, so the size is untrusted input like
// any other. Every sibling repository read is already bounded — source files and diffs at
// defaultMaxParseBytes, ignore files at maxIgnoreFileBytes, call-site snippets at
// callSiteMaxFileBytes — and this one was not: it read to EOF and then kept the whole file
// alive through validation, marker splitting, and the rendered replacement, so peak memory ran
// to a multiple of the file. Measured on an otherwise empty repository, a 2 GiB AGENTS.md
// drove init-agents to 6.5 GiB peak RSS.
//
// 4 MiB matches defaultMaxParseBytes, the ceiling the rest of the tool already applies to a
// repository-authored text file, and is orders of magnitude above any real agent instruction
// file (this repository's own is ~2 KiB).
const maxInstructionFileBytes = 4 << 20

// errContainedFileTooLarge reports that a contained read stopped at its limit. The caller owns
// the message, because only it knows which file the limit was applied to and why.
var errContainedFileTooLarge = errors.New("file is larger than the read limit")

func runAgentGuide(opts Options, args []string) error {
	fs := flag.NewFlagSet("agent-guide", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprint(opts.Stdout, agentGuide)
	return nil
}

// runInitAgents installs the guide into a consuming project so ANY coding agent finds it:
// writes .entire/graph-agent.md (plugin-managed, overwritten on re-run) and upserts a
// marker-guarded pointer block into AGENTS.md (the cross-agent convention) and CLAUDE.md
// (which additionally understands the @-import line).
func runInitAgents(opts Options, args []string) error {
	fs := flag.NewFlagSet("init-agents", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	repo := fs.String("repo", ".", "project root to install the agent guide into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("init-agents: %s is not a directory", root)
	}

	// init-agents is the only command that writes into the repository tree, and
	// docs/trust-and-security.md scopes that to exactly three files inside it. A
	// repository-committed symlink must not redirect any of those writes elsewhere, so every
	// write below goes through this os.Root instead of an absolute path. os.Root resolves each
	// component with openat relative to the opened directory: a link that stays inside the
	// repository is still followed — which is what the documented AGENTS.md/CLAUDE.md alias
	// needs — while one that escapes, is absolute, or crosses an escaping directory is refused.
	repoRoot, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}
	defer repoRoot.Close()

	guideDirName := ".entire"
	guideName := filepath.Join(guideDirName, "graph-agent.md")
	guidePath := filepath.Join(root, guideName)
	pointer := agentPointerBegin + "\n" +
		"This repo has the entire-graph code graph installed. Before exploring code with\n" +
		"grep/find/whole-file reads, read .entire/graph-agent.md — resolution-first guidance\n" +
		"for using graph retrieval, focused source inspection, and verification.\n" +
		"@.entire/graph-agent.md\n" +
		agentPointerEnd + "\n"

	agentsName, claudeName := "AGENTS.md", "CLAUDE.md"
	agentsPath := filepath.Join(root, agentsName)
	claudePath := filepath.Join(root, claudeName)

	// Establish containment for all four write targets before inspecting or writing anything,
	// so a repository that redirects one of them fails with a single actionable error and no
	// partial installation, exactly like the malformed-marker preflight below.
	for _, target := range []struct {
		name, path     string
		createsParents bool
	}{
		{guideDirName, filepath.Join(root, guideDirName), true},
		{guideName, guidePath, false},
		{agentsName, agentsPath, false},
		{claudeName, claudePath, false},
	} {
		if err := ensureContainedInRepo(repoRoot, target.name, target.path, target.createsParents, guideDirName); err != nil {
			return fmt.Errorf("init-agents: %w", err)
		}
	}
	if err := validateManagedTargetTopology(repoRoot, guideDirName, guideName, agentsName, claudeName); err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}

	agentsInfo, err := inspectInstructionFile(repoRoot, agentsName, agentsPath)
	if err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}
	claudeInfo, err := inspectInstructionFile(repoRoot, claudeName, claudePath)
	if err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}

	sharedInstructions := agentsInfo != nil && claudeInfo != nil && os.SameFile(agentsInfo, claudeInfo)
	agentsSource, agentsBegin, agentsEnd, err := readAndValidateInstructionFile(repoRoot, agentsName, agentsPath)
	if err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}
	var claudeSource []byte
	claudeBegin, claudeEnd := -1, -1
	if !sharedInstructions {
		claudeSource, claudeBegin, claudeEnd, err = readAndValidateInstructionFile(repoRoot, claudeName, claudePath)
		if err != nil {
			return fmt.Errorf("init-agents: %w", err)
		}
	}

	// Compute every byte that depends on instruction-file reads before creating or
	// modifying anything. In particular, Claude import detection must see the same
	// validated snapshot that is rendered below.
	agentsContent := renderPointerBlock(agentsSource, agentsBegin, agentsEnd, pointer)
	claudeBlock := pointer
	if !sharedInstructions && claudeDirectlyImportsAgents(claudeSource, claudePath, agentsPath) {
		claudeBlock = agentPointerBegin + "\n<!-- Entire Graph instructions are inherited through AGENTS.md. -->\n" + agentPointerEnd + "\n"
	}
	var claudeContent []byte
	if !sharedInstructions {
		claudeContent = renderPointerBlock(claudeSource, claudeBegin, claudeEnd, claudeBlock)
	}

	// The read bound accepts a file sitting exactly on the limit, but the managed
	// block is APPENDED to it, so the rendered result can land past the bound the
	// read enforces. Writing that would leave the repository holding an instruction
	// file this command itself produced and will refuse on the next run, telling
	// the user to shrink a file whose excess bytes are the block init-agents added.
	// The bound therefore has to hold for what is WRITTEN, not only for what was
	// read, and the refusal has to land here — every byte is computed and nothing
	// has been created or modified yet.
	if err := ensureRenderedInstructionFits(agentsPath, agentsSource, agentsContent); err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}
	if !sharedInstructions {
		if err := ensureRenderedInstructionFits(claudePath, claudeSource, claudeContent); err != nil {
			return fmt.Errorf("init-agents: %w", err)
		}
	}

	guideResolvedName, guideInfo, err := resolvedManagedTarget(repoRoot, guideName, false)
	if err != nil {
		return fmt.Errorf("init-agents: %w", err)
	}
	guideWasMissing := guideInfo == nil
	var createdGuideDirs []createdManagedTarget
	rollback := func(cause error, createdGuide os.FileInfo) error {
		cleanupErr := rollbackManagedTargets(repoRoot, guideResolvedName, createdGuide, createdGuideDirs)
		if cleanupErr == nil {
			return cause
		}
		return errors.Join(cause, fmt.Errorf("could not remove partial init-agents output: %w", cleanupErr))
	}

	createdGuideDirs, err = mkdirAllContained(repoRoot, guideDirName, 0o755)
	if err != nil {
		return rollback(err, nil)
	}
	// Name equality is a filesystem operation, not a portable string operation: for
	// example, APFS may identify two Unicode normalizations, and Win32 aliases trailing
	// dots and spaces. Materializing the planned directory lets the filesystem expose a
	// directory/file collision before the first file write.
	if err := validateManagedTargetTopology(repoRoot, guideDirName, guideName, agentsName, claudeName); err != nil {
		return fmt.Errorf("init-agents: %w", rollback(err, nil))
	}
	var guideCreated os.FileInfo
	if guideWasMissing {
		guideCreated, err = writeNewContainedFile(repoRoot, guideName, []byte(agentGuide), 0o644)
	} else {
		err = writeContainedFile(repoRoot, guideName, []byte(agentGuide), 0o644)
	}
	if err != nil {
		return rollback(err, guideCreated)
	}
	// A missing guide and instruction target can have distinct spellings but become the
	// same inode only once one is created. Ask the filesystem again now, before reporting
	// or writing either instruction file, and roll the new guide back on collision.
	if err := validateManagedTargetTopology(repoRoot, guideDirName, guideName, agentsName, claudeName); err != nil {
		return fmt.Errorf("init-agents: %w", rollback(err, guideCreated))
	}
	fmt.Fprintf(opts.Stdout, "wrote %s\n", guidePath)

	if err := writeContainedFile(repoRoot, agentsName, agentsContent, 0o644); err != nil {
		return fmt.Errorf("init-agents: AGENTS.md: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "updated %s\n", agentsPath)

	if sharedInstructions {
		// A symlink or hard link already gives Claude the AGENTS.md pointer. Updating the
		// same inode a second time could replace that pointer with an inheritance notice
		// that no longer has anything to inherit from.
		return nil
	}
	// Preserve the existing first-run behavior for a dangling AGENTS.md/CLAUDE.md alias:
	// writing AGENTS.md may have created the shared target that did not exist at preflight.
	agentsInfo, err = statContainedFile(repoRoot, agentsName)
	if err != nil {
		return fmt.Errorf("init-agents: AGENTS.md: %w", err)
	}
	claudeInfo, err = statContainedFile(repoRoot, claudeName)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("init-agents: CLAUDE.md: %w", err)
	}
	if err == nil && os.SameFile(agentsInfo, claudeInfo) {
		return nil
	}
	if err := writeContainedFile(repoRoot, claudeName, claudeContent, 0o644); err != nil {
		return fmt.Errorf("init-agents: CLAUDE.md: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "updated %s\n", claudePath)
	return nil
}

// ensureContainedInRepo refuses a write target whose resolution leaves the repository. It is a
// preflight only: root.Stat resolves the same way the write does but is not atomic with it, so
// writeContainedFile below remains the boundary that actually holds. Its job is to turn an
// escaping alias into one clear error before any file is created.
//
// A missing target is not an escape. init-agents legitimately creates all four paths, and the
// documented dangling alias (CLAUDE.md -> AGENTS.md before AGENTS.md exists) resolves to
// fs.ErrNotExist inside the root.
//
// Nor is an unreadable directory, a symlink loop, or an I/O error. Every remaining stat failure
// still aborts the install, but only the ones os.Root attributes to confinement are described as
// one; see isRootEscape.
//
// A path the walk below cannot resolve as spelled (errUnresolvableAlias) is neither. It has to be
// checked before the missing-target case because it arrives as the kernel's own ENOENT/ENOTDIR
// and would otherwise read as a file init-agents may create — and it is not creatable: an
// O_CREAT open of that same spelling fails exactly as the stat did.
func ensureContainedInRepo(root *os.Root, name, path string, createsParents bool, plannedDirs ...string) error {
	var err error
	if createsParents {
		var resolved string
		resolved, err = resolveContainedDirectoryName(root, name)
		if err == nil {
			_, err = statResolvedContained(root, resolved)
		}
	} else {
		_, err = statContainedFile(root, name)
	}
	switch {
	case errors.Is(err, errUnresolvableAlias):
		// Not an escape and not a target to create: the chain names its landing only
		// after a ".." the kernel could not have taken, so there is nothing to write.
		return fmt.Errorf(
			"%s: refusing to write through a link the filesystem cannot resolve (%w); "+
				"the target is not traversable as written, so repoint or remove the link, then rerun init-agents",
			path, err,
		)
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if createsParents {
			return nil
		}
		if parentErr := ensureMissingTargetParent(root, name, plannedDirs); parentErr != nil {
			return fmt.Errorf(
				"%s: refusing to write through a link the filesystem cannot resolve (%w); "+
					"the target's parent directory will not exist when it is written, so create it or repoint the link, then rerun init-agents",
				path, parentErr,
			)
		}
		return nil
	case errors.Is(err, errGitDirManagedTarget):
		return fmt.Errorf(
			"%s: %w; init-agents installs instruction files, and none of them belongs in the "+
				"git directory, so repoint or remove the link, then rerun init-agents",
			path, err,
		)
	case isRootEscape(err):
		return fmt.Errorf(
			"%s: refusing to write through a link that leaves the repository (%w); "+
				"init-agents only writes inside the project root, so repoint or remove the link, then rerun init-agents",
			path, err,
		)
	default:
		// An unreadable parent directory, a symlink loop, or an I/O error is not an attack.
		// Report it as itself: naming it a repository escape would send the reader hunting a
		// link that does not exist and hide the cause that does.
		return fmt.Errorf("%s: inspect write target: %w", path, err)
	}
}

// ensureMissingTargetParent distinguishes a creatable missing leaf from a path whose parent is
// also absent. OpenFile creates only the leaf, so accepting the latter would let init-agents write
// the guide and an earlier instruction file before the eventual ENOENT. plannedDirs are the
// directories runInitAgents creates before any file write; an alias whose resolved parent is one
// of those remains safely creatable.
func ensureMissingTargetParent(root *os.Root, name string, plannedDirs []string) error {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return err
	}
	parent := filepath.Dir(resolved)
	if parent == "." {
		return nil
	}
	info, err := root.Stat(parent)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("%w: parent %q is not a directory", errUnresolvableAlias, parent)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, planned := range plannedDirs {
		resolvedPlanned, resolveErr := resolveContainedDirectoryName(root, planned)
		if resolveErr == nil {
			rel, relErr := filepath.Rel(filepath.Clean(parent), filepath.Clean(resolvedPlanned))
			if relErr == nil && filepath.IsLocal(rel) {
				// MkdirAll creates resolvedPlanned and every missing ancestor
				// between it and the root, including parent.
				return nil
			}
		}
	}
	return fmt.Errorf("%w: parent %q does not exist", errUnresolvableAlias, parent)
}

func rollbackManagedTargets(root *os.Root, guide string, createdGuide os.FileInfo, directories []createdManagedTarget) error {
	var cleanupErrs []error
	if createdGuide != nil {
		if err := removeOwnedManagedTarget(root, guide, createdGuide); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := removeOwnedManagedTarget(root, directories[i].name, directories[i].info); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	return errors.Join(cleanupErrs...)
}

func removeOwnedManagedTarget(root *os.Root, name string, owned os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s before rollback: %w", name, err)
	}
	if !os.SameFile(owned, current) {
		return fmt.Errorf("refusing to remove %s during rollback because it was replaced concurrently", name)
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// validateManagedTargetTopology ensures the directory and three file writes cannot become one
// another after alias resolution. AGENTS.md and CLAUDE.md may intentionally share a file, but the
// guide must stay distinct, and no file target may be an ancestor that MkdirAll will turn into a
// directory. Checking before reads and writes preserves the all-target preflight guarantee.
func validateManagedTargetTopology(root *os.Root, guideDir, guide, agents, claude string) error {
	dirName, dirInfo, err := resolvedManagedTarget(root, guideDir, true)
	if err != nil {
		return err
	}
	if dirInfo != nil && !dirInfo.IsDir() {
		return fmt.Errorf("%s: expected a directory, found %s", guideDir, fileTypeName(dirInfo.Mode()))
	}
	guideName, guideInfo, err := resolvedManagedTarget(root, guide, false)
	if err != nil {
		return err
	}
	if guideInfo != nil && !guideInfo.Mode().IsRegular() {
		return fmt.Errorf("%s: expected a regular file, found %s", guide, fileTypeName(guideInfo.Mode()))
	}
	if pathIsAncestorOrSame(guideName, dirName) {
		return fmt.Errorf("%s: managed guide collides with directory target %s", guide, guideDir)
	}

	for _, instruction := range []string{agents, claude} {
		instructionName, instructionInfo, resolveErr := resolvedManagedTarget(root, instruction, false)
		if resolveErr != nil {
			return resolveErr
		}
		if instructionInfo != nil && !instructionInfo.Mode().IsRegular() {
			return fmt.Errorf("%s: expected a regular file, found %s", instruction, fileTypeName(instructionInfo.Mode()))
		}
		if pathIsAncestorOrSame(instructionName, dirName) {
			return fmt.Errorf("%s: instruction target will be created as a directory by %s", instruction, guideDir)
		}
		if sameResolvedName(guideName, instructionName) ||
			(guideInfo != nil && instructionInfo != nil && os.SameFile(guideInfo, instructionInfo)) {
			return fmt.Errorf("%s and %s resolve to the same managed file", guide, instruction)
		}
	}
	return nil
}

func resolvedManagedTarget(root *os.Root, name string, directory bool) (string, os.FileInfo, error) {
	var (
		resolved string
		err      error
	)
	if directory {
		resolved, err = resolveContainedDirectoryName(root, name)
	} else {
		resolved, err = resolveContainedName(root, name)
	}
	if err != nil {
		return "", nil, err
	}
	info, err := statResolvedContained(root, resolved)
	if errors.Is(err, fs.ErrNotExist) {
		return resolved, nil, nil
	}
	return resolved, info, err
}

// statResolvedContained distinguishes a genuinely absent landing from an existing object the
// host cannot follow. The distinction matters on Windows, where an unknown name-surrogate
// reparse point can make Stat report ENOENT even though Lstat sees an entry; treating that as a
// creatable leaf would defer failure until after another managed file had been written.
func statResolvedContained(root *os.Root, resolved string) (os.FileInfo, error) {
	info, err := root.Stat(resolved)
	if !errors.Is(err, fs.ErrNotExist) {
		return info, err
	}
	if _, lstatErr := root.Lstat(resolved); lstatErr == nil {
		return nil, fmt.Errorf("%w: %s exists but cannot be followed", errUnresolvableAlias, resolved)
	} else if !errors.Is(lstatErr, fs.ErrNotExist) {
		return nil, lstatErr
	}
	return nil, err
}

func sameResolvedName(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathIsAncestorOrSame(ancestor, path string) bool {
	ancestor = filepath.Clean(ancestor)
	path = filepath.Clean(path)
	if ancestor == "." {
		return true
	}
	ancestorParts := splitPathComponents(ancestor)
	pathParts := splitPathComponents(path)
	if len(ancestorParts) > len(pathParts) {
		return false
	}
	for i := range ancestorParts {
		if ancestorParts[i] != pathParts[i] {
			return false
		}
	}
	return true
}

// isRootEscape reports whether err is os.Root refusing to resolve a path outside its root.
//
// os.Root signals that with an unexported sentinel (os.errPathEscapes, a plain errors.New), so
// there is nothing to match with errors.Is. What separates it from every other failure is
// provenance: an operational failure is the kernel's answer relayed verbatim and therefore wraps
// a syscall.Errno, while the escape refusal is produced by os.Root itself before it issues the
// syscall. os.ErrClosed and os.ErrInvalid are the only other refusals os.Root raises without a
// syscall and neither is about containment, so they are excluded here rather than left to fall
// through to the security wording.
//
// This classification is advisory, not the security boundary. writeContainedFile refuses the
// same escapes on its own, which is what lets the ambiguous case above be reported as an
// ordinary error instead of being assumed hostile.
func isRootEscape(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return false
	}
	// errUnresolvableAlias is raised by the walk above, not by os.Root, and reports the
	// kernel's answer as text rather than as a wrapped errno — so it reaches here looking
	// syscall-free. It is a broken path, not an escape; saying otherwise would send the
	// reader hunting a link that leaves when none does.
	// errGitDirManagedTarget is likewise not an escape: it is raised for a link that stays
	// comfortably inside the root and lands in `.git`. Classifying it as an escape would tell
	// the reader the link leaves the repository when the whole point is that it does not.
	return !errors.Is(err, errUnresolvableAlias) && !errors.Is(err, errGitDirManagedTarget) &&
		!errors.Is(err, os.ErrClosed) && !errors.Is(err, os.ErrInvalid)
}

// containedLinkHopLimit bounds the manual expansion at the host family's kernel limit. A single
// cross-platform value is unsafe: Darwin and the BSDs reject a chain before Linux does, and
// stripping the links before handing the result to os.Root must not turn their ELOOP into a write.
func containedLinkHopLimit(fullyQualifiedTarget bool) int {
	switch runtime.GOOS {
	case "windows":
		if fullyQualifiedTarget {
			return 31
		}
		return 63
	case "illumos", "solaris":
		return 19
	case "aix", "darwin", "dragonfly", "freebsd", "netbsd", "openbsd":
		return 31
	default:
		return 40
	}
}

// mkdirAllContained is os.Root.MkdirAll with the same alias handling the confined writes get, so a
// project whose .entire is an in-repository alias spelled as an absolute path installs instead of
// failing preflight. It creates one component at a time and returns only directories this call
// successfully created; callers can therefore roll them back without deleting a pre-existing
// entry that merely looked missing through Stat or appeared concurrently.
type createdManagedTarget struct {
	name string
	info os.FileInfo
}

func mkdirAllContained(root *os.Root, name string, perm os.FileMode) ([]createdManagedTarget, error) {
	resolved, err := resolveContainedDirectoryName(root, name)
	if err != nil {
		return nil, err
	}
	for len(resolved) > 1 && os.IsPathSeparator(resolved[len(resolved)-1]) {
		resolved = resolved[:len(resolved)-1]
	}
	if resolved == "." {
		return nil, nil
	}

	var created []createdManagedTarget
	current := ""
	for _, component := range splitPathComponents(resolved) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		mkdirErr := root.Mkdir(current, perm)
		if mkdirErr == nil {
			info, statErr := root.Lstat(current)
			if statErr != nil {
				return created, statErr
			}
			created = append(created, createdManagedTarget{name: current, info: info})
			continue
		}
		if !errors.Is(mkdirErr, fs.ErrExist) {
			return created, mkdirErr
		}
		info, statErr := root.Stat(current)
		if statErr != nil {
			return created, statErr
		}
		if !info.IsDir() {
			return created, fmt.Errorf("%s: expected a directory, found %s", current, fileTypeName(info.Mode()))
		}
	}
	return created, nil
}

// resolveContainedName rewrites name into the equivalent os.Root can resolve. It is the single
// place confinement understands an alias, and every sink plus the preflight above goes through it,
// so no call site can be the one that forgets.
//
// os.Root resolves each component with openat relative to the opened directory, which is exactly
// what makes it a boundary — and also means it has no starting point for a symlink whose target is
// spelled as an absolute path. It refuses such a link as an escape even when that path names a
// file inside the very repository being written to, which is a legitimate and previously working
// shape of the documented AGENTS.md/CLAUDE.md alias. Confinement must judge where a link LANDS,
// not how it is spelled — and where it lands is only visible once the WHOLE chain is followed, so
// an absolute hop reached through a relative one has to be resolved too.
//
// So this walks the chain the way the kernel does. resolved holds the prefix already stripped of
// symlinks, which is half of what makes ".." correct: popping its last element is what the kernel
// does after following a link, whereas trimming the name as text would ignore the link. The other
// half is that the kernel must have been able to take that step at all — see
// ensureParentTraversable, which asks it before anything is popped. A relative
// target is pushed back as components so its own links are expanded in turn; an absolute target
// that lands inside the repository restarts the walk from the root under its repository-relative
// name. containedLinkHopLimit bounds the expansion at the host family's kernel limit.
//
// Any chain that leaves the root — an absolute target outside it, or a ".." above it — returns the
// ORIGINAL name so os.Root walks the real links itself and refuses them. That is the whole safety
// argument: this function only ever hands os.Root a name, never a file, so it is a translation and
// never a permission, and a rewrite that is wrong can only fail closed.
func resolveContainedName(root *os.Root, name string) (string, error) {
	return resolveContainedNameWithOptions(root, name, false)
}

// resolveContainedDirectoryName permits a terminal directory requirement to name a missing
// landing because its caller is MkdirAll (or the preflight for that exact operation). File sinks
// use resolveContainedName and still reject the same spelling before any write.
func resolveContainedDirectoryName(root *os.Root, name string) (string, error) {
	return resolveContainedNameWithOptions(root, name, true)
}

// resolveContainedNameWithOptions is resolveContainedLanding plus the one landing that
// containment alone cannot refuse: a managed target that resolves INSIDE the repository's git
// directory.
//
// os.Root keeps the write in the project root, and until now that was the whole argument — see
// the os.OpenRoot comment in runInitAgents, which reasons that a link staying inside the
// repository is safe to follow because the repository is where init-agents writes anyway. That
// is true of repository CONTENT and false of `.git`, which is inside the root and is not content.
// A hostile checkout that commits `CLAUDE.md -> .git/config` gets the managed block appended to
// git's own config, and git then refuses to operate on the repository at all ("fatal: bad config
// line N"); `.git/hooks/*` is the same primitive aimed somewhere worse. Nothing about the
// documented alias needs it: docs/agents.md offers symlinks so AGENTS.md and CLAUDE.md can share
// ONE INSTRUCTION FILE, and no instruction file lives in `.git`.
//
// It is also the boundary the rest of the tool already holds from the other side. The indexer
// refuses to READ inside a git directory whatever its name or depth (hasGitDirComponent, which
// this asks through sem.PathLandsInGitDir rather than restating); a writer that will not read
// there must not write there either.
//
// This sits on the resolver rather than on the preflight because the preflight is not the
// enforcement: ensureContainedInRepo is explicitly not atomic with the write, so a link swapped
// in afterwards would pass a preflight-only check. Every stat, read and write of a managed target
// resolves through here, so each one re-asks, and a link swapped between them is refused by the
// operation that finds it.
func resolveContainedNameWithOptions(root *os.Root, name string, allowMissingDirectory bool) (string, error) {
	resolved, err := resolveContainedLanding(root, name, allowMissingDirectory)
	if err != nil {
		return "", err
	}
	if sem.PathLandsInGitDir(resolved) {
		return "", fmt.Errorf(
			"%w: %s resolves to %s, inside the repository's git directory, where init-agents must never write",
			errGitDirManagedTarget, name, filepath.ToSlash(resolved),
		)
	}
	return resolved, nil
}

// errGitDirManagedTarget marks a managed target whose resolution lands in the git directory.
var errGitDirManagedTarget = errors.New("refusing to write into the git directory")

func resolveContainedLanding(root *os.Root, name string, allowMissingDirectory bool) (string, error) {
	pending := splitPathComponents(name)
	var resolved []string
	requiresDirectory := false
	fullyQualifiedTarget := false
	var windowsFinalDirectory *bool
	for hops := 0; len(pending) > 0; {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "":
			// A trailing separator requires the landing to be a directory. Preserve that
			// requirement in the name handed to os.Root; an empty component in the middle
			// is just a repeated separator.
			if len(pending) == 0 {
				requiresDirectory = true
			}
			continue
		case ".":
			// A terminal "/." carries the same directory requirement as a trailing
			// separator. Interior dots may be dropped because the following component
			// still forces the prefix to be traversed as a directory.
			if len(pending) == 0 {
				requiresDirectory = true
			}
			continue
		case "..":
			if len(resolved) == 0 {
				// Above the root. Let os.Root refuse the real chain.
				return name, nil
			}
			if err := ensureParentTraversable(root, resolved); err != nil {
				return "", err
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := filepath.Join(append(slices.Clone(resolved), component)...)
		info, err := root.Lstat(candidate)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				resolved = append(resolved, component)
				continue
			}
			return "", err
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		isWindowsReparsePoint := runtime.GOOS == "windows" && info.Mode()&os.ModeIrregular != 0
		if !isSymlink && !isWindowsReparsePoint {
			resolved = append(resolved, component)
			continue
		}
		var target string
		if runtime.GOOS == "windows" {
			rawTarget, available, rawErr := windowsRawReparseTarget(root.Name(), candidate, info)
			if rawErr != nil {
				return "", fmt.Errorf("%w: cannot inspect raw Windows reparse target: %v", errUnresolvableAlias, rawErr)
			}
			if !available {
				// Since Go 1.23, Windows reports every reparse point as
				// ModeIrregular. Preserve tags that do not participate in path
				// resolution for the ordinary file-type check below.
				if isWindowsReparsePoint && !isSymlink {
					resolved = append(resolved, component)
					continue
				}
				return "", fmt.Errorf("%w: cannot inspect raw Windows reparse target", errUnresolvableAlias)
			}
			// Resolve the raw target from the same reparse-buffer snapshot that was
			// screened, so malformed offsets never reach Readlink's unsafe parser and
			// an in-place update cannot pair an unchecked target with this walk.
			target = rawTarget
		} else {
			var readErr error
			target, readErr = root.Readlink(candidate)
			if readErr != nil {
				return "", readErr
			}
		}
		if runtime.GOOS == "windows" {
			linkDirectory, ok := windowsLinkRequiresDirectory(info)
			if !ok {
				return "", fmt.Errorf("%w: cannot determine Windows link type for %s", errUnresolvableAlias, candidate)
			}
			if len(pending) > 0 {
				if !linkDirectory {
					return "", fmt.Errorf("%w: Windows file symlink %s cannot be traversed as a directory", errUnresolvableAlias, candidate)
				}
			} else {
				if windowsFinalDirectory != nil && *windowsFinalDirectory != linkDirectory {
					return "", fmt.Errorf("%w: Windows link chain changes its required target type", errUnresolvableAlias)
				}
				required := linkDirectory
				windowsFinalDirectory = &required
			}
		}
		hops++
		if filepath.IsAbs(target) || filepath.VolumeName(target) != "" ||
			(len(target) > 0 && os.IsPathSeparator(target[0])) {
			fullyQualifiedTarget = true
		}
		if hops > containedLinkHopLimit(fullyQualifiedTarget) {
			return "", &fs.PathError{Op: "resolve", Path: name, Err: syscall.ELOOP}
		}
		if !filepath.IsAbs(target) {
			// Windows drive-relative (C:foo) and drive-rooted (\foo) targets are not
			// link-relative even though filepath.IsAbs reports false. A drive-relative
			// target has process-global semantics and is left for os.Root to reject.
			if filepath.VolumeName(target) != "" {
				return name, nil
			}
			if len(target) > 0 && os.IsPathSeparator(target[0]) {
				// A Windows drive-rooted target (\foo) is anchored at the link's
				// volume. Expand that volume without cleaning the suffix so a legitimate
				// in-repository target can be judged by filesystem identity.
				target = filepath.VolumeName(root.Name()) + target
			} else {
				if runtime.GOOS == "windows" {
					// Windows applies lexical cleaning to the entire path before it
					// opens any component, including after substituting a relative
					// reparse target. Rebuild that whole path here so a/../b has the
					// same meaning as b even when a is missing or not a directory.
					combined := strings.Join(resolved, string(filepath.Separator))
					if combined != "" {
						combined += string(filepath.Separator)
					}
					combined += target
					if len(pending) > 0 {
						combined += string(filepath.Separator) + strings.Join(pending, string(filepath.Separator))
					}
					resolved = resolved[:0]
					pending = splitPathComponents(cleanWindowsPathPreservingDirectory(combined))
					continue
				}
				pending = append(splitPathComponents(target), pending...)
				continue
			}
		}
		if runtime.GOOS == "windows" {
			if windowsPathDisablesCleaning(target) {
				// Extended/device paths disable parts of Win32 parsing that the
				// confined relative operations below necessarily apply (including
				// separator and trailing-dot handling). There is no generally safe
				// spelling-preserving translation, so fail before any write.
				return "", fmt.Errorf("%w: unsupported Windows extended or device path", errUnresolvableAlias)
			}
			target = cleanWindowsPathPreservingDirectory(target)
		}
		rel, prefixHops, ok := repoRelativeName(root.Name(), target)
		if !ok {
			// Lands outside. Let os.Root refuse the real chain.
			return name, nil
		}
		hops += prefixHops
		if hops > containedLinkHopLimit(fullyQualifiedTarget) {
			return "", &fs.PathError{Op: "resolve", Path: name, Err: syscall.ELOOP}
		}
		// An absolute target is anchored at the root, so the walk restarts there.
		resolved = resolved[:0]
		pending = append(splitPathComponents(rel), pending...)
	}
	result := "."
	if len(resolved) > 0 {
		result = filepath.Join(resolved...)
	}
	if windowsFinalDirectory != nil {
		info, err := statResolvedContained(root, result)
		switch {
		case err == nil && info.IsDir() != *windowsFinalDirectory:
			return "", fmt.Errorf("%w: Windows link target type does not match the link", errUnresolvableAlias)
		case errors.Is(err, fs.ErrNotExist) && *windowsFinalDirectory != allowMissingDirectory:
			return "", fmt.Errorf("%w: dangling Windows link type does not match the managed target", errUnresolvableAlias)
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", err
		}
	}
	if requiresDirectory {
		result += string(filepath.Separator)
		if _, err := root.Lstat(result); err != nil {
			if allowMissingDirectory && errors.Is(err, fs.ErrNotExist) {
				return result, nil
			}
			return "", fmt.Errorf("%w: %v", errUnresolvableAlias, err)
		}
	}
	if len(resolved) == 0 {
		// The chain resolved to the repository root itself.
		return result, nil
	}
	return result, nil
}

// splitPathComponents preserves empty and terminal components while accepting every separator
// the host accepts. In particular, Windows treats both '\\' and '/' as separators; leaving '/'
// embedded in a component would let filepath.Join clean a raw "missing/../victim" before the
// component walker can ask the filesystem whether "missing" is traversable.
func splitPathComponents(path string) []string {
	components := make([]string, 0, strings.Count(path, string(filepath.Separator))+1)
	start := 0
	for i := 0; i < len(path); i++ {
		if os.IsPathSeparator(path[i]) {
			components = append(components, path[start:i])
			start = i + 1
		}
	}
	return append(components, path[start:])
}

func cleanWindowsPathPreservingDirectory(path string) string {
	requiresDirectory := len(path) > 0 && os.IsPathSeparator(path[len(path)-1])
	if !requiresDirectory {
		components := splitPathComponents(path)
		requiresDirectory = len(components) > 0 && components[len(components)-1] == "."
	}
	cleaned := filepath.Clean(path)
	if requiresDirectory && (len(cleaned) == 0 || !os.IsPathSeparator(cleaned[len(cleaned)-1])) {
		cleaned += string(filepath.Separator)
	}
	return cleaned
}

func windowsPathDisablesCleaning(path string) bool {
	upper := strings.ToUpper(strings.ReplaceAll(path, "/", `\`))
	return strings.HasPrefix(upper, `\\?\`) || strings.HasPrefix(upper, `\\.\`) ||
		strings.HasPrefix(upper, `\??\`)
}

// errUnresolvableAlias marks an alias spelling that does not name a creatable managed target.
// This includes a ".." over a component the kernel cannot traverse and a file target ending in
// "/" or "/.". Folding the kernel's answer in as text rather than wrapping it keeps
// os.IsNotExist from reading either shape as the ordinary creatable-file case.
var errUnresolvableAlias = errors.New("link target is not traversable as written")

// ensureParentTraversable asks the kernel whether ".." may be taken out of the last component of
// resolved, and is what makes collapsing it equivalent to the walk the kernel would perform.
//
// filepath.Abs, filepath.Join and filepath.Clean collapse ".." LEXICALLY — before a single
// component is opened — so text alone cannot tell a legal traversal from one that never happens.
// A prefix that does not exist, is not a directory, or cannot be searched stops the kernel with
// ENOENT/ENOTDIR/EACCES; erasing it as text instead leaves the remaining elements naming a file
// the kernel would never have reached, and init-agents would write there.
//
// os.Root is the oracle rather than a rule reconstructed here: it resolves each component with
// openat, ".." included, so handing it the prefix with the ".." still attached returns exactly
// the kernel's own verdict — including reasons this code does not enumerate, such as a directory
// without search permission. The probe path is assembled with strings.Join, not filepath.Join,
// because Join cleans: it would delete the ".." this probe exists to send to the kernel.
//
// The pop itself stays lexical, and is correct once the probe passes: resolved holds only
// components already stripped of symlinks, so its parent is its textual parent.
func ensureParentTraversable(root *os.Root, resolved []string) error {
	probe := strings.Join(append(slices.Clone(resolved), ".."), string(filepath.Separator))
	if _, err := root.Lstat(probe); err != nil {
		return fmt.Errorf("%w: %v", errUnresolvableAlias, err)
	}
	return nil
}

// repoRelativeName reports whether target lands inside the repository rooted at rootPath, and
// under what name. It deliberately preserves the raw suffix after the repository root: cleaning
// an absolute target such as /repo/missing/../victim before the component walk would erase the
// missing directory the kernel stops at and redirect the write to victim.
//
// Each raw prefix is compared with the root by filesystem identity. That supports roots reached
// through a symlinked parent and case-only spelling differences without collapsing any remaining
// components. A prefix that lands in a repository subdirectory through an external directory
// alias is mapped separately, only after that prefix resolved successfully; its untouched suffix
// is then appended for the confined component walk to validate. The returned name is still
// resolved by os.Root, so this translation never grants access outside the root.
func repoRelativeName(rootPath, target string) (string, int, bool) {
	rootInfo, err := os.Stat(rootPath)
	if err != nil {
		return "", 0, false
	}

	// Do not probe a different UNC share when the repository root has another spelling;
	// os.Root will reject that absolute link without contacting the remote filesystem.
	// Local device and volume-GUID spellings are left to the identity check because they
	// can name the same directory as an ordinary drive path.
	if runtime.GOOS == "windows" {
		targetVolume := filepath.VolumeName(target)
		if targetShare, remote := uncShareName(target); remote {
			rootShare, rootRemote := uncShareName(rootPath)
			if !rootRemote || !strings.EqualFold(rootShare, targetShare) {
				return "", 0, false
			}
		} else if isUNCVolume(targetVolume) {
			return "", 0, false
		}
	}

	if rel, hops, ok := repoRelativeRawName(rootInfo, target); ok {
		return rel, hops, true
	}
	return "", 0, false
}

func isUNCVolume(volume string) bool {
	normalized := strings.ReplaceAll(volume, "/", `\`)
	upper := strings.ToUpper(normalized)
	if upper == `\\?\UNC` || upper == `\??\UNC` ||
		strings.HasPrefix(upper, `\\?\UNC\`) || strings.HasPrefix(upper, `\\.\UNC\`) ||
		strings.HasPrefix(upper, `\??\UNC\`) {
		return true
	}
	for _, prefix := range []string{`\\?\`, `\\.\`, `\??\`} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		device := strings.TrimSuffix(strings.TrimPrefix(upper, prefix), `\`)
		if len(device) == 2 && device[1] == ':' {
			return false
		}
		if strings.HasPrefix(device, `VOLUME{`) && strings.HasSuffix(device, `}`) {
			return false
		}
		// Unknown device namespaces include GLOBALROOT and named-pipe/network
		// providers. Refuse them before any filesystem probe; only known local
		// drive and volume-GUID spellings are safe to identity-check.
		return true
	}
	return strings.HasPrefix(normalized, `\\`)
}

// uncShareName returns a namespace-independent server/share key for every UNC spelling Windows'
// path parser accepts. filepath.VolumeName is insufficient here: for an extended UNC path it
// returns only "\\?\UNC", which would make two different remote shares look like one volume.
func uncShareName(path string) (string, bool) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	upper := strings.ToUpper(normalized)
	var rest string
	switch {
	case strings.HasPrefix(upper, `\\?\UNC\`):
		rest = normalized[len(`\\?\UNC\`):]
	case strings.HasPrefix(upper, `\\.\UNC\`):
		rest = normalized[len(`\\.\UNC\`):]
	case strings.HasPrefix(upper, `\??\UNC\`):
		rest = normalized[len(`\??\UNC\`):]
	case strings.HasPrefix(normalized, `\\`) &&
		!strings.HasPrefix(upper, `\\?\`) && !strings.HasPrefix(upper, `\\.\`):
		rest = normalized[2:]
	default:
		return "", false
	}
	parts := strings.Split(rest, `\`)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return strings.ToUpper(rest), true
	}
	return strings.ToUpper(parts[0] + `\` + parts[1]), true
}

// repoRelativeRawName first looks for a raw prefix that names the root directly. If none does, it
// resolves each successfully traversed prefix independently and checks whether that prefix lands
// in a root subdirectory. It never resolves the whole target and then appends a cleaned result:
// the remaining raw suffix may contain a ".." whose preceding component the kernel cannot enter.
func repoRelativeRawName(rootInfo os.FileInfo, target string) (string, int, bool) {
	if rel, hops, ok := repoRelativeDirectName(rootInfo, target); ok {
		return rel, hops, true
	}

	volumeLen := len(filepath.VolumeName(target))
	end := volumeLen
	for end < len(target) && os.IsPathSeparator(target[end]) {
		end++
	}
	for end < len(target) {
		for end < len(target) && os.IsPathSeparator(target[end]) {
			end++
		}
		if end == len(target) {
			break
		}
		for end < len(target) && !os.IsPathSeparator(target[end]) {
			end++
		}
		prefix := target[:end]
		if _, err := os.Stat(prefix); err != nil {
			continue
		}
		resolvedPrefix, hops, err := resolvePathAndCountLinks(prefix)
		if err != nil {
			continue
		}
		base, _, ok := repoRelativeDirectName(rootInfo, resolvedPrefix)
		if !ok {
			continue
		}
		return appendRawRelativeSuffix(base, target[end:]), hops, true
	}
	return "", 0, false
}

// repoRelativeDirectName finds the first raw prefix that names the repository root and returns
// the untouched suffix after it. Prefixes are passed to os.Stat exactly as spelled, so a symlink
// or ".." is resolved by the kernel before identity is compared; the suffix is not cleaned.
func repoRelativeDirectName(rootInfo os.FileInfo, target string) (string, int, bool) {
	volumeLen := len(filepath.VolumeName(target))
	end := volumeLen
	for end < len(target) && os.IsPathSeparator(target[end]) {
		end++
	}
	if end > 0 {
		if rel, ok := relativeSuffixIfRoot(rootInfo, target, end); ok {
			_, hops, err := resolvePathAndCountLinks(target[:end])
			return rel, hops, err == nil
		}
	}
	for end < len(target) {
		for end < len(target) && os.IsPathSeparator(target[end]) {
			end++
		}
		if end == len(target) {
			break
		}
		for end < len(target) && !os.IsPathSeparator(target[end]) {
			end++
		}
		if rel, ok := relativeSuffixIfRoot(rootInfo, target, end); ok {
			_, hops, err := resolvePathAndCountLinks(target[:end])
			return rel, hops, err == nil
		}
	}
	return "", 0, false
}

// resolvePathAndCountLinks resolves path while counting every link the host follows, including
// links in an absolute target's directory prefix. Those prefix links are invisible after the
// target is translated to a repository-relative name, but the kernel counts them toward the same
// ELOOP limit as the link whose target contained them. Counting only the links opened through
// os.Root would therefore authorize chains the host rejects (for example, each /tmp component on
// macOS also follows /tmp -> /private/tmp).
//
// The walk mirrors filepath.EvalSymlinks' component semantics but returns the count rather than a
// cleaned name. Callers first prove the prefix resolves and lands inside the repository; any race
// or unexpected spelling encountered here is an error, so this helper can only make translation
// fail closed.
func resolvePathAndCountLinks(path string) (string, int, error) {
	original := path
	volumeLen := len(filepath.VolumeName(path))
	if volumeLen < len(path) && os.IsPathSeparator(path[volumeLen]) {
		volumeLen++
	}
	volume := path[:volumeLen]
	dest := volume
	hops := 0

	for start, end := volumeLen, volumeLen; start < len(path); start = end {
		for start < len(path) && os.IsPathSeparator(path[start]) {
			start++
		}
		end = start
		for end < len(path) && !os.IsPathSeparator(path[end]) {
			end++
		}

		isWindowsDot := runtime.GOOS == "windows" && path[len(filepath.VolumeName(path)):] == "."
		if end == start {
			break
		}
		component := path[start:end]
		if component == "." && !isWindowsDot {
			continue
		}
		if component == ".." {
			lastSeparator := len(dest) - 1
			for ; lastSeparator >= volumeLen; lastSeparator-- {
				if os.IsPathSeparator(dest[lastSeparator]) {
					break
				}
			}
			if lastSeparator < volumeLen || dest[lastSeparator+1:] == ".." {
				if len(dest) > volumeLen {
					dest += string(filepath.Separator)
				}
				dest += ".."
			} else {
				dest = dest[:lastSeparator]
			}
			continue
		}

		if len(dest) > len(filepath.VolumeName(dest)) && !os.IsPathSeparator(dest[len(dest)-1]) {
			dest += string(filepath.Separator)
		}
		dest += component

		info, err := os.Lstat(dest)
		if err != nil {
			return "", 0, err
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		isWindowsReparsePoint := runtime.GOOS == "windows" && info.Mode()&os.ModeIrregular != 0
		if !isSymlink && !isWindowsReparsePoint {
			if !info.IsDir() && end < len(path) {
				return "", 0, &fs.PathError{Op: "resolve", Path: original, Err: syscall.ENOTDIR}
			}
			continue
		}

		var link string
		if runtime.GOOS == "windows" {
			rawTarget, available, rawErr := windowsRawReparseTargetAtPath(dest, info)
			if rawErr != nil {
				return "", 0, rawErr
			}
			if !available {
				if isWindowsReparsePoint && !isSymlink {
					if !info.IsDir() && end < len(path) {
						return "", 0, &fs.PathError{Op: "resolve", Path: original, Err: syscall.ENOTDIR}
					}
					continue
				}
				return "", 0, fmt.Errorf("cannot inspect raw Windows reparse target")
			}
			link = rawTarget
		} else {
			var readErr error
			link, readErr = os.Readlink(dest)
			if readErr != nil {
				return "", 0, readErr
			}
		}
		hops++
		if hops > 255 {
			return "", 0, &fs.PathError{Op: "resolve", Path: original, Err: syscall.ELOOP}
		}
		if isWindowsDot && !filepath.IsAbs(link) {
			break
		}

		// A rooted Windows target without its own volume stays on the volume of the
		// link being followed. Make that implicit anchor explicit before restarting.
		if runtime.GOOS == "windows" && filepath.VolumeName(link) == "" &&
			len(link) > 0 && os.IsPathSeparator(link[0]) {
			link = filepath.VolumeName(volume) + link
		}
		path = link + path[end:]
		linkVolumeLen := len(filepath.VolumeName(link))
		if linkVolumeLen > 0 {
			if linkVolumeLen < len(link) && os.IsPathSeparator(link[linkVolumeLen]) {
				linkVolumeLen++
			}
			volume = link[:linkVolumeLen]
			volumeLen = linkVolumeLen
			dest = volume
			end = volumeLen
		} else if len(link) > 0 && os.IsPathSeparator(link[0]) {
			volume = link[:1]
			volumeLen = 1
			dest = volume
			end = volumeLen
		} else {
			lastSeparator := len(dest) - 1
			for ; lastSeparator >= volumeLen; lastSeparator-- {
				if os.IsPathSeparator(dest[lastSeparator]) {
					break
				}
			}
			if lastSeparator < volumeLen {
				dest = volume
			} else {
				dest = dest[:lastSeparator]
			}
			end = 0
		}
	}
	return filepath.Clean(dest), hops, nil
}

func appendRawRelativeSuffix(base, suffix string) string {
	hadSeparator := len(suffix) > 0 && os.IsPathSeparator(suffix[0])
	for len(suffix) > 0 && os.IsPathSeparator(suffix[0]) {
		suffix = suffix[1:]
	}
	if suffix == "" {
		if hadSeparator {
			return base + string(filepath.Separator)
		}
		return base
	}
	if base == "." {
		return suffix
	}
	return base + string(filepath.Separator) + suffix
}

func relativeSuffixIfRoot(rootInfo os.FileInfo, target string, prefixEnd int) (string, bool) {
	info, err := os.Stat(target[:prefixEnd])
	if err != nil || !os.SameFile(rootInfo, info) {
		return "", false
	}
	suffix := target[prefixEnd:]
	for len(suffix) > 0 && os.IsPathSeparator(suffix[0]) {
		suffix = suffix[1:]
	}
	if suffix == "" {
		return ".", true
	}
	return suffix, true
}

// statContainedFile is os.Stat confined to root. It follows a link that stays inside the
// repository — os.Root does the following, so the boundary is enforced by the same openat walk the
// writes use — and the FileInfo it returns describes the TARGET inode, which is what os.SameFile
// needs to recognize the documented AGENTS.md/CLAUDE.md alias.
func statContainedFile(root *os.Root, name string) (os.FileInfo, error) {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return nil, err
	}
	return statResolvedContained(root, resolved)
}

// readContainedFile is os.ReadFile confined to root and bounded by limit bytes.
//
// Reading through the root rather than through the absolute path is not redundant with the
// preflight. ensureContainedInRepo is explicitly not atomic with what follows, so a link swapped
// to an outside file after the preflight and restored before the write would otherwise have that
// file's contents read in and then written back into the repository under the managed block. The
// escape has to be refused at the read too, not only at the preflight and the write.
//
// The limit is enforced on the read itself rather than on a preceding Stat, for the same
// non-atomicity reason: a file that grows between the check and the read would defeat a
// Stat-based gate, and a growing file is exactly the case the limit exists for. One byte past
// the limit is requested so a file sitting exactly on it is still accepted, and anything larger
// is refused rather than truncated — a truncated instruction file would be written back over the
// user's own text, which is a far worse outcome than a refusal.
func readContainedFile(root *os.Root, name string, limit int64) ([]byte, error) {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errContainedFileTooLarge
	}
	return content, nil
}

// writeContainedFile is os.WriteFile confined to root. The perm argument applies only when the
// file is created, matching os.WriteFile, so an existing file keeps its mode.
func writeContainedFile(root *os.Root, name string, content []byte, perm os.FileMode) error {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// writeNewContainedFile is the create-only counterpart to writeContainedFile. The returned
// FileInfo identifies the entry this call acquired even if writing or closing it subsequently
// failed, so rollback can refuse to remove a concurrent replacement.
func writeNewContainedFile(root *os.Root, name string, content []byte, perm os.FileMode) (os.FileInfo, error) {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	createdInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		return createdInfo, writeErr
	}
	return createdInfo, closeErr
}

// inspectInstructionFile identifies existing aliases and rejects targets that cannot be safely
// read as instruction files. A missing target—including a dangling alias—is preserved as the
// empty-file case supported by init-agents.
//
// A stat that follows the link, not an Lstat, is deliberate and load-bearing: AGENTS.md and
// CLAUDE.md are documented as permitted to be the same file, and os.SameFile in runInitAgents can
// only see that when both FileInfos describe the TARGET inode. Following it is safe because the
// following is done by os.Root, which refuses a link that leaves the repository; containment, not
// link rejection, is what keeps the write in the project root. path is carried alongside name only
// so the error names the file the caller asked about.
func inspectInstructionFile(root *os.Root, name, path string) (os.FileInfo, error) {
	info, err := statContainedFile(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: inspect target: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: expected a regular file, found %s", path, fileTypeName(info.Mode()))
	}
	return info, nil
}

// fileTypeName names what was found in place of a regular file. FileMode.Type()
// formats as permission bits ("d---------"), which tells whoever hit this nothing
// about what to remove or move aside.
func fileTypeName(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "device"
	case mode&os.ModeIrregular != 0:
		return "irregular file"
	default:
		return mode.Type().String()
	}
}

func readAndValidateInstructionFile(root *os.Root, name, path string) ([]byte, int, int, error) {
	content, err := readContainedFile(root, name, maxInstructionFileBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, -1, -1, nil
		}
		if errors.Is(err, errContainedFileTooLarge) {
			return nil, -1, -1, fmt.Errorf(
				"%s: file is larger than the %d-byte limit init-agents will read; "+
					"an instruction file this size is not one this command can safely rewrite, "+
					"so reduce it (or move its bulk into a file it imports) and rerun init-agents",
				path, maxInstructionFileBytes,
			)
		}
		return nil, -1, -1, fmt.Errorf("%s: read file: %w", path, err)
	}
	begin, end, err := validatePointerMarkers(path, content)
	if err != nil {
		return nil, -1, -1, err
	}
	return content, begin, end, nil
}

// ensureRenderedInstructionFits keeps init-agents from writing an instruction file
// it would refuse to read. It reports the source and rendered sizes because the
// difference between them is the block this command adds, which is the part the
// user cannot shrink.
func ensureRenderedInstructionFits(path string, source, rendered []byte) error {
	if len(rendered) <= maxInstructionFileBytes {
		return nil
	}
	return fmt.Errorf(
		"%s: the file is %d bytes and the Entire Graph managed block would take it to %d, "+
			"past the %d-byte limit init-agents will read back on its next run; "+
			"reduce it (or move its bulk into a file it imports) and rerun init-agents",
		path, len(source), len(rendered), maxInstructionFileBytes,
	)
}

// validatePointerMarkers intentionally counts the raw marker tokens. Markers in examples,
// comments, or other Markdown regions are still managed-marker tokens and must not make the
// replacement range ambiguous.
func validatePointerMarkers(path string, content []byte) (int, int, error) {
	beginToken := []byte(agentPointerBegin)
	endToken := []byte(agentPointerEnd)
	beginCount := bytes.Count(content, beginToken)
	endCount := bytes.Count(content, endToken)
	begin := bytes.Index(content, beginToken)
	end := bytes.Index(content, endToken)

	if beginCount == 0 && endCount == 0 {
		return -1, -1, nil
	}
	if beginCount == 1 && endCount == 1 && begin < end {
		return begin, end, nil
	}

	reason := fmt.Sprintf("found %d begin marker(s) and %d end marker(s)", beginCount, endCount)
	if beginCount == 1 && endCount == 1 {
		reason = "the end marker appears before the begin marker"
	}
	return -1, -1, fmt.Errorf(
		"%s: malformed Entire Graph managed markers (%s); back up the file, preserve user-owned text, reduce it to zero markers or exactly one complete %q / %q pair with begin before end, then rerun init-agents",
		path, reason, agentPointerBegin, agentPointerEnd,
	)
}

// claudeDirectlyImportsAgents recognizes a standalone @path directive outside the Markdown
// regions where Claude suppresses imports. Ambiguous syntax returns false so the direct pointer
// remains in place.
func claudeDirectlyImportsAgents(content []byte, claudePath, agentsPath string) bool {
	var found, fence bool
	var until string
	var fenceChar byte
	var fenceWidth int
	var codeTicks int
	for _, raw := range strings.Split(string(content), "\n") {
		indent := markdownIndent(raw)
		line := strings.TrimSpace(raw)
		if until != "" {
			if strings.Contains(line, until) {
				until = ""
			}
			continue
		}
		if strings.Contains(line, agentPointerBegin) {
			if !strings.Contains(line, agentPointerEnd) {
				until = agentPointerEnd
			}
			continue
		}
		if strings.Contains(line, agentPointerEnd) {
			return false
		}
		if strings.Contains(line, "<!--") {
			if !strings.Contains(line, "-->") {
				until = "-->"
			}
			continue
		}
		if strings.Contains(line, "-->") {
			return false
		}
		if fence {
			width := len(line) - len(strings.TrimLeft(line, string(fenceChar)))
			if indent <= 3 && width >= fenceWidth && strings.TrimSpace(line[width:]) == "" {
				fence = false
			}
			continue
		}
		if indent <= 3 && len(line) >= 3 && (strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")) {
			fence, fenceChar = true, line[0]
			fenceWidth = len(line) - len(strings.TrimLeft(line, string(fenceChar)))
			continue
		}
		if codeTicks != 0 || strings.Contains(line, "`") {
			codeTicks = nextCodeSpanWidth(line, codeTicks)
			continue
		}
		// Preserve raw indentation: trimming must not turn an indented Markdown code
		// example into a live import and suppress Claude's direct guide pointer.
		if indent >= 4 {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			continue
		}
		imported := strings.TrimPrefix(line, "@")
		if imported == "" || len(strings.Fields(imported)) != 1 {
			continue
		}
		candidate := imported
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(filepath.Dir(claudePath), candidate)
		}
		if filepath.Clean(candidate) == filepath.Clean(agentsPath) {
			found = true
		}
	}
	return found && until == "" && !fence
}

func markdownIndent(line string) int {
	indent := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			indent++
		case '\t':
			return 4
		default:
			return indent
		}
	}
	return indent
}

func nextCodeSpanWidth(line string, open int) int {
	for cursor := 0; cursor < len(line); {
		if line[cursor] != '`' {
			cursor++
			continue
		}
		width := len(line[cursor:]) - len(strings.TrimLeft(line[cursor:], "`"))
		if open == 0 {
			open = width
		} else if open == width {
			open = 0
		}
		cursor += width
	}
	return open
}

// renderPointerBlock appends the block to an empty/unmanaged snapshot, or replaces its already
// validated marker-delimited block in place. It performs no I/O so callers can render all output
// before the first write.
func renderPointerBlock(existing []byte, begin, end int, block string) []byte {
	content := string(existing)
	switch {
	case begin >= 0 && end > begin:
		content = content[:begin] + strings.TrimSuffix(block, "\n") + content[end+len(agentPointerEnd):]
	case len(content) == 0:
		content = block
	default:
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + block
	}
	return []byte(content)
}
