package cli

import (
	"fmt"
	"io"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// warnf writes a "graph:"-prefixed warning line to w, painting the prefix in
// the entire CLI palette's warning yellow (entireio/cli
// cmd/entire/cli/palette/palette.go @ bc9a821d) when w is a color-capable
// terminal. It exists for diagnostic paths that write straight to stderr with
// no renderer in scope. On a pipe the output for a message without control
// bytes is byte-identical to a plain fmt.Fprintf("graph: ...\n"); the message
// is neutralized either way because it can embed repository-derived error
// text, so control bytes are escaped even where the old raw Fprintf passed
// them through.
func warnf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "%s %s\n", warnPrefix(w, "graph:"), termsafe.Line(fmt.Sprintf(format, args...)))
}

// warnPrefix paints a warning line's prefix for w; the message bytes stay the
// caller's. For sites whose message carries its own multi-line layout, which
// warnf's single-line neutralization would flatten.
func warnPrefix(w io.Writer, prefix string) string {
	if sem.ShouldUseColor(w) {
		return "\x1b[33m" + prefix + "\x1b[0m" // palette.Warning (yellow)
	}
	return prefix
}
