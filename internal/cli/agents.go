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
	"slices"
	"strings"
	"syscall"
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
	for _, target := range []struct{ name, path string }{
		{guideDirName, filepath.Join(root, guideDirName)},
		{guideName, guidePath},
		{agentsName, agentsPath},
		{claudeName, claudePath},
	} {
		if err := ensureContainedInRepo(repoRoot, target.name, target.path); err != nil {
			return fmt.Errorf("init-agents: %w", err)
		}
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

	if err := mkdirAllContained(repoRoot, guideDirName, 0o755); err != nil {
		return err
	}
	if err := writeContainedFile(repoRoot, guideName, []byte(agentGuide), 0o644); err != nil {
		return err
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
func ensureContainedInRepo(root *os.Root, name, path string) error {
	_, err := statContainedFile(root, name)
	switch {
	case err == nil || errors.Is(err, fs.ErrNotExist):
		return nil
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
	return !errors.Is(err, os.ErrClosed) && !errors.Is(err, os.ErrInvalid)
}

// maxContainedLinkHops bounds the link expansion below the way SYMLOOP_MAX bounds the kernel's own
// resolution, so links naming each other cannot spin forever.
const maxContainedLinkHops = 40

// mkdirAllContained is os.Root.MkdirAll with the same alias handling the confined writes get, so a
// project whose .entire is an in-repository alias spelled as an absolute path installs instead of
// failing preflight.
func mkdirAllContained(root *os.Root, name string, perm os.FileMode) error {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return err
	}
	return root.MkdirAll(resolved, perm)
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
// symlinks, which is what makes ".." correct: popping its last element is what the kernel does
// after following a link, whereas trimming the name as text would ignore the link. A relative
// target is pushed back as components so its own links are expanded in turn; an absolute target
// that lands inside the repository restarts the walk from the root under its repository-relative
// name. maxContainedLinkHops bounds the expansion the way SYMLOOP_MAX bounds the kernel's.
//
// Any chain that leaves the root — an absolute target outside it, or a ".." above it — returns the
// ORIGINAL name so os.Root walks the real links itself and refuses them. That is the whole safety
// argument: this function only ever hands os.Root a name, never a file, so it is a translation and
// never a permission, and a rewrite that is wrong can only fail closed.
func resolveContainedName(root *os.Root, name string) (string, error) {
	pending := strings.Split(name, string(filepath.Separator))
	var resolved []string
	for hops := 0; len(pending) > 0; {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				// Above the root. Let os.Root refuse the real chain.
				return name, nil
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
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, component)
			continue
		}
		if hops++; hops > maxContainedLinkHops {
			return "", &fs.PathError{Op: "resolve", Path: name, Err: syscall.ELOOP}
		}
		target, err := root.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			pending = append(strings.Split(target, string(filepath.Separator)), pending...)
			continue
		}
		rel, ok := repoRelativeName(root.Name(), target)
		if !ok {
			// Lands outside. Let os.Root refuse the real chain.
			return name, nil
		}
		// An absolute target is anchored at the root, so the walk restarts there.
		resolved = resolved[:0]
		pending = append(strings.Split(rel, string(filepath.Separator)), pending...)
	}
	if len(resolved) == 0 {
		// The chain resolved to the repository root itself.
		return ".", nil
	}
	return filepath.Join(resolved...), nil
}

// repoRelativeName reports whether target lands inside the repository rooted at rootPath, and
// under what name.
//
// The decision is made by filesystem identity, not by matching path text. Comparing the two paths
// lexically gets this wrong in both directions that matter in practice: --repo and a committed link
// routinely spell one directory two ways (/tmp/x against /private/tmp/x, a symlinked checkout
// parent), and on a case-insensitive volume — the default on macOS and Windows — they can differ
// only in the case of a component while the kernel resolves both to the same file. So target's
// ancestors are walked upward and each is compared with the root by os.SameFile; the components
// stepped over on the way become the repository-relative name. Ancestors that do not exist yet are
// simply stepped over, which is the case that matters here: an alias may legitimately point at a
// file init-agents is about to create.
//
// The root itself is inside, and is reported as ".", because an alias may point at the project
// directory and the components after it still have to be appended.
//
// This walk is textual about the components it steps over, so a target routed through a symlinked
// ancestor can yield a name that does not in fact stay inside. That cannot grant anything: the name
// is resolved again by os.Root, which refuses it.
func repoRelativeName(rootPath, target string) (string, bool) {
	rootInfo, err := os.Stat(rootPath)
	if err != nil {
		return "", false
	}
	var suffix []string
	for current := filepath.Clean(target); ; {
		if info, statErr := os.Stat(current); statErr == nil && os.SameFile(rootInfo, info) {
			if len(suffix) == 0 {
				return ".", true
			}
			slices.Reverse(suffix)
			return filepath.Join(suffix...), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
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
	return root.Stat(resolved)
}

// readContainedFile is os.ReadFile confined to root.
//
// Reading through the root rather than through the absolute path is not redundant with the
// preflight. ensureContainedInRepo is explicitly not atomic with what follows, so a link swapped
// to an outside file after the preflight and restored before the write would otherwise have that
// file's contents read in and then written back into the repository under the managed block. The
// escape has to be refused at the read too, not only at the preflight and the write.
func readContainedFile(root *os.Root, name string) ([]byte, error) {
	resolved, err := resolveContainedName(root, name)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
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
	content, err := readContainedFile(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, -1, -1, nil
		}
		return nil, -1, -1, fmt.Errorf("%s: read file: %w", path, err)
	}
	begin, end, err := validatePointerMarkers(path, content)
	if err != nil {
		return nil, -1, -1, err
	}
	return content, begin, end, nil
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
