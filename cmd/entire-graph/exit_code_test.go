package main

import (
	"errors"
	"syscall"
	"testing"

	"github.com/entireio/entire-graph/internal/cli"
)

// TestExitCodeUsesConventionalSignalStatusForSignalError reproduces the
// trail finding on main: every non-nil error from cli.Execute exited 1,
// including a *cli.SignalError raised because the operator sent SIGINT or
// SIGTERM, misreporting a Ctrl-C as an ordinary command failure to any shell
// or supervisor watching the exit status.
func TestExitCodeUsesConventionalSignalStatusForSignalError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"ordinary error", errors.New("boom"), 1},
		{"SIGINT", &cli.SignalError{Signal: syscall.SIGINT, Err: errors.New("context canceled")}, 130},
		{"SIGTERM", &cli.SignalError{Signal: syscall.SIGTERM, Err: errors.New("context canceled")}, 143},
		{
			"wrapped SignalError",
			errAfter(&cli.SignalError{Signal: syscall.SIGINT, Err: errors.New("context canceled")}),
			130,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// errAfter wraps err the way fmt.Errorf("%w") or a higher layer's own error
// type might, to confirm exitCode finds a SignalError through errors.As
// rather than only via a direct type assertion.
func errAfter(err error) error {
	return wrapper{err}
}

type wrapper struct{ err error }

func (w wrapper) Error() string { return "wrapped: " + w.err.Error() }
func (w wrapper) Unwrap() error { return w.err }
