//go:build !linux

package compiler

import (
	"context"
	"errors"
	"os/exec"
)

func isolatedCommand(context.Context, Config, string, string, ...string) (*exec.Cmd, error) {
	return nil, errors.New("compiler live execution requires the tested Linux Bubblewrap boundary")
}
