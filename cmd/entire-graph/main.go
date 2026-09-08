// entire-graph is an Entire CLI external command.
//
// Once built as an executable named `entire-graph`, the parent Entire CLI
// dispatches it when a user runs `entire graph`.
package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/entireio/entire-graph/internal/cli"
	"github.com/entireio/entire-graph/internal/termsafe"
)

var version = "dev"

func main() {
	err := cli.Execute(version, os.Args[1:])
	if err != nil {
		// Escape by VALUE, not by wrapping os.Stderr. Error text is not
		// tool-authored - it carries pathnames from `git diff -z`, Git's own
		// stderr, and the argv gitutil's run() echoes back, and a Git pathname
		// may hold any byte but NUL and '/' - so an ESC, a C1 CSI or an LF in a
		// scanned repository would otherwise be obeyed by the reader's terminal
		// rather than displayed. A termsafe Writer around os.Stderr cannot be
		// used here for two reasons: stderr also carries the index progress
		// bar's own cursor control (internal/cli/progressbar.go writes
		// "\r...\033[K" to opts.Stderr), which a writer wrap could not tell
		// apart from an injected sequence; and Writer keeps LF as layout, which
		// is precisely the byte a one-line error report must not let a
		// repository forge. Line() is the same by-value treatment the other
		// stderr sink already uses for a repository-derived path
		// (internal/cli/root.go, diffProgressLine).
		//
		// ACCEPTED COST: Line() escapes LF and TAB, so a Git diagnostic that is
		// legitimately several lines would print on one line with its breaks
		// shown as \x0a rather than taken. No such diagnostic is reachable
		// through this sink today - every revision this tool resolves goes
		// through `git rev-parse --verify --end-of-options`, whose failures are
		// one terse line - but git's stderr is folded into these errors
		// verbatim, so a multi-line one is a change in a git version away. That
		// collapse is deliberate and cannot be avoided here: by the time a
		// hint's newline and an injected one are both bytes inside one error
		// string, nothing at this sink can tell which of them git wrote. A
		// one-line report that is honest about its breaks beats a multi-line
		// one a repository can append to.
		fmt.Fprintln(os.Stderr, termsafe.Line(err.Error()))
	}
	os.Exit(exitCode(err))
}

// exitCode maps Execute's result to a process exit status. A *cli.SignalError
// means the operator asked the process to stop (Ctrl-C, SIGTERM) rather than
// the command itself failing, so it gets the conventional 128+signal status
// (130 for SIGINT, 143 for SIGTERM) shells and supervisors already know how
// to interpret, instead of the generic failure status every other error
// gets — which would otherwise misreport operator cancellation as a command
// failure.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var sigErr *cli.SignalError
	if errors.As(err, &sigErr) {
		if sig, ok := sigErr.Signal.(syscall.Signal); ok {
			return 128 + int(sig)
		}
	}
	return 1
}
