package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func printResultFixture() sem.Result {
	return sem.Result{
		Base:          "aaaaaaa",
		Head:          "bbbbbbb",
		SchemaVersion: sem.SchemaVersion,
		Files: []sem.FileChange{{
			Path:   "internal/sem/provider.go",
			Status: "modified",
		}},
	}
}

// TestPrintResultReportsWriteErrorOnCanceledContext pins that a render which
// could not finish is reported as one. printResult wraps its sink in
// contextChunkWriter, whose whole purpose is to fail mid-stream on
// cancellation, but both render paths dropped the resulting error --
// fmt.Fprintln's is discarded by convention and sem.WriteText has none. Run
// therefore returned nil after emitting a truncated document, and the process
// exited 0 on a partial answer.
func TestPrintResultReportsWriteErrorOnCanceledContext(t *testing.T) {
	t.Parallel()

	for _, asJSON := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out bytes.Buffer
		err := printResult(ctx, &out, printResultFixture(), asJSON, "test")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("json=%v: printResult = %v, want context.Canceled (wrote %d byte(s))", asJSON, err, out.Len())
		}
	}
}

// TestPrintResultReportsSinkFailure covers the same hole for an ordinary broken
// stdout (a closed pipe), which needs no cancellation to reach it.
func TestPrintResultReportsSinkFailure(t *testing.T) {
	t.Parallel()

	sinkErr := errors.New("stdout is closed")
	for _, asJSON := range []bool{true, false} {
		err := printResult(context.Background(), writerFunc(func(p []byte) (int, error) {
			return 0, sinkErr
		}), printResultFixture(), asJSON, "test")
		if !errors.Is(err, sinkErr) {
			t.Errorf("json=%v: printResult = %v, want the sink's error", asJSON, err)
		}
	}
}

// TestPrintResultStillRendersOnAHealthySink keeps the fix from turning every
// render into an error path.
func TestPrintResultStillRendersOnAHealthySink(t *testing.T) {
	t.Parallel()

	for _, asJSON := range []bool{true, false} {
		var out bytes.Buffer
		if err := printResult(context.Background(), &out, printResultFixture(), asJSON, "test"); err != nil {
			t.Errorf("json=%v: printResult = %v, want nil", asJSON, err)
		}
		if !strings.Contains(out.String(), "internal/sem/provider.go") {
			t.Errorf("json=%v: rendered output lost the file change: %q", asJSON, out.String())
		}
	}
}
