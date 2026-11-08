package agentsetup

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// Existing Graph safety tests call this adapter, keeping all filesystem cases
// independent of installed plugins and real state. CLI routing is tested separately.
type OptionsForTest struct{ Stdout, Stderr io.Writer }

const agentGuide = GraphGuide

func Run(_ context.Context, opts OptionsForTest, args []string) error {
	if args[0] == "agent-guide" {
		_, err := fmt.Fprint(opts.Stdout, GraphGuide)
		return err
	}
	fs := flag.NewFlagSet("init-agents", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	repo := fs.String("repo", ".", "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return Install(*repo, func() (string, error) { return GraphGuide, nil }, opts.Stdout)
}
