package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

const agentGuide = agentsetup.GraphGuide

func graphAgentCommand(ctx context.Context, opts Options, args []string, install bool) error {
	fs := flag.NewFlagSet("agent instructions", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	repo := fs.String("repo", "", "Project root (default: host repository or nearest repository)")
	strict := fs.Bool("strict", false, "Use strict guidance (saved by setup; preview only for agent-guide)")
	normal := fs.Bool("normal", false, "Use normal guidance (saved by setup; preview only for agent-guide)")
	noClaudeHook := new(bool)
	statusline := new(bool)
	noStatusline := new(bool)
	if install {
		fs.BoolVar(noClaudeHook, "no-claude-hook", false, "Do not register the Claude Code SubagentStart hook")
		fs.BoolVar(statusline, "statusline", false, "Install the Claude Code status line even when another settings scope defines one")
		fs.BoolVar(noStatusline, "no-statusline", false, "Do not install the Claude Code status line")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *statusline && *noStatusline {
		return fmt.Errorf("--statusline and --no-statusline are mutually exclusive")
	}
	mode, err := agentsetup.SelectMode(*strict, *normal)
	if err != nil {
		return err
	}
	root, err := agentsetup.Context(*repo, opts.Env.RepoRoot)
	if err != nil {
		return err
	}
	render := func() (string, error) { return agentsetup.Preview(root, "graph", agentsetup.Options{Mode: mode}) }
	if install {
		if root == "" {
			return fmt.Errorf("outside a repository; supply --repo")
		}
		// Name both roots when this one sits inside another repository. Writing
		// the guide into a nested clone is silent and looks like success, and the
		// instruction files land where the outer repository cannot see them. Warn
		// BEFORE any validation or write — the person may want to abort — and
		// only when no explicit --repo was given, which is consent to the nested
		// target. The path in the SUGGESTED COMMAND is shell-quoted; the two in
		// the prose are not, because they are prose. An unquoted path with a
		// space — ordinary on macOS — made the remedy silently do the wrong thing
		// when copied, and one with $ or a backtick would expand. See
		// agentsetup.OuterRepo. A repository AT the user's home directory is
		// exempted: that is dotfiles tracking, not a project anyone meant to
		// install agent instructions into, and warning there — with a remedy
		// pointing at $HOME — would fire on every setup run of every project
		// under a git-tracked home.
		if outer := agentsetup.OuterRepo(root); outer != "" && *repo == "" && !isHomeDir(outer) {
			fmt.Fprintf(opts.Stderr,
				"%s %s is a git repository inside %s.\nThe guide, AGENTS.md and CLAUDE.md go to the inner one. If you meant the project, run:\n    entire graph setup --repo %s\n\n",
				warnPrefix(opts.Stderr, "warning:"), root, outer, shellQuotedRepoDir(outer))
		}
		// Reject malformed or unsafe settings before writing the guides. This is
		// input preflight, not a multi-file transaction: a later I/O failure or
		// concurrent settings change can leave installed guides without the hook.
		if !*noClaudeHook {
			if err := agentsetup.CheckClaudeSubagentHook(root); err != nil {
				return err
			}
		}
		// InstallChanged rather than Install: the installer's own line-per-write
		// output is replaced by one settled row per phase, composed here the way
		// brain's setup composes its rows.
		ui := newSetupUI(opts.Stdout)
		ui.Intro("Entire Graph creates a map of your code that your coding agents query instead of grepping.")
		// The status line is decided, and asked about, before anything is written.
		statusLine, err := planStatusLine(ctx, opts, root, *statusline, *noStatusline)
		if err != nil {
			return err
		}
		changed, err := agentsetup.InstallChanged(root, render)
		if err != nil {
			return err
		}
		if len(changed) == 0 {
			targets := strings.Join([]string{agentsetup.Path, "AGENTS.md", "CLAUDE.md"}, ", ")
			ui.Row(setupMarkDone, "agent instructions", "already current ("+targets+")")
		} else {
			ui.Row(setupMarkDone, "agent instructions", "written ("+strings.Join(changed, ", ")+")")
		}
		if *noClaudeHook {
			ui.Row(setupMarkSkipped, "Claude hook", "skipped (--no-claude-hook)")
		} else {
			path, created, hookChanged, err := agentsetup.InstallClaudeSubagentHookChanged(root)
			if err != nil {
				return err
			}
			switch {
			case !hookChanged:
				ui.Row(setupMarkDone, "Claude hook", "unchanged "+path+" (Claude Code SubagentStart hook already present)")
			case created:
				ui.Row(setupMarkDone, "Claude hook", "wrote "+path+" (Claude Code SubagentStart hook; per-user, keep it untracked)")
			default:
				ui.Row(setupMarkDone, "Claude hook", "updated "+path+" (Claude Code SubagentStart hook; per-user, keep it untracked)")
			}
		}
		if !statusLine.install {
			ui.Row(statusLine.mark, "status line", statusLine.row)
		} else if err := installStatusLine(ui, root, statusLine.foreign, statusLine.scopeErr, statusLine.force); err != nil {
			return err
		}
		ui.End()
		return nil
	}
	guide, err := render()
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(opts.Stdout, guide)
	return err
}

// statusLinePlan is setup's status line decision, made before anything is written. Without install
// the row is settled as is; with it installStatusLine settles it from foreign, scopeErr and force.
type statusLinePlan struct {
	install  bool
	mark     setupMark
	row      string
	foreign  string
	scopeErr error
	force    bool
}

// statusLinePrompt is the question an interactive setup asks. Only y or yes installs.
const statusLinePrompt = "Install the Claude Code status line (estimated graph tokens, graph size)? [y/N] "

// planStatusLine decides the status line step; the first match wins:
//
//  1. --no-statusline skips without reading any settings.
//  2. --statusline installs as before: the preflight, then ours in settings.local.json even when
//     ours is active in another scope, overriding a foreign shared or user line in this repo, never
//     replacing a foreign line inside settings.local.json.
//  3. Otherwise the active status line (ActiveStatusLine) being ours is "unchanged", naming the
//     file that holds it, so a second copy is never written.
//     3a. A legacy shim line (scripts/entire-graph-statusline.sh) in any scope settles the row
//     without asking, naming it and the command to switch to.
//  4. Not interactive: skip with the --statusline hint, also when a scope cannot be read.
//  5. Interactive: an unreadable scope or a foreign line in any scope settles the row without
//     asking; else ask, and only a yes runs the preflight and installs.
//
// So settings.local.json is refused only when the status line is chosen, and a skip never writes:
// a line installed earlier stays.
func planStatusLine(ctx context.Context, opts Options, root string, force, skip bool) (statusLinePlan, error) {
	switch {
	case skip:
		return statusLinePlan{mark: setupMarkSkipped, row: "skipped (--no-statusline)"}, nil
	case force:
		// settings.local.json is rewritten, so a malformed one is refused here. The shared and user
		// files are only inspected.
		if err := agentsetup.CheckClaudeStatusLine(root); err != nil {
			return statusLinePlan{}, err
		}
		active, kind, scopeErr := agentsetup.ActiveStatusLine(root, opts.userSettingsDir())
		return statusLinePlan{install: true, foreign: foreignStatusLine(active, kind), scopeErr: scopeErr, force: true}, nil
	}
	active, kind, scopeErr := agentsetup.ActiveStatusLine(root, opts.userSettingsDir())
	switch {
	case scopeErr == nil && kind == agentsetup.StatusLineOurs:
		return statusLinePlan{mark: setupMarkDone, row: "unchanged " + active + " (Claude Code status line already present)"}, nil
	case scopeErr == nil && kind == agentsetup.StatusLineLegacy:
		return statusLinePlan{mark: setupMarkSkipped, row: "kept the legacy status line script (" + active +
			") — change its command to `" + agentsetup.StatusLineCommand + "`, or rerun with --statusline"}, nil
	case !opts.isInteractive():
		return statusLinePlan{mark: setupMarkSkipped, row: "skipped (pass --statusline to install it)"}, nil
	case active != "" || scopeErr != nil:
		// One that cannot be read, or a foreign line in any scope, is settled by installStatusLine
		// without force, which writes nothing.
		return statusLinePlan{install: true, foreign: active, scopeErr: scopeErr}, nil
	}
	if _, err := io.WriteString(opts.Stdout, statusLinePrompt); err != nil {
		return statusLinePlan{}, err
	}
	yes, err := readYes(ctx, opts.Stdin)
	if err != nil {
		return statusLinePlan{}, err
	}
	if !yes {
		return statusLinePlan{mark: setupMarkSkipped, row: "skipped (not chosen)"}, nil
	}
	if err := agentsetup.CheckClaudeStatusLine(root); err != nil {
		return statusLinePlan{}, err
	}
	return statusLinePlan{install: true}, nil
}

// foreignStatusLine is the active status line's path when that line is not ours, else "". A legacy
// line counts as not ours here: --statusline then overrides it, or replaces it in
// settings.local.json.
func foreignStatusLine(active string, kind agentsetup.StatusLineKind) string {
	if kind == agentsetup.StatusLineOurs {
		return ""
	}
	return active
}

// readYes reads one answer from in: the text up to the first newline or EOF, trimmed. y or yes in
// any case is yes; anything else is no, including EOF, a read error and a nil reader. The read runs
// in a goroutine so a cancelled ctx returns its error at once, before setup writes anything. On
// cancel that goroutine stays blocked on in until it yields a line or EOF. That is deliberate:
// setup is one process that exits right after, and the buffered channel lets the goroutine finish
// without a receiver.
func readYes(ctx context.Context, in io.Reader) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if in == nil {
		return false, nil
	}
	answer := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			line = ""
		}
		answer <- line
	}()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case line := <-answer:
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		}
		return false, nil
	}
}

