// entire-graph is an Entire CLI external command.
//
// Once built as an executable named `entire-graph`, the parent Entire CLI
// dispatches it when a user runs `entire graph`.
package main

import (
	"fmt"
	"os"

	"github.com/entireio/entire-graph/internal/cli"
	"github.com/entireio/entire-graph/internal/termsafe"
)

var version = "dev"

func main() {
	if err := cli.Execute(version, os.Args[1:]); err != nil {
		// Escape by VALUE, not by wrapping os.Stderr. Error text is not
		// tool-authored - it carries pathnames from `git diff -z` and Git's own
		// stderr, and a Git pathname may hold any byte but NUL and '/' - so an
		// ESC, a C1 CSI or an LF in a scanned repository would otherwise be
		// obeyed by the reader's terminal rather than displayed. A termsafe
		// Writer around os.Stderr cannot be used here: stderr also carries the
		// index progress bar's own cursor control (internal/cli/progressbar.go
		// writes "\r...\033[K" to opts.Stderr), which a writer wrap could not
		// tell apart from an injected sequence. Line() is the same by-value
		// treatment the other stderr sink already uses for a repository-derived
		// path (internal/cli/root.go, diffProgressLine).
		fmt.Fprintln(os.Stderr, termsafe.Line(err.Error()))
		os.Exit(1)
	}
}
