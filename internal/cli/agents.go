package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	guidePath := filepath.Join(root, ".entire", "graph-agent.md")
	if err := os.MkdirAll(filepath.Dir(guidePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(guidePath, []byte(agentGuide), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "wrote %s\n", guidePath)

	pointer := agentPointerBegin + "\n" +
		"This repo has the entire-graph code graph installed. Before exploring code with\n" +
		"grep/find/whole-file reads, read .entire/graph-agent.md — resolution-first guidance\n" +
		"for using graph retrieval, focused source inspection, and verification.\n" +
		"@.entire/graph-agent.md\n" +
		agentPointerEnd + "\n"

	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := upsertPointerBlock(agentsPath, pointer); err != nil {
		return fmt.Errorf("init-agents: AGENTS.md: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "updated %s\n", agentsPath)

	claudePath := filepath.Join(root, "CLAUDE.md")
	agentsInfo, err := os.Stat(agentsPath)
	if err != nil {
		return fmt.Errorf("init-agents: AGENTS.md: %w", err)
	}
	claudeInfo, err := os.Stat(claudePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("init-agents: CLAUDE.md: %w", err)
	}
	if err == nil && os.SameFile(agentsInfo, claudeInfo) {
		// A symlink or hard link already gives Claude the AGENTS.md pointer. Updating the
		// same inode a second time could replace that pointer with an inheritance notice
		// that no longer has anything to inherit from.
		return nil
	}

	importsAgents, err := claudeDirectlyImportsAgents(claudePath, agentsPath)
	if err != nil {
		return fmt.Errorf("init-agents: CLAUDE.md: %w", err)
	}
	claudeBlock := pointer
	if importsAgents {
		claudeBlock = agentPointerBegin + "\n<!-- Entire Graph instructions are inherited through AGENTS.md. -->\n" + agentPointerEnd + "\n"
	}
	if err := upsertPointerBlock(claudePath, claudeBlock); err != nil {
		return fmt.Errorf("init-agents: CLAUDE.md: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "updated %s\n", claudePath)
	return nil
}

// claudeDirectlyImportsAgents recognizes a standalone @path directive outside the Markdown
// regions where Claude suppresses imports. Ambiguous syntax returns false so the direct pointer
// remains in place.
func claudeDirectlyImportsAgents(claudePath, agentsPath string) (bool, error) {
	content, err := os.ReadFile(claudePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
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
			return false, nil
		}
		if strings.Contains(line, "<!--") {
			if !strings.Contains(line, "-->") {
				until = "-->"
			}
			continue
		}
		if strings.Contains(line, "-->") {
			return false, nil
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
	return found && until == "" && !fence, nil
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

// upsertPointerBlock appends the block to path (creating the file if absent), or replaces the
// existing marker-delimited block in place, so repeated runs never duplicate content.
func upsertPointerBlock(path, block string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(existing)
	begin := strings.Index(content, agentPointerBegin)
	end := strings.Index(content, agentPointerEnd)
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
	return os.WriteFile(path, []byte(content), 0o644)
}