// installStatusLine settles setup's status line row. A project-local statusLine overrides the
// shared and user ones, so writing ours would silently hide a status line the user already has:
// without force (--statusline) a foreign line in any scope is kept and nothing is written. A
// foreign line inside settings.local.json itself is kept even with force.
//
// foreign is the path of the active status line when that line is not ours (ActiveStatusLine), and
// scopeErr is ActiveStatusLine's failure to inspect the scope foreign then names. Without force the
// step is skipped, since that scope may hold a line ours would hide; with force ours is installed
// and the row says the scope went unchecked.
func installStatusLine(ui *setupUI, root, foreign string, scopeErr error, force bool) error {
	const title = "status line"
	local := filepath.Join(root, filepath.FromSlash(agentsetup.ClaudeLocalSettings))
	if scopeErr != nil && !force {
		ui.Row(setupMarkSkipped, title, "skipped (could not read "+foreign+": "+scopeErr.Error()+") — fix it, or rerun with --statusline")
		return nil
	}
	if foreign != "" && !force {
		if foreign == local {
			ui.Row(setupMarkSkipped, title, "kept your status line ("+foreign+")")
		} else {
			ui.Row(setupMarkSkipped, title, "kept your status line ("+foreign+") — rerun with --statusline to use entire graph's in this repo")
		}
		return nil
	}
	path, _, changed, foreignLocal, err := agentsetup.InstallClaudeStatusLineChanged(root)
	if err != nil {
		return err
	}
	switch {
	case foreignLocal:
		ui.Row(setupMarkSkipped, title, "kept your status line ("+path+")")
	case !changed:
		ui.Row(setupMarkDone, title, "unchanged "+path+" (Claude Code status line already present)")
	case scopeErr != nil:
		ui.Row(setupMarkDone, title, "wrote "+path+" ("+agentsetup.StatusLineCommand+"); could not check "+foreign+": "+scopeErr.Error())
	case foreign == local:
		// Only the legacy shim line in settings.local.json gets here: mergeStatusLine replaced it.
		ui.Row(setupMarkDone, title, "wrote "+path+" (replaced the legacy status line script with "+agentsetup.StatusLineCommand+")")
	case foreign != "":
		ui.Row(setupMarkDone, title, "wrote "+path+" ("+agentsetup.StatusLineCommand+"); overrides "+foreign+" in this repo")
	default:
		ui.Row(setupMarkDone, title, "wrote "+path+" (Claude Code status line: "+agentsetup.StatusLineCommand+")")
	}
	return nil
}

