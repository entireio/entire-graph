// entire-graph is an Entire CLI external command.
//
// Once built as an executable named `entire-graph`, the parent Entire CLI
// dispatches it when a user runs `entire graph`.
package main

import (
	"fmt"
	"os"

	"github.com/entireio/entire-graph/internal/cli"
)

var version = "dev"

func main() {
	if err := cli.Execute(version, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
