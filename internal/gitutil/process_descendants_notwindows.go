//go:build !windows

package gitutil

import (
	"os/exec"
	"time"
)

type pathOutputDescendants struct{}

func capturePathOutputDescendants(*exec.Cmd) pathOutputDescendants {
	return pathOutputDescendants{}
}

func (pathOutputDescendants) terminateAndWait(time.Duration) {}

func (pathOutputDescendants) close() {}