func runAgentGuide(ctx context.Context, opts Options, args []string) error {
	return graphAgentCommand(ctx, opts, args, false)
}

func runSetup(ctx context.Context, opts Options, args []string) error {
	return graphAgentCommand(ctx, opts, args, true)
}

// isHomeDir reports whether dir is the user's home directory. Compared as
// cleaned absolute paths: OuterRepo already returns an absolute path, and the
// two spellings meet here only through filepath.Abs, so no symlink resolution
// is attempted — a home reached through an alias keeps the warning, which errs
// on the side of saying something.
func isHomeDir(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(absHome, absDir)
	}
	return absHome == absDir
}

// shellQuotedRepoDir makes the nested-repo remedy copy-pasteable when the
// repository path contains anything a shell would act on. Spaces and quotes are
// not the whole set: $, a backtick, ;, |, &, a glob or a newline is equally
// hostile to a copy-pasted command, and $ and backtick still EXPAND inside the
// double quotes a naive fix would reach for. Single quotes suppress all of it,
// with '\” to carry an embedded single quote through. Quoting more than
// strictly necessary is free: a single-quoted token is valid wherever a bare
// one was.
func shellQuotedRepoDir(repoDir string) string {
	if strings.TrimSpace(repoDir) == "" {
		return "."
	}
	if strings.ContainsAny(repoDir, " \t\n\r\"'$`;|&<>()[]{}*?!#~^\\") {
		return "'" + strings.ReplaceAll(repoDir, "'", `'\''`) + "'"
	}
	return repoDir
}
