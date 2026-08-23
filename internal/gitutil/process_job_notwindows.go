//go:build !windows

package gitutil

import (
	"errors"
	"os/exec"
)

type pathOutputJob struct{}

type pathOutputLaunch struct{}

func preparePathOutputCommand(cmd *exec.Cmd) (pathOutputLaunch, error) {
	if cmd == nil {
		return pathOutputLaunch{}, errors.New("prepare path-output command: nil command")
	}
	return pathOutputLaunch{}, nil
}

func (*pathOutputLaunch) start(cmd *exec.Cmd) (pathOutputJob, error) {
	if err := cmd.Start(); err != nil {
		return pathOutputJob{}, err
	}
	return pathOutputJob{}, nil
}

func (*pathOutputLaunch) close() {}

func (pathOutputJob) terminate() error { return nil }

func (pathOutputJob) close() {}
