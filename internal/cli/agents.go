package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

const agentGuide = agentsetup.GraphGuide

func graphAgentCommand(opts Options, args []string, install bool) error {
	fs := flag.NewFlagSet("agent instructions", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	repo := fs.String("repo", "", "Project root (default: host repository or nearest repository)")
	strict := fs.Bool("strict", false, "Use strict guidance (saved by setup; preview only for agent-guide)")
	normal := fs.Bool("normal", false, "Use normal guidance (saved by setup; preview only for agent-guide)")
	noClaudeHook := new(bool)
	if install {
		fs.BoolVar(noClaudeHook, "no-claude-hook", false, "Do not register the Claude Code SubagentStart hook")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
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
				"warning: %s is a git repository inside %s.\nThe guide, AGENTS.md and CLAUDE.md go to the inner one. If you meant the project, run:\n    entire graph setup --repo %s\n\n",
				root, outer, shellQuotedRepoDir(outer))
		}
		// Reject malformed or unsafe settings before writing the guides. This is
		// input preflight, not a multi-file transaction: a later I/O failure or
		// concurrent settings change can leave installed guides without the hook.
		if !*noClaudeHook {
			if err := agentsetup.CheckClaudeSubagentHook(root); err != nil {
				return err
			}
		}
		if err := agentsetup.Install(root, render, opts.Stdout); err != nil {
			return err
		}
		if *noClaudeHook {
			return nil
		}
		return agentsetup.InstallClaudeSubagentHook(root, opts.Stdout)
	}
	guide, err := render()
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(opts.Stdout, guide)
	return err
}
func runAgentGuide(opts Options, args []string) error { return graphAgentCommand(opts, args, false) }
func runSetup(opts Options, args []string) error      { return graphAgentCommand(opts, args, true) }

// shellQuotedRepoDir makes the nested-repo remedy copy-pasteable when the
// repository path contains anything a shell would act on. Spaces and quotes are
// not the whole set: $, a backtick, ;, |, &, a glob or a newline is equally
// hostile to a copy-pasted command, and $ and backtick still EXPAND inside the
// double quotes a naive fix would reach for. Single quotes suppress all of it,
// with '\” to carry an embedded single quote through. Quoting more than
// strictly necessary is free: a single-quoted token is valid wherever a bare
// one was.
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

func shellQuotedRepoDir(repoDir string) string {
	if strings.TrimSpace(repoDir) == "" {
		return "."
	}
	if strings.ContainsAny(repoDir, " \t\n\r\"'$`;|&<>()[]{}*?!#~^\\") {
		return "'" + strings.ReplaceAll(repoDir, "'", `'\''`) + "'"
	}
	return repoDir
}
