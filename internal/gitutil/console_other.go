//go:build !windows

package gitutil

import "os/exec"

// hideConsoleWindow keeps cmd from opening a visible console window on
// Windows. Other platforms have no console windows, so it does nothing.
func hideConsoleWindow(cmd *exec.Cmd) {}
