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
)

var version = "dev"

func main() {
	err := cli.Execute(version, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
