package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// stalledPipe models the sink every "Ctrl-C did nothing" report about this
// binary reduces to: an output pipe whose reader has stopped draining, so the
// write parks in the kernel where no context can reach it. Unlike
// blockingWriter it survives repeated Writes, because a command that ignores
// the error from its first write goes on to attempt the next one.
type stalledPipe struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newStalledPipe() *stalledPipe {
	return &stalledPipe{entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *stalledPipe) Write(b []byte) (int, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return len(b), nil
}

// TestRunUnwindsWhenOutputBlocksAndContextIsCanceled pins the guarantee
// Execute's signal handler is worthless without.
//
// Execute converts the FIRST SIGINT/SIGTERM from "terminate the process" into
// "cancel this context" — a deliberate trade that only pays if every command
// path can actually observe the cancellation. A path that writes straight to
// opts.Stdout has no such observation point: once the write blocks, the
// process cannot be stopped by the signal the operator already sent, and only
// a SECOND one gets them out. That is strictly worse than the no-handler
// behavior it replaced, where one Ctrl-C was enough.
//
// The commands below are the ones with no long-running work of their own, so
// their output IS the whole command: help (root and per-command),
// capabilities, version, agent-guide and snapshot-query.
func TestRunUnwindsWhenOutputBlocksAndContextIsCanceled(t *testing.T) {
	t.Parallel()

	compactPath, symbolID := compactSnapshotFixture(t)

	cases := []struct {
		name string
		args []string
	}{
		{"root help", []string{"help"}},
		{"per-command help", []string{"search", "--help"}},
		{"capabilities", []string{"capabilities", "--json"}},
		{"version json", []string{"version", "--json"}},
		{"agent guide", []string{"agent-guide"}},
		{"snapshot query", []string{"snapshot-query", "--input", compactPath, "--symbol", symbolID, "--format", "ndjson"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			sink := newStalledPipe()
			defer close(sink.release)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, Options{Version: "test", Stdout: sink, Stderr: io.Discard}, testCase.args)
			}()

			select {
			case <-sink.entered:
			case err := <-done:
				t.Fatalf("command returned %v without ever writing; the fixture no longer exercises a blocked sink", err)
			case <-time.After(30 * time.Second):
				t.Fatal("command never reached its first write")
			}

			// The operator's first Ctrl-C, delivered while the write is parked.
			cancel()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return after the context was canceled with output blocked: the first SIGINT is swallowed and the operator must send a second one")
			}
		})
	}
}

// compactSnapshotFixture builds a real compact artifact so the snapshot-query
// case runs the production loader and encoder rather than a stub.
func compactSnapshotFixture(t *testing.T) (path, symbolID string) {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\n\nfunc caller() { callee() }\nfunc callee() {}\n")
	var compact bytes.Buffer
	if err := Run(t.Context(), Options{Version: "compact-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &compact, Stderr: io.Discard},
		[]string{"snapshot", "--repo", repo, "--worktree", "--format", "compact-ndjson"}); err != nil {
		t.Fatal(err)
	}
	index, err := sem.LoadCompactSnapshot(bytes.NewReader(compact.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Snapshot.Symbols) == 0 {
		t.Fatal("fixture produced no symbols")
	}
	path = filepath.Join(t.TempDir(), "graph.compact.ndjson")
	if err := os.WriteFile(path, compact.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, index.Snapshot.Symbols[0].ID
}

// TestIsTerminalSeesThroughTheCancellationWrapper guards the cost of wrapping
// opts.Stdout/opts.Stderr: format selection and the index progress bar both
// ask whether the sink is a TTY by type-asserting *os.File, and a wrapper that
// hid the file would silently switch `index` to JSON on a terminal and drop
// the progress bar.
func TestIsTerminalSeesThroughTheCancellationWrapper(t *testing.T) {
	t.Parallel()

	// /dev/zero, not /dev/tty: `go test` has no controlling terminal, and all
	// isTerminal actually asks of a sink is "are you a *os.File on a character
	// device that is not /dev/null". /dev/zero answers yes on every platform
	// this ships to, so it stands in for the terminal without needing one.
	device, err := os.Open("/dev/zero")
	if err != nil {
		t.Skipf("no character device to stand in for a terminal: %v", err)
	}
	defer device.Close()

	if !isTerminal(device) {
		t.Fatal("isTerminal said a character device is not a terminal; the fixture no longer tests anything")
	}
	wrapped := &contextChunkWriter{ctx: context.Background(), w: device}
	if !isTerminal(wrapped) {
		t.Fatal("isTerminal lost the terminal through contextChunkWriter")
	}
	if isTerminal(&contextChunkWriter{ctx: context.Background(), w: io.Discard}) {
		t.Fatal("isTerminal called a non-file sink a terminal")
	}
}
