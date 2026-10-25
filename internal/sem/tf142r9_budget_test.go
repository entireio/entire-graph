package sem

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// tf142r9LaggingErrContext reports no error for the first tolerate polls and
// context.DeadlineExceeded afterwards. Its Done() channel is nil, so the
// tree-sitter parse inside the unwrap pass is never cancelled and the walk that
// follows it is reached deterministically -- which is the condition the finding
// describes: the deadline expires AFTER the parse.
type tf142r9LaggingErrContext struct {
	context.Context
	polls    atomic.Int64
	tolerate int64
}

func (c *tf142r9LaggingErrContext) Err() error {
	if c.polls.Add(1) <= c.tolerate {
		return nil
	}
	return context.DeadlineExceeded
}

// tf142r9RustLateMacro puts the cfg_net! wrapper AFTER n plain items, so the
// walk must traverse the whole prefix before it can unwrap anything. A walk
// that stops early therefore leaves the source unmasked, and the difference is
// observable in the returned bytes rather than in a stopwatch.
func tf142r9RustLateMacro(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "pub fn plain%d(x: u32) -> u32 { let y = x + %d; y }\n", i, i)
	}
	sb.WriteString("cfg_net! {\npub struct Late { pub a: u32 }\n}\n")
	return sb.String()
}

// TestTF142R9RustUnwrapWalkStopsAfterTheParse reproduces the finding at
// parser.go:736. Handing ctx to the unwrap pass bounds only the tree-sitter
// parse: the recursive AST walk over the tree it produced polled nothing, so a
// budget that expired after the parse still paid for a complete traversal --
// once per unwrap iteration, up to maxUnwrapDepth of them.
func TestTF142R9RustUnwrapWalkStopsAfterTheParse(t *testing.T) {
	t.Parallel()

	content := tf142r9RustLateMacro(400)
	// tolerate=1 is the entry check of the unwrap pass; the next poll is the
	// walk's own, at the first stride boundary.
	stopped := &tf142r9LaggingErrContext{Context: context.Background(), tolerate: 1}
	masked := maskRustUnsupportedSyntax(stopped, content)
	if masked != content {
		t.Fatalf("a context that stopped during the walk still unwrapped %d byte(s) of macro wrapper: the walk polls nothing",
			len(content)-strings.Count(masked, "cfg_net"))
	}
	if stopped.polls.Load() < 2 {
		t.Fatalf("fixture: the walk must poll the context at least once, got %d poll(s)", stopped.polls.Load())
	}
}

// TestTF142R9RustUnwrapUnbudgetedIsUnchanged is the widening direction: with no
// deadline the same input must still be fully unwrapped, byte for byte.
func TestTF142R9RustUnwrapUnbudgetedIsUnchanged(t *testing.T) {
	t.Parallel()

	content := tf142r9RustLateMacro(400)
	masked := maskRustUnsupportedSyntax(context.Background(), content)
	if masked == content {
		t.Fatal("an unbudgeted unwrap pass must still unwrap the cfg_net! wrapper")
	}
	if len(masked) != len(content) {
		t.Fatalf("the mask must be length-preserving: %d vs %d", len(masked), len(content))
	}
	if strings.Contains(masked, "cfg_net") {
		t.Fatal("the wrapper must be blanked")
	}
}
