//go:build !windows

package gitutil

import "os/exec"

type pathOutputJob struct{}

func startPathOutputCommand(cmd *exec.Cmd) (pathOutputJob, error) {
	if err := cmd.Start(); err != nil {
		return pathOutputJob{}, err
	}
	return pathOutputJob{}, nil
}

func (pathOutputJob) terminate() {}

func (pathOutputJob) close() {}
